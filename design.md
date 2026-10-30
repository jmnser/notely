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


- API: `GET /api/notes`, `PUT /api/notes/{id}` (create or replace; `kind: "excalidraw"` for a drawing), `DELETE /api/notes/{id}`, images (see below), `GET /api/events` (change events), `GET /healthz` (also outside `-base`); sign-on: `POST /api/login`, `POST /api/logout`, `GET /api/session`.
- Both writes are idempotent, so the frontend can safely retry them.
- Each user has a directory, `<data>/users/<user>/`, with all their files: notes in their space directories, images next to them and the git backup state in `.sync/`. A signed-on user only reaches their own and their shared spaces'; requests name no user. Notes live in memory; each one is also a Markdown file, written atomically and durably (temp file, fsync, rename, fsync of the directory) on every change. A failed write leaves the note unchanged.
- Shared spaces, listed per user in the users file, are top-level spaces with their own directory, `<data>/shared/<space>/`; the space name is left out of paths below it. A user sees their own notes and those of their shared spaces. A note's space picks its store: one whose top space is a shared space of the user goes there, spelt as configured, any other to the user's own, and a note of the user's own keeps its store while its top space stays, so notes a user had in a space before it was shared stay private. Moving a note between stores copies the images it references by name along and makes references of the former layout relative; changes and image uploads run one at a time across users, as they may touch several stores. A note in several stores, e.g. recreated by an offline device after a move, shows its newest copy; saving or deleting it acts on all copies. Change events go to all users who see the note. `GET /api/me` returns the user and their shared spaces; the app marks them and offers no rename, which would move every note out of the space for all members.
- `PUT` cleans the space path as the apps do and rejects titles over 200 characters, spaces over 1000, bodies over 100 000 and control characters in titles and spaces.

### Sign-on

- Users live in `<data>/.users.json` (`-users` overrides), a JSON object of user name → argon2id hash in PHC format, or → `{"hash": ..., "spaces": [...]}` with the user's shared spaces. A missing file is created with the default user `joe` and a random password, logged once. User names are unique, also ignoring case, as they name directories. The file is read on start; restart after changing it.
- Before v0.13.0 all users shared the notes in `<data>`, images in `<data>/.images` and backup state in `<data>/.sync/<user>`. On start every user gets a copy of the notes, as they all saw them, and their backup state moves to `users/<user>/.sync`; images are copied next to the notes referencing them as `/images/<name>`, which then reference them by name, and the others to each user's top directory. Only directories and `.md` files count as notes, so a users file in `<data>` stays. `<data>/.layout` records the migration, so a former space named `users` is migrated too; a `users` directory with subdirectories but no marker stops the start instead of being migrated again, and every step can be repeated, so an interrupted migration continues on the next start.
- New hashes: `echo -n 'password' | notely-server -hash` (argon2id, 64 MiB, 3 passes, 1 thread, random 16-byte salt). Hashes with other parameters are accepted within bounds.
- `POST /api/login` checks the password in constant time and sets a random session token as cookie: `HttpOnly`, `SameSite=Strict`, `Secure` behind HTTPS (or `X-Forwarded-Proto: https` from a proxy in front), valid 30 days. Every other `/api/` call needs it, else 401.
- Brute force: at most two password checks run at once (each takes 64 MiB); a sign-in waits up to 5 s for one, else gets 503. Failures wait one second, unknown users cost a hash check like known ones. After 5 failures for a user name, its sign-ins are refused with 429 for a doubling time up to a minute, so guessing a password takes about one try a minute; a success resets it. This counts per user name, not per address, as behind a proxy all clients share one, so an attacker can keep that user from signing in meanwhile; existing sessions keep working.
- The cookie's path stays `/` under `-base`, as proxies may serve the app under another path than the server's.
- Sessions are kept in memory: restarting the backend signs everyone out.

The frontend shows a sign-in screen on any 401. Local edits made meanwhile stay pending and sync after signing in. Signing out removes the notes from the device and is remembered there, so a reload does not sign in again with the cookie if the logout request failed.

### Git backup

Each user can push the notes to a git repository of their own, set in the app's **Git backup** dialog: repository (`https://` only, no credentials in the URL), branch and token. The API is `GET/PUT/DELETE /api/sync` and `POST /api/sync/run`.

- State per user lives in `<data>/users/<user>/.sync/`: `settings.json` (repository, branch), `token` (mode 0600) and `repo/`, a clone. The token never leaves this file: it is not committed and not returned by the API, which only reports whether one is set.
- A sync (go-git, pure Go) fetches the branch, mirrors the user's `.md` and `.excalidraw` files and images, and their shared spaces' in `shared/<space>/`, into the clone, commits if anything changed and pushes without force. Dot files and symbolic links are never copied; copying and the per-user files go through `os.Root`, and the clone lives under a validated user name. A sync times out after 5 minutes. A stored token is only reused for the host it was saved for. Repositories resolving to loopback, link-local (such as cloud metadata), multicast or Tailscale (`100.64.0.0/10`, `fd7a:115c:a1e0::/48`) addresses are refused when connecting, so users cannot reach the server's own services or its tailnet; LAN addresses stay allowed for self-hosted git. Files an edit moves during the copy are left for the next sync. On stop, the last sync gets a minute, so give the container a longer stop timeout than the default 10 s; a server that never started serving skips it.
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

### Drawings

A note with `kind: "excalidraw"` is an Excalidraw drawing; its body is the scene as JSON (at most 2 MiB, a JSON object). It is stored next to the notes as `<title>.excalidraw`, a regular Excalidraw file indented for readable diffs, with the note's fields under the key `notely` instead of front matter; Excalidraw ignores the key, so the file opens there too. Names follow the rules above, per extension, and the git backup includes the files.

### Images

- Images are files in the space directories, next to the notes: `work/beach-day.png`. Names are DNS labels as in Kubernetes (RFC 1123), up to 63 lower case letters, digits and `-`, starting and ending with a letter or digit, and the extension `.png`, `.jpg`, `.gif` or `.webp`. Uploads are named after the file, lower-cased with other characters turned into `-` and cut to 63, `image` if nothing is left, with `-2`, `-3`, … on collisions within a space; the same image is stored once per space. PNG, JPEG, GIF and WebP up to 2 MiB; the type comes from the content, SVG is refused because it can carry scripts.
- A space exists while it holds notes or images. A directory with images also names its space in a `.space` file, as directory names lose the `/` of subspaces; without notes, the images show in that space.
- Notes reference images as drawings: `![alt](beach-day.png)` in their own space, so GitHub resolves them in the git backup too, `![](Home/Trips/beach-day.png)` in another space and `![](/beach-day.png)` in the default one; `<…>` around paths with spaces. Links `[text](…)` work the same.
- API: `GET /api/images` lists all images with space, name, upload time and `version` (modification time and size, kept by renames); `POST /api/images?space=<space>&name=<file>` uploads to a space; `GET`, `PUT` (`{"name", "space"}`: rename or move) and `DELETE /api/images/<space path>/<name>`; `POST /api/notes/{id}/images?name=<file>` uploads next to note `id` and answers `{"name": "<name>"}`. `?v=<version>` URLs are cached as immutable; others revalidate by that ETag, as a name can get other content. Images are served with a `sandbox` CSP.
- Renaming or moving an image changes the references of all notes the user sees, which then sync like any edit; the app does the same to its notes with pending changes. Notes waiting on another, offline device keep the former reference. Renaming a space moves its images along.
- When a note moves to another space, the images it references by name come along: moved if no other note of the former space uses them, else copied. One of the same name but other content there makes it take another name, and the note's reference changes along. References in code blocks and spans are left alone; reference-style links (`[1]: a.png`) count.
- Nothing is deleted automatically: the image view says how many notes use an image.
- Before v0.19, images were named after their content, `<hash>.<ext>`, and deleted weekly once unused. Those names are valid image names, so their references keep working; references of the layout before v0.13, `/images/<hash>`, load from `GET /api/images/<hash>`, which finds the image in any of the user's directories. `GET /api/notes/{id}/images[/<name>]` and `POST /api/images` without `name`, which stores by hash and answers `{"path": "/images/<name>"}`, still serve app versions cached in browsers. The former Assets space, `.assets/[<subspace>/]<name>`, moves on start to the space `Assets`, its subspaces to `Assets/<subspace>`, leaving out images the user has in a space already.

## Frontend: `cmd/notely-server`

One package built twice:

- as WebAssembly it is the app that runs in the browser;
- as a native binary it embeds that wasm and CSS, serves the PWA shell and the API. Browser and API therefore share one origin, so no CORS is needed.

Size: the wasm calls the backend with the browser's `fetch`, not `net/http`, which keeps Go's TLS and HTTP client stack out of it (10 MB instead of 15 MB). `notely-server` serves the embedded `.wasm`, `.js`, `.css` and `.svg` files gzip-compressed (2.6 MB for the wasm, 1 MB for Mermaid, 1.2 MB for Excalidraw and 0.5 MB for its fonts), compressing each once on first request, and tells go-app the uncompressed wasm size for its loading progress.

The app is local first:

1. Every change (create, edit, pin, delete) is applied to the in-memory list at once and marked pending.
2. When changes pause for 500 ms, the state is written to `localStorage` and synced; when the page is hidden or closed, it is written at once. Saving keeps pending notes another tab stored that are newer than this tab's copy, so they sync on the next start rather than being lost; this tab does not push them, as their tab may still change them.
3. A sync pushes pending notes and deletions, fetches the backend's list and merges it: pending local changes win, everything else comes from the backend.
4. Syncs also run on start, on window focus, when the browser comes back online and every 30 s after a failure.
5. While the page is visible and signed on, it keeps a server-sent event stream, `GET /api/events`, open and syncs when it opens and whenever another app changes a note, so changes show up within about a second. Each page sends a random client ID with its requests, in `X-Notely-Client`, and is not told about its own changes. The stream closes while the page is hidden, sends a heartbeat every 25 s for proxies, and ends when the server shuts down or before the next message once its session ends. Up to 10 streams per user and 100 in all are served. When the stream gives up, e.g. after a server restart signed the page out, the page syncs 30 s later, which shows the sign-in screen or reopens the stream.

IDs are generated in the browser, which is what lets notes be created offline. The clients remember the user their changes belong to: signing in as another user while changes are unsynced asks to sign in again to discard them, so they never go to another user's notes.

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

### Drawings

Drawings show in the main pane in Excalidraw's editor, without view and edit mode; changes are saved once they pause for 300 ms, like any edit. Create one by ending a name with `.excalidraw` in the New dialog, e.g. `Work/Flow.excalidraw`, or from a note: `![](Flow.excalidraw)` embeds the drawing Flow of the note's space as SVG, `![](Work/Flow.excalidraw)` the one in Work and `![](/Flow.excalidraw)` the one without a space. Clicking an embed opens the drawing, the back gesture returns to the note; an embed of a missing drawing creates it. Search matches drawings by title only.

Excalidraw (MIT, 0.18.1) with React is bundled by `tools/excalidraw` (esbuild, `task excalidraw`) into `web/excalidraw/`, embedded and loaded on the first drawing like Mermaid, and cached for offline use. `tools/excalidraw/index.js` is the bridge the wasm calls (`mount`, `embed`). The bundle leaves out the translations (Notely is in English), Mermaid to Excalidraw (it would bundle Mermaid again), the CJK font Xiaolai (13 MB; CJK text uses a system font) and the CDN that Excalidraw falls back to for fonts. The editor has no image tool, embeds or AI features; the page's Content Security Policy keeps fetches, fonts and frames on the origin.

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

### Sync daemon

`notely -daemon`, on Linux and macOS only, mirrors the notes and images to a directory (`-dir`, `daemon.dir`, default `~/notely`, or an existing `~/Notely`), signing in with the password command; a wrong password stops it, as retrying would lock the user out.

- Layout: a directory per space and subspace, each part named as on the server (`internal/notefile`, shared with it), with the server's file format: Markdown with front matter, `.excalidraw` drawings, images by name. Paths are derived from title and space, with `-2`, … on collisions; files it does not own are never replaced. All access goes through `os.Root`; dot files and symbolic links are skipped.
- State lives in `~/.config/notely/<hash of the URL>.daemon.json` (mode 0600), apart from the terminal client's, so both can run: the session and each file as last synced (sum, size, modification time, server version). That base tells local from remote changes, a three-way sync: a file whose size or time changed is read and compared by sum. `<state>.lock` and `<dir>/.notely.lock` allow one daemon per server and per directory.
- A sync scans the directory, fetches the server's lists, pushes changed notes and deletions (the server carries the images of moved notes along), then changed images (a file gone and one with the same content elsewhere is a move, which the server carries into the notes' references, pulled by a follow-up sync), and writes the server's notes and images unless their files changed meanwhile. Local changes win, as in the other clients. The front matter is authoritative, except that a moved or copied file takes its directory's space and its file name as title. Files without front matter are adopted as new notes; files the server rejects or that cannot be parsed are logged once and left alone until they change. A tracked file that lost its fields, e.g. saved by Excalidraw, keeps its note; an emptied one is left alone. A file with an ID but no base, or with an updated time newer than its base, e.g. left by a crash, is compared with the server: the newer updated time wins.
- Guards against data loss: a directory with files it never synced is refused; a missing directory or one recreated meanwhile (its `.notely.lock` is gone) stops the sync; a directory emptied of all its files is restored from the server, and a server found empty at the start of a sync, while files were synced before, stops the sync instead of removing the files. A file is only deleted on the server once two scans miss it, as editors may move it away while saving. An image whose content is in a file not synced yet is never deleted; a changed image is uploaded before its former version is deleted. Pulled moves rename the file first, so a crash leaves one file per note. A file whose size and time look unchanged is still read within 2 s of its modification time, for coarse file times. Case-only renames keep files on case-insensitive file systems and never replace files the daemon does not own. Spaces whose directory names differ only in case get `-2`, … as on macOS they would share a directory. A directory the scan cannot read keeps its files as synced.
- Syncs run on start, 500 ms after file changes settle (fsnotify: inotify, kqueue), on every change the server's event stream reports and every 30 s.

## Shared: `internal`

- `note`: the `Note` model (space, title, Markdown body, pin), its sort order (pinned first, then newest), space paths, the space tree and the local-first merge.
- `serve`: HTTP server with timeouts and graceful shutdown.

## Features

- Spaces with subspaces holding notes; create, rename, collapse, pin, move notes between them
- Collapsible sidebar on large screens
- Excalidraw drawings, stored as `.excalidraw` files and embeddable in notes
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

- Only whole top-level spaces can be shared, not single notes.
- Images need the backend: uploading needs a connection, and images not yet loaded do not show offline. Moving a note copies the images other notes of its former space still use, so they are stored twice. Images are stored as uploaded, including metadata such as a photo's GPS location, and go into the git backup.
- Drawings are saved whole: concurrent edits on two devices keep the last one, not both. Drawings have no image tool; pasted images count towards their 2 MiB. The app refuses to save a drawing over that size. When the browser storage is full, synced drawings are left out of it and do not show offline. The terminal client lists drawings but only the web app shows and edits them.
- Sessions and the last git backup results are lost on backend restart.
- Git backup is one-way; edits made in the repository are not imported.
- `notely` runs once per server and user account at a time; `notely -daemon` once per server and per directory, and only syncs while it runs.
- Run behind HTTPS beyond localhost: over plain HTTP the session cookie and password travel unencrypted.
- Signing out leaves loaded images in the browser's cache, which keeps them for offline use.
- The browser's `localStorage` holds about 5 MB, which caps the web app's offline copy; beyond it the app shows "Could not save on this device."
- Images from other sites do not show in notes: the CSP allows images only from the server itself (`img-src 'self' data: blob:`), so notes cannot make readers' browsers request other sites.
- Last write wins: an offline edit can overwrite a newer one, or bring back a note deleted elsewhere.
- All notes are held in memory, fine for thousands of notes, not for millions.
- Files changed on disk while the backend runs are not picked up until restart, and are overwritten by the next edit in the app.
- Notes are Markdown, up to 100 000 characters with titles up to 200; the server rejects longer ones.
