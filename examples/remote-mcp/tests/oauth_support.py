"""OAuth test fixtures: a fake Multica (sessions, /api/me, PAT creation), a controllable clock and the flow steps."""
import base64
import hashlib
import json
import re
import secrets
import unittest
from dataclasses import replace
from html import unescape
from urllib.parse import parse_qs, urlencode, urlsplit

try:
    from cryptography.fernet import Fernet
except ImportError:
    raise unittest.SkipTest('OAuth sign-in needs the cryptography package') from None

from support import McpCase
from multica_mcp.multica_auth import IssuedPat, MulticaAuthError, User
from multica_mcp.http import AuthLimiter, make_handler
from multica_mcp.oauth_http import OAuthHttp
from multica_mcp.oauth_service import OAuthService
from multica_mcp.oauth_store import OAuthStore
from multica_mcp.redaction import Redactor

CLAUDE_CALLBACK = 'https://claude.ai/api/mcp/auth_callback'
PUBLIC_URL = 'https://multica.example.com'
RESOURCE = PUBLIC_URL + '/mcp'
ANNA = User('user-anna', 'Anna')
ANNA2 = User('user-anna-2', 'Anna')  # another person with the same display name
SESSIONS = {'SESS-ANNA': ANNA, 'SESS-ANNA2': ANNA2}


class Clock:
    def __init__(self):
        self.now = 1_800_000_000.0

    def __call__(self):
        return self.now

    def advance(self, seconds):
        self.now += seconds


class FakeMultica:
    """The MulticaApi contract without a network: tests control sessions, PATs, revocation and an API outage."""

    def __init__(self):
        self.pats = {}  # token -> user
        self.created = []  # (PAT name, days)
        self.sessions = dict(SESSIONS)
        self.down = False
        self.me_pat_calls = 0

    def _check(self):
        if self.down:
            raise MulticaAuthError('unavailable', 'down')

    def me_by_session(self, session):
        self._check()
        if session not in self.sessions:
            raise MulticaAuthError('unauthenticated')
        return self.sessions[session]

    def me_by_pat(self, pat):
        self._check()
        self.me_pat_calls += 1
        if pat not in self.pats:
            raise MulticaAuthError('unauthenticated')
        return self.pats[pat]

    def create_pat(self, session, csrf, name, days):
        user = self.me_by_session(session)
        if csrf != 'csrf-' + session:
            raise MulticaAuthError('unauthenticated', 'CSRF')
        token = 'mul_oauth_' + secrets.token_hex(20)
        self.pats[token] = user
        self.created.append((name, days))
        return IssuedPat(f'pat-{len(self.created)}', token)

    def revoke_all(self):
        self.pats.clear()


def pkce():
    verifier = secrets.token_urlsafe(48)
    challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).rstrip(b'=').decode()
    return verifier, challenge


def cookie(session='SESS-ANNA'):
    return f'multica_auth={session}; multica_csrf_session=csrf-{session}'


class OAuthCase(McpCase):
    clients = ()  # OAuth only: no bearer keys

    def setUp(self):
        super().setUp()
        self.config = replace(self.config, public_url=PUBLIC_URL)
        self.clock = Clock()
        self.multica = FakeMultica()
        self.key = Fernet.generate_key()
        self.redactor = Redactor()
        self.audit = type(self.audit)(self.state / 'audit.jsonl', redactor=self.redactor)
        from multica_mcp.runner import Runner
        from multica_mcp.service import Service
        self.service = Service(Runner(self.config, redactor=self.redactor), self.audit, clock=self.clock)
        self.build_oauth()

    def build_oauth(self):
        """Builds OAuth again on top of the same state file, which is how a restart is tested."""
        self.store = OAuthStore(self.state / 'oauth.sqlite')
        self.addCleanup(self.store.close)
        self.oauth = OAuthService(self.config, self.store, self.multica, self.key, self.redactor, clock=self.clock)
        self.oauth_http = OAuthHttp(self.oauth, self.config, clock=self.clock)
        self.limiter = AuthLimiter(self.config.auth_fail_limit, self.config.auth_fail_window)
        self.handler = make_handler(self.config, self.service, self.limiter, self.oauth_http)

    def start_http(self):
        self.build_oauth()

    # --- flow steps ---------------------------------------------------------------------------

    def register(self, redirect_uris=(CLAUDE_CALLBACK,), **extra):
        body = {'redirect_uris': list(redirect_uris), 'client_name': 'Claude', 'token_endpoint_auth_method': 'none', **extra}
        status, text, _ = self.http('POST', '/mcp/oauth/register', body=body, headers={'Content-Type': 'application/json'})
        return status, json.loads(text)

    def client_id(self, redirect_uris=(CLAUDE_CALLBACK,), **extra):
        status, body = self.register(redirect_uris, **extra)
        self.assertEqual(status, 201, body)
        return body['client_id']

    def authorize(self, client_id, challenge, redirect_uri=CLAUDE_CALLBACK, state='st-1', **extra):
        query = {'response_type': 'code', 'client_id': client_id, 'redirect_uri': redirect_uri, 'state': state,
                 'code_challenge': challenge, 'code_challenge_method': 'S256', 'resource': RESOURCE, **extra}
        query = {k: v for k, v in query.items() if v is not None}
        return self.http('GET', '/mcp/oauth/authorize?' + urlencode(query))

    def request_id(self, client_id, challenge, **kw):
        status, text, _ = self.authorize(client_id, challenge, **kw)
        self.assertEqual(status, 200, text)
        return re.search(r'req=([A-Za-z0-9_-]+)', unescape(text.decode())).group(1)

    def consent_page(self, req_id, session='SESS-ANNA'):
        headers = {'Cookie': cookie(session)} if session else {}
        return self.http('GET', f'/mcp/oauth/continue?req={req_id}', headers=headers)

    def csrf_of(self, html):
        return re.search(r'name="csrf" value="([0-9a-f]+)"', html.decode()).group(1)

    def decide(self, req_id, csrf, decision='approve', session='SESS-ANNA', headers=None):
        form = urlencode({'req': req_id, 'csrf': csrf, 'decision': decision}).encode()
        h = {'Content-Type': FORM, **({'Cookie': cookie(session)} if session else {}), **(headers or {})}
        return self.http('POST', '/mcp/oauth/consent', raw=form, headers=h)

    def approved_code(self, client_id=None, redirect_uri=CLAUDE_CALLBACK, session='SESS-ANNA'):
        client_id = client_id or self.client_id()
        verifier, challenge = pkce()
        req = self.request_id(client_id, challenge, redirect_uri=redirect_uri)
        status, html, _ = self.consent_page(req, session)
        self.assertEqual(status, 200)
        status, _, resp = self.decide(req, self.csrf_of(html), session=session)
        self.assertEqual(status, 302)
        query = parse_qs(urlsplit(resp.getheader('Location')).query)
        self.assertEqual(query['state'], ['st-1'])
        return client_id, verifier, query['code'][0]

    def token(self, **form):
        status, text, _ = self.http('POST', '/mcp/oauth/token', raw=urlencode(form).encode(), headers={'Content-Type': FORM})
        return status, json.loads(text)

    def exchange(self, client_id, verifier, code, redirect_uri=CLAUDE_CALLBACK, **extra):
        return self.token(grant_type='authorization_code', client_id=client_id, code=code, code_verifier=verifier,
                          redirect_uri=redirect_uri, resource=RESOURCE, **extra)

    def connect(self, session='SESS-ANNA'):
        """A complete sign-in: (client_id, token endpoint response)."""
        client_id, verifier, code = self.approved_code(session=session)
        status, body = self.exchange(client_id, verifier, code)
        self.assertEqual(status, 200, body)
        return client_id, body

    def mcp(self, method, access, params=None):
        return self.rpc(method, params, key=access)

    def issue_get(self, access):
        return self.rpc('tools/call', {'name': 'multica', 'arguments': {'args': ['issue', 'get', 'TST-1'], 'workspace': 'acme'}}, key=access)


FORM = 'application/x-www-form-urlencoded'
