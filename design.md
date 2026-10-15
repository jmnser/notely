# Design

gnotes is a single-user, offline-first note taking app made of two programs.

```
browser (PWA, wasm) ──► gnotes-web ──/api/──► gnotes-api ──► notes.json
     localStorage        static files + proxy    JSON API
```

## Backend: `cmd/gnotes-api`

- API: `GET /api/notes`, `PUT /api/notes/{id}` (create or replace), `DELETE /api/notes/{id}`, `GET /healthz`.
- Both writes are idempotent, so the frontend can safely retry them.
- Notes live in memory and every write rewrites the JSON file atomically (temp file + rename). A failed write is rolled back.
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

A note's `space` field names the space it belongs to, like a directory. A space exists as long as it holds notes: creating a space creates its first note, renaming a space moves all its notes, and a space disappears with its last note. Notes without a space are shown under "Notes".

### View and edit mode

A note opens rendered as GitHub Flavored Markdown (goldmark; raw HTML is not rendered). Double-click or "Edit" shows the Markdown source, Escape or "View" goes back. New notes open in edit mode.

Task list checkboxes can be checked in view mode. Each rendered checkbox carries its index; clicking it flips `[ ]`/`[x]` at that task's source offset, taken from the goldmark syntax tree, so code blocks and nested lists stay intact.

## Shared: `internal`

- `note`: the `Note` model (space, title, Markdown body, pin) and its sort order (pinned first, then newest).
- `serve`: HTTP server with timeouts and graceful shutdown.

## Features

- Spaces (directories) holding notes; create, rename, collapse, move notes between them
- Notes render as Markdown; double-click to edit the source; check task list items in view mode
- Create, edit (autosave), pin, delete and full-text search notes
- Works offline: installable PWA, cached app shell, local copy of all notes, changes sync when the backend is reachable again
- Picks up changes made on other devices
- Responsive layout, light and dark theme
- Cross-origin write protection on both servers
- Self-contained binaries; releases via svu and GoReleaser

## Limitations

- No authentication and a single user.
- Last write wins: an offline edit can overwrite a newer one, or bring back a note deleted elsewhere.
- The whole file is rewritten on each change, fine for thousands of notes, not for millions.
- Notes are Markdown, up to 100 000 characters.
- Spaces are flat: no nested spaces.
