# OctoPort

> Open local ports to the public internet on random subdomains.

`octoport` is a hosted tunnel service.
Expose any local port to a public URL in one command.

**This repository is a monolith and holds the whole product.** End users install only a client
(CLI or desktop app); the control plane is operated by me and is not something you need to deploy when you run the agent by building the applciaiton locally else if you want the control plabe is availabel to host yourself too.

- **Control plane** — Go. Auth, routing: it assigns random
  subdomains, muxes public traffic to agents over a multiplexed WebSocket
  stream, and enforces idle-expiry so links self-destruct.
  **Data plane** — [YugabyteDB](https://yugabyte.com) (hash sharding +
  partitioning) and [Valkey](https://valkey.io) (TTL + LRU eviction).
- **Agent** — Rust. Ships as a cross-platform **CLI** and a native **desktop
  app** (Dioxus) that compile for Linux, macOS, and Windows.

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

## Using OctoPort

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

## Running it yourself -- for fun and learning :):

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
## Architecture

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
[`control-plane/.env.example`](control-plane/.env.example) for the full list.

Agent settings use `OCTOPORT_*` env vars too (`OCTOPORT_API_URL`, `OCTOPORT_WS_URL`,
`OCTOPORT_BASE_DOMAIN`, `OCTOPORT_MAX_FRAME_SIZE`, `OCTOPORT_MAX_STREAMS`).

## License

Apache-2.0