# Design

gnotes is a single-user, offline-first note taking app made of two programs.

```
browser (PWA, wasm) ──► gnotes-web ──/api/──► gnotes-api ──► notes/*.md
     localStorage        static files + proxy    JSON API
```

## Principles

1. **Portable, no cgo (top priority).** Everything builds with `CGO_ENABLED=0` for every Go target, including `js/wasm`. The Taskfile and CI set `CGO_ENABLED=0`; GoReleaser builds with it. Only the optional race-detector test (`task test:race`, a CI step) enables cgo, because Go's race detector needs it on Linux. New dependencies must not need cgo: check with `go list -deps -f '{{if .CgoFiles}}{{.ImportPath}}{{end}}' ./...`, which must print nothing.
2. **Self-contained and offline.** Binaries embed everything the browser needs; no CDNs, web fonts or third-party requests.

## Backend: `cmd/gnotes-api`

- API: `GET /api/notes`, `PUT /api/notes/{id}` (create or replace), `DELETE /api/notes/{id}`, `GET /healthz`.
- Both writes are idempotent, so the frontend can safely retry them.
- Notes live in memory; each one is also a Markdown file, written atomically (temp file + rename) on every change. A failed write leaves the note unchanged.

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

- One directory per space, one file per note, named after the title. Characters unsafe on any OS become `-`; names are capped at 100 bytes.
- Name collisions, compared case-insensitively, get `-2`, `-3`, …; foreign files are never overwritten.
- The front matter holds all fields, so the path is only derived: renaming a note or changing its space moves its file, and an emptied space directory is removed.
- The body follows the front matter byte for byte.
- On start, files and directories starting with `.` (e.g. `.git`) are ignored and files without a valid `id` are skipped with a log line, so the directory can be a git repository.
- The server stamps `updated`; concurrent edits resolve as last write wins.

## Frontend: `cmd/gnotes-web`

One package built twice:

- as WebAssembly it is the app that runs in the browser;
- as a native binary it embeds that wasm and CSS, serves the PWA shell and proxies `/api/` to the backend. Browser and API therefore share one origin, so no CORS is needed.

The app is local first:

1. Every change (create, edit, pin, delete) is applied to the in-memory list at once and marked pending.
2. When changes pause for 500 ms, the state is written to `localStorage` and synced.
3. A sync pushes pending notes and deletions, fetches the backend's list and merges it: pending local changes win, everything else comes from the backend.
4. Syncs also run on start, on window focus, when the browser comes back online and every 30 s after a failure.

IDs are generated in the browser, which is what lets notes be created offline.

### Spaces

A note's `space` field is a path like `Work/Projects`: spaces nest with `/`. A space exists as long as it or one of its subspaces holds notes: creating a space or subspace creates its first note, and a space disappears with its last note. Renaming a space moves its whole subtree; a path in the dialog moves it elsewhere. Notes without a space are shown under "Notes".

Pinned spaces sort first on their level. Pins and the hidden sidebar are kept per device in `localStorage`, not synced.

Creating starts at the plus button next to the search: type a name. `Ideas` creates the note Ideas in the default space, `Work/Ideas` creates it in Work, and a trailing slash, `Work/`, creates the space Work with a first note. The dialog starts with the open note's space; the plus on a space row starts with that space.

Each note in the sidebar can be pinned, renamed and deleted from its row; the actions show on hover.

### View and edit mode

A note opens rendered as GitHub Flavored Markdown (goldmark; raw HTML is not rendered). Double-click or "Edit" shows the Markdown source, Escape or "View" goes back. New notes open in edit mode.

In the editor, Enter in a list item (`-`, `*`, `+`, `1.`, `- [ ]`) starts the next item with the same indent and marker; numbers count up and task boxes start unchecked. Enter on an empty item removes its marker and ends the list. Shift+Enter inserts a plain newline. Edits go through `execCommand`, so undo keeps working.

Task list checkboxes can be checked in view mode. Each rendered checkbox carries its index; clicking it flips `[ ]`/`[x]` at that task's source offset, taken from the goldmark syntax tree, so code blocks and nested lists stay intact.

### Mermaid

Fenced `mermaid` code blocks render as diagrams. The renderer turns them into `<pre class="mermaid">` with the source still HTML-escaped; the browser draws them with Mermaid's official bundle (`web/mermaid/`, MIT, 11.17.2), embedded in `gnotes-web`. It loads on the first diagram and is cached for offline use. Mermaid runs with `securityLevel: strict` (labels sanitised, no click handlers) and only on a visible view, because it measures text. Invalid diagrams show Mermaid's error diagram.

Rendering stays in the browser so the Go build keeps no cgo or headless browser dependency.

## Shared: `internal`

- `note`: the `Note` model (space, title, Markdown body, pin) and its sort order (pinned first, then newest).
- `serve`: HTTP server with timeouts and graceful shutdown.

## Features

- Spaces with subspaces holding notes; create, rename, collapse, pin, move notes between them
- Collapsible sidebar on large screens
- Notes render as Markdown with Mermaid diagrams; double-click to edit the source; check task list items in view mode
- Lists continue on Enter while editing
- Create notes and spaces by typing a path; edit (autosave), pin, rename, delete and full-text search notes
- Works offline: installable PWA, cached app shell, local copy of all notes, changes sync when the backend is reachable again
- Picks up changes made on other devices
- Material Design 3 look: navigation drawer, search bar, plus button next to search, top app bar, segmented View/Edit button, dialogs, snackbar; M3 baseline colors in light and dark
- Responsive layout (single pane up to 600px), app icons for installation
- Cross-origin write protection on both servers
- Self-contained binaries; releases via svu and GoReleaser

### Look

Material Design 3 is implemented in plain CSS (`web/app.css`: M3 color, type, shape and elevation tokens) with Material Icons as inline SVG (`icons.go`). No component library or web fonts are loaded, so the app stays self-contained and works offline.

## Limitations

- No authentication and a single user.
- Last write wins: an offline edit can overwrite a newer one, or bring back a note deleted elsewhere.
- All notes are held in memory, fine for thousands of notes, not for millions.
- Files changed on disk while the backend runs are not picked up until restart, and are overwritten by the next edit in the app.
- Notes are Markdown, up to 100 000 characters.
