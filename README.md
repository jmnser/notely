# notely

Offline-first Markdown notes, organised in spaces. See [design.md](design.md).

- `notely-api`: Go backend, a JSON API storing notes as Markdown files
- `notely-web`: [go-app](https://go-app.dev) PWA frontend and the server hosting it

```sh
task run   # http://localhost:8000
task test lint
```

| Binary       | Flag             | Default                 |                                                       |
| ------------ | ---------------- | ----------------------- | ----------------------------------------------------- |
| `notely-api` | `-addr`          | `localhost:8001`        | listen address                                        |
| `notely-api` | `-data`          | `notes`                 | notes directory                                       |
| `notely-api` | `-users`         | `<data>/.users.json`    | users file                                            |
| `notely-api` | `-sync-interval` | `1h`                    | git backup interval, `0` for manual only              |
| `notely-api` | `-hash`          |                         | print the argon2id hash of a password read from stdin |
| `notely-web` | `-addr`          | `localhost:8000`        | listen address                                        |
| `notely-web` | `-api`           | `http://localhost:8001` | backend URL                                           |
| `notely-web` | `-base`          |                         | path prefix to serve under, e.g. `/notely`            |

## Container images

`task image` builds `notely-api` and `notely-web` on distroless (`static-debian13:nonroot`), tagged with `VERSION` (default `v0.0.0-<commit>`).

```sh
task image VERSION=v1.2.3
docker network create notely
docker run -d --name notely-api --network notely -v notely:/data notely-api:v1.2.3
docker run -d --name notely-web --network notely -p 8000:8000 notely-web:v1.2.3
```

Releases push both images, for `linux/amd64` and `linux/arm64`, to `docker.io/<DOCKERHUB_USERNAME>/notely-{api,web}`, tagged with the version and `latest`. The release workflow needs the repository variable `DOCKERHUB_USERNAME` and the secret `DOCKERHUB_TOKEN`, a Docker Hub access token with read and write access.

Both run as UID 65532 and listen on all interfaces: `notely-api` on 8001 with notes in `/data`, `notely-web` on 8000 proxying to `http://notely-api:8001` (override with `-api=...`).

## Podman, systemd and Tailscale Funnel

Run both containers as rootless Quadlet services and publish them at `https://<host>.<tailnet>.ts.net/notely`. Replace `<user>` with the Docker Hub user, then:

```sh
mkdir -p /data/notely ~/.config/containers/systemd
cd ~/.config/containers/systemd
printf '[Network]\n' > notely.network
cat > notely-api.container <<'UNIT'
[Container]
ContainerName=notely-api
Image=docker.io/<user>/notely-api:latest
UserNS=keep-id:uid=65532,gid=65532
Network=notely.network
Volume=/data/notely:/data

[Service]
Restart=always

[Install]
WantedBy=default.target
UNIT
cat > notely-web.container <<'UNIT'
[Unit]
Requires=notely-api.service
After=notely-api.service

[Container]
ContainerName=notely-web
Image=docker.io/<user>/notely-web:latest
Network=notely.network
PublishPort=127.0.0.1:8000:8000
Exec=-addr=:8000 -api=http://notely-api:8001 -base=/notely

[Service]
Restart=always

[Install]
WantedBy=default.target
UNIT
systemctl --user daemon-reload
systemctl --user start notely-web
tailscale funnel --bg --set-path=/notely http://127.0.0.1:8000/notely
```

`UserNS=keep-id:uid=65532,gid=65532` maps your user to the image's user, so `/data/notely` stays yours. `-base=/notely` serves the app under that path. Check with `tailscale funnel status` and `journalctl --user -u notely-api`.

## Sign-on

On first start `notely-api` creates `<data>/.users.json` with the user `joe`. Add or change users there with hashes from:

```sh
echo -n 'new password' | notely-api -hash
```

Serve over HTTPS beyond localhost; see [design.md](design.md#sign-on).

## Git backup

Each user can push the notes to their own repository every hour: open **Git backup** at the bottom of the sidebar and enter the repository URL, branch and a token. For GitHub, create a fine-grained token for that repository only with _Contents: read and write_. The token is stored in `<data>/.sync/<user>/token` on the server, never in git. See [design.md](design.md#git-backup).
