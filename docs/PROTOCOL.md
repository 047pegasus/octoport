# Wire Protocol

The control plane and agents talk over a single TLS WebSocket per agent.
Traffic for many concurrent proxied connections is multiplexed onto that
socket as **logical streams**, each identified by a 32-bit stream id.

This document is normative for both implementations:

- Go: `control-plane/internal/protocol/protocol.go`
- Rust: `agent/crates/octoport-core/src/protocol.rs`

## Framing

One WebSocket **binary message** carries exactly one frame.

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  msg_type     |              stream_id (u32 BE)                |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                  flags (u32 BE)  |    payload (variable)      |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

- `msg_type`: 1 byte, see table below.
- `stream_id`: 4 bytes big-endian. `0` is reserved for connection-level
  control (PING/PONG).
- `flags`: 4 bytes, reserved for future use (must be zero today).
- `payload`: up to `OCTOPORT_MAX_FRAME_SIZE` bytes (default 1 MiB).

## Message types

| Value | Name   | Sender         | Payload                                  |
| ----- | ------ | -------------- | ---------------------------------------- |
| 1     | OPEN   | proxy → agent  | JSON `OpenMeta`                          |
| 2     | DATA   | both           | raw stream bytes                         |
| 3     | CLOSE  | both           | empty (graceful end of stream)           |
| 4     | ERROR  | both           | UTF-8 reason string                      |
| 5     | PING   | either         | echo payload                             |
| 6     | PONG   | either         | echoes the PING payload                  |

### OpenMeta (payload of OPEN)

```json
{
  "stream": 1,
  "protocol": "http",
  "target": "127.0.0.1:3000",
  "host": "k7xq2p9m.itanishq.space",
  "tls": false
}
```

`target` is the address the agent must dial on its localhost.

## Stream lifecycle

1. A public client connects to the proxy (`:8090` HTTP or `:8091` TCP).
2. The proxy resolves the subdomain (Host header for HTTP, SNI/`Host:` for
   TCP), looks up the tunnel, and checks the agent is live.
3. The proxy allocates a stream id on that agent and sends **OPEN**.
4. The agent dials `target`. Failure → **ERROR**.
5. Bytes flow as **DATA** in both directions, up to one frame per message.
6. The local service EOF or the client disconnects → **CLOSE**.
7. Either side may send **ERROR** to abort a stream (dial refused, write
   error, frame too large, stream limit reached).

## Flow control & safety

- Agents reject OPEN beyond `OCTOPORT_MAX_STREAMS_PER_AGENT`.
- Frames larger than `OCTOPORT_MAX_FRAME_SIZE` are dropped; sockets are only
  torn down on repeated protocol errors.
- `PING` at `stream_id = 0` keeps NAT/lb connections alive; a `PONG` echoes
  the payload.

## Idle expiry

The proxy slides a tunnel's Valkey TTL on every lookup. When a tunnel is idle
longer than `OCTOPORT_TUNNEL_IDLE_TIMEOUT`, its key expires and the control
plane's sweeper sends **CLOSE** on any live streams before dropping the route.
