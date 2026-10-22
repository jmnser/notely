# Notely

Self-hosted, offline-first Markdown notes, organised in spaces. A web app you can install on your phone, a terminal client, and a single Go server that keeps every note as a plain Markdown file.

- Markdown with task lists, Mermaid diagrams and images
- Excalidraw drawings, on their own or embedded in notes
- Spaces and sub-spaces, pinning and search
- Works offline; changes sync when the server is reachable
- Multiple users, with spaces shared between them
- Per-user backup to a git repository
- Terminal client for reading and editing in `$EDITOR`

## Quick start

```sh
docker run -d --name notely --stop-timeout 70 -p 8000:8000 -v notely:/data docker.io/jonas27/notely:<version>
docker logs notely   # prints the password of the first user, joe
```

Open http://localhost:8000. Image tags are the [release versions](https://github.com/jmnser/notely/releases); there is no `latest`. The image is distroless, runs as UID 65532, listens on 8000 and stores notes in `/data`. Serve it over HTTPS beyond localhost, e.g. behind a reverse proxy.

## Server

| Flag             | Default              |                                                       |
| ---------------- | -------------------- | ----------------------------------------------------- |
| `-addr`          | `localhost:8000`     | listen address                                        |
| `-data`          | `notes`              | notes directory                                       |
| `-users`         | `<data>/.users.json` | users file                                            |
| `-sync-interval` | `1h`                 | git backup interval, `0` for manual only              |
| `-base`          |                      | path prefix to serve under, e.g. `/notely`            |
| `-hash`          |                      | print the argon2id hash of a password read from stdin |

The stop timeout of 70 s gives the last git backup time to finish.

## Users

On first start the server creates `<data>/.users.json` with the user `joe` and a random password, which it logs once. Edit the file to change passwords and add users, then restart. Hash passwords with:

```sh
echo -n 'new password' | notely-server -hash
```

Each user's notes live in `<data>/users/<user>/`. To share a top-level space, list it for each member:

```json
{
  "ann": { "hash": "$argon2id$...", "spaces": ["Family"] },
  "bob": { "hash": "$argon2id$...", "spaces": ["Family"] }
}
```

Members see and edit its notes and sub-spaces, stored in `<data>/shared/Family/`. Moving a note into the space shares it; moving it out makes it the mover's. A shared space name has no `/`, is not `Notes`, does not start with a dot and is spelt the same for all members.

## Images and drawings

In edit mode, the image button or pasting an image uploads it and inserts `![name](<hash>.png)`. Images (PNG, JPEG, GIF, WebP, up to 2 MiB) are stored next to the note. The gallery button lists a space's images to insert them again. Unused images are deleted weekly once they are a week old.

Type `Flow.excalidraw` in the New dialog to create a drawing, or embed one in a note with `![](Flow.excalidraw)`; clicking the embed opens it, or creates it if missing. `![](Work/Flow.excalidraw)` refers to a drawing in another space.

## Git backup

Each user can push their notes to their own repository: open **Git backup** at the bottom of the sidebar and enter the repository URL, branch and a token. For GitHub, use a fine-grained token for that repository only with _Contents: read and write_. Shared spaces go to `shared/<space>/` in each member's repository. The token stays on the server, never in git. Repositories on loopback, link-local or Tailscale addresses are refused.

## Terminal client

`notely` reads and edits the notes of a server in the terminal. It keeps a local copy and syncs when the server is reachable.

```sh
go install github.com/jmnser/notely/cmd/notely@latest
notely -init -server https://<host>/notely -user joe
notely
```

`-init` writes `~/.config/notely/config.toml` (`$NOTELY_CONFIG` overrides it) and prints how to store the password in the system keychain, which `password_command` reads it from:

```toml
[server]
url = "https://<host>/notely"
user = "joe"
password_command = "secret-tool lookup service notely user joe"
```

`enter`, `←` and `→` fold spaces, `e` edits a note in `$VISUAL` or `$EDITOR`, `/` searches and `?` lists all keys. `-logout` signs out and removes the local copy; it refuses while changes are not synced, unless `-force`. Drawings are listed but only open in the web app.

## Development

Needs Go and [Task](https://taskfile.dev).

```sh
task run          # http://localhost:8000
task test lint
task image VERSION=v1.2.3
task excalidraw   # rebuild the Excalidraw bundle, needs Node.js
```

[design.md](design.md) describes the architecture, storage format and limitations.

Releases are tagged on every merge to `main` and push multi-arch images to `docker.io/<DOCKERHUB_USERNAME>/notely`; the workflow needs the repository variable `DOCKERHUB_USERNAME` and the secret `DOCKERHUB_TOKEN`. Each push to a pull request also pushes a dev image tagged `<release>-dev-<commit>`.
