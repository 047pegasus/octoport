package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"octoport/control-plane/internal/config"
)

func TestExtractSubdomain(t *testing.T) {
	p := &Proxy{Cfg: &config.Config{BaseDomain: "octoport.dev", ReservedSubdomains: []string{"www", "portainer"}}}
	cases := []struct {
		host string
		want string
		ok   bool
	}{
		{"abc123.octoport.dev", "abc123", true},
		{"abc123.octoport.dev:443", "abc123", true},
		{"X7k2.octoport.dev", "x7k2", true},              // lowercased
		{"my-web-app.octoport.dev", "my-web-app", true},  // dashes ok (custom subdomains)
		{"octoport.dev", "", false},
		{"www.octoport.dev", "", false},        // reserved label - not a tunnel
		{"Portainer.octoport.dev", "", false},  // reserved, case-insensitive
		{"evil.com", "", false},
		{"a.b.c.octoport.dev", "", false},        // dots rejected
		{"evil-octoport.dev", "", false},         // not a subdomain of octoport.dev
		{"x.itanishq.space", "", false},        // not under .octoport.dev
	}
	for _, c := range cases {
		got, ok := p.extractSubdomain(c.host)
		if got != c.want || ok != c.ok {
			t.Errorf("extractSubdomain(%q) = %q,%v want %q,%v", c.host, got, ok, c.want, c.ok)
		}
	}
}

func TestParseHostHeader(t *testing.T) {
	req := "GET / HTTP/1.1\r\nHost: abc123.octoport.dev:8080\r\nUser-Agent: curl/8\r\n\r\n"
	got, ok := parseHostHeader([]byte(req))
	if !ok || got != "abc123.octoport.dev:8080" {
		t.Fatalf("parseHostHeader = %q,%v", got, ok)
	}
}

func TestParseSNI(t *testing.T) {
	// Build a minimal TLS 1.2 ClientHello carrying an SNI for "abc123.octoport.dev".
	hello := buildClientHello("abc123.octoport.dev")
	got, ok := parseSNI(hello)
	if !ok || got != "abc123.octoport.dev" {
		t.Fatalf("parseSNI = %q,%v", got, ok)
	}
}

func TestNormalizeSub(t *testing.T) {
	p := &Proxy{Cfg: &config.Config{BaseDomain: "octoport.dev"}}
	cases := map[string]string{
		"abc123.octoport.dev":      "abc123",
		"abc123.octoport.dev:443":  "abc123",
		"X7K2.octoport.dev":        "x7k2",
		"abc123":                 "abc123",
		"abc123.octoport.dev.":     "abc123.octoport.dev.",
		"abc123.octoport.dev:8080": "abc123",
	}
	for in, want := range cases {
		if got := p.normalizeSub(in); got != want {
			t.Errorf("normalizeSub(%q) = %q want %q", in, got, want)
		}
	}
}

func TestNormalizeSubTCPBase(t *testing.T) {
	p := &Proxy{Cfg: &config.Config{BaseDomain: "itanishq.space", TCPBase: "tcp.itanishq.space"}}
	cases := map[string]string{
		"abc123.tcp.itanishq.space":      "abc123",
		"abc123.tcp.itanishq.space:443":  "abc123",
		"abc123.itanishq.space":          "abc123",
		"tcp.itanishq.space":             "tcp",
		"abc123.tcp.itanishq.space:2222": "abc123",
	}
	for in, want := range cases {
		if got := p.normalizeSub(in); got != want {
			t.Errorf("normalizeSub(%q) = %q want %q", in, got, want)
		}
	}
}

func TestConfigTCPURL(t *testing.T) {
	c := &config.Config{BaseDomain: "itanishq.space", TCPBase: "tcp.itanishq.space", TCPPort: 443}
	if got, want := c.TCPURL("abc123"), "abc123.tcp.itanishq.space:443"; got != want {
		t.Errorf("TCPURL = %q want %q", got, want)
	}
	// TCPBase defaults to BaseDomain when unset; the configured port is kept.
	c2 := &config.Config{BaseDomain: "octoport.dev", TCPPort: 8091}
	if got, want := c2.TCPURL("abc123"), "abc123.octoport.dev:8091"; got != want {
		t.Errorf("TCPURL default = %q want %q", got, want)
	}
}

// writeTestCert creates a self-signed cert for *.<base> and returns cert/key paths.
func writeTestCert(t *testing.T, base string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: base},
		DNSNames:     []string{base, "*." + base},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

// TestTCPResolveRouteTLS terminates a real TLS handshake and confirms the SNI
// routes to the tunnel subdomain.
func TestTCPResolveRouteTLS(t *testing.T) {
	certPath, keyPath := writeTestCert(t, "tcp.octoport.dev")
	p := &Proxy{Cfg: &config.Config{
		BaseDomain: "octoport.dev",
		TCPBase:    "tcp.octoport.dev",
		TCPTLS:     true,
		TCPTLSCert: certPath,
		TCPTLSKey:  keyPath,
	}}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	serverErr := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		sub, _, _, err := p.resolveRoute(conn)
		serverErr <- err
		if sub != "abc123" {
			serverErr <- nil
			t.Errorf("resolveRoute SNI = %q want abc123", sub)
			return
		}
		serverErr <- nil
	}()

	client := &tls.Config{InsecureSkipVerify: true, ServerName: "abc123.tcp.octoport.dev"}
	conn, err := tls.Dial("tcp", ln.Addr().String(), client)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if err := <-serverErr; err != nil {
		t.Fatalf("server: %v", err)
	}
}

// TestTCPResolveRoutePlaintext confirms the SNI/Host sniffing path still works
// when TLS termination is disabled.
func TestTCPResolveRoutePlaintext(t *testing.T) {
	p := &Proxy{Cfg: &config.Config{BaseDomain: "octoport.dev"}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	serverErr := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		sub, buffered, forward, err := p.resolveRoute(conn)
		if err != nil {
			serverErr <- err
			return
		}
		if sub != "abc123" {
			t.Errorf("resolveRoute = %q want abc123", sub)
		}
		if forward != conn {
			t.Errorf("forward conn should be the raw conn when TLS disabled")
		}
		if len(buffered) == 0 {
			t.Errorf("expected buffered Host bytes")
		}
		serverErr <- err
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Write([]byte("GET / HTTP/1.1\r\nHost: abc123.octoport.dev\r\n\r\n"))
	_ = conn.Close()
	if err := <-serverErr; err != nil {
		t.Fatalf("server: %v", err)
	}
}
