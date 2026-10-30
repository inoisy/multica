"""OAuth HTTP routes: metadata, registration, sign-in and consent, token and revoke. OAuthService makes the decisions."""
import json
from urllib.parse import parse_qs, quote, urlsplit

from . import consent_page
from .http import AuthLimiter
from .oauth_service import OAuthError, PageError, Unavailable

PRM_PATHS = ('/.well-known/oauth-protected-resource', '/.well-known/oauth-protected-resource/mcp')
AS_PATH = '/.well-known/oauth-authorization-server'
BASE = '/mcp/oauth'
PATHS = frozenset((*PRM_PATHS, AS_PATH, BASE + '/register', BASE + '/authorize', BASE + '/continue', BASE + '/consent',
                   BASE + '/token', BASE + '/revoke'))
MAX_FORM = 16 * 1024
FORM = 'application/x-www-form-urlencoded'


def _single(query):
    """Form or query parameters without repeats; a repeated name gives None, and the ambiguous request is rejected."""
    try:
        parsed = parse_qs(query, keep_blank_values=True, strict_parsing=bool(query), max_num_fields=40)
    except ValueError:
        return None
    return None if any(len(v) != 1 for v in parsed.values()) else {k: v[0] for k, v in parsed.items()}


class OAuthHttp:
    def __init__(self, service, config, clock=None):
        self.service, self.config = service, config
        extra = {'clock': clock} if clock else {}
        self.failures = AuthLimiter(20, 60.0, **extra)       # failed token/consent attempts per address
        self.authorizations = AuthLimiter(30, 60.0, **extra)  # /authorize requests: each one stores a row
        self.registrations = AuthLimiter(20, 3600.0, **extra)

    @staticmethod
    def owns(path):
        return path in PATHS

    def handle(self, h, path):
        ip = h.client_ip()
        try:
            if path in PRM_PATHS:
                return self._get_json(h, self.service.protected_resource_metadata())
            if path == AS_PATH:
                return self._get_json(h, self.service.authorization_server_metadata())
            if path == BASE + '/register':
                return self._register(h, ip)
            if path == BASE + '/authorize':
                return self._authorize(h, ip)
            if path == BASE + '/continue':
                return self._continue(h)
            if path == BASE + '/consent':
                return self._consent(h, ip)
            if path == BASE + '/token':
                return self._token(h, ip)
            return self._revoke(h, ip)
        except Unavailable:
            return h.send_html(503, consent_page.error('Multica is temporarily unavailable. Try again in a minute.'),
                               [('Retry-After', '30')])

    # --- metadata ---------------------------------------------------------------------------------

    @staticmethod
    def _get_json(h, body):
        if h.command not in ('GET', 'HEAD'):
            return h.reject(405, 'use GET', [('Allow', 'GET, HEAD')])
        h.send_json(200, body)

    # --- registration -----------------------------------------------------------------------------

    def _register(self, h, ip):
        if h.command != 'POST':
            return h.reject(405, 'use POST', [('Allow', 'POST')])
        if not h.origin_allowed():
            return h.reject(403, 'origin not allowed')
        if self.registrations.blocked(ip):
            return h.reject(429, 'too many registrations', [('Retry-After', '3600')])
        self.registrations.fail(ip)
        raw = h.read_body(MAX_FORM)
        if raw is None:
            return
        try:
            meta = json.loads(raw)
        except ValueError:
            return self._oauth_error(h, OAuthError('invalid_client_metadata', 'body is not JSON'))
        try:
            h.send_json(201, self.service.register(meta), [('Pragma', 'no-cache')])
        except OAuthError as e:
            self._oauth_error(h, e)

    # --- sign-in and consent ----------------------------------------------------------------------

    def _authorize(self, h, ip):
        if h.command != 'GET':
            return h.reject(405, 'use GET', [('Allow', 'GET')])
        if self.authorizations.blocked(ip):
            return h.reject(429, 'too many requests', [('Retry-After', '60')])
        self.authorizations.fail(ip)
        params = _single(urlsplit(h.path).query)
        if params is None:
            return h.send_html(400, consent_page.error('Invalid request.'))
        try:
            self.service.purge()
            req_id = self.service.begin_authorize(params)
        except PageError as e:
            return h.send_html(e.status, consent_page.error(str(e)))
        except OAuthError as e:
            return h.redirect(e.redirect)
        # A page, not a 302: a navigation started on another site would arrive without the session cookie (SameSite=Strict)
        h.send_html(200, consent_page.continue_page(f'{BASE}/continue?req={quote(req_id)}'))

    def _continue(self, h):
        if h.command != 'GET':
            return h.reject(405, 'use GET', [('Allow', 'GET')])
        params = _single(urlsplit(h.path).query) or {}
        req_id = params.get('req', '')
        view = self.service.pending_view(req_id)
        if view is None:
            return h.send_html(400, consent_page.error('The connection request expired or was not found.'))
        user = self.service.whoami(h.headers.get('Cookie', ''))
        if user is None:
            target = quote(f'{BASE}/continue?req={quote(req_id)}', safe='')
            return h.redirect(f'/login?next={target}')
        h.send_html(200, consent_page.consent(user.name, view['client_name'], view['return_host'], req_id,
                                              self.service.csrf_token(req_id, user.id)))

    def _consent(self, h, ip):
        if h.command != 'POST':
            return h.reject(405, 'use POST', [('Allow', 'POST')])
        origin = h.headers.get('Origin')
        if origin is not None and origin != self.config.public_url:
            return h.reject(403, 'origin not allowed')
        if self.failures.blocked(ip):
            return h.reject(429, 'too many failed attempts', [('Retry-After', '60')])
        raw = h.read_body(MAX_FORM)
        if raw is None:
            return
        form = _single(raw.decode('utf-8', errors='replace')) or {}
        decision = form.get('decision')
        if decision not in ('approve', 'deny'):
            self.failures.fail(ip)
            return h.send_html(400, consent_page.error('Invalid form.'))
        try:
            location = self.service.decide(h.headers.get('Cookie', ''), form.get('req', ''), form.get('csrf', ''),
                                           decision == 'approve')
        except OAuthError as e:
            self.failures.fail(ip)
            return h.send_html(e.status, consent_page.error(e.description))
        h.redirect(location)

    # --- token / revoke ---------------------------------------------------------------------------

    def _form(self, h, ip):
        """The parsed form, or None when a response was already sent."""
        if h.command != 'POST':
            h.reject(405, 'use POST', [('Allow', 'POST')])
        elif not h.origin_allowed():
            h.reject(403, 'origin not allowed')
        elif self.failures.blocked(ip):
            h.reject(429, 'too many failed attempts', [('Retry-After', '60')])
        elif h.headers.get('Content-Type', '').split(';')[0].strip().lower() != FORM:
            # The body was not read: close the connection, or its bytes would be parsed as the next request
            self._oauth_error(h, OAuthError('invalid_request', 'Content-Type must be ' + FORM), close=True)
        elif (raw := h.read_body(MAX_FORM)) is not None:
            form = _single(raw.decode('utf-8', errors='replace'))
            if form is None:
                self.failures.fail(ip)
                self._oauth_error(h, OAuthError('invalid_request', 'repeated parameters'))
            return form
        return None

    def _token(self, h, ip):
        form = self._form(h, ip)
        if form is None:
            return
        try:
            h.send_json(200, self.service.token(form), [('Pragma', 'no-cache')])
        except OAuthError as e:
            self.failures.fail(ip)
            self._oauth_error(h, e)
        except Unavailable:
            # Not invalid_grant: the client must not treat its token as revoked, and should retry later
            h.send_json(503, {'error': 'temporarily_unavailable'}, [('Retry-After', '30'), ('Pragma', 'no-cache')])

    def _revoke(self, h, ip):
        form = self._form(h, ip)
        if form is None:
            return
        self.service.revoke(form)
        h.send_json(200, {})

    @staticmethod
    def _oauth_error(h, e, close=False):
        body = {'error': e.error, **({'error_description': e.description} if e.description else {})}
        h.send_json(e.status, body, [('Pragma', 'no-cache')], close=close)
