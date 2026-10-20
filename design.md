# Design

Notely is a small multi-user, offline-first note taking app: one server binary and a terminal client.

```
browser (PWA, wasm) ──► notely-server ──► notes/*.md
     localStorage        static files + JSON API (/api/)
```

## Principles

1. **Portable, no cgo (top priority).** Everything builds with `CGO_ENABLED=0` for every Go target, including `js/wasm`. The Taskfile and CI set `CGO_ENABLED=0`; GoReleaser builds with it. Only the optional race-detector test (`task test:race`, a CI step) enables cgo, because Go's race detector needs it on Linux. New dependencies must not need cgo: check with `CGO_ENABLED=0 go list -deps -f '{{if .CgoFiles}}{{.ImportPath}}{{end}}' ./...`, which must print nothing (CI runs it).
2. **Self-contained and offline.** Binaries embed everything the browser needs; no CDNs, web fonts or third-party requests.

## Backend: `internal/api`

Imported only by files built with `!wasm`, so none of it reaches the browser; CI fails if the wasm imports it or grows beyond 11 MB.


- API: `GET /api/notes`, `PUT /api/notes/{id}` (create or replace), `DELETE /api/notes/{id}`, `GET /healthz` (also outside `-base`); sign-on: `POST /api/login`, `POST /api/logout`, `GET /api/session`.
- Both writes are idempotent, so the frontend can safely retry them.
- Notes live in memory; each one is also a Markdown file, written atomically and durably (temp file, fsync, rename, fsync of the directory) on every change. A failed write leaves the note unchanged.
- `PUT` cleans the space path as the apps do and rejects titles over 200 characters, spaces over 1000, bodies over 100 000 and control characters in titles and spaces.

### Sign-on

- Users live in `<data>/.users.json` (`-users` overrides), a JSON object of user name → argon2id hash in PHC format. A missing file is created with the default user `joe` and a random password, logged once. All users share the same notes. The file is read on start; restart after changing it.
- New hashes: `echo -n 'password' | notely-server -hash` (argon2id, 64 MiB, 3 passes, 1 thread, random 16-byte salt). Hashes with other parameters are accepted within bounds.
- `POST /api/login` checks the password in constant time and sets a random session token as cookie: `HttpOnly`, `SameSite=Strict`, `Secure` behind HTTPS (or `X-Forwarded-Proto: https` from a proxy in front), valid 30 days. Every other `/api/` call needs it, else 401.
- Brute force: at most two password checks run at once (each takes 64 MiB); a sign-in waits up to 5 s for one, else gets 503. Failures wait one second, unknown users cost a hash check like known ones. After 5 failures for a user name, its sign-ins are refused with 429 for a doubling time up to a minute, so guessing a password takes about one try a minute; a success resets it. This counts per user name, not per address, as behind a proxy all clients share one, so an attacker can keep that user from signing in meanwhile; existing sessions keep working.
- The cookie's path stays `/` under `-base`, as proxies may serve the app under another path than the server's.
- Sessions are kept in memory: restarting the backend signs everyone out.

The frontend shows a sign-in screen on any 401. Local edits made meanwhile stay pending and sync after signing in. Signing out removes the notes from the device and is remembered there, so a reload does not sign in again with the cookie if the logout request failed.

### Git backup

Each user can push the notes to a git repository of their own, set in the app's **Git backup** dialog: repository (`https://` only, no credentials in the URL), branch and token. The API is `GET/PUT/DELETE /api/sync` and `POST /api/sync/run`.

- State per user lives in `<data>/.sync/<user>/`: `settings.json` (repository, branch), `token` (mode 0600) and `repo/`, a clone. The token never leaves this file: it is not committed and not returned by the API, which only reports whether one is set.
- A sync (go-git, pure Go) fetches the branch, mirrors the notes' `.md` files into the clone, commits if anything changed and pushes without force. Dot files and symbolic links are never copied; copying and the per-user files go through `os.Root`, and the clone lives under a validated user name. A sync times out after 5 minutes. A stored token is only reused for the host it was saved for. Repositories resolving to loopback, link-local (such as cloud metadata) or multicast addresses are refused when connecting, so users cannot reach the server's own services; LAN addresses stay allowed for self-hosted git. Files an edit moves during the copy are left for the next sync. On stop, the last sync gets a minute, so give the container a longer stop timeout than the default 10 s; a server that never started serving skips it.
- One-way: the app wins. Changes made in the repository are replaced by the next sync, but stay in its history.
- Syncs run every `-sync-interval` (default 1h), on **Sync now**, after saving the settings and once on shutdown. One sync runs at a time; the last result per user is shown in the dialog.
- For GitHub use a fine-grained token limited to the one repository with *Contents: read and write*.

### Files

```
notes/
  Inbox.md            # no space
  work/
    Roadmap.md
    Roadmap-2.md      # second note titled "Roadmap"
```

```markdown
---
id: 3f2a…
title: "Roadmap"
space: "work"
pinned: true
updated: 2026-10-15T09:12:00.123Z
---
# Q4
```

- One directory per space, one file per note, named after the title. Characters unsafe on any OS become `-`, Windows device names such as `CON` get a `-`, and names are capped at 100 bytes. A nested space `Work/Projects` is the directory `Work-Projects`.
- Name collisions, compared case-insensitively, get `-2`, `-3`, …; foreign files are never overwritten.
- The front matter holds all fields, so the path is only derived: renaming a note or changing its space moves its file, and an emptied space directory is removed.
- The body follows the front matter byte for byte.
- On start, files and directories starting with `.` (e.g. `.git`) are ignored and files without a valid `id` are skipped with a log line, so the directory can be a git repository. Of two files with the same `id`, left by a crash while moving a note, the older is removed, so deleting the note does not bring it back.
- The server stamps `updated`; concurrent edits resolve as last write wins.

### Images

- `POST /api/images` stores a PNG, JPEG, GIF or WebP image of up to 10 MiB as `<data>/.images/<hash>.<ext>`, named after the first 128 bits of its SHA-256, and answers `{"path": "/images/<name>"}`. The type comes from the content, not the request; SVG is refused because it can carry scripts. The same image is stored once.
- Notes reference it as `![alt](/images/<name>)`. The app loads it from `GET /api/images/<name>` (signed on, cached as immutable, `sandbox` CSP); the git backup copies `.images/` to `images/`, where GitHub resolves the same root-relative link. Links `[text](/images/<name>)` work the same.
- In edit mode the image button or pasting an image uploads it and inserts the Markdown at the cursor.

## Frontend: `cmd/notely-server`

One package built twice:

- as WebAssembly it is the app that runs in the browser;
- as a native binary it embeds that wasm and CSS, serves the PWA shell and the API. Browser and API therefore share one origin, so no CORS is needed.

Size: the wasm calls the backend with the browser's `fetch`, not `net/http`, which keeps Go's TLS and HTTP client stack out of it (10 MB instead of 15 MB). `notely-server` serves the embedded `.wasm`, `.js`, `.css` and `.svg` files gzip-compressed (2.6 MB for the wasm, 1 MB for Mermaid), compressing each once on first request, and tells go-app the uncompressed wasm size for its loading progress.

The app is local first:

1. Every change (create, edit, pin, delete) is applied to the in-memory list at once and marked pending.
2. When changes pause for 500 ms, the state is written to `localStorage` and synced; when the page is hidden or closed, it is written at once. Saving keeps pending notes another tab stored that are newer than this tab's copy, so they sync on the next start rather than being lost; this tab does not push them, as their tab may still change them.
3. A sync pushes pending notes and deletions, fetches the backend's list and merges it: pending local changes win, everything else comes from the backend.
4. Syncs also run on start, on window focus, when the browser comes back online and every 30 s after a failure.
5. While the page is visible and signed on, it keeps a server-sent event stream, `GET /api/events`, open and syncs when it opens and whenever another app changes a note, so changes show up within about a second. Each page sends a random client ID with its requests and is not told about its own changes. The stream closes while the page is hidden, sends a heartbeat every 25 s for proxies, and ends when the server shuts down. Up to 100 streams are served.

IDs are generated in the browser, which is what lets notes be created offline.

### Spaces

A note's `space` field is a path like `Work/Projects`: spaces nest with `/`. A space exists as long as it or one of its subspaces holds notes: creating a space or subspace creates its first note, and a space disappears with its last note. Renaming a space moves its whole subtree; a path in the dialog moves it elsewhere. Notes without a space are shown under "Notes".

Pinned spaces sort first on their level. Pins and the hidden sidebar are kept per device in `localStorage`, not synced.

Creating starts at the plus button next to the search: type a name. `Ideas` creates the note Ideas in the default space, `Work/Ideas` creates it in Work, and a trailing slash, `Work/`, creates the space Work with a first note. The dialog starts with the open note's space; the plus on a space row starts with that space.

Each note in the sidebar can be pinned, renamed and deleted from its row; the actions show on hover.

### View and edit mode

A note opens rendered as GitHub Flavored Markdown (goldmark; raw HTML is not rendered). Double-click or "Edit" shows the Markdown source, Escape or "View" goes back. New notes open in edit mode.

The back gesture (Android) and the browser's back button step back inside the app: edit mode to view mode, then the open note to the list, then out. Each step has its own history entry, pushed only on a user action, because Chrome skips entries pushed without one; the app's own steps back go back in history too, and a reload drops stale entries.

In the editor, Enter in a list item (`-`, `*`, `+`, `1.`, `- [ ]`) starts the next item with the same indent and marker; numbers count up and task boxes start unchecked. Enter on an empty item removes its marker and ends the list. Shift+Enter inserts a plain newline. Edits go through `execCommand`, so undo keeps working.

Task list checkboxes can be checked in view mode. Each rendered checkbox carries its index; clicking it flips `[ ]`/`[x]` at that task's source offset, taken from the goldmark syntax tree, so code blocks and nested lists stay intact. The list is then grouped, items moving with their nested lines: unchecked and plain items first, then checked ones, each group in its order. A just checked item leads the checked ones, a just unchecked one ends the unchecked ones. Each position keeps its marker and blank lines, so numbering and spacing stay. A line separates the groups. Lists keep their order if an item's text does not start on its marker's line, or if items with several lines have markers of different widths.

### Android and offline

- Under `-base`, the app's root is `base/` (`base` redirects there), so the PWA's start URL lies in the service worker's scope and the app starts offline. The manifest `id` stays the old start URL (`base`), so existing installs update in place; reinstall if one does not.
- The `theme-color` meta follows the light or dark scheme for the status bar. Chrome colors the navigation bar from the manifest `theme_color` only, which cannot follow the scheme, so it is the dark surface `#211f26`. Android 15+ ignores that color and shows the window background, which Chrome takes from the manifest `background_color`, so the manifest's is `#211f26` too (the page's loading background stays light); content keeps clear of them with `env(safe-area-inset-*)`.

### Mermaid

Fenced `mermaid` code blocks render as diagrams. The renderer turns them into `<pre class="mermaid">` with the source still HTML-escaped; the browser draws them with Mermaid's official bundle (`web/mermaid/`, MIT, 11.17.2), embedded in `notely-server`. It loads on the first diagram and is cached for offline use. Mermaid runs with `securityLevel: strict` (labels sanitised, no click handlers) and only on a visible view, because it measures text. Invalid diagrams show Mermaid's error diagram.

Rendering stays in the browser so the Go build keeps no cgo or headless browser dependency.

## Terminal client: `cmd/notely`

A [Bubble Tea](https://github.com/charmbracelet/bubbletea) v2 client for the same API, local first like the web app and sharing its merge (`note.Merge`).

- Configuration lives in `~/.config/notely/config.toml` (`$XDG_CONFIG_HOME` is honoured on every OS, `$NOTELY_CONFIG` overrides the path): server URL, user and `password_command`. Without a session the client runs that command with the terminal handed over, so a keychain can ask to be unlocked, and signs in with its first line; once per sign-out, then it shows the sign-in screen. `-init` writes the file with the OS keychain's lookup (`secret-tool`, libsecret, on Linux; `security` on macOS); user names are checked against the server's pattern, as they go into that shell command.
- The password command only runs when the server is the configured `server.url`; `-server` elsewhere asks for the password.
- State per server lives in `~/.config/notely/<hash of the URL>.json` (mode 0600), also when `$NOTELY_CONFIG` moves the config: session token, notes and pending changes. It is saved atomically and durably on every change. An OS lock on `<state>.lock` (`flock`, `LockFileEx`), which the OS releases when the process ends, keeps a second client for the same server from overwriting it; the file holds the process ID for the error. Closing the terminal (SIGHUP) or SIGTERM quits cleanly. Titles over the server's 200 characters are refused when entered; a note the server rejects is named in the status. State of v0.4 and v0.5, in the OS config directory, moves there on start.
- The list is the space tree of the web app's sidebar (`note.Tree`): subspaces, then notes, each space with its note count. `enter` folds a space or opens a note, `←`/`→` fold and go to the parent; folds last for the run. Search keeps the tree, showing the matching notes with their spaces open.
- Syncs run on start, after each change, on terminal focus and every 30 s.
- Notes render with Glamour; editing opens the body in `$VISUAL`, `$EDITOR` or `vi` (`notepad` on Windows). An edit to a note deleted elsewhere meanwhile restores it. Search matches every word in title, space and body.
- It refuses plain HTTP except to loopback addresses and never follows redirects, so the password and token stay on the server's TLS connection.
- Bubble Tea does not build for `js/wasm`; `main_wasm.go` is a stub so the wasm lint covers the module.

## Shared: `internal`

- `note`: the `Note` model (space, title, Markdown body, pin), its sort order (pinned first, then newest), space paths, the space tree and the local-first merge.
- `serve`: HTTP server with timeouts and graceful shutdown.

## Features

- Spaces with subspaces holding notes; create, rename, collapse, pin, move notes between them
- Collapsible sidebar on large screens
- Notes render as Markdown with Mermaid diagrams; double-click to edit the source; check task list items in view mode; checked items sink to the bottom
- Lists continue on Enter while editing
- Create notes and spaces by typing a path; edit (autosave), pin, rename, delete and full-text search notes
- Works offline: installable PWA, cached app shell, local copy of all notes, changes sync when the backend is reachable again
- Picks up changes made on other devices
- Material Design 3 look: navigation drawer, search bar, plus button next to search, top app bar, segmented View/Edit button, dialogs, snackbar; M3 baseline colors in light and dark
- Responsive layout (single pane up to 600px), app icons for installation
- Sign-on with argon2id passwords and session cookies
- Periodic one-way git backup per user (`-sync-interval`), configured in the app
- Cross-origin write protection on the API; responses forbid framing and MIME sniffing
- Self-contained binaries; releases via svu and GoReleaser

### Look

Material Design 3 is implemented in plain CSS (`web/app.css`: M3 color, type, shape and elevation tokens) with Material Icons as inline SVG (`icons.go`). No component library or web fonts are loaded, so the app stays self-contained and works offline.

## Limitations

- Users share one set of notes; there are no per-user notes or permissions.
- Images need the backend: uploading needs a connection, and images not yet loaded do not show offline. Images no note references are kept. Images are stored as uploaded, including metadata such as a photo's GPS location, and go into the git backup.
- Sessions and the last git backup results are lost on backend restart.
- Git backup is one-way; edits made in the repository are not imported.
- `notely` runs once per server and user account at a time.
- Run behind HTTPS beyond localhost: over plain HTTP the session cookie and password travel unencrypted.
- Signing out leaves loaded images in the browser's cache, which keeps them for offline use.
- The browser's `localStorage` holds about 5 MB, which caps the web app's offline copy; beyond it the app shows "Could not save on this device."
- Images from other sites do not show in notes: the CSP allows images only from the server itself (`img-src 'self' data: blob:`), so notes cannot make readers' browsers request other sites.
- Last write wins: an offline edit can overwrite a newer one, or bring back a note deleted elsewhere.
- All notes are held in memory, fine for thousands of notes, not for millions.
- Files changed on disk while the backend runs are not picked up until restart, and are overwritten by the next edit in the app.
- Notes are Markdown, up to 100 000 characters with titles up to 200; the server rejects longer ones.
