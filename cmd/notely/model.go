//go:build !wasm

package main

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"

	"github.com/jmnser/notely/internal/note"
)

const (
	syncEvery   = 30 * time.Second // picks up changes made elsewhere and retries failed syncs
	syncTimeout = 2 * time.Minute
	maxWrap     = 100 // columns notes are wrapped at
)

type screen int

const (
	listScreen screen = iota
	viewScreen
	loginScreen
	promptScreen
	confirmScreen
)

type (
	syncedMsg struct {
		remote   []note.Note
		rejected map[string]error
		err      error
		dirty    map[string]int // the changes pushed
		deleted  int            // the deletions pushed
		session  string
	}
	passwordMsg struct {
		password string
		err      error
	}
	loggedInMsg struct {
		token string
		err   error
	}
	editedMsg struct {
		note note.Note // as it was when the editor opened
		body string
		err  error
	}
	tickMsg struct{}
)

var keys = struct {
	open, edit, create, rename, pin, remove, sync, back, quit key.Binding
}{
	open:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
	edit:   key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit")),
	create: key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new")),
	rename: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "rename/move")),
	pin:    key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "pin")),
	remove: key.NewBinding(key.WithKeys("x", "delete"), key.WithHelp("x", "delete")),
	sync:   key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sync")),
	back:   key.NewBinding(key.WithKeys("esc", "q", "backspace"), key.WithHelp("esc", "back")),
	quit:   key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
}

// item is a note in the list.
type item struct{ note.Note }

func (i item) Title() string { return i.Label() }

func (i item) Description() string {
	d := cmp.Or(i.Space, note.DefaultSpace) + " · " + i.Updated.Local().Format("2006-01-02 15:04")
	if i.Pinned {
		d = "📌 " + d
	}
	return d
}

// FilterValue lets the filter search titles, spaces and bodies.
func (i item) FilterValue() string { return i.Label() + "\n" + i.Space + "\n" + i.Body }

// filter keeps the items containing every word of term, ignoring case. Unlike
// the list's fuzzy default it does not match scattered letters of long bodies.
func filter(term string, targets []string) []list.Rank {
	words := strings.Fields(strings.ToLower(term))
	var ranks []list.Rank
	for i, t := range targets {
		t = strings.ToLower(t)
		if !slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(t, w) }) {
			ranks = append(ranks, list.Rank{Index: i})
		}
	}
	return ranks
}

// model is the terminal UI. Changes apply locally at once and are synced in
// the background, as in the web app.
type model struct {
	client *client
	state  *state
	cfg    config

	screen        screen
	back          screen // where a prompt returns to
	width, height int
	dark          bool

	list     list.Model
	view     viewport.Model
	renderer *glamour.TermRenderer
	// rendererKey is the style and wrap width renderer was made for.
	rendererKey string
	current     string // ID of the open note
	selectID    string // ID of the note selected in the list
	user        textinput.Model
	password    textinput.Model
	input       textinput.Model
	question    string
	answer      func(string) tea.Cmd // runs the prompt's input, or "y" on confirmation

	syncing, again bool
	autoTried      bool // signed in with the config since the last sign-out
	seq            int  // change sequence, see state.Dirty
	status         string
}

func newModel(c *client, s *state, cfg config) *model {
	if s.Dirty == nil {
		s.Dirty = map[string]int{}
	}
	m := &model{client: c, state: s, cfg: cfg, dark: true}
	for _, seq := range s.Dirty {
		m.seq = max(m.seq, seq)
	}

	m.list = list.New(nil, list.NewDefaultDelegate(), 0, 0)
	m.list.Title = "Notely"
	m.list.Filter = filter
	m.list.KeyMap.Quit = key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")) // the default is v
	m.list.SetStatusBarItemName("note", "notes")
	m.list.AdditionalShortHelpKeys = func() []key.Binding {
		return []key.Binding{keys.create, keys.edit, keys.pin, keys.remove}
	}
	m.list.AdditionalFullHelpKeys = func() []key.Binding {
		return []key.Binding{keys.open, keys.create, keys.edit, keys.rename, keys.pin, keys.remove, keys.sync}
	}
	m.view = viewport.New()
	m.user, m.password, m.input = textinput.New(), textinput.New(), textinput.New()
	m.user.Placeholder, m.password.Placeholder = "User name", "Password"
	m.password.EchoMode = textinput.EchoPassword
	m.user.SetWidth(40)
	m.password.SetWidth(40)
	m.input.CharLimit = 300
	m.setStyles()
	m.refresh()
	return m
}

func (m *model) Init() tea.Cmd {
	var login tea.Cmd
	if m.state.Session == "" {
		login = m.signIn()
	}
	return tea.Batch(tea.RequestBackgroundColor, m.sync(), tick(), login)
}

func tick() tea.Cmd { return tea.Tick(syncEvery, func(time.Time) tea.Msg { return tickMsg{} }) }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.list.SetSize(msg.Width, msg.Height-1)
		m.view.SetWidth(msg.Width)
		m.view.SetHeight(msg.Height - 3)
		m.input.SetWidth(min(msg.Width-6, 80))
		m.render()
		return m, nil
	case tea.BackgroundColorMsg:
		m.dark = msg.IsDark()
		m.setStyles()
		m.render()
		return m, nil
	case tickMsg:
		return m, tea.Batch(m.sync(), tick())
	case tea.FocusMsg:
		return m, m.sync()
	case syncedMsg:
		return m, m.synced(msg)
	case passwordMsg:
		if msg.err != nil {
			return m, m.loginFailed(fmt.Errorf("password_command: %w", msg.err))
		}
		return m, m.login(m.cfg.Server.User, msg.password)
	case loggedInMsg:
		if msg.err != nil {
			return m, m.loginFailed(msg.err)
		}
		m.autoTried = false // the next sign-out tries the config again
		m.state.Session, m.status, m.screen = msg.token, "", listScreen
		m.password.SetValue("")
		m.password.Blur()
		m.save()
		return m, m.sync()
	case editedMsg:
		if msg.err != nil {
			m.status = "Editor: " + msg.err.Error()
			return m, nil
		}
		c, n := msg.note, m.find(msg.note.ID)
		switch {
		case n == nil && strings.TrimSpace(msg.body) == "":
			return m, nil
		case n == nil: // deleted elsewhere while editing: the edit wins, as in the web app
			m.status = "“" + c.Label() + "” was deleted elsewhere; your edit restored it."
		case msg.body == n.Body || msg.body == n.Body+"\n":
			return m, nil
		default:
			c = *n
		}
		c.Body = msg.body
		return m, m.change(c)
	case tea.KeyPressMsg:
		if key.Matches(msg, keys.quit) {
			return m, tea.Quit
		}
		return m, m.key(msg)
	case list.FilterMatchesMsg: // also while a note is open
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		m.reselect(m.selectID)
		return m, cmd
	}
	return m, m.forward(msg)
}

// forward passes other messages, such as a cursor blink, to the screen's component.
func (m *model) forward(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	switch m.screen {
	case listScreen:
		m.list, cmd = m.list.Update(msg)
	case viewScreen:
		m.view, cmd = m.view.Update(msg)
	case loginScreen:
		var c2 tea.Cmd
		m.user, cmd = m.user.Update(msg)
		m.password, c2 = m.password.Update(msg)
		cmd = tea.Batch(cmd, c2)
	case promptScreen:
		m.input, cmd = m.input.Update(msg)
	case confirmScreen:
	}
	return cmd
}

func (m *model) key(msg tea.KeyPressMsg) tea.Cmd {
	switch m.screen {
	case loginScreen:
		return m.loginKey(msg)
	case promptScreen:
		switch msg.String() {
		case "enter":
			return tea.Batch(m.leave(), m.answer(m.input.Value()))
		case "esc":
			return m.leave()
		}
		return m.forward(msg)
	case confirmScreen:
		if msg.String() == "y" {
			return tea.Batch(m.leave(), m.answer("y"))
		}
		return m.leave()
	case listScreen:
		if m.list.FilterState() == list.Filtering {
			return m.forward(msg)
		}
		if key.Matches(msg, keys.open) {
			if i, ok := m.list.SelectedItem().(item); ok {
				m.open(i.ID)
			}
			return nil
		}
	case viewScreen:
		if key.Matches(msg, keys.back) {
			m.screen = listScreen
			m.reselect(m.current)
			m.current = ""
			return nil
		}
	}
	if cmd, ok := m.action(msg); ok {
		return cmd
	}
	return m.forward(msg)
}

func (m *model) loginKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc": // browse the local copy
		m.screen = listScreen
		return nil
	case "tab", "shift+tab", "up", "down":
		if m.user.Focused() {
			m.user.Blur()
			return m.password.Focus()
		}
		m.password.Blur()
		return m.user.Focus()
	case "enter":
		if m.user.Focused() {
			m.user.Blur()
			return m.password.Focus()
		}
		return m.login(strings.TrimSpace(m.user.Value()), m.password.Value())
	}
	return m.forward(msg)
}

func (m *model) login(user, password string) tea.Cmd {
	m.status = "Signing in…"
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		token, err := m.client.login(ctx, user, password)
		return loggedInMsg{token: token, err: err}
	}
}

// autoLogin signs in with the configured password command. It gets the
// terminal, so it can ask to unlock the keychain.
func (m *model) autoLogin() tea.Cmd {
	m.autoTried, m.status = true, "Signing in…"
	var out bytes.Buffer
	return tea.ExecProcess(passwordCommand(m.cfg.Server.PasswordCommand, &out), func(err error) tea.Msg {
		if err != nil {
			return passwordMsg{err: err}
		}
		password, err := firstLine(out.String())
		return passwordMsg{password: password, err: err}
	})
}

// loginFailed shows err on the sign-in screen.
func (m *model) loginFailed(err error) tea.Cmd {
	var cmd tea.Cmd
	if m.screen != loginScreen {
		cmd = m.signIn()
	}
	m.status = err.Error()
	return cmd
}

// action runs the note actions shared by the list and the view.
func (m *model) action(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if key.Matches(msg, keys.create) {
		space := ""
		if n := m.selected(); n != nil && n.Space != "" {
			space = n.Space + "/"
		}
		return m.ask("New note (Space/Title)", space, func(path string) tea.Cmd {
			space, title, ok := note.ParsePath(path)
			if !ok {
				return nil
			}
			n := note.Note{ID: randID(), Space: space, Title: title}
			m.open(n.ID)
			return tea.Batch(m.change(n), m.edit(n))
		}), true
	}
	if key.Matches(msg, keys.sync) {
		if m.state.Session == "" {
			return m.signIn(), true
		}
		return m.sync(), true
	}
	n := m.selected()
	if n == nil {
		return nil, false
	}
	c := *n
	switch {
	case key.Matches(msg, keys.edit):
		return m.edit(c), true
	case key.Matches(msg, keys.pin):
		c.Pinned = !c.Pinned
		return m.change(c), true
	case key.Matches(msg, keys.rename):
		path := c.Title
		if c.Space != "" {
			path = c.Space + "/" + c.Title
		}
		return m.ask("Rename or move (Space/Title)", path, func(path string) tea.Cmd {
			n := m.find(c.ID) // may have changed meanwhile
			if space, title, ok := note.ParsePath(path); ok && n != nil {
				c := *n
				c.Space, c.Title = space, title
				return m.change(c)
			}
			return nil
		}), true
	case key.Matches(msg, keys.remove):
		m.back, m.screen = m.screen, confirmScreen
		m.question = fmt.Sprintf("Delete “%s” on all devices? (y/N)", c.Label())
		m.answer = func(string) tea.Cmd { return m.delete(c.ID) }
		return nil, true
	}
	return nil, false
}

// signIn signs in with the configured user and password command, once per
// sign-out, else shows the sign-in screen after the prompt if one is open.
func (m *model) signIn() tea.Cmd {
	if m.screen == promptScreen || m.screen == confirmScreen {
		m.back = loginScreen
		return nil
	}
	if !m.autoTried && m.cfg.Server.User != "" && m.cfg.Server.PasswordCommand != "" {
		return m.autoLogin()
	}
	m.screen = loginScreen
	if m.user.Value() == "" {
		m.user.SetValue(m.cfg.Server.User)
	}
	if m.user.Value() != "" {
		m.user.Blur()
		return m.password.Focus()
	}
	m.password.Blur()
	return m.user.Focus()
}

// leave closes a prompt and returns to where it was opened, unless that is gone.
func (m *model) leave() tea.Cmd {
	m.screen = m.back
	switch {
	case m.screen == loginScreen:
		return m.signIn()
	case m.screen == viewScreen && m.find(m.current) == nil:
		m.screen, m.current = listScreen, ""
	}
	return nil
}

func (m *model) ask(question, value string, answer func(string) tea.Cmd) tea.Cmd {
	m.back, m.screen = m.screen, promptScreen
	m.question, m.answer = question, answer
	m.input.SetValue(value)
	m.input.CursorEnd()
	return m.input.Focus()
}

// selected is the open note, else the one selected in the list.
func (m *model) selected() *note.Note {
	if m.screen == viewScreen {
		return m.find(m.current)
	}
	if i, ok := m.list.SelectedItem().(item); ok {
		return m.find(i.ID)
	}
	return nil
}

func (m *model) find(id string) *note.Note {
	if i := slices.IndexFunc(m.state.Notes, func(n note.Note) bool { return n.ID == id }); i >= 0 {
		return &m.state.Notes[i]
	}
	return nil
}

func (m *model) open(id string) {
	m.screen, m.current = viewScreen, id
	m.render()
	m.view.GotoTop()
}

// edit opens the note's body in $VISUAL or $EDITOR.
func (m *model) edit(n note.Note) tea.Cmd {
	f, err := os.CreateTemp("", "notely-*.md") // mode 0600
	if err != nil {
		m.status = err.Error()
		return nil
	}
	_, err = f.WriteString(n.Body)
	if err = cmp.Or(err, f.Close()); err != nil {
		os.Remove(f.Name()) //nolint:errcheck,gosec // best effort
		m.status = err.Error()
		return nil
	}
	editor := editorCommand()
	cmd := exec.Command(editor[0], append(editor[1:], f.Name())...) //nolint:gosec // the user's own editor
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		defer os.Remove(f.Name()) //nolint:errcheck // best effort
		if err != nil {
			return editedMsg{note: n, err: err}
		}
		body, err := os.ReadFile(f.Name())
		return editedMsg{note: n, body: string(body), err: err}
	})
}

// editorCommand is $VISUAL or $EDITOR with its arguments, else the system's
// default editor.
func editorCommand() []string {
	for _, env := range []string{"VISUAL", "EDITOR"} {
		if e := strings.Fields(os.Getenv(env)); len(e) > 0 {
			return e
		}
	}
	if runtime.GOOS == windows {
		return []string{"notepad"}
	}
	return []string{"vi"}
}

// change stores a new or changed note and syncs it.
func (m *model) change(n note.Note) tea.Cmd {
	n.Updated = time.Now()
	if p := m.find(n.ID); p != nil {
		*p = n
	} else {
		m.state.Notes = append(m.state.Notes, n)
	}
	note.Sort(m.state.Notes)
	m.seq++
	m.state.Dirty[n.ID] = m.seq
	return m.changed()
}

func (m *model) delete(id string) tea.Cmd {
	m.state.Notes = slices.DeleteFunc(m.state.Notes, func(n note.Note) bool { return n.ID == id })
	delete(m.state.Dirty, id)
	m.state.Deleted = append(m.state.Deleted, id)
	if m.current == id {
		m.screen, m.current = listScreen, ""
	}
	return m.changed()
}

func (m *model) changed() tea.Cmd {
	m.save()
	return tea.Batch(m.refresh(), m.sync())
}

func (m *model) save() {
	if err := m.state.save(); err != nil {
		m.status = "Could not save on this device: " + err.Error()
	}
}

// sync pushes pending changes and then adopts the backend's notes. Only one
// sync runs at a time.
func (m *model) sync() tea.Cmd {
	if m.state.Session == "" {
		return nil
	}
	if m.syncing {
		m.again = true
		return nil
	}
	m.syncing, m.again = true, false
	var push []note.Note
	for _, n := range m.state.Notes {
		if _, ok := m.state.Dirty[n.ID]; ok {
			push = append(push, n)
		}
	}
	dirty, deleted, session := maps.Clone(m.state.Dirty), slices.Clone(m.state.Deleted), m.state.Session
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
		defer cancel()
		remote, rejected, err := m.client.exchange(ctx, session, push, deleted)
		return syncedMsg{
			remote: remote, rejected: rejected, err: err, dirty: dirty, deleted: len(deleted), session: session,
		}
	}
}

func (m *model) synced(msg syncedMsg) tea.Cmd {
	m.syncing = false
	if msg.session != m.state.Session { // signed in again meanwhile
		return m.sync()
	}
	if errors.Is(msg.err, errSignedOut) {
		m.state.Session, m.status = "", "Signed out. Changes stay on this device until you sign in."
		m.save()
		return m.signIn()
	}
	if msg.err != nil {
		m.status = "Offline. Changes are kept on this device."
		if m.again { // changed meanwhile
			return m.sync()
		}
		return nil
	}
	m.status = ""
	for _, err := range msg.rejected {
		m.status = "Some notes could not be saved: " + err.Error()
	}
	for id, seq := range msg.dirty {
		if _, bad := msg.rejected[id]; !bad && m.state.Dirty[id] == seq {
			delete(m.state.Dirty, id)
		}
	}
	m.state.Deleted = m.state.Deleted[msg.deleted:]
	m.state.Notes = note.Merge(msg.remote, m.state.Notes, m.state.Dirty, m.state.Deleted)
	if m.screen == viewScreen && m.find(m.current) == nil { // deleted elsewhere
		m.screen, m.current = listScreen, ""
	}
	m.save()
	cmd := m.refresh()
	if m.again {
		return tea.Batch(cmd, m.sync())
	}
	return cmd
}

// refresh shows the notes in the list and the open note. The command
// filters the list again.
func (m *model) refresh() tea.Cmd {
	if i, ok := m.list.SelectedItem().(item); ok {
		m.selectID = i.ID
	}
	items := make([]list.Item, len(m.state.Notes))
	for i, n := range m.state.Notes {
		items[i] = item{n}
	}
	cmd := m.list.SetItems(items)
	m.reselect(m.selectID)
	m.render()
	return cmd
}

// reselect selects the note id in the list, which sorting may have moved.
// While the list filters it again, its matches arrive later and select it then.
func (m *model) reselect(id string) {
	m.selectID = id
	if i := slices.IndexFunc(m.list.VisibleItems(), func(i list.Item) bool { return i.(item).ID == id }); i >= 0 {
		m.list.Select(i)
	}
}

// render shows the open note as Markdown.
func (m *model) render() {
	n := m.find(m.current)
	if n == nil || m.width == 0 {
		return
	}
	style := "light"
	if m.dark {
		style = "dark"
	}
	wrap := min(m.width-4, maxWrap)
	if k := fmt.Sprint(style, wrap); m.renderer == nil || m.rendererKey != k {
		r, err := glamour.NewTermRenderer(glamour.WithStandardStyle(style), glamour.WithWordWrap(wrap))
		if err != nil {
			m.view.SetContent(n.Body)
			return
		}
		m.renderer, m.rendererKey = r, k
	}
	text, err := m.renderer.Render(n.Body)
	if err != nil {
		text = n.Body
	}
	m.view.SetContent(text)
}

func (m *model) setStyles() {
	d := list.NewDefaultDelegate()
	d.Styles = list.NewDefaultItemStyles(m.dark)
	m.list.SetDelegate(d)
	m.list.Styles = list.DefaultStyles(m.dark)
	for _, in := range []*textinput.Model{&m.user, &m.password, &m.input} {
		in.SetStyles(textinput.DefaultStyles(m.dark))
	}
}

func (m *model) View() tea.View {
	faint := lipgloss.NewStyle().Faint(true)
	title := lipgloss.NewStyle().Bold(true).Padding(0, 1)
	var b strings.Builder
	switch m.screen {
	case listScreen:
		b.WriteString(m.list.View())
	case viewScreen:
		if n := m.find(m.current); n != nil {
			b.WriteString(title.Render(n.Label()) + faint.Render(cmp.Or(n.Space, note.DefaultSpace)) + "\n")
		}
		b.WriteString(m.view.View() + "\n")
		b.WriteString(faint.Render(fmt.Sprintf(" %3.f%% · e edit · r rename · p pin · x delete · esc back",
			m.view.ScrollPercent()*100)))
	case loginScreen:
		fmt.Fprintf(&b, "\n%s\n\n  %s\n\n  %s\n  %s\n\n  %s", title.Render("Sign in to Notely"),
			faint.Render(m.client.base), m.user.View(), m.password.View(),
			faint.Render("enter sign in · tab next field · esc browse offline"))
	case promptScreen:
		fmt.Fprintf(&b, "\n%s\n\n  %s\n\n  %s", title.Render(m.question), m.input.View(),
			faint.Render("enter ok · esc cancel"))
	case confirmScreen:
		fmt.Fprintf(&b, "\n%s", title.Render(m.question))
	}
	if status := m.footer(); status != "" {
		b.WriteString("\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Render(" "+status))
	}
	v := tea.NewView(b.String())
	v.AltScreen, v.ReportFocus, v.WindowTitle = true, true, "Notely"
	return v
}

func (m *model) footer() string {
	if m.status != "" {
		return m.status
	}
	if n := len(m.state.Dirty) + len(m.state.Deleted); n > 0 && !m.syncing {
		return fmt.Sprintf("%d changes not synced", n)
	}
	return ""
}
