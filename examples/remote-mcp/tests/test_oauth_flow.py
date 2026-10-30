import json
import unittest
from html import unescape
from urllib.parse import parse_qs, urlsplit

from oauth_support import CLAUDE_CALLBACK, RESOURCE, OAuthCase, pkce


class Metadata(OAuthCase):
    def test_missing_token_gets_401_pointing_to_metadata_and_is_not_a_failure(self):
        for _ in range(self.config.auth_fail_limit + 2):
            status, _, resp = self.http('POST', '/mcp', body={})
            self.assertEqual(status, 401)
        self.assertEqual(resp.getheader('WWW-Authenticate'),
                         'Bearer resource_metadata="https://multica.example.com/.well-known/oauth-protected-resource/mcp"')
        status, _, _ = self.http('POST', '/mcp', body={}, headers={'Authorization': 'Bearer wrong'})
        self.assertEqual(status, 401, 'a plain challenge must not block the address')

    def test_wrong_token_counts_as_guessing(self):
        for _ in range(self.config.auth_fail_limit):
            self.http('POST', '/mcp', body={}, headers={'Authorization': 'Bearer wrong'})
        status, _, _ = self.http('POST', '/mcp', body={}, headers={'Authorization': 'Bearer wrong'})
        self.assertEqual(status, 429)

    def test_protected_resource_and_root_variant(self):
        for path in ('/.well-known/oauth-protected-resource/mcp', '/.well-known/oauth-protected-resource'):
            status, text, _ = self.http('GET', path)
            self.assertEqual(status, 200)
            self.assertEqual(json.loads(text), {
                'resource': RESOURCE, 'authorization_servers': ['https://multica.example.com'],
                'scopes_supported': ['multica', 'offline_access'], 'bearer_methods_supported': ['header'],
                'resource_name': 'Multica'})

    def test_authorization_server(self):
        status, text, _ = self.http('GET', '/.well-known/oauth-authorization-server')
        meta = json.loads(text)
        self.assertEqual(status, 200)
        self.assertEqual(meta['issuer'], 'https://multica.example.com')
        self.assertEqual(meta['registration_endpoint'], 'https://multica.example.com/mcp/oauth/register')
        self.assertEqual(meta['code_challenge_methods_supported'], ['S256'])
        self.assertEqual(meta['token_endpoint_auth_methods_supported'], ['none'])
        self.assertEqual(meta['grant_types_supported'], ['authorization_code', 'refresh_token'])
        self.assertEqual(meta['response_types_supported'], ['code'])

    def test_metadata_is_get_only_and_needs_an_allowed_host(self):
        self.assertEqual(self.http('POST', '/.well-known/oauth-authorization-server', body={})[0], 405)
        self.assertEqual(self.http('GET', '/.well-known/oauth-authorization-server', headers={'Host': 'evil.example'})[0], 403)

    def test_without_oauth_the_routes_do_not_exist(self):
        self.handler = __import__('multica_mcp.http', fromlist=['make_handler']).make_handler(self.config, self.service, self.limiter)
        self.assertEqual(self.http('GET', '/mcp/oauth/authorize')[0], 404)


class Registration(OAuthCase):
    def test_claude_and_loopback_callbacks_register(self):
        status, body = self.register([CLAUDE_CALLBACK, 'http://localhost:54321/callback', 'http://127.0.0.1:1/callback'])
        self.assertEqual(status, 201)
        self.assertEqual(body['token_endpoint_auth_method'], 'none')
        self.assertNotIn('client_secret', body)
        self.assertEqual(self.http('GET', '/.well-known/oauth-authorization-server')[0], 200)

    def test_foreign_redirect_uris_are_rejected(self):
        for uri in ('https://evil.example/cb', 'http://evil.example/callback', 'https://claude.ai/other',
                    'http://localhost:1/other', 'http://localhost/callback?x=1', 'https://claude.ai.evil.example/api/mcp/auth_callback',
                    'javascript:alert(1)', 'http://user@localhost:1/callback', 'http://localhost:99999/callback'):
            with self.subTest(uri=uri):
                status, body = self.register([uri])
                self.assertEqual((status, body['error']), (400, 'invalid_redirect_uri'))

    def test_secret_auth_method_and_extra_grants_are_rejected(self):
        for extra in ({'token_endpoint_auth_method': 'client_secret_basic'}, {'grant_types': ['password']},
                      {'response_types': ['token']}, {'client_name': '\x00'}):
            with self.subTest(extra=extra):
                self.assertEqual(self.register(**{'redirect_uris': [CLAUDE_CALLBACK], **extra})[0], 400)

    def test_registrations_are_limited_per_address(self):
        statuses = [self.register()[0] for _ in range(22)]
        self.assertEqual(statuses[:20], [201] * 20)
        self.assertEqual(statuses[20], 429)

    def test_foreign_origin_and_non_json(self):
        self.assertEqual(self.http('POST', '/mcp/oauth/register', body={'redirect_uris': [CLAUDE_CALLBACK]},
                                   headers={'Origin': 'https://evil.example'})[0], 403)
        status, _, _ = self.http('POST', '/mcp/oauth/register', raw=b'{', headers={'Content-Type': 'application/json'})
        self.assertEqual(status, 400)


class Authorize(OAuthCase):
    def setUp(self):
        super().setUp()
        self.cid = self.client_id()
        self.verifier, self.challenge = pkce()

    def redirect_error(self, **kw):
        status, _, resp = self.authorize(self.cid, self.challenge, **kw)
        self.assertEqual(status, 302)
        return parse_qs(urlsplit(resp.getheader('Location')).query), resp.getheader('Location')

    def test_unknown_client_and_foreign_redirect_show_a_page_without_redirecting(self):
        for kw in ({'client_id': 'nope'}, {'redirect_uri': 'https://evil.example/cb'}):
            with self.subTest(kw=kw):
                cid = kw.get('client_id', self.cid)
                status, text, resp = self.authorize(cid, self.challenge, **{k: v for k, v in kw.items() if k != 'client_id'})
                self.assertEqual(status, 400)
                self.assertIsNone(resp.getheader('Location'))
                self.assertNotIn('evil.example', text.decode())

    def test_pkce_is_required_and_s256_only(self):
        self.assertEqual(self.redirect_error(code_challenge=None)[0]['error'], ['invalid_request'])
        self.assertEqual(self.redirect_error(code_challenge_method='plain')[0]['error'], ['invalid_request'])
        self.assertEqual(self.redirect_error(code_challenge='short')[0]['error'], ['invalid_request'])
        self.assertEqual(self.redirect_error(code_challenge_method=None)[0]['error'], ['invalid_request'])

    def test_wrong_resource_scope_and_response_type(self):
        self.assertEqual(self.redirect_error(resource='https://evil.example/mcp')[0]['error'], ['invalid_target'])
        self.assertEqual(self.redirect_error(scope='admin')[0]['error'], ['invalid_scope'])
        self.assertEqual(self.redirect_error(response_type='token')[0]['error'], ['unsupported_response_type'])

    def test_error_returns_state(self):
        query, _ = self.redirect_error(response_type='token', state='abc')
        self.assertEqual(query['state'], ['abc'])

    def test_repeated_parameters_are_rejected(self):
        status, _, _ = self.http('GET', f'/mcp/oauth/authorize?client_id={self.cid}&client_id={self.cid}')
        self.assertEqual(status, 400)

    def test_hop_page_instead_of_a_redirect(self):
        status, text, resp = self.authorize(self.cid, self.challenge)
        self.assertEqual(status, 200)
        self.assertIsNone(resp.getheader('Location'))
        self.assertIn('http-equiv="refresh"', text.decode())
        self.assertIn("frame-ancestors 'none'", resp.getheader('Content-Security-Policy'))


class Consent(OAuthCase):
    def setUp(self):
        super().setUp()
        self.cid = self.client_id()
        self.verifier, self.challenge = pkce()
        self.req = self.request_id(self.cid, self.challenge)

    def test_signed_out_redirects_to_multica_login_with_an_opaque_next(self):
        status, _, resp = self.consent_page(self.req, session=None)
        self.assertEqual(status, 302)
        location = resp.getheader('Location')
        self.assertTrue(location.startswith('/login?next=%2Fmcp%2Foauth%2Fcontinue%3Freq%3D' + self.req), location)
        self.assertNotIn(self.challenge, location)
        self.assertNotIn(self.cid, location)

    def test_stale_session_also_goes_to_login(self):
        self.assertEqual(self.consent_page(self.req, session='SESS-OLD')[0], 302)

    def test_screen_shows_user_application_and_return_address(self):
        status, html, resp = self.consent_page(self.req)
        text = unescape(html.decode())
        self.assertEqual(status, 200)
        for expected in ('Anna', 'Claude', 'claude.ai', 'all workspaces you belong to'):
            self.assertIn(expected, text)
        self.assertIn('value="approve"', text)
        self.assertIn('value="deny"', text)
        self.assertEqual(resp.getheader('Cache-Control'), 'no-store')

    def test_application_name_is_escaped(self):
        cid = self.client_id(client_name='<script>alert(1)</script>')
        req = self.request_id(cid, self.challenge)
        _, html, _ = self.consent_page(req)
        self.assertNotIn('<script>alert', html.decode())
        self.assertIn('&lt;script&gt;', html.decode())

    def test_consent_page_keeps_the_form_origin(self):
        # no-referrer makes browsers send Origin: null on the form POST, which is rejected
        _, _, resp = self.consent_page(self.req)
        self.assertEqual(resp.getheader('Referrer-Policy'), 'same-origin')

    def test_public_origin_passes_but_null_and_foreign_do_not(self):
        _, html, _ = self.consent_page(self.req)
        csrf = self.csrf_of(html)
        for origin in ('null', 'https://evil.example', 'http://multica.example.com'):
            with self.subTest(origin=origin):
                self.assertEqual(self.decide(self.req, csrf, headers={'Origin': origin})[0], 403)
        self.assertEqual(self.multica.created, [])
        self.assertEqual(self.decide(self.req, csrf, headers={'Origin': 'https://multica.example.com'})[0], 302)

    def test_loopback_return_address_is_shown_with_its_port(self):
        cid = self.client_id(['http://localhost:54321/callback'])
        req = self.request_id(cid, self.challenge, redirect_uri='http://localhost:54321/callback')
        _, html, _ = self.consent_page(req)
        self.assertIn('localhost:54321', html.decode())

    def test_consent_issues_a_code_and_an_expiring_pat(self):
        _, html, _ = self.consent_page(self.req)
        status, _, resp = self.decide(self.req, self.csrf_of(html))
        self.assertEqual(status, 302)
        location = resp.getheader('Location')
        self.assertTrue(location.startswith(CLAUDE_CALLBACK + '?'))
        query = parse_qs(urlsplit(location).query)
        self.assertEqual(query['state'], ['st-1'])
        self.assertEqual(len(self.multica.created), 1)
        name, days = self.multica.created[0]
        self.assertEqual(days, 90)
        self.assertTrue(name.startswith('MCP: Claude'), name)

    def test_deny_issues_no_pat(self):
        _, html, _ = self.consent_page(self.req)
        status, _, resp = self.decide(self.req, self.csrf_of(html), decision='deny')
        query = parse_qs(urlsplit(resp.getheader('Location')).query)
        self.assertEqual((status, query['error'], query['state']), (302, ['access_denied'], ['st-1']))
        self.assertNotIn('code', query)
        self.assertEqual(self.multica.created, [])

    def test_csrf_is_required_and_bound_to_user_and_request(self):
        _, html, _ = self.consent_page(self.req)
        good = self.csrf_of(html)
        other_req = self.request_id(self.cid, self.challenge)
        for csrf, session in (('', 'SESS-ANNA'), ('0' * 64, 'SESS-ANNA'), (good, 'SESS-ANNA2')):
            with self.subTest(csrf=csrf[:6], session=session):
                status, _, _ = self.decide(self.req, csrf, session=session)
                self.assertEqual(status, 403)
        self.assertEqual(self.decide(other_req, good)[0], 403, 'token from another request')
        self.assertEqual(self.multica.created, [])

    def test_foreign_origin_or_no_session_issues_no_pat(self):
        _, html, _ = self.consent_page(self.req)
        csrf = self.csrf_of(html)
        self.assertEqual(self.decide(self.req, csrf, headers={'Origin': 'https://evil.example'})[0], 403)
        self.assertEqual(self.decide(self.req, csrf, session=None)[0], 401)
        self.assertEqual(self.multica.created, [])

    def test_resubmission_does_not_issue_a_second_pat(self):
        _, html, _ = self.consent_page(self.req)
        csrf = self.csrf_of(html)
        self.assertEqual(self.decide(self.req, csrf)[0], 302)
        self.assertEqual(self.decide(self.req, csrf)[0], 400)
        self.assertEqual(len(self.multica.created), 1)

    def test_request_expires(self):
        self.clock.advance(11 * 60)
        self.assertEqual(self.consent_page(self.req)[0], 400)

    def test_multica_down_gives_503_and_no_pat(self):
        self.multica.down = True
        status, _, resp = self.consent_page(self.req)
        self.assertEqual((status, resp.getheader('Retry-After')), (503, '30'))

    def test_session_without_matching_csrf_creates_no_pat(self):
        # the session has no readable CSRF cookie: Multica refuses to create the PAT, so consent must fail
        _, html, _ = self.consent_page(self.req)
        status, _, _ = self.decide(self.req, self.csrf_of(html), headers={'Cookie': 'multica_auth=SESS-ANNA'})
        self.assertEqual(status, 401)
        self.assertEqual(self.multica.created, [])


if __name__ == '__main__':
    unittest.main()
