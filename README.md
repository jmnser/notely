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

## Container images

`task image` builds `gnotes-api` and `gnotes-web` on distroless (`static-debian13:nonroot`), tagged with `VERSION` (default `v0.0.0-<commit>`).

```sh
task image VERSION=v1.2.3
docker network create gnotes
docker run -d --name gnotes-api --network gnotes -v gnotes:/data gnotes-api:v1.2.3
docker run -d --name gnotes-web --network gnotes -p 8000:8000 gnotes-web:v1.2.3
```

Both run as UID 65532 and listen on all interfaces: `gnotes-api` on 8001 with notes in `/data`, `gnotes-web` on 8000 proxying to `http://gnotes-api:8001` (override with `-api=...`).

## Sign-on

On first start `gnotes-api` creates `<data>/.users.json` with the user `joe`. Add or change users there with hashes from:

```sh
echo -n 'new password' | gnotes-api -hash
```

Serve over HTTPS beyond localhost; see [design.md](design.md#sign-on).

## Git backup

Each user can push the notes to their own repository every hour: open **Git backup** at the bottom of the sidebar and enter the repository URL, branch and a token. For GitHub, create a fine-grained token for that repository only with _Contents: read and write_. The token is stored in `<data>/.sync/<user>/token` on the server, never in git. See [design.md](design.md#git-backup).
