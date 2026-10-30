"""Transport, the only public entry point. Rejection order: path, Host, Origin, rate limit, key or OAuth token, size, JSON."""
import hmac
import json
import logging
import threading
import time
from collections import defaultdict, deque
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit

from . import rpc
from .oauth_service import Unavailable

log = logging.getLogger('multica_mcp.http')
LOOPBACK = ('127.0.0.1', '::1')


class AuthLimiter:
    """Key guessing: after `limit` failures from one address within `window` seconds, answer 429 until the window ends."""

    def __init__(self, limit, window, clock=time.monotonic):
        self.limit, self.window, self.clock = limit, window, clock
        self._fails = defaultdict(deque)
        self._lock = threading.Lock()

    def blocked(self, ip):
        with self._lock:
            fails = self._fails[ip]
            while fails and fails[0] <= self.clock() - self.window:
                fails.popleft()
            return len(fails) >= self.limit

    def fail(self, ip):
        with self._lock:
            self._fails[ip].append(self.clock())


def _host_name(header):
    host = (header or '').strip().lower()
    if host.startswith('['):
        return host[1:host.find(']')] if ']' in host else host
    return host.rsplit(':', 1)[0] if host.count(':') == 1 else host


def make_handler(config, service, limiter, oauth=None):
    """oauth is an OAuthHttp or None; without it only the bearer keys from the configuration are accepted."""
    keys = [(c, c.key.encode()) for c in config.clients]

    class Handler(BaseHTTPRequestHandler):
        server_version = 'multica-mcp'
        sys_version = ''
        protocol_version = 'HTTP/1.1'
        timeout = 30

        def log_message(self, fmt, *args):  # structured logging happens in send_json
            pass

        def client_ip(self):
            peer = self.client_address[0]
            forwarded = self.headers.get('X-Forwarded-For', '')
            # Behind a reverse proxy on loopback the client is the last X-Forwarded-For entry; other peers cannot set it
            if peer in LOOPBACK and forwarded:
                return forwarded.split(',')[-1].strip()[:64] or peer
            return peer

        def send_json(self, status, body=None, headers=(), close=False):
            data = b'' if body is None else json.dumps(body, ensure_ascii=False).encode()
            self.send_response(status)
            self.send_header('Content-Type', 'application/json; charset=utf-8')
            self.send_header('Content-Length', str(len(data)))
            self.send_header('Cache-Control', 'no-store')
            self.send_header('X-Content-Type-Options', 'nosniff')
            for name, value in headers:
                self.send_header(name, value)
            if close:
                self.send_header('Connection', 'close')
                self.close_connection = True
            self.end_headers()
            if data and self.command != 'HEAD':
                self.wfile.write(data)
            log.info('request', extra={'ctx': {'method': self.command, 'path': self.path.split('?')[0],
                                               'status': status, 'ip': self.client_ip()}})

        def send_html(self, status, html, headers=()):
            data = html.encode()
            self.send_response(status)
            self.send_header('Content-Type', 'text/html; charset=utf-8')
            self.send_header('Content-Length', str(len(data)))
            self.send_header('Cache-Control', 'no-store')
            self.send_header('X-Content-Type-Options', 'nosniff')
            self.send_header('X-Frame-Options', 'DENY')
            # Not no-referrer: with it browsers send `Origin: null` on the consent form POST, and consent would fail
            self.send_header('Referrer-Policy', 'same-origin')
            self.send_header('Content-Security-Policy', "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
            for name, value in headers:
                self.send_header(name, value)
            self.end_headers()
            if self.command != 'HEAD':
                self.wfile.write(data)
            log.info('request', extra={'ctx': {'method': self.command, 'path': self.path.split('?')[0],
                                               'status': status, 'ip': self.client_ip()}})

        def redirect(self, location):
            self.send_response(302)
            self.send_header('Location', location)
            self.send_header('Content-Length', '0')
            self.send_header('Cache-Control', 'no-store')
            self.send_header('Referrer-Policy', 'no-referrer')
            self.end_headers()
            log.info('request', extra={'ctx': {'method': self.command, 'path': self.path.split('?')[0],
                                               'status': 302, 'ip': self.client_ip()}})

        def origin_allowed(self):
            origin = self.headers.get('Origin')
            return origin is None or origin in config.allowed_origins

        def read_body(self, limit):
            """The request body, or None when a rejection was already sent (missing Content-Length or over the limit)."""
            length = self.headers.get('Content-Length')
            if length is None or not length.isdigit():
                self.reject(411, 'Content-Length required')
            elif int(length) > limit:
                self.reject(413, 'request body too large')
            else:
                return self.rfile.read(int(length))
            return None

        def reject(self, status, message, headers=()):
            # The body was not read: close the connection, or its bytes would be parsed as the next request
            self.send_json(status, {'error': message}, headers, close=True)

        def authenticate(self, ip):
            """Client for the bearer key or OAuth access token; None means a 401/503 was already sent.

            A request without Authorization is the normal OAuth challenge, not guessing, so it does not count as a failure.
            """
            header = self.headers.get('Authorization', '')
            scheme, _, token = header.partition(' ')
            candidate = token.strip().encode()
            challenge = oauth.service.challenge_header if oauth else 'Bearer'
            if not header:
                self.reject(401, 'unauthorized', [('WWW-Authenticate', challenge)])
                return None
            # Compare against every key without an early exit, so timing does not tell which matched.
            found = None
            for client, key in keys:
                if hmac.compare_digest(candidate, key) and scheme.lower() == 'bearer':
                    found = client
            if found is None and oauth and scheme.lower() == 'bearer':
                try:
                    found = oauth.service.authenticate(token.strip())
                except Unavailable:
                    self.reject(503, 'authorization service unavailable', [('Retry-After', '30')])
                    return None
            if found is None:
                limiter.fail(ip)
                self.reject(401, 'unauthorized', [('WWW-Authenticate', challenge + ', error="invalid_token"' if oauth else challenge)])
            return found

        def route(self):
            path = urlsplit(self.path).path
            if path == '/healthz' and self.command == 'GET' and self.client_address[0] in LOOPBACK:
                return self.send_json(200, {'status': 'ok'})
            oauth_route = bool(oauth) and oauth.owns(path)
            if path != '/mcp' and not oauth_route:
                return self.reject(404, 'not found')
            if _host_name(self.headers.get('Host')) not in config.allowed_hosts:
                return self.reject(403, 'host not allowed')
            if oauth_route:
                return oauth.handle(self, path)
            if not self.origin_allowed():
                return self.reject(403, 'origin not allowed')
            ip = self.client_ip()
            if limiter.blocked(ip):
                return self.reject(429, 'too many failed attempts', [('Retry-After', str(int(config.auth_fail_window)))])
            client = self.authenticate(ip)
            if client is None:
                return
            if self.command != 'POST':
                return self.reject(405, 'use POST', [('Allow', 'POST')])
            version = self.headers.get('MCP-Protocol-Version')
            if version is not None and version not in rpc.PROTOCOLS:
                return self.reject(400, 'unsupported MCP protocol version')
            self.post_mcp(ip, client)

        def post_mcp(self, ip, client):
            raw = self.read_body(config.max_body)
            if raw is None:
                return
            try:
                message = json.loads(raw)
            except ValueError:
                return self.send_json(400, {'jsonrpc': '2.0', 'id': None, 'error': {'code': -32700, 'message': 'parse error'}})
            if isinstance(message, list):
                return self.send_json(400, {'jsonrpc': '2.0', 'id': None,
                                            'error': {'code': -32600, 'message': 'batch requests are not supported'}})
            response = rpc.handle(message, service, ip, client)
            if response is None:
                return self.send_json(202)
            self.send_json(200, response)

        do_GET = do_POST = do_PUT = do_DELETE = do_PATCH = do_HEAD = do_OPTIONS = route

    return Handler


def make_server(config, service, limiter=None, oauth=None):
    limiter = limiter or AuthLimiter(config.auth_fail_limit, config.auth_fail_window)
    server = ThreadingHTTPServer((config.host, config.port), make_handler(config, service, limiter, oauth))
    server.daemon_threads = True
    return server
