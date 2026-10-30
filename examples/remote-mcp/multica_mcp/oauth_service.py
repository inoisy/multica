"""Owns OAuth sign-in: client registration, consent, code + PKCE, refresh, token checks and revocation.

The authorization server and the resource server are one process. Multica checks who the person is by their browser
session; after explicit consent a separate PAT is issued for them. The MCP client only ever receives this service's own
tokens, never the PAT or the session. The HTTP layer routes requests here and decides nothing itself.

Encrypting stored PATs needs the `cryptography` package; it is imported lazily so bearer keys keep working on the
standard library alone.
"""
import base64
import hashlib
import hmac
import logging
import re
import secrets
import stat
import time
import unicodedata
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit

try:
    from cryptography.fernet import Fernet, InvalidToken
except ImportError:  # OAuth sign-in is optional; load_key reports the missing package at startup
    Fernet = None

    class InvalidToken(Exception):
        pass

from .config import ALL_WORKSPACES, Client, ConfigError
from .multica_auth import MulticaAuthError, session_from_cookie_header

log = logging.getLogger('multica_mcp.oauth')

SCOPE = 'multica'
SCOPES = ('multica', 'offline_access')
CODE_TTL = 120
ACCESS_TTL = 15 * 60
PENDING_TTL = 10 * 60
GRANT_TTL = 89 * 24 * 3600  # shorter than the PAT (90 days), so a refresh token never outlives the PAT behind it
PAT_DAYS = 90
MAX_CLIENTS = 2000
MAX_REDIRECT_URIS = 5
HOSTED_CALLBACKS = ('https://claude.ai/api/mcp/auth_callback', 'https://claude.com/api/mcp/auth_callback')
LOOPBACK_HOSTS = ('localhost', '127.0.0.1')
LOOPBACK_PATH = '/callback'
CHALLENGE_RE = re.compile(r'[A-Za-z0-9_-]{43}')
VERIFIER_RE = re.compile(r'[A-Za-z0-9._~-]{43,128}')
REQUEST_ID_RE = re.compile(r'[A-Za-z0-9_-]{22,64}')
MAX_STATE = 512


class OAuthError(Exception):
    """error is the OAuth error code (RFC 6749 section 5.2); status is the HTTP status of the token/register response."""

    def __init__(self, error, description='', status=400):
        super().__init__(description or error)
        self.error, self.description, self.status = error, description, status


class Unavailable(Exception):
    """Multica did not answer: access fails with a temporary error and the grant is left as is."""


class PageError(Exception):
    """An error before redirect_uri is trusted: show the person a page instead of redirecting to it."""

    def __init__(self, message, status=400):
        super().__init__(message)
        self.status = status


def _hash(token):
    return hashlib.sha256(token.encode()).hexdigest()


def _new_secret():
    return secrets.token_urlsafe(32)


def _loopback(uri):
    parts = urlsplit(uri)
    return parts.scheme == 'http' and parts.hostname in LOOPBACK_HOSTS and parts.path == LOOPBACK_PATH


def valid_redirect_uri(uri):
    if uri in HOSTED_CALLBACKS:
        return True
    try:
        parts = urlsplit(uri)
        parts.port  # noqa: B018 -- raises on an invalid port
    except ValueError:
        return False
    return _loopback(uri) and not parts.query and not parts.fragment and not parts.username and not parts.password


def redirect_matches(registered, requested):
    """Hosted callbacks match exactly; loopback ignores the port (RFC 8252 section 7.3) but keeps host and path."""
    if requested in registered:
        return True
    if not _loopback(requested):
        return False
    wanted = urlsplit(requested)
    for uri in registered:
        if _loopback(uri):
            known = urlsplit(uri)
            if known.hostname == wanted.hostname and known.path == wanted.path and not wanted.query and not wanted.fragment:
                return True
    return False


def _with_params(uri, params):
    parts = urlsplit(uri)
    query = parse_qsl(parts.query, keep_blank_values=True) + [(k, v) for k, v in params.items() if v is not None]
    return urlunsplit((parts.scheme, parts.netloc, parts.path, urlencode(query), ''))


def _printable(text, limit):
    cleaned = ''.join(c for c in unicodedata.normalize('NFC', text) if c.isprintable()).strip()
    return cleaned[:limit]


def load_key(path):
    if Fernet is None:
        raise ConfigError('MULTICA_MCP_OAUTH_KEY_FILE: OAuth sign-in needs the cryptography package '
                          '(pip install -r requirements-oauth.txt)')
    try:
        mode = path.stat().st_mode
        raw = path.read_bytes().strip()
    except OSError as e:
        raise ConfigError(f'MULTICA_MCP_OAUTH_KEY_FILE: file is not readable ({e.strerror})') from None
    if mode & (stat.S_IRWXG | stat.S_IRWXO):
        raise ConfigError('MULTICA_MCP_OAUTH_KEY_FILE: file must be accessible by its owner only (chmod 600)')
    try:
        Fernet(raw)
    except ValueError:
        raise ConfigError('MULTICA_MCP_OAUTH_KEY_FILE: expected a Fernet key (32 bytes, url-safe base64)') from None
    return raw


class OAuthService:
    def __init__(self, config, store, api, key, redactor, clock=time.time):
        self.config, self.store, self.api, self.redactor, self.clock = config, store, api, redactor, clock
        self._fernet = Fernet(key)
        self._csrf_key = hashlib.sha256(b'consent-csrf\0' + key).digest()
        self.issuer = config.public_url
        self.resource = config.public_url + '/mcp'

    # --- metadata ---------------------------------------------------------------------------------

    def protected_resource_metadata(self):
        return {'resource': self.resource, 'authorization_servers': [self.issuer], 'scopes_supported': list(SCOPES),
                'bearer_methods_supported': ['header'], 'resource_name': 'Multica'}

    def authorization_server_metadata(self):
        base = self.issuer + '/mcp/oauth'
        return {'issuer': self.issuer, 'authorization_endpoint': base + '/authorize', 'token_endpoint': base + '/token',
                'registration_endpoint': base + '/register', 'revocation_endpoint': base + '/revoke',
                'response_types_supported': ['code'], 'grant_types_supported': ['authorization_code', 'refresh_token'],
                'code_challenge_methods_supported': ['S256'], 'token_endpoint_auth_methods_supported': ['none'],
                'scopes_supported': list(SCOPES)}

    @property
    def challenge_header(self):
        return f'Bearer resource_metadata="{self.issuer}/.well-known/oauth-protected-resource/mcp"'

    # --- client registration (DCR) ----------------------------------------------------------------

    def register(self, meta):
        if not isinstance(meta, dict):
            raise OAuthError('invalid_client_metadata', 'expected a JSON object')
        uris = meta.get('redirect_uris')
        if (not isinstance(uris, list) or not uris or len(uris) > MAX_REDIRECT_URIS
                or not all(isinstance(u, str) and valid_redirect_uri(u) for u in uris)):
            raise OAuthError('invalid_redirect_uri', 'redirect_uris: only the Claude callbacks and loopback http://localhost|127.0.0.1/callback are allowed')
        if meta.get('token_endpoint_auth_method', 'none') != 'none':
            raise OAuthError('invalid_client_metadata', 'token_endpoint_auth_method: only none (public client)')
        grants = meta.get('grant_types', ['authorization_code'])
        if (not isinstance(grants, list) or not grants
                or not set(grants) <= {'authorization_code', 'refresh_token'} or not all(isinstance(g, str) for g in grants)):
            raise OAuthError('invalid_client_metadata', 'grant_types: authorization_code and refresh_token only')
        if meta.get('response_types', ['code']) != ['code']:
            raise OAuthError('invalid_client_metadata', 'response_types: code only')
        name = meta.get('client_name', 'MCP client')
        if not isinstance(name, str) or not _printable(name, 100):
            raise OAuthError('invalid_client_metadata', 'client_name: expected a non-empty string')
        name = _printable(name, 100)
        if self.store.count_clients() >= MAX_CLIENTS:
            raise OAuthError('invalid_client_metadata', 'too many registered clients', 503)
        client_id = secrets.token_urlsafe(24)
        self.store.add_client(client_id, name, uris, self.clock())
        return {'client_id': client_id, 'client_name': name, 'redirect_uris': uris, 'grant_types': ['authorization_code', 'refresh_token'],
                'response_types': ['code'], 'token_endpoint_auth_method': 'none', 'scope': ' '.join(SCOPES)}

    # --- authorization request --------------------------------------------------------------------

    def begin_authorize(self, params):
        """Id of the stored request. PageError: redirect_uri is not trusted; OAuthError.redirect: send the client back there."""
        client = self.store.get_client(params.get('client_id', ''))
        redirect_uri = params.get('redirect_uri', '')
        if not client:
            raise PageError('Unknown application. Add the connector again.')
        if not redirect_matches(client['redirect_uris'], redirect_uri):
            raise PageError('This return address is not registered for the application.')
        state = params.get('state')
        if state is not None and len(state) > MAX_STATE:
            raise PageError('The state parameter is too long.')

        def fail(error, description):
            exc = OAuthError(error, description)
            exc.redirect = _with_params(redirect_uri, {'error': error, 'error_description': description, 'state': state})
            return exc

        if params.get('response_type') != 'code':
            raise fail('unsupported_response_type', 'response_type=code is required')
        challenge = params.get('code_challenge', '')
        if params.get('code_challenge_method') != 'S256' or not CHALLENGE_RE.fullmatch(challenge):
            raise fail('invalid_request', 'PKCE is required: code_challenge_method=S256 and code_challenge')
        if params.get('resource', self.resource) != self.resource:
            raise fail('invalid_target', 'resource must be ' + self.resource)
        scope = params.get('scope', '').split()
        if not set(scope) <= set(SCOPES):
            raise fail('invalid_scope', 'unknown scope')
        req_id = secrets.token_urlsafe(24)
        self.store.put_pending(req_id, client['client_id'], redirect_uri, state, challenge, self.resource,
                               SCOPE, self.clock() + PENDING_TTL)
        return req_id

    def pending_view(self, req_id):
        """Data for the consent screen, or None when the request is unknown or expired."""
        if not REQUEST_ID_RE.fullmatch(req_id or ''):
            return None
        pending = self.store.get_pending(req_id, self.clock())
        if not pending:
            return None
        client = self.store.get_client(pending['client_id'])
        return {'client_name': client['name'], 'return_host': urlsplit(pending['redirect_uri']).netloc}

    def whoami(self, cookie_header):
        """The Multica user behind the browser session, or None when signed out. Unavailable: Multica did not answer."""
        session, _ = session_from_cookie_header(cookie_header)
        try:
            return self.api.me_by_session(session)
        except MulticaAuthError as e:
            if e.kind == 'unauthenticated':
                return None
            raise Unavailable() from None

    def csrf_token(self, req_id, user_id):
        return hmac.new(self._csrf_key, f'{req_id}\0{user_id}'.encode(), hashlib.sha256).hexdigest()

    def decide(self, cookie_header, req_id, csrf, approve):
        """Where to send the client back. Signed out: OAuthError('login_required'); wrong CSRF token: 'invalid_csrf'."""
        user = self.whoami(cookie_header)
        if user is None:
            raise OAuthError('login_required', 'sign in to Multica first', 401)
        if not hmac.compare_digest(self.csrf_token(req_id or '', user.id), csrf or ''):
            raise OAuthError('invalid_csrf', 'the consent form is stale, start connecting again', 403)
        pending = self.store.take_pending(req_id, self.clock())
        if not pending:
            raise OAuthError('expired_request', 'the connection request expired, start again', 400)
        redirect_uri, state = pending['redirect_uri'], pending['state']
        if not approve:
            return _with_params(redirect_uri, {'error': 'access_denied', 'state': state})
        client = self.store.get_client(pending['client_id'])
        session, csrf_cookie = session_from_cookie_header(cookie_header)
        stamp = time.strftime('%Y-%m-%d', time.gmtime(self.clock()))
        try:
            pat = self.api.create_pat(session, csrf_cookie, f'MCP: {client["name"][:40]} ({stamp})', PAT_DAYS)
        except MulticaAuthError as e:
            if e.kind == 'unauthenticated':
                raise OAuthError('login_required', 'the Multica session expired', 401) from None
            raise Unavailable() from None
        self.redactor.add(pat.token)
        now = self.clock()
        grant = {'id': secrets.token_urlsafe(16), 'client_id': client['client_id'], 'user_id': user.id, 'user_name': user.name,
                 'pat_id': pat.id, 'pat_cipher': self._fernet.encrypt(pat.token.encode()), 'scope': pending['scope'],
                 'created': now, 'expires': now + GRANT_TTL}
        code = _new_secret()
        self.store.create_grant_with_code(grant, _hash(code), {
            'client_id': client['client_id'], 'redirect_uri': redirect_uri, 'challenge': pending['challenge'],
            'resource': pending['resource'], 'expires': now + CODE_TTL})
        log.info('grant issued', extra={'ctx': {'user_id': user.id, 'client': client['name']}})
        return _with_params(redirect_uri, {'code': code, 'state': state})

    # --- token endpoint ---------------------------------------------------------------------------

    def token(self, form):
        client = self.store.get_client(form.get('client_id', ''))
        if not client:
            raise OAuthError('invalid_client', 'unknown client_id', 401)
        grant_type = form.get('grant_type')
        if grant_type == 'authorization_code':
            return self._exchange_code(client, form)
        if grant_type == 'refresh_token':
            return self._refresh(client, form)
        raise OAuthError('unsupported_grant_type', 'supported: authorization_code and refresh_token')

    def _exchange_code(self, client, form):
        code, verifier = form.get('code', ''), form.get('code_verifier', '')
        if not code or not VERIFIER_RE.fullmatch(verifier):
            raise OAuthError('invalid_request', 'code and code_verifier are required')
        row, reused = self.store.redeem_code(_hash(code))
        if row is None:
            raise OAuthError('invalid_grant', 'unknown code')
        if reused:
            self.store.revoke_grant(row['grant_id'])  # a replayed code suggests theft: revoke everything issued from it
            raise OAuthError('invalid_grant', 'code already used')
        challenge = base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).rstrip(b'=').decode()
        problems = (row['expires'] <= self.clock(), row['client_id'] != client['client_id'],
                    form.get('redirect_uri') != row['redirect_uri'],
                    not hmac.compare_digest(challenge.encode(), row['challenge'].encode()),
                    form.get('resource', row['resource']) != row['resource'])
        if any(problems):
            self.store.revoke_grant(row['grant_id'])
            raise OAuthError('invalid_grant', 'code does not match: expired, or a different client, redirect_uri, verifier or resource')
        grant = self.store.get_grant(row['grant_id'])
        self._check_grant(grant)
        return self._issue(grant)

    def _refresh(self, client, form):
        old = form.get('refresh_token', '')
        row = self.store.get_token(_hash(old)) if old else None
        if not row or row['kind'] != 'refresh':
            raise OAuthError('invalid_grant', 'unknown refresh_token')
        grant = self.store.get_grant(row['grant_id'])
        if grant is None or grant['client_id'] != client['client_id']:
            raise OAuthError('invalid_grant', 'refresh_token belongs to another client')
        if form.get('resource', self.resource) != self.resource:
            raise OAuthError('invalid_target', 'resource does not match')
        if row['used']:
            self.store.revoke_grant(grant['id'])  # reuse revokes the whole token family
            raise OAuthError('invalid_grant', 'refresh_token already used')
        if row['expires'] <= self.clock():
            raise OAuthError('invalid_grant', 'refresh_token expired')
        self._check_grant(grant)
        self._live(grant)  # a PAT revoked in Multica cannot be refreshed past
        return self._issue(grant, rotate=_hash(old))

    def _check_grant(self, grant):
        if grant is None or grant['revoked'] or grant['expires'] <= self.clock():
            raise OAuthError('invalid_grant', 'access was revoked or expired')

    def _issue(self, grant, rotate=None):
        now = self.clock()
        access, refresh = _new_secret(), _new_secret()
        access_hash, refresh_hash = _hash(access), _hash(refresh)
        if rotate:
            if not self.store.rotate_refresh(rotate, grant['id'], access_hash, now + ACCESS_TTL, refresh_hash, grant['expires']):
                self.store.revoke_grant(grant['id'])
                raise OAuthError('invalid_grant', 'refresh_token already used')
        else:
            self.store.put_tokens(grant['id'], access_hash, now + ACCESS_TTL, refresh_hash, grant['expires'])
        return {'access_token': access, 'token_type': 'Bearer', 'expires_in': ACCESS_TTL, 'refresh_token': refresh,
                'scope': SCOPE}

    def revoke(self, form):
        """Idempotent: an unknown token or one of another client is ignored. The PAT in Multica is not deleted."""
        token = form.get('token', '')
        row = self.store.get_token(_hash(token)) if token else None
        if row:
            grant = self.store.get_grant(row['grant_id'])
            if grant and grant['client_id'] == form.get('client_id'):
                self.store.revoke_grant(grant['id'])

    # --- token check on /mcp ----------------------------------------------------------------------

    def authenticate(self, token):
        """The Client for a /mcp request, or None for an unknown, expired or revoked token. Unavailable: Multica did not answer."""
        row = self.store.get_token(_hash(token)) if token else None
        if not row or row['kind'] != 'access' or row['expires'] <= self.clock():
            return None
        grant = self.store.get_grant(row['grant_id'])
        if grant is None or grant['revoked'] or grant['expires'] <= self.clock():
            return None
        try:
            pat, user = self._live(grant)
        except OAuthError:
            return None
        return Client(user.name, '', pat, (ALL_WORKSPACES,), False, ident=grant['user_id'], source='oauth')

    def _live(self, grant):
        """(PAT, user): the PAT decrypts and is valid in Multica right now; there is no positive cache."""
        try:
            pat = self._fernet.decrypt(grant['pat_cipher']).decode()
        except InvalidToken:
            raise OAuthError('invalid_grant', 'PAT does not decrypt: the service key changed') from None
        self.redactor.add(pat)
        try:
            user = self.api.me_by_pat(pat)
        except MulticaAuthError as e:
            if e.kind == 'unauthenticated':
                self.store.revoke_grant(grant['id'])
                raise OAuthError('invalid_grant', 'PAT was revoked or expired in Multica') from None
            raise Unavailable() from None
        if user.id != grant['user_id']:
            self.store.revoke_grant(grant['id'])
            raise OAuthError('invalid_grant', 'PAT belongs to another user')
        return pat, user

    def purge(self):
        self.store.purge(self.clock())
