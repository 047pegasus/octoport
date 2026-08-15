// Package proxy implements the public entry point. It accepts traffic for
// *.BaseDomain and routes each connection to the right agent over that
// agent's multiplexed WebSocket stream.
package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"octoport/control-plane/internal/cache"
	"octoport/control-plane/internal/config"
	"octoport/control-plane/internal/protocol"
	"octoport/control-plane/internal/tunnel"
)

// Proxy routes public traffic to agents.
type Proxy struct {
	Cfg  *config.Config
	Cache *cache.Client
	Hub  *tunnel.Hub
	Log  *slog.Logger

	tlsOnce sync.Once
	tlsCfg  *tls.Config
}

// New builds a Proxy.
func New(cfg *config.Config, c *cache.Client, h *tunnel.Hub, log *slog.Logger) *Proxy {
	return &Proxy{Cfg: cfg, Cache: c, Hub: h, Log: log}
}

// Handler returns the HTTP proxy handler that routes on Host header.
func (p *Proxy) Handler() http.Handler {
	return http.HandlerFunc(p.serveHTTP)
}

func (p *Proxy) serveHTTP(w http.ResponseWriter, r *http.Request) {
	subdomain, ok := p.extractSubdomain(r.Host)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	entry, err := p.Cache.GetTunnel(r.Context(), subdomain)
	if errors.Is(err, cache.ErrMiss) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		p.Log.Error("proxy: cache lookup failed", "subdomain", subdomain, "err", err)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if !entry.Enabled {
		// Paused tunnels keep their subdomain reserved but stop serving.
		// Return 404 to avoid leaking tunnel existence.
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	t, ok := p.Hub.Lookup(subdomain)
	if !ok {
		// Tunnel exists but no live agent is connected.
		// Return 404 to avoid leaking tunnel existence.
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	agent, ok := p.Hub.AgentByID(entry.AgentID)
	if !ok || agent == nil {
		// Agent offline.
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if t.Protocol != "http" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	t.BumpActivity()

	upgrade := isUpgradeRequest(r)

	// Serialise the request BEFORE hijacking. r.Write reads r.Body, and once
	// the connection is hijacked the server's body reader is no longer ours to
	// use -- the previous code did this after the hijack, which happens to work
	// for bodiless GETs but is undefined for anything carrying a payload.
	raw, err := p.encodeRequest(r, upgrade)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// We own the socket from here: stream the raw request to the agent and
	// the raw response back, byte-for-byte.
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "server does not support hijacking", http.StatusInternalServerError)
		return
	}
	// Hijack, keeping the buffered reader: the http server may have already
	// read bytes past the request head into it (a pipelined request, or the
	// first WebSocket frames a client sends immediately after the handshake).
	// Discarding it as the previous version did silently loses those bytes.
	clientConn, brw, err := hj.Hijack()
	if err != nil {
		p.Log.Error("proxy: hijack failed", "subdomain", subdomain, "err", err)
		return
	}
	defer clientConn.Close()

	ctx, cancel := context.WithTimeout(r.Context(), p.Cfg.ProxyTimeout)
	defer cancel()
	stream := tunnel.NewStream(64)
	stream.WriteHook = func() {
		_ = clientConn.Close()
		p.Hub.RemoveStream(subdomain, stream)
	}
	streamID := agent.AllocStream(stream)
	p.Hub.AddStream(subdomain, stream)

	if err := stream.SendOpen(ctx, protocol.OpenMeta{
		Stream:   streamID,
		Protocol: "http",
		Target:   entry.LocalAddr,
		Host:     r.Host,
		TLS:      r.TLS != nil,
	}, p.Cfg.MaxFrameSize); err != nil {
		stream.Finish()
		return
	}

	// Ship the already-serialised request.
	if err := p.sendRequest(stream, raw, t); err != nil {
		stream.Finish()
		return
	}

	// Relay agent -> client concurrently with client -> agent.
	//
	// This connection is full-duplex from here on. The previous version sent
	// the request, immediately half-closed with SendClose, and then only ever
	// pumped agent -> client -- it never read another byte from the client.
	// For a plain one-shot GET that happens to work, but it makes protocol
	// upgrades impossible: a WebSocket handshake gets its 101 relayed back, so
	// the browser believes the socket is open, yet every frame it then sends
	// is discarded. Worse, the CLOSE frame arrived at the agent before the
	// origin had even answered, so the agent dropped the stream from its map
	// and any later DATA for it was silently ignored.
	//
	// That is exactly the Astro/Vite infinite-reload loop: Vite's HMR client
	// opens a WebSocket, gets a socket that accepts nothing and dies, retries,
	// and after its retry budget calls location.reload() -- which re-runs the
	// whole sequence forever. The fix is to keep both directions alive for the
	// life of the connection, and to only send CLOSE on a genuine client EOF.
	responded := make(chan bool, 1)
	go func() {
		ok := p.pumpAgentToClient(stream, clientConn, t)
		// The response side is done, so nothing more will come from the
		// origin. Unblock the request-side reader below without closing the
		// connection, so we can still write a 502 if we owe the client one.
		_ = clientConn.SetReadDeadline(time.Now())
		responded <- ok
	}()

	// Pump client -> agent until the client goes away (or the goroutine above
	// trips the read deadline). Reads go through brw, not clientConn, so any
	// bytes the http server already buffered are forwarded too.
	buf := make([]byte, 32*1024)
	for {
		n, rerr := brw.Read(buf)
		if n > 0 {
			if serr := stream.SendData(buf[:n], p.Cfg.MaxFrameSize); serr != nil {
				break
			}
			t.AddBytes(n, 0)
			t.BumpActivity()
		}
		if rerr != nil {
			break
		}
	}
	// Half-close the request direction: the agent shuts down its write side to
	// the origin but keeps reading the response.
	_ = stream.SendClose()

	wroteResponse := <-responded
	if !wroteResponse && !upgrade {
		_ = clientConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, _ = clientConn.Write([]byte(
			"HTTP/1.1 502 Bad Gateway\r\n" +
				"Content-Type: text/plain\r\n" +
				"Content-Length: 12\r\n" +
				"Connection: close\r\n\r\n" +
				"bad gateway\n"))
	}
	stream.Finish()
}

// isUpgradeRequest reports whether the client asked for a protocol switch
// (WebSocket, or anything else negotiated via the Upgrade header). Such
// connections must stay full-duplex for their whole lifetime and must never
// have Connection: close forced onto them.
func isUpgradeRequest(r *http.Request) bool {
	if r.Header.Get("Upgrade") == "" {
		return false
	}
	for _, v := range r.Header.Values("Connection") {
		for _, tok := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(tok), "upgrade") {
				return true
			}
		}
	}
	return false
}

// encodeRequest re-encodes the parsed request into the raw bytes the origin
// should see. Must be called before the connection is hijacked, while r.Body
// is still readable.
func (p *Proxy) encodeRequest(r *http.Request, upgrade bool) ([]byte, error) {
	p.setForwardedHeaders(r)
	if !upgrade {
		// One request per connection: tell the origin so it closes its side
		// after responding, instead of advertising keep-alive on a socket we
		// are about to tear down (which makes browsers retry on a dead
		// connection). Upgrade requests must keep their own Connection header.
		r.Header.Set("Connection", "close")
	}
	var buf bytes.Buffer
	if err := r.Write(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// sendRequest forwards the serialised request to the agent in frame-sized
// chunks.
//
// It deliberately does NOT send CLOSE afterwards: the caller keeps pumping
// client bytes into the stream and only half-closes on a real client EOF.
// Sending CLOSE here is what previously made upgrades (WebSocket) impossible.
func (p *Proxy) sendRequest(stream *tunnel.Stream, raw []byte, t *tunnel.Tunnel) error {
	t.AddBytes(len(raw), 0)
	const chunk = 32 * 1024
	for len(raw) > 0 {
		n := min(chunk, len(raw))
		if err := stream.SendData(raw[:n], p.Cfg.MaxFrameSize); err != nil {
			return err
		}
		raw = raw[n:]
	}
	return nil
}

// setForwardedHeaders adds the standard reverse-proxy provenance headers so the
// origin can build correct absolute URLs. Existing values are preserved (we may
// ourselves be behind Cloudflare or another proxy), and X-Forwarded-For is
// appended to rather than replaced.
func (p *Proxy) setForwardedHeaders(r *http.Request) {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil && host != "" {
		if prior := r.Header.Get("X-Forwarded-For"); prior != "" {
			r.Header.Set("X-Forwarded-For", prior+", "+host)
		} else {
			r.Header.Set("X-Forwarded-For", host)
		}
	}
	if r.Header.Get("X-Forwarded-Host") == "" {
		r.Header.Set("X-Forwarded-Host", r.Host)
	}
	if r.Header.Get("X-Forwarded-Proto") == "" {
		if r.TLS != nil {
			r.Header.Set("X-Forwarded-Proto", "https")
		} else {
			r.Header.Set("X-Forwarded-Proto", "http")
		}
	}
}

// pumpAgentToClient relays DATA frames from the agent to the client conn.
// It reports false when the stream ended in an error before any response
// bytes were relayed (so the caller can synthesise a 502 for HTTP).
func (p *Proxy) pumpAgentToClient(stream *tunnel.Stream, conn net.Conn, t *tunnel.Tunnel) bool {
	wroteResponse := false
	for {
		select {
		case f, ok := <-stream.In:
			if !ok {
				return wroteResponse
			}
			switch f.Type {
			case protocol.MsgData:
				if _, err := conn.Write(f.Payload); err != nil {
					return wroteResponse
				}
				wroteResponse = true
				t.AddBytes(0, len(f.Payload))
			case protocol.MsgClose:
				return wroteResponse
			case protocol.MsgError:
				p.Log.Debug("proxy: stream error", "err", string(f.Payload))
				return wroteResponse
			}
		case <-stream.Done():
			return wroteResponse
		}
	}
}

// extractSubdomain pulls the random label from a Host header, validating that
// it belongs to our base domain.
func (p *Proxy) extractSubdomain(host string) (string, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	if i := strings.IndexByte(host, ':'); i > 0 {
		host = host[:i] // strip port
	}
	base := strings.ToLower(p.Cfg.BaseDomain)
	if base != "" {
		if !strings.HasSuffix(host, "."+base) {
			return "", false
		}
		host = strings.TrimSuffix(host, "."+base)
	}
	if host == "" || host == base {
		return "", false
	}
	for _, r := range host {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' {
			return "", false
		}
	}
	// Never treat another service's label as a tunnel: reserved subdomains
	// (portainer, octoport, octoport-control-plane, ...) must be ignored here even
	// if a stray Host header sneaks through, so they 404 instead of colliding.
	if p.Cfg.IsReservedSubdomain(host) {
		return "", false
	}
	return host, true
}
