# gnotes

Offline-first Markdown notes, organised in spaces. See [design.md](design.md).

- `gnotes-api`: Go backend, a JSON API storing notes as Markdown files
- `gnotes-web`: [go-app](https://go-app.dev) PWA frontend and the server hosting it

```sh
task run   # http://localhost:8000
task test lint
```

| Binary       | Flag             | Default                 |                                                       |
| ------------ | ---------------- | ----------------------- | ----------------------------------------------------- |
| `gnotes-api` | `-addr`          | `localhost:8001`        | listen address                                        |
| `gnotes-api` | `-data`          | `notes`                 | notes directory                                       |
| `gnotes-api` | `-users`         | `<data>/.users.json`    | users file                                            |
| `gnotes-api` | `-sync-interval` | `1h`                    | git backup interval, `0` for manual only              |
| `gnotes-api` | `-hash`          |                         | print the argon2id hash of a password read from stdin |
| `gnotes-web` | `-addr`          | `localhost:8000`        | listen address                                        |
| `gnotes-web` | `-api`           | `http://localhost:8001` | backend URL                                           |
| `gnotes-web` | `-base`          |                         | path prefix to serve under, e.g. `/gnotes`            |

## Container images

`task image` builds `gnotes-api` and `gnotes-web` on distroless (`static-debian13:nonroot`), tagged with `VERSION` (default `v0.0.0-<commit>`).

```sh
task image VERSION=v1.2.3
docker network create gnotes
docker run -d --name gnotes-api --network gnotes -v gnotes:/data gnotes-api:v1.2.3
docker run -d --name gnotes-web --network gnotes -p 8000:8000 gnotes-web:v1.2.3
```

Both run as UID 65532 and listen on all interfaces: `gnotes-api` on 8001 with notes in `/data`, `gnotes-web` on 8000 proxying to `http://gnotes-api:8001` (override with `-api=...`).

## Podman, systemd and Tailscale Funnel

Run both containers as rootless Quadlet services and publish them at `https://<host>.<tailnet>.ts.net/gnotes`. Build the images with `podman build --target api -t gnotes-api .` and `podman build --target web -t gnotes-web .`, then:

```sh
mkdir -p /data/gnotes ~/.config/containers/systemd
cd ~/.config/containers/systemd
printf '[Network]\n' > gnotes.network
cat > gnotes-api.container <<'UNIT'
[Container]
ContainerName=gnotes-api
Image=localhost/gnotes-api:latest
UserNS=keep-id:uid=65532,gid=65532
Network=gnotes.network
Volume=/data/gnotes:/data

[Service]
Restart=always

[Install]
WantedBy=default.target
UNIT
cat > gnotes-web.container <<'UNIT'
[Unit]
Requires=gnotes-api.service
After=gnotes-api.service

[Container]
ContainerName=gnotes-web
Image=localhost/gnotes-web:latest
Network=gnotes.network
PublishPort=127.0.0.1:8000:8000
Exec=-addr=:8000 -api=http://gnotes-api:8001 -base=/gnotes

[Service]
Restart=always

[Install]
WantedBy=default.target
UNIT
systemctl --user daemon-reload
systemctl --user start gnotes-web
tailscale funnel --bg --set-path=/gnotes http://127.0.0.1:8000/gnotes
```

`UserNS=keep-id:uid=65532,gid=65532` maps your user to the image's user, so `/data/gnotes` stays yours. `-base=/gnotes` serves the app under that path. Check with `tailscale funnel status` and `journalctl --user -u gnotes-api`.

## Sign-on

On first start `gnotes-api` creates `<data>/.users.json` with the user `joe`. Add or change users there with hashes from:

```sh
echo -n 'new password' | gnotes-api -hash
```

Serve over HTTPS beyond localhost; see [design.md](design.md#sign-on).

## Git backup

Each user can push the notes to their own repository every hour: open **Git backup** at the bottom of the sidebar and enter the repository URL, branch and a token. For GitHub, create a fine-grained token for that repository only with _Contents: read and write_. The token is stored in `<data>/.sync/<user>/token` on the server, never in git. See [design.md](design.md#git-backup).
