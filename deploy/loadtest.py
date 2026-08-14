#!/usr/bin/env python3
"""
OctoPort load tester - send random load through tunnels exposed by the client.

Usage:
  # hammer existing tunnels discovered from `octoport list`
  python3 deploy/loadtest.py --duration 30 --workers 8

  # hit specific tunnel URLs directly
  python3 deploy/loadtest.py --url http://a1b2c3d4.itanishq.space
  python3 deploy/loadtest.py --url http://a1b2c3d4.itanishq.space --url http://e5f6g7h8.itanishq.space

  # turnkey: start a local origin, expose it through a NEW tunnel, hammer it
  python3 deploy/loadtest.py --auto-tunnel 8123 --duration 20 --workers 8

  # raw TCP tunnel (bytes over sockets)
  python3 deploy/loadtest.py --url tcp://a1b2c3d4.tcp.itanishq.space:443 --tcp --duration 15

Environment: honors OCTOPORT_API_URL / OCTOPORT_WS_URL / OCTOPORT_BASE_DOMAIN for the
auto-tunnel mode (same env vars the `octoport` CLI uses). Requires the `octoport`
binary on PATH for --auto-tunnel and URL auto-discovery.
"""

import argparse
import json
import os
import random
import re
import shutil
import socket
import statistics
import subprocess
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.request import Request, urlopen
from urllib.error import URLError, HTTPError

METHODS = ("GET", "POST", "PUT", "PATCH")
BOUNDARY = b"-----octoport-loadtest-----"


class OriginHandler(BaseHTTPRequestHandler):
    def _serve(self, size):
        body = os.urandom(size)
        self.send_response(200)
        self.send_header("Content-Type", "application/octet-stream")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _serve_echo(self):
        # /echo returns exactly what was uploaded, so one request pushes data
        # upstream AND downstream — a round-trip both directions.
        size = int(self.headers.get("Content-Length", "0") or "0")
        body = self.rfile.read(size) if size else b""
        self.send_response(200)
        self.send_header("Content-Type", "application/octet-stream")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path.startswith("/echo"):
            self._serve_echo()
            return
        size = random.randint(0, 8192)
        self._serve(size)

    def do_POST(self):
        if self.path.startswith("/echo"):
            self._serve_echo()
            return
        size = int(self.headers.get("Content-Length", "0") or "0")
        if size:
            self.rfile.read(size)
        self._serve(random.randint(0, 8192))

    def do_PUT(self):
        self.do_POST()

    def do_PATCH(self):
        self.do_POST()

    def log_message(self, *args):
        pass


def start_tcp_origin(port):
    """A tiny echo server so --tcp --auto-tunnel has a real origin to hit."""
    def handle(conn):
        try:
            while True:
                chunk = conn.recv(65536)
                if not chunk:
                    break
                conn.sendall(chunk)
        except OSError:
            pass
        finally:
            conn.close()

    srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind(("127.0.0.1", port))
    srv.listen(64)

    def accept_loop():
        while True:
            try:
                conn, _ = srv.accept()
            except OSError:
                return
            threading.Thread(target=handle, args=(conn,), daemon=True).start()

    thread = threading.Thread(target=accept_loop, daemon=True)
    thread.start()
    return srv


class Metrics:
    def __init__(self):
        self.lock = threading.Lock()
        self.latencies = []
        self.ok = 0
        self.errors = 0
        self.sent = 0
        self.received = 0
        self.started = time.monotonic()
        self.error_samples = []

    def record(self, latency, sent, received, error=None):
        with self.lock:
            self.latencies.append(latency)
            self.sent += sent
            self.received += received
            if error is None:
                self.ok += 1
            else:
                self.errors += 1
                if len(self.error_samples) < 10:
                    self.error_samples.append(error)

    def snapshot(self):
        with self.lock:
            return {
                "ok": self.ok,
                "errors": self.errors,
                "sent": self.sent,
                "received": self.received,
                "elapsed": time.monotonic() - self.started,
                "latencies": list(self.latencies),
                "error_samples": list(self.error_samples),
            }


class RateLimiter:
    def __init__(self, rate):
        self.interval = 1.0 / rate if rate > 0 else 0.0
        self.lock = threading.Lock()
        self.next_ok = time.monotonic()

    def wait(self):
        if self.interval <= 0.0:
            return
        with self.lock:
            now = time.monotonic()
            if now < self.next_ok:
                delay = self.next_ok - now
                self.next_ok += self.interval
            else:
                delay = 0.0
                self.next_ok = now + self.interval
        if delay:
            time.sleep(delay)


def http_load(url, payload, metrics, echo=False):
    method = "POST" if echo else random.choice(METHODS)
    headers = {"X-OctoPort-Load": "1"}
    body = b""
    sent = 0
    if method in ("POST", "PUT", "PATCH"):
        body = b"--octoport-loadtest--\r\n" + payload + b"\r\n--octoport-loadtest----\r\n"
        headers["Content-Type"] = "multipart/form-data; boundary=%s" % BOUNDARY.decode()
        headers["Content-Length"] = str(len(body))
        sent = len(body)
    # For duplex (echo=True), try /echo endpoint first; fall back to base URL if 404
    if echo:
        path = url.rstrip("/") + "/echo"
    else:
        path = url
    req = Request(path, data=body if body else None, headers=headers, method=method)
    start = time.monotonic()
    try:
        with urlopen(req, timeout=15) as resp:
            data = resp.read()
            latency = (time.monotonic() - start) * 1000.0
            metrics.record(latency, sent, len(data))
    except HTTPError as e:
        # If /echo returns 404 in duplex mode, retry without /echo
        if echo and e.code == 404:
            path = url
            req = Request(path, data=body if body else None, headers=headers, method=method)
            try:
                with urlopen(req, timeout=15) as resp:
                    data = resp.read()
                    latency = (time.monotonic() - start) * 1000.0
                    metrics.record(latency, sent, len(data))
                    return
            except Exception:
                pass
        latency = (time.monotonic() - start) * 1000.0
        metrics.record(latency, sent, 0, error=classify_http_error(e))
    except (URLError, OSError) as e:
        latency = (time.monotonic() - start) * 1000.0
        metrics.record(latency, sent, 0, error=str(e).split(":")[-1].strip()[:120])


def classify_http_error(e):
    code = getattr(e, "code", 0)
    if code in (502, 503, 504):
        return f"origin {code}: tunnel not serving (local origin down?)"
    if code in (404,):
        return "404: tunnel not found or expired"
    return f"HTTP {code}: {str(e).split(':')[-1].strip()[:80]}"


def build_client_hello(server_name):
    name = server_name.encode()
    ext_sni = b"\x00" + len(name).to_bytes(2, "big") + name
    ext_data = len(ext_sni).to_bytes(2, "big") + ext_sni
    ext_server_name = (0).to_bytes(2, "big") + len(ext_data).to_bytes(2, "big") + ext_data
    body = (
        b"\x01"  # ClientHello
        + (0).to_bytes(3, "big")  # placeholder length, patched below
        + b"\x03\x03"  # TLS 1.2
        + bytes(range(0, 32))  # random
        + b"\x00"  # session id len
        + b"\x00\x02\x13\x01"  # TLS_AES_128_GCM_SHA256
        + b"\x01\x00"  # compression: null
        + len(ext_server_name).to_bytes(2, "big")
        + ext_server_name
    )
    body = body[:1] + (len(body) - 4).to_bytes(3, "big") + body[4:]
    return b"\x16\x03\x01" + len(body).to_bytes(2, "big") + body


def tcp_load(url, subdomain, payload, metrics):
    host, port = parse_url(url)
    start = time.monotonic()
    sent = 0
    received = 0
    try:
        with socket.create_connection((host, port), timeout=15) as sock:
            hello = build_client_hello(subdomain)
            sock.sendall(hello)
            sent += len(hello)
            sock.sendall(payload)
            sent += len(payload)
            sock.settimeout(15)
            while received < sent:
                try:
                    chunk = sock.recv(min(65536, sent - received))
                except socket.timeout:
                    break
                if not chunk:
                    break
                received += len(chunk)
        latency = (time.monotonic() - start) * 1000.0
        metrics.record(latency, sent, received)
    except OSError as e:
        latency = (time.monotonic() - start) * 1000.0
        metrics.record(latency, sent, received, error=str(e)[:120])


def tcp_duplex_load(url, subdomain, size, chunk, metrics):
    """Stream `size` bytes up and down CONCURRENTLY on one socket, the way a
    real bidirectional origin would. Sending and reading happen in parallel so
    neither direction waits on the other (true full-duplex load)."""
    host, port = parse_url(url)
    start = time.monotonic()
    sent = 0
    received = 0
    conn_error = None
    try:
        sock = socket.create_connection((host, port), timeout=15)
        sock.settimeout(30)
    except OSError as e:
        metrics.record(0, 0, 0, error=str(e)[:120])
        return

    stop = threading.Event()

    def sender():
        nonlocal sent
        try:
            sock.sendall(build_client_hello(subdomain))
            sent += len(build_client_hello(subdomain))
            remaining = size
            while remaining > 0 and not stop.is_set():
                block = os.urandom(min(chunk, remaining))
                sock.sendall(block)
                sent += len(block)
                remaining -= len(block)
        except OSError as e:
            nonlocal conn_error
            conn_error = e

    def receiver():
        nonlocal received
        try:
            while not stop.is_set():
                data = sock.recv(65536)
                if not data:
                    break
                received += len(data)
        except OSError as e:
            nonlocal conn_error
            conn_error = e

    st = threading.Thread(target=sender, daemon=True)
    rt = threading.Thread(target=receiver, daemon=True)
    st.start()
    rt.start()
    st.join(timeout=40)
    if st.is_alive():
        conn_error = conn_error or TimeoutError("sender stalled (no full duplex? origin not an echo server?)")
    stop.set()
    rt.join(timeout=5)
    sock.close()

    latency = (time.monotonic() - start) * 1000.0
    err = str(conn_error)[:120] if conn_error else None
    metrics.record(latency, sent, received, error=err)


def worker(url, protocol, min_payload, max_payload, think_ms, metrics, limiter, stop, duplex):
    parsed = parse_url(url)
    subdomain = subdomain_for(url)
    while not stop.is_set():
        payload = os.urandom(random.randint(min_payload, max_payload))
        if protocol == "tcp":
            if duplex:
                tcp_duplex_load(url, subdomain, random.randint(max_payload, max_payload * 4), max(4096, max_payload // 2), metrics)
            else:
                tcp_load(url, subdomain, payload, metrics)
        else:
            http_load(url, payload, metrics, echo=duplex)
        limiter.wait()
        if think_ms > 0:
            time.sleep(random.uniform(0.0, think_ms) / 1000.0)


def subdomain_for(url):
    host = parse_url(url)[0]
    return host.split(":")[0]


def parse_url(url):
    m = re.match(r"^(?:tcp|http)://([^:/]+):(\d+)/?", url)
    if not m:
        raise SystemExit(f"cannot parse URL (need scheme://host:port): {url}")
    return m.group(1), int(m.group(2))


def verify_tunnel(url, protocol):
    """Verify a tunnel is reachable by making a test request."""
    import urllib.request
    import urllib.error
    try:
        if protocol == "tcp":
            # For TCP, we can't easily verify without knowing the protocol
            # Just try to connect
            host, port = parse_url(url)
            sock = socket.create_connection((host, port), timeout=5)
            sock.close()
            return True
        else:
            # Use GET with a small payload to trigger actual routing (HEAD may bypass)
            req = urllib.request.Request(url, method="GET")
            req.add_header("X-OctoPort-Load", "verify")
            with urllib.request.urlopen(req, timeout=5) as resp:
                body = resp.read().decode().lower()
                # Check for tunnel-specific errors in response body
                if "tunnel not found" in body or "tunnel not serving" in body or "tunnel not bound" in body or "unknown host" in body:
                    return False
                return True
    except urllib.error.HTTPError as e:
        body = e.read().decode().lower()
        # Check for tunnel-specific errors
        if e.code in (404, 502, 503):
            if "tunnel not found" in body or "tunnel not serving" in body or "tunnel not bound" in body or "unknown host" in body or "agent offline" in body:
                return False
        # Other HTTP errors (like 500 from origin) mean tunnel works but origin has issues
        return True
    except Exception:
        return False


def discover_from_cli(octoport_bin):
    try:
        out = subprocess.run([octoport_bin, "list"], capture_output=True, text=True, timeout=30)
    except (FileNotFoundError, subprocess.TimeoutExpired) as e:
        raise SystemExit(f"could not run `{octoport_bin} list`: {e}")
    urls = []
    for line in out.stdout.splitlines():
        m = re.match(r"\S+\s+(\S+)\s+proto=", line)
        if m:
            urls.append(m.group(1))
    if not urls:
        raise SystemExit("no live tunnels found; run `octoport expose <port>` first or pass --url")
    return urls


def start_origin(port):
    server = ThreadingHTTPServer(("127.0.0.1", port), OriginHandler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    return server


def expose_tunnel(octoport_bin, port, protocol="http"):
    env = dict(os.environ)
    for var in ("OCTOPORT_API_URL", "OCTOPORT_WS_URL", "OCTOPORT_BASE_DOMAIN"):
        if var in env:
            env.setdefault(var, env[var])
    cmd = [octoport_bin, "expose", str(port)]
    if protocol == "tcp":
        cmd += ["--protocol", "tcp"]
    proc = subprocess.Popen(
        cmd,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        env=env,
    )
    try:
        while True:
            line = proc.stdout.readline()
            if not line:
                break
            m = re.search(r"Public URL:\s*(\S+)", line)
            if m:
                return proc, m.group(1)
    except Exception:
        pass
    proc.terminate()
    raise SystemExit(f"could not read Public URL from `{octoport_bin} expose`")


def format_bytes(n):
    for unit in ("B", "KiB", "MiB", "GiB"):
        if n < 1024.0 or unit == "GiB":
            return f"{n:.2f} {unit}" if unit != "B" else f"{int(n)} B"
        n /= 1024.0
    return f"{n:.2f} B"


def main():
    ap = argparse.ArgumentParser(description="Send random load through OctoPort tunnels.")
    ap.add_argument("--url", action="append", default=[], help="tunnel URL (repeatable); defaults to `octoport list`")
    ap.add_argument("--tcp", action="store_true", help="treat URLs as raw TCP tunnels")
    ap.add_argument("--duration", type=float, default=30.0, help="seconds to run (default 30)")
    ap.add_argument("--workers", type=int, default=4, help="concurrent workers (default 4)")
    ap.add_argument("--rate", type=int, default=0, help="global max requests/sec, 0 = unlimited (default 0)")
    ap.add_argument("--min-payload", type=int, default=64, help="min payload bytes (default 64)")
    ap.add_argument("--max-payload", type=int, default=16384, help="max payload bytes (default 16384)")
    ap.add_argument("--think", type=int, default=0, help="random delay between requests, ms (default 0)")
    ap.add_argument("--auto-tunnel", type=int, default=0, metavar="PORT", help="start a local origin and expose it via a new tunnel")
    ap.add_argument("--duplex", action="store_true", help="bidirectional load: for HTTP, uses /echo endpoint (falls back to regular request if unavailable); for TCP, concurrent send+recv on one socket (needs echo server)")
    ap.add_argument("--octoport-bin", default=None, help="path to the OctoPort CLI binary (default: `octoport` on PATH)")
    ap.add_argument("--json", action="store_true", help="print a JSON summary at the end")
    ap.add_argument("--verify", action="store_true", default=True, help="verify tunnel is reachable before starting load (default: True)")
    ap.add_argument("--no-verify", action="store_false", dest="verify", help="skip tunnel reachability verification")
    args = ap.parse_args()

    octoport_bin = args.octoport_bin or shutil.which("octoport")
    if args.auto_tunnel and not octoport_bin:
        raise SystemExit("--auto-tunnel needs the OctoPort CLI: install it or pass --octoport-bin")
    if not args.url and not args.auto_tunnel and not octoport_bin:
        raise SystemExit("could not find the OctoPort CLI (needed for URL discovery); pass --url or --octoport-bin")

    if args.min_payload < 0 or args.max_payload < args.min_payload:
        raise SystemExit("invalid payload range (--min-payload <= --max-payload)")
    if args.workers < 1:
        raise SystemExit("--workers must be >= 1")

    origin = None
    tunnel_proc = None
    urls = list(args.url)

    if args.auto_tunnel:
        if args.tcp:
            origin = start_tcp_origin(args.auto_tunnel)
        else:
            origin = start_origin(args.auto_tunnel)
        tunnel_proc, url = expose_tunnel(octoport_bin, args.auto_tunnel, "tcp" if args.tcp else "http")
        urls.append(url)
        print(f"auto tunnel up: {url}")

    if not urls:
        print("no --url given; discovering tunnels from `octoport list`...")
        urls = discover_from_cli(octoport_bin)

    protocol = "tcp" if args.tcp else "http"
    print(f"targets ({len(urls)}):")
    for u in urls:
        print(f"  {u}")
    print(f"protocol={protocol} duplex={'on' if args.duplex else 'off'} workers={args.workers} duration={args.duration:g}s "
          f"payload={args.min_payload}-{args.max_payload} B rate={'unlimited' if args.rate == 0 else args.rate}")

    # Verify tunnel reachability before starting load
    if args.verify:
        print("verifying tunnel reachability...")
        for u in urls:
            if not verify_tunnel(u, protocol):
                raise SystemExit(f"tunnel not reachable: {u}\n"
                                 f"  Make sure the OctoPort agent is running (`octoport expose <port>` or use --auto-tunnel)\n"
                                 f"  and the local origin service is listening on the target port.")
        print("all tunnels verified reachable")

    metrics = Metrics()
    limiter = RateLimiter(args.rate)
    stop = threading.Event()
    threads = []
    for _ in range(args.workers):
        url = random.choice(urls)
        t = threading.Thread(target=worker, args=(url, protocol, args.min_payload, args.max_payload, args.think, metrics, limiter, stop, args.duplex), daemon=True)
        t.start()
        threads.append(t)

    deadline = time.monotonic() + args.duration
    while time.monotonic() < deadline:
        time.sleep(1.0)
        snap = metrics.snapshot()
        elapsed = max(snap["elapsed"], 0.001)
        sys.stdout.write(
            f"\r  {snap['ok']} ok / {snap['errors']} err   {snap['ok'] / elapsed:.1f} req/s   "
            f"{format_bytes(snap['sent'] / elapsed)}/s up   {format_bytes(snap['received'] / elapsed)}/s down   "
            f"{len(threading.enumerate()) - 1} workers   "
        )
        sys.stdout.flush()

    stop.set()
    for t in threads:
        t.join(timeout=20)

    snap = metrics.snapshot()
    elapsed = max(snap["elapsed"], 0.001)
    print("\n")
    print("=" * 60)
    print("SUMMARY")
    print("=" * 60)
    print(f"  duration            {elapsed:.1f}s")
    print(f"  requests            {snap['ok']}")
    print(f"  errors              {snap['errors']}")
    print(f"  throughput          {snap['ok'] / elapsed:.1f} req/s")
    print(f"  data up             {format_bytes(snap['sent'])} ({format_bytes(snap['sent'] / elapsed)}/s)")
    print(f"  data down           {format_bytes(snap['received'])} ({format_bytes(snap['received'] / elapsed)}/s)")

    if snap["latencies"]:
        lat = sorted(snap["latencies"])
        n = len(lat)
        pct = lambda p: lat[min(n - 1, int(p * n))]
        print(f"  latency avg         {statistics.mean(lat):.1f} ms")
        print(f"  latency p50         {pct(0.50):.1f} ms")
        print(f"  latency p95         {pct(0.95):.1f} ms")
        print(f"  latency p99         {pct(0.99):.1f} ms")
        print(f"  latency max         {lat[-1]:.1f} ms")

    if snap["error_samples"]:
        print("\n  sample errors:")
        for e in snap["error_samples"]:
            print(f"    - {e}")

    if args.json:
        out = {
            "duration": round(elapsed, 1),
            "requests": snap["ok"],
            "errors": snap["errors"],
            "throughput": round(snap["ok"] / elapsed, 1),
            "bytes_up": snap["sent"],
            "bytes_down": snap["received"],
            "urls": urls,
        }
        if snap["latencies"]:
            lat = sorted(snap["latencies"])
            n = len(lat)
            out["latency_ms"] = {
                "avg": round(statistics.mean(lat), 1),
                "p50": round(lat[min(n - 1, int(0.50 * n))], 1),
                "p95": round(lat[min(n - 1, int(0.95 * n))], 1),
                "p99": round(lat[min(n - 1, int(0.99 * n))], 1),
            }
        print("\n" + json.dumps(out, indent=2))

    if tunnel_proc:
        tunnel_proc.terminate()
    if origin is not None:
        if isinstance(origin, socket.socket):
            try:
                origin.close()
            except OSError:
                pass
        else:
            try:
                origin.shutdown()
            except OSError:
                pass
            try:
                origin.server_close()
            except (AttributeError, OSError):
                pass


if __name__ == "__main__":
    main()
