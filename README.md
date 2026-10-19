# Notely

Offline-first Markdown notes, organised in spaces. See [design.md](design.md).

`notely-server` serves the [go-app](https://go-app.dev) PWA and its JSON API, storing notes as Markdown files.

```sh
task run   # http://localhost:8000
task test lint
```

| Flag             | Default              |                                                       |
| ---------------- | -------------------- | ----------------------------------------------------- |
| `-addr`          | `localhost:8000`     | listen address                                        |
| `-data`          | `notes`              | notes directory                                       |
| `-users`         | `<data>/.users.json` | users file                                            |
| `-sync-interval` | `1h`                 | git backup interval, `0` for manual only              |
| `-base`          |                      | path prefix to serve under, e.g. `/notely`            |
| `-hash`          |                      | print the argon2id hash of a password read from stdin |

## Terminal client

`notely` reads and edits the notes of a `notely-server` in the terminal. It keeps a copy and syncs changes when the server is reachable.

```sh
go install github.com/jmnser/notely/cmd/notely@latest
notely -init -server https://<host>/notely -user joe
secret-tool store --label=Notely service notely user joe   # as -init prints; macOS: security add-generic-password …
notely
```

`-init` writes `~/.config/notely/config.toml` (`$NOTELY_CONFIG` overrides it). `notely` signs in with its `password_command`, which reads the password from the system keychain, and asks for it if that fails. `-server` and `$NOTELY_SERVER` override `server.url`.

```toml
[server]
url = "https://<host>/notely"
user = "joe"
password_command = "secret-tool lookup service notely user joe"
```

Notes are listed in their spaces; `enter`, `←` and `→` fold spaces. Notes open rendered; `e` edits one in `$VISUAL` or `$EDITOR`, `/` searches, `?` lists all keys. Plain HTTP only works for localhost. `-logout` signs out and removes the notes from the device; it refuses while changes are not synced, unless `-force`.

## Container image

`task image` builds `notely-server` on distroless (`static-debian13:nonroot`), tagged with `VERSION` (default `v0.0.0-<commit>`). It runs as UID 65532 and listens on 8000 with notes in `/data`.

```sh
task image VERSION=v1.2.3
docker run -d --name notely -p 8000:8000 -v notely:/data notely:v1.2.3
```

Releases push it for `linux/amd64` and `linux/arm64` to `docker.io/<DOCKERHUB_USERNAME>/notely`, tagged with the version (tags are immutable, so there is no `latest`). The release workflow needs the repository variable `DOCKERHUB_USERNAME` and the secret `DOCKERHUB_TOKEN`, a Docker Hub access token with read and write access.

## Upgrading to v0.5.0

The server binary is now `notely-server`, also in the image (`/notely-server`); `notely` is the terminal client, which was `notely-tui`. Flags, image name and data are unchanged, so Quadlet units keep working. Replace `notely` with `notely-server` in your own scripts, and reinstall the client from `cmd/notely`: its state moves on first start.

## Upgrading from notely-api and notely-web

Since v0.2.0 one `notely` binary and image replace both. Stop and remove the `notely-api` and `notely-web` units and `notely.network`, then set up `notely.container` below with the same data directory; its layout is unchanged. The `-api` flag is gone and `/healthz` moved to port 8000.

## Podman, systemd and Tailscale Funnel

Run Notely as a rootless Quadlet service at `https://<host>.<tailnet>.ts.net/notely`. Replace `<user>` with the Docker Hub user, then:

```sh
mkdir -p /data/notely ~/.config/containers/systemd
cat > ~/.config/containers/systemd/notely.container <<'UNIT'
[Container]
ContainerName=notely
Image=docker.io/<user>/notely:<latest release tag>
UserNS=keep-id:uid=65532,gid=65532
Volume=/data/notely:/data
PublishPort=127.0.0.1:8000:8000
Exec=-addr=:8000 -data=/data -base=/notely

[Service]
Restart=always

[Install]
WantedBy=default.target
UNIT
systemctl --user daemon-reload
systemctl --user start notely
tailscale funnel --bg --set-path=/notely http://127.0.0.1:8000/notely
```

`UserNS=keep-id:uid=65532,gid=65532` maps your user to the image's user, so `/data/notely` stays yours. `-base=/notely` serves the app under that path. Check with `tailscale funnel status` and `journalctl --user -u notely`. To update, change the tag in `Image=`, then run `systemctl --user daemon-reload && systemctl --user restart notely`.

## Sign-on

On first start `notely-server` creates `<data>/.users.json` with the user `joe`. Add or change users there, then restart, with hashes from:

```sh
echo -n 'new password' | notely-server -hash
```

Serve over HTTPS beyond localhost; see [design.md](design.md#sign-on).

## Images

In edit mode, the image button or pasting an image uploads it and inserts `![name](/images/<hash>.png)`. Images (PNG, JPEG, GIF, WebP, up to 10 MiB) live in `<data>/.images/` and are part of the git backup under `images/`.

## Git backup

Each user can push the notes to their own repository every `-sync-interval` (default 1h): open **Git backup** at the bottom of the sidebar and enter the repository URL, branch and a token. For GitHub, create a fine-grained token for that repository only with _Contents: read and write_. The token is stored in `<data>/.sync/<user>/token` on the server, never in git. See [design.md](design.md#git-backup).
