# OctoPort

> Open local ports to the public internet on random subdomains.

`octoport` is a hosted tunnel service — think [localxpose](https://localxpose.io/).
Expose any local port to a public URL in one command.

**This repository holds the whole product.** End users install only a client
(CLI or desktop app); the control plane is operated by us at
`octoport-control-plane.itanishq.space` and is not something users deploy.
The server code lives here because the project is open source and auditable.

- **Control plane** — Go. Auth, routing, and all the "magic": it assigns random
  subdomains, muxes public traffic to agents over a multiplexed WebSocket
  stream, and enforces idle-expiry so links self-destruct.
- **Agent** — Rust. Ships as a cross-platform **CLI** and a native **desktop
  app** (Dioxus) that compile for Linux, macOS, and Windows.
- **Website** — Astro. A marketing site modeled on localxpose.io.
- **Data plane** — [YugabyteDB](https://yugabyte.com) (hash sharding +
  partitioning) and [Valkey](https://valkey.io) (TTL + LRU eviction).

```
                          ��──────────────────────────────────────────────��
                          │                 CONTROL PLANE                 │
                          │                                              │
  Browser  ──��  :8090  ──��│  public proxy (Host/SNI routing)             │
  App      ──��  :8091  ──��│       │                                      │
                          │       ��                                      │
                          │  Hub ── streams ──�� agent WebSocket :8081    │
                          │   │        ▲                    │            │
                          │   ��        │                    ��            │
                          │  REST API :8080  ��──  auth  ──  CLI / GUI    │
                          │   │            (JWT + bcrypt)                │
                          │   ��                                          │
                          │  Valkey (hot registry, TTL=5m idle)          │
                          │  YugabyteDB (users/tunnels, hash-sharded)    │
                          └──────────────────────────────────────────────��
                                        ▲
                                        │ websocket (multiplexed streams)
                                        ��
                                   ��──────────��
                                   │  AGENT   │  (Rust — CLI or desktop app)
                                   └──────────��
                                        │ dials localhost:<port>
                                        ��
                                   your local app
```

## Quickstart (using OctoPort)

OctoPort is a hosted service. To *use* it you only need a client — there is
nothing to deploy and nothing to configure.

```bash
curl -sL https://octoport.itanishq.space/install.sh | sh
octoport login          # opens your browser
octoport expose 3000
# Public URL:  https://k7xq2p9m.itanishq.space
curl https://k7xq2p9m.itanishq.space   # <- hits your localhost:3000
```

`octoport expose <port>` creates a tunnel, then streams traffic until you press
Ctrl+C. Tunnels idle out after 5 minutes and are cleaned up automatically.
The hosted endpoints are compiled into the client, so no environment variables
are required.

The desktop app does everything the CLI does, with live traffic charts; both
are attached to every [release](https://github.com/047pegasus/octoport/releases).

## Installation

### One-line installer (CLI + optional GUI)

```bash
# CLI only (default)
curl -sL https://octoport.itanishq.space/install.sh | sh

# GUI only
OCTOPORT_INSTALL_GUI=true bash <(curl -sL https://octoport.itanishq.space/install.sh)

# Both CLI + GUI
OCTOPORT_INSTALL_CLI=true OCTOPORT_INSTALL_GUI=true bash <(curl -sL https://octoport.itanishq.space/install.sh)

# Specific version
OCTOPORT_VERSION=v0.2.0 bash <(curl -sL https://octoport.itanishq.space/install.sh)
```

The installer auto-detects OS/arch, prefers native packages
(`.deb`/`.rpm` on Linux, `.msi` on Windows, `.pkg`/`.dmg` on macOS),
falls back to tarballs/AppImage, verifies SHA-256 against the release
`SHA256SUMS`, and uses `sudo` only when the destination directory isn't
writable.

Flags: `--cli-only`, `--gui-only`, `--both`, `--version VER`, `--repo URL`,
`--dest DIR`.

### Manual downloads

All artefacts are attached to every [GitHub release](https://github.com/047pegasus/octoport/releases):

| Platform | CLI | GUI |
|----------|-----|-----|
| Linux x86_64 / aarch64 | `.deb`, `.rpm`, `.tar.gz` | `.deb`, `.rpm`, `.tar.gz`, `.AppImage` |
| macOS x86_64 | `.pkg`, `.dmg`, raw binary | `.pkg`, `.dmg`, raw binary |
| Windows x86_64 / aarch64 | `.msi`, `.exe` | `.msi`, `.exe` |

SHA-256 checksums are published as `SHA256SUMS` and verified by the installer.

## Quickstart (using OctoPort)

```bash
curl -sL https://octoport.itanishq.space/install.sh | sh
octoport login          # opens your browser
octoport expose 3000
# Public URL:  https://k7xq2p9m.itanishq.space
curl https://k7xq2p9m.itanishq.space   # <- hits your localhost:3000
```

`octoport expose <port>` creates a tunnel, then streams traffic until you press
Ctrl+C. Tunnels idle out after 5 minutes and are cleaned up automatically.
The hosted endpoints are compiled into the client, so no environment variables
are required.

The desktop app does everything the CLI does, with live traffic charts; both
are attached to every [release](https://github.com/047pegasus/octoport/releases).

## Running it yourself (contributors)

You only need this to work on the server. The control plane and its data plane
come up with:

```bash
docker compose -f deploy/docker-compose.yml up -d
```

That boots YugabyteDB, Valkey and the control plane with four listeners:

| Port | Role                     |
| ---- | ------------------------ |
| 8080 | REST API (auth, tunnels) |
| 8081 | Agent WebSocket ingress  |
| 8090 | Public HTTP proxy (Host) |
| 8091 | Public TCP proxy (SNI)   |

**Dev builds** (`cargo build`, `cargo run`) automatically point at the local
stack — no flags or env vars needed. The defaults are compiled in:

```bash
cd agent && cargo build --release      # binary: target/release/octoport
./target/release/octoport expose 3000  # hits localhost:8080/8081
```

Release builds (`cargo build --release`) automatically use the production
hosted endpoints. To override for local development, use `~/.config/octoport/settings.json`:

```json
{ "api_url": "http://localhost:8080",
  "ws_url": "ws://localhost:8081/agent/connect",
  "base_domain": "localhost" }
```

Resolution order: flags (dev only) → `OCTOPORT_*` env vars → `settings.json` →
compiled-in defaults (dev vs release profile).

## Command reference

```
octoport login                                sign in via the browser
octoport whoami                               show the signed-in account
octoport logout                               forget stored credentials
octoport expose <port> [--protocol http|tcp]  expose + stream (foreground)
octoport list                                 list live tunnels (table)
octoport history [--limit N]                  show tunnel history (table)
octoport pause <id>                           stop routing, keep the subdomain
octoport resume <id>                          resume a paused tunnel
octoport delete <id>                          delete a tunnel
```

**Output formats:**
- `list` and `history` render Docker-style tables with auto-sized columns
- `login` and `whoami` show the OctoPort logo; other commands print plain text
- `expose`/`pause`/`resume`/`delete` print single-line status messages

The CLI **no longer exposes** `--api-url`, `--ws-url`, `--base-domain` flags
or `OCTOPORT_API_URL`/`OCTOPORT_WS_URL`/`OCTOPORT_BASE_DOMAIN` env vars —
endpoints are baked into the binary (dev profile = localhost, release = production).
Local overrides use `~/.config/octoport/settings.json` only.

## Architecture

### Control plane (`control-plane/`, Go)

| Package            | Responsibility |
| ------------------ | -------------- |
| `cmd/server`       | wiring, listeners, graceful shutdown |
| `internal/config`  | 100% env-driven configuration + `.env` loader |
| `internal/db`      | YugabyteDB/Postgres pool, schema, sharding DDL |
| `internal/cache`   | Valkey client, tunnel registry, rate limiting |
| `internal/auth`    | JWT issuance/verification, bcrypt hashing |
| `internal/api`     | REST routes, middleware, agent WS handler |
| `internal/tunnel`  | agent hub, multiplexed streams, idle sweeper |
| `internal/proxy`   | public HTTP (Host) + TCP (SNI) proxy |
| `internal/protocol`| wire framing shared with agents |

### Wire protocol

One WebSocket **binary message** = one frame; streams are multiplexed with a
32-bit id.

```
+------+----------+---------+-------------------+
| type | streamId |  flags  |     payload       |
+------+----------+---------+-------------------+
| 1B   | 4B BE    | 4B BE   | variable          |
```

| Type | Direction | Meaning |
| ---- | --------- | ------- |
| OPEN  | proxy → agent | dial `target`, begin a stream |
| DATA  | both       | raw bytes |
| CLOSE | both       | end of stream |
| ERROR | both       | stream failed (reason in payload) |
| PING/PONG | both | socket keep-alive |

The Rust mirror lives in `agent/crates/octoport-core/src/protocol.rs`; the Go
original in `control-plane/internal/protocol/protocol.go`. **Keep them in
sync.**

### Sharding & partitioning (YugabyteDB)

- `users` and `tunnels` are **hash-sharded** across `OCTOPORT_DB_SHARDS` tablets,
  keyed on user id so one user's rows are co-located on a single tablet
  (`internal/db/migrate.go`).
- `events` is **range-partitioned by month**, so audit traffic rolls off by
  dropping partitions instead of expensively deleting rows.

### Caching, eviction & expiry (Valkey)

- The **hot tunnel registry** lives in Valkey: `tunnel:<subdomain>` → JSON entry.
  Every key carries a TTL equal to `OCTOPORT_TUNNEL_IDLE_TIMEOUT` (default 5m).
- Each proxy hit **slides the TTL forward** (sliding-window expiry). When a
  tunnel is idle past the window its key expires, and the control-plane sweeper
  (`internal/tunnel/sweep.go`) CLOSEs the agent's streams, drops the in-memory
  route, and marks the tunnel inactive in YugabyteDB.
- Under memory pressure Valkey evicts with `allkeys-lru` (see
  `deploy/docker-compose.yml`).
- API rate limiting uses a sliding-window token bucket (`INCR` + `EXPIRE`).

### Security

- **AuthN/AuthZ**: bcrypt passwords, scoped JWTs (`api` vs `agent`), required
  `OCTOPORT_JWT_SECRET` in production.
- **Quotas**: 5 concurrent tunnels/user, 64 streams/agent, 1 MiB frame cap.
- **Isolated listeners**: REST, agent ingress, and public proxy never share a
  surface.
- **Protocol-safe routing**: HTTP routes on the Host header; raw TCP routes on
  TLS SNI (or a plaintext Host header) — payloads are never misinterpreted.
- **Expiry by default**: every link self-destructs after idle.

## Configuration

Everything is configurable via environment variables. See
[`control-plane/.env.example`](control-plane/.env.example) for the full list
with inline docs. Layered on top of a `.env` file in the working directory.

Agent settings use `OCTOPORT_*` env vars too (`OCTOPORT_API_URL`, `OCTOPORT_WS_URL`,
`OCTOPORT_BASE_DOMAIN`, `OCTOPORT_MAX_FRAME_SIZE`, `OCTOPORT_MAX_STREAMS`).

## Repository layout

```
octoport/
├── agent/                     # Rust workspace
│   ├── crates/
│   │   ├── octoport-core/       # protocol, settings, REST client, streaming agent
│   │   ├── octoport-cli/        # `octoport` command-line tool
│   │   └── octoport-gui/        # native desktop app (Dioxus)
│   ├── install.sh             # one-line installer (CLI + GUI)
│   └── crates/octoport-cli/entitlements.plist
├── control-plane/             # Go control plane
│   ├── cmd/server/            # entrypoint
│   └── internal/…             # config, db, cache, auth, api, tunnel, proxy
├── website/                   # Astro marketing site
├── deploy/                    # docker-compose + Dockerfile
├── docs/                      # deeper design notes
├── .github/workflows/
│   ├── build-push-control-plane.yml
│   └── release-clients.yml
��── README.md
```

## Development

### Control plane

```bash
cd control-plane && go test ./... && go vet ./...
```

### Local stack (DB + cache + control plane)

The bundled compose file stands up YugabyteDB, Valkey and the control plane so
you can exercise the full data/control plane locally, exactly as it runs in
production (paths are mounted for live server code):

```bash
cd deploy && docker compose up -d --build

# health: API :8080, agent WS :8081, public proxy :8090
curl -s http://localhost:8080/healthz
```

### Agent (CLI + GUI)

```bash
cd agent && cargo build && cargo test        # CLI + core
cargo run -p octoport-gui                     # desktop app (needs a display)
```

**Dev builds default to localhost** — no flags or env vars needed. Point the
client at the local stack, then:

```bash
python3 -m http.server 3000 &
octoport expose 3000        # prints http://<label>.localhost
```

To override for local development, use `~/.config/octoport/settings.json`
(or `OCTOPORT_*` env vars).

### Website

```bash
cd website && npm install && npm run build   # build; or `npm run dev` for live reload
```

## Releases & CI

| Workflow | Trigger | What it does |
| -------- | ------- | ------------ |
| `.github/workflows/build-push-control-plane.yml` | push to `main` touching `control-plane/**` | vets + tests, builds the image, pushes `ghcr.io/047pegasus/octoport-control-plane:{sha,latest}`. Watchtower on the server deploys `:latest` automatically. |
| `.github/workflows/release-clients.yml` | tag `v*` | builds CLI + GUI for 7 targets (Linux x86_64/aarch64, macOS x86_64, Windows x86_64/aarch64), builds native packages (`.deb`/`.rpm`/`.tar.gz`/`.AppImage` Linux; `.dmg`/`.pkg` macOS; `.msi`/`.exe` Windows), optional code signing/notarization, publishes `SHA256SUMS` + a GitHub release. |

### Packaging matrix

| Platform | CLI | GUI |
|----------|-----|-----|
| Linux x86_64 / aarch64 | `.deb`, `.rpm`, `.tar.gz` | `.deb`, `.rpm`, `.tar.gz`, `.AppImage` |
| macOS x86_64 | `.pkg`, `.dmg`, raw binary | `.pkg`, `.dmg`, raw binary |
| Windows x86_64 / aarch64 | `.msi`, `.exe` | `.msi`, `.exe` |

All builds run with `--release`; dev builds (debug profile) use localhost endpoints automatically via `debug_assertions`.

### Signing & verification

- **Build attestations** (Sigstore, keyless) on every artefact:
  `gh attestation verify octoport-linux-x86_64 --repo 047pegasus/octoport`
- **Platform code signing** (Apple Developer ID / Windows Authenticode) applied only when repository secrets are set. Optional — builds succeed without them; users see standard first-launch warnings.
- **SHA-256SUMS** published with every release; installer verifies before installing.

Optional secrets (set in GitHub repo settings → Secrets → Actions):
- macOS: `APPLE_CERTIFICATE_BASE64`, `APPLE_CERTIFICATE_PASSWORD`, `APPLE_NOTARIZATION_APPLE_ID`, `APPLE_NOTARIZATION_TEAM_ID`, `APPLE_NOTARIZATION_PASSWORD`
- Windows: `WINDOWS_CERTIFICATE_BASE64`, `WINDOWS_CERTIFICATE_PASSWORD`

Without secrets, unsigned artefacts are still published (macOS: Gatekeeper warning; Windows: SmartScreen).

`install.sh` verifies SHA-256 checksums before installing; aborts on mismatch or missing checksum.

## License

Apache-2.0