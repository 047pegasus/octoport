package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"time"

	"octoport/control-plane/internal/cache"
	"octoport/control-plane/internal/protocol"
	"octoport/control-plane/internal/tunnel"
)

// ServeTCP runs the raw TCP listener used by "tcp" tunnels. Routing is
// resolved from the SNI of a TLS ClientHello (or a plaintext HTTP Host
// header), then the entire byte stream is multiplexed to the agent. When
// TCP TLS termination is enabled the connection is first wrapped in tls.Server
// so every byte on the wire is encrypted and routing uses the handshake SNI.
func (p *Proxy) ServeTCP(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go p.handleTCP(conn)
	}
}

func (p *Proxy) handleTCP(conn net.Conn) {
	defer conn.Close()

	// With TLS termination we route on the handshake SNI; the ClientHello is
	// consumed by tls.Server, so peekRoute would never see it. Without TLS we
	// sniff SNI / HTTP Host from the raw stream as before. resolveRoute returns
	// the conn to forward (the decrypted tls.Conn when TLS is terminated).
	subdomain, buffered, forward, err := p.resolveRoute(conn)
	if err != nil {
		return
	}
	conn = forward
	if subdomain == "" {
		p.Log.Warn("tcp proxy: no routable subdomain", "err", err)
		return
	}
	if p.Cfg.IsReservedSubdomain(subdomain) {
		p.Log.Warn("tcp proxy: reserved subdomain", "subdomain", subdomain)
		return
	}
	p.Log.Debug("tcp route", "subdomain", subdomain, "peeked", len(buffered))
	entry, err := p.Cache.GetTunnel(context.Background(), subdomain)
	if errors.Is(err, cache.ErrMiss) {
		p.Log.Warn("tcp proxy: tunnel miss", "subdomain", subdomain)
		return
	}
	if err != nil {
		p.Log.Error("tcp proxy: cache lookup failed", "subdomain", subdomain, "err", err)
		return
	}
	if !entry.Enabled {
		p.Log.Debug("tcp proxy: tunnel paused", "subdomain", subdomain)
		return
	}

	t, ok := p.Hub.Lookup(subdomain)
	if !ok || t.Protocol != "tcp" {
		p.Log.Warn("tcp proxy: no tcp tunnel in hub", "subdomain", subdomain, "ok", ok)
		return
	}
	agent, ok := p.Hub.AgentByID(entry.AgentID)
	if !ok {
		p.Log.Warn("tcp proxy: agent offline", "subdomain", subdomain, "agent", entry.AgentID)
		return
	}
	t.BumpActivity()

	ctx, cancel := context.WithTimeout(context.Background(), p.Cfg.ProxyTimeout)
	defer cancel()

	stream := tunnel.NewStream(64)
	stream.WriteHook = func() {
		_ = conn.Close()
		p.Hub.RemoveStream(subdomain, stream)
	}
	streamID := agent.AllocStream(stream)
	p.Hub.AddStream(subdomain, stream)

	if err := stream.SendOpen(ctx, protocol.OpenMeta{
		Stream:   streamID,
		Protocol: "tcp",
		Target:   entry.LocalAddr,
		Host:     subdomain + "." + p.Cfg.BaseDomain,
	}, p.Cfg.MaxFrameSize); err != nil {
		p.Log.Error("tcp proxy: send open failed", "err", err)
		stream.Finish()
		return
	}
	p.Log.Debug("tcp proxy: open sent", "stream", streamID)

	done := make(chan struct{})
	go func() {
		defer close(done)
		p.pumpAgentToClient(stream, conn, t)
	}()

	// Forward buffered bytes + anything else the client sends.
	if len(buffered) > 0 {
		if err := stream.SendData(buffered, p.Cfg.MaxFrameSize); err != nil {
			p.Log.Warn("tcp proxy: buffered send failed", "err", err)
		}
		t.AddBytes(len(buffered), 0)
	}
	buf := make([]byte, 32*1024)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			if sendErr := stream.SendData(buf[:n], p.Cfg.MaxFrameSize); sendErr != nil {
				break
			}
			t.AddBytes(n, 0)
		}
		if err != nil {
			break
		}
	}
	_ = stream.SendClose()
	stream.Finish()
	<-done
}

// resolveRoute returns the tunnel subdomain for a connection, any bytes already
// buffered from it, and the conn that must be forwarded (which differs from the
// original only when TLS is terminated: the caller must relay the decrypted
// stream, not the raw socket). With TLS enabled the SNI comes from the
// handshake; otherwise it is sniffed from the raw bytes.
func (p *Proxy) resolveRoute(conn net.Conn) (subdomain string, buffered []byte, forward net.Conn, err error) {
	if !p.Cfg.TCPTLS {
		subdomain, buffered, err = p.peekRoute(conn)
		if err != nil {
			return "", nil, conn, err
		}
		return p.normalizeSub(subdomain), buffered, conn, nil
	}

	tlsCfg := p.tcpTLSConfig()
	if tlsCfg == nil {
		p.Log.Error("tcp proxy: TLS enabled but no cert configured")
		return "", nil, conn, errors.New("tcp tls misconfigured")
	}
	tconn := tls.Server(conn, tlsCfg)
	if err := tconn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return "", nil, conn, err
	}
	if err := tconn.Handshake(); err != nil {
		p.Log.Debug("tcp proxy: tls handshake failed", "err", err)
		return "", nil, conn, err
	}
	_ = tconn.SetDeadline(time.Time{})
	return p.normalizeSub(tconn.ConnectionState().ServerName), nil, tconn, nil
}

// peekRoute determines the subdomain from either a TLS SNI extension or an
// HTTP/1.1 Host header, returning the bytes already consumed so they can be
// replayed into the stream.
func (p *Proxy) peekRoute(conn net.Conn) (string, []byte, error) {
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	peeked := make([]byte, 0, 8192)
	buf := make([]byte, 4096)
	for len(peeked) < 16384 {
		n, err := conn.Read(buf)
		if n > 0 {
			peeked = append(peeked, buf[:n]...)
			// Resolve the route as soon as the Host header / SNI is present so
			// we don't wait out the deadline on already-complete requests.
			if sub, ok := tryRoute(peeked); ok {
				return sub, peeked, nil
			}
		}
		if err != nil && !isTimeout(err) {
			if len(peeked) == 0 {
				return "", nil, err
			}
			break
		}
		if err == nil {
			continue
		}
		// timeout: use what we have
		break
	}

	if sub, ok := tryRoute(peeked); ok {
		return sub, peeked, nil
	}
	return "", nil, errors.New("unable to route connection")
}

// tryRoute attempts SNI-first, then the HTTP Host header.
func tryRoute(peeked []byte) (string, bool) {
	if len(peeked) >= 5 && peeked[0] == 0x16 && peeked[1] == 0x03 {
		if sub, ok := parseSNI(peeked); ok && sub != "" {
			return sub, true
		}
	}
	return parseHostHeader(peeked)
}

// normalizeSub strips any port and the base-domain (or TCP base-domain)
// suffix — SNI / Host headers carry the fully-qualified name — and lower-cases
// the label. TCP tunnels are dialed as <sub>.<TCPBase> (e.g.
// abc123.tcp.itanishq.space) so the TCP base is tried first, then the regular
// base as a fallback.
func (p *Proxy) normalizeSub(sub string) string {
	sub = strings.ToLower(strings.TrimSpace(sub))
	if i := strings.IndexByte(sub, ':'); i > 0 {
		sub = sub[:i]
	}
	for _, base := range []string{p.Cfg.TCPBaseDomain(), p.Cfg.BaseDomain} {
		if base != "" {
			if s := strings.TrimSuffix(sub, "."+strings.ToLower(base)); s != sub {
				sub = s
				break
			}
		}
	}
	return sub
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// parseHostHeader extracts the Host from an HTTP/1.1 request head.
func parseHostHeader(b []byte) (string, bool) {
	low := make([]byte, len(b))
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		low[i] = c
	}
	idx := indexOf(low, []byte("\r\nhost:"))
	prefix := 2 // skip \r\n
	if idx < 0 {
		idx = indexOf(low, []byte("\nhost:"))
		prefix = 1
	}
	if idx < 0 {
		return "", false
	}
	valStart := idx + prefix + len("host:")
	lineEnd := valStart
	for lineEnd < len(b) && b[lineEnd] != '\r' && b[lineEnd] != '\n' {
		lineEnd++
	}
	host := stringsTrimSpaceBytes(b[valStart:lineEnd])
	if host == "" {
		return "", false
	}
	return host, true
}

func indexOf(hay, needle []byte) int {
	if len(needle) == 0 {
		return 0
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		ok := true
		for j := range needle {
			if hay[i+j] != needle[j] {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

func stringsTrimSpaceBytes(b []byte) string {
	start, end := 0, len(b)
	for start < end && (b[start] == ' ' || b[start] == '\t' || b[start] == '\r' || b[start] == '\n') {
		start++
	}
	for end > start && (b[end-1] == ' ' || b[end-1] == '\t' || b[end-1] == '\r' || b[end-1] == '\n') {
		end--
	}
	return string(b[start:end])
}

// tcpTLSConfig returns the server TLS config for the public TCP proxy, loading
// the wildcard cert once. A nil config means TLS is misconfigured; callers must
// guard on it.
func (p *Proxy) tcpTLSConfig() *tls.Config {
	p.tlsOnce.Do(func() {
		cert, err := tls.LoadX509KeyPair(p.Cfg.TCPTLSCert, p.Cfg.TCPTLSKey)
		if err != nil {
			p.Log.Error("tcp proxy: load cert failed", "cert", p.Cfg.TCPTLSCert, "key", p.Cfg.TCPTLSKey, "err", err)
			return
		}
		p.tlsCfg = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
	})
	return p.tlsCfg
}

// parseSNI extracts the server name from a TLS ClientHello.
func parseSNI(b []byte) (string, bool) {
	// b[0]=0x16 handshake, b[1..3]=record len; expect at least 9 bytes.
	if len(b) < 9 {
		return "", false
	}
	hsLen := int(b[3])<<8 | int(b[4])
	if hsLen+5 > len(b) {
		hsLen = len(b) - 5
	}
	pos := 5 + 1 // handshake type
	if pos+3 > len(b) {
		return "", false
	}
	pos += 3 // handshake length (3 bytes)

	if pos+2 > len(b) {
		return "", false
	}
	pos += 2 // client version

	if pos+32 > len(b) {
		return "", false
	}
	pos += 32 // random

	if pos+1 > len(b) {
		return "", false
	}
	sidLen := int(b[pos])
	pos++
	if pos+sidLen+2 > len(b) {
		return "", false
	}
	pos += sidLen

	ciphersLen := int(b[pos])<<8 | int(b[pos+1])
	pos += 2
	if pos+ciphersLen > len(b) {
		return "", false
	}
	pos += ciphersLen

	if pos+1 > len(b) {
		return "", false
	}
	compLen := int(b[pos])
	pos++
	if pos+compLen+2 > len(b) {
		return "", false
	}
	pos += compLen

	extLen := int(b[pos])<<8 | int(b[pos+1])
	pos += 2
	end := pos + extLen
	if end > len(b) {
		end = len(b)
	}
	for pos+4 <= end {
		extType := int(b[pos])<<8 | int(b[pos+1])
		extDataLen := int(b[pos+2])<<8 | int(b[pos+3])
		pos += 4
		if pos+extDataLen > end {
			break
		}
		if extType == 0 && extDataLen >= 5 { // server_name
			inner := b[pos:]
			listLen := int(inner[0])<<8 | int(inner[1])
			if listLen+2 <= len(inner) && inner[2] == 0 {
				nameLen := int(inner[3])<<8 | int(inner[4])
				if 5+nameLen <= len(inner) {
					return string(inner[5 : 5+nameLen]), true
				}
			}
		}
		pos += extDataLen
	}
	return "", false
}
