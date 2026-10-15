# gnotes

Offline-first Markdown notes, organised in spaces. See [design.md](design.md).

- `gnotes-api`: Go backend, a JSON API storing notes in a file
- `gnotes-web`: [go-app](https://go-app.dev) PWA frontend and the server hosting it

```sh
task run   # http://localhost:8000
task test lint
```

| Binary       | Flag    | Default                 |                 |
| ------------ | ------- | ----------------------- | --------------- |
| `gnotes-api` | `-addr` | `localhost:8001`        | listen address  |
| `gnotes-api` | `-data` | `notes.json`            | notes file      |
| `gnotes-web` | `-addr` | `localhost:8000`        | listen address  |
| `gnotes-web` | `-api`  | `http://localhost:8001` | backend URL     |

There is no authentication: keep both on localhost or put them behind an authenticating proxy.
