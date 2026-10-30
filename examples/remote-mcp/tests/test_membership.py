import json
import tempfile
import threading
import unittest
from unittest import mock
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

from support import OTHER_WS, WS, WORKSPACES, McpCase, can_listen_tcp
from multica_mcp.config import Client, Config, ConfigError, load_clients
from multica_mcp.errors import InvalidParams
from multica_mcp.multica_auth import MulticaApi, MulticaAuthError
from multica_mcp import oauth_service
from multica_mcp.oauth_service import load_key
from multica_mcp.service import Service
from multica_mcp.runner import Runner


class Clock:
    now = 1000.0

    def __call__(self):
        return self.now


class Membership(McpCase):
    clients = ()

    def setUp(self):
        super().setUp()
        self.clock = Clock()
        self.service = Service(Runner(self.config), self.audit, clock=self.clock, membership_ttl=30)
        self.anna = Client('Anna', '', 'mul_anna', ('*',), ident='u-anna', source='oauth')

    def run_issue(self, client, workspace):
        return self.service.call_tool('multica', {'args': ['issue', 'list'], 'workspace': workspace}, '', client)

    def listings(self):
        return len([c for c in self.cli.calls if c['argv'][:2] == ['workspace', 'list']])

    def set_workspaces(self, rows):
        self.cli.behavior['workspaces'] = rows
        self.cli.save()

    def test_all_user_workspaces_without_a_configured_list(self):
        for ref in ('acme', 'toons', 'hidden', OTHER_WS):
            with self.subTest(ref=ref):
                self.assertFalse(self.run_issue(self.anna, ref)[0])

    def test_workspace_may_be_omitted_when_there_is_only_one(self):
        self.set_workspaces([WORKSPACES[0]])
        self.run_issue(self.anna, None)
        self.assertEqual(self.cli.calls[-1]['env']['MULTICA_WORKSPACE_ID'], WS)
        self.set_workspaces(WORKSPACES)
        self.clock.now += 31
        with self.assertRaises(InvalidParams):
            self.run_issue(self.anna, None)

    def test_new_workspace_is_visible_on_the_next_call_without_a_restart(self):
        self.set_workspaces([WORKSPACES[0]])
        self.run_issue(self.anna, 'acme')
        with self.assertRaises(InvalidParams):
            self.run_issue(self.anna, 'toons')
        before = self.listings()
        self.set_workspaces(WORKSPACES[:2])
        self.run_issue(self.anna, 'toons')
        self.assertEqual(self.listings() - before, 1, 'a cache miss reloads exactly once')
        self.assertEqual(self.cli.calls[-1]['env']['MULTICA_WORKSPACE_ID'], OTHER_WS)

    def test_removal_from_a_workspace_closes_access_after_the_ttl(self):
        self.run_issue(self.anna, 'toons')
        self.set_workspaces([WORKSPACES[0]])
        self.clock.now += 31
        with self.assertRaises(InvalidParams):
            self.run_issue(self.anna, 'toons')
        launched = [c for c in self.cli.calls if c['argv'][:2] == ['issue', 'list']]
        self.assertEqual(len(launched), 1, 'no process ran after the removal')

    def test_cache_hit_does_not_reload_the_list(self):
        self.run_issue(self.anna, 'acme')
        self.run_issue(self.anna, 'toons')
        self.assertEqual(self.listings(), 1)
        self.clock.now += 31
        self.run_issue(self.anna, 'acme')
        self.assertEqual(self.listings(), 2)

    def test_unknown_workspace_reloads_once_and_is_refused(self):
        self.run_issue(self.anna, 'acme')
        with self.assertRaises(InvalidParams):
            self.run_issue(self.anna, 'nope')
        self.assertEqual(self.listings(), 2)

    def test_cache_is_keyed_by_ident_not_display_name(self):
        twin = Client('Anna', '', 'mul_twin', ('*',), ident='u-twin', source='oauth')
        self.run_issue(self.anna, 'acme')
        self.set_workspaces([WORKSPACES[1]])
        with self.assertRaises(InvalidParams):
            self.run_issue(twin, 'acme')
        self.assertEqual(self.run_issue(self.anna, 'acme')[0], False, 'the cache of the first Anna is untouched')
        tokens = [c['env']['MULTICA_TOKEN'] for c in self.cli.calls if c['argv'][:2] == ['workspace', 'list']]
        self.assertEqual(tokens, ['mul_anna', 'mul_twin', 'mul_twin'], 'the twin has their own cache: a miss and one reload with their token')

    def test_explicit_slug_list_works_as_before(self):
        limited = Client('limited', 'x' * 40, 'mul_lim', ('acme',))
        self.assertFalse(self.run_issue(limited, 'acme')[0])
        for ref in ('toons', 'hidden'):
            with self.assertRaises(InvalidParams):
                self.run_issue(limited, ref)

    def test_star_key_sees_every_workspace_of_the_pat_owner(self):
        starred = Client('everyone', 'y' * 40, 'mul_all', ('*',))
        self.assertFalse(self.run_issue(starred, 'hidden')[0])
        self.set_workspaces(WORKSPACES + [{'id': 'aaaaaaaa-0000-4000-8000-000000000009', 'name': 'Fresh', 'slug': 'fresh'}])
        self.assertFalse(self.run_issue(starred, 'fresh')[0])

    def test_uuid_from_the_explicit_list_needs_no_lookup(self):
        by_id = Client('X', 'x' * 40, 'mul_x', (WS,))
        self.service.call_tool('multica', {'args': ['issue', 'list']}, '', by_id)
        self.assertEqual(self.listings(), 0)

    def test_oauth_audit_has_the_user_and_the_method(self):
        self.run_issue(self.anna, 'acme')
        entry = self.audit_lines()[-1]
        self.assertEqual((entry['client'], entry['user_id'], entry['auth']), ('Anna', 'u-anna', 'oauth'))
        self.run_issue(Client('keyed', 'z' * 40, 'mul_z', ('acme',)), 'acme')
        self.assertNotIn('auth', self.audit_lines()[-1])


class ConfigCases(unittest.TestCase):
    def env(self, tmp, **extra):
        return {'MULTICA_API_URL': 'http://127.0.0.1:8080', 'MULTICA_MCP_STATE_DIR': tmp, **extra}

    def oauth_env(self, tmp, **extra):
        return self.env(tmp, **{'MULTICA_MCP_OAUTH_KEY_FILE': str(Path(tmp) / 'oauth.key'),
                                'MULTICA_MCP_PUBLIC_URL': 'https://multica.example.com/',
                                'MULTICA_MCP_ALLOWED_HOSTS': 'multica.example.com,127.0.0.1', **extra})

    def keyfile(self, tmp, mode=0o600, content=None):
        from cryptography.fernet import Fernet
        path = Path(tmp) / 'oauth.key'
        path.write_bytes(content or Fernet.generate_key())
        path.chmod(mode)
        return path

    def test_starts_with_oauth_only_and_no_keys(self):
        with tempfile.TemporaryDirectory() as tmp:
            config = Config.from_env(self.oauth_env(tmp))
            self.assertEqual(config.clients, ())
            self.assertEqual(config.public_url, 'https://multica.example.com')
            self.assertEqual(config.oauth_key_file, Path(tmp) / 'oauth.key')

    @unittest.skipIf(oauth_service.Fernet is None, 'needs the cryptography package')
    def test_key_file_loads(self):
        with tempfile.TemporaryDirectory() as tmp:
            self.assertEqual(len(load_key(self.keyfile(tmp))), 44)

    def test_oauth_needs_the_public_url_and_its_host_allowed(self):
        with tempfile.TemporaryDirectory() as tmp:
            env = self.oauth_env(tmp)
            for extra in ({'MULTICA_MCP_PUBLIC_URL': ''}, {'MULTICA_MCP_ALLOWED_HOSTS': '127.0.0.1'}):
                with self.subTest(extra=extra), self.assertRaises(ConfigError):
                    Config.from_env({**env, **extra})

    def test_missing_cryptography_is_a_clear_startup_error(self):
        with tempfile.TemporaryDirectory() as tmp, mock.patch.object(oauth_service, 'Fernet', None):
            with self.assertRaisesRegex(ConfigError, 'cryptography'):
                load_key(Path(tmp) / 'oauth.key')

    def test_no_clients_and_no_oauth_refuses_to_start(self):
        with tempfile.TemporaryDirectory() as tmp, self.assertRaises(ConfigError):
            Config.from_env(self.env(tmp))

    @unittest.skipIf(oauth_service.Fernet is None, 'needs the cryptography package')
    def test_encryption_key_mode_and_format(self):
        with tempfile.TemporaryDirectory() as tmp:
            with self.assertRaises(ConfigError):
                load_key(self.keyfile(tmp, mode=0o644))
            with self.assertRaises(ConfigError):
                load_key(self.keyfile(tmp, content=b'not-a-key'))
            with self.assertRaises(ConfigError):
                load_key(Path(tmp) / 'missing')

    def test_public_url_without_a_path(self):
        with tempfile.TemporaryDirectory() as tmp, self.assertRaises(ConfigError):
            Config.from_env(self.oauth_env(tmp, MULTICA_MCP_PUBLIC_URL='https://multica.example.com/mcp'))

    def test_star_in_clients_json(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'clients.json'
            path.write_text(json.dumps([{'name': 'everyone', 'key': 'k' * 40, 'token': 'mul_t', 'workspaces': ['*']}]))
            path.chmod(0o600)
            (client,) = load_clients(path, ())
            self.assertTrue(client.any_workspace)
            path.write_text(json.dumps([{'name': 'everyone', 'key': 'k' * 40, 'token': 'mul_t', 'workspaces': ['*', 'acme']}]))
            with self.assertRaises(ConfigError):
                load_clients(path, ())


@unittest.skipUnless(can_listen_tcp(), 'no TCP in this sandbox')
class MulticaApiOverHttp(unittest.TestCase):
    """Real HTTP: exactly what is sent to Multica (cookie, CSRF, body) and how the answers are parsed."""

    def setUp(self):
        self.seen = []
        test = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                test.seen.append(('GET', self.path, dict(self.headers), b''))
                if self.path == '/api/redirect':
                    self.send_response(302)
                    self.send_header('Location', 'http://127.0.0.1:1/')
                    self.end_headers()
                    return
                ok = 'multica_auth=GOOD' in self.headers.get('Cookie', '') or self.headers.get('Authorization') == 'Bearer mul_good'
                self.reply(200 if ok else 401, {'id': 'u1', 'name': ' Anna '} if ok else {'error': 'no'})

            def do_POST(self):
                body = self.rfile.read(int(self.headers['Content-Length']))
                test.seen.append(('POST', self.path, dict(self.headers), body))
                if self.headers.get('X-CSRF-Token') != 'tok':
                    return self.reply(403, {'error': 'CSRF'})
                self.reply(201, {'id': 'p1', 'token': 'mul_new', 'name': 'x'})

            def reply(self, status, body):
                data = json.dumps(body).encode()
                self.send_response(status)
                self.send_header('Content-Length', str(len(data)))
                self.end_headers()
                self.wfile.write(data)

        self.server = HTTPServer(('127.0.0.1', 0), Handler)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.api = MulticaApi(f'http://127.0.0.1:{self.server.server_port}', timeout=3)

    def test_session_is_sent_as_a_single_cookie(self):
        self.assertEqual(self.api.me_by_session('GOOD').name, 'Anna')
        self.assertEqual(self.seen[0][2]['Cookie'], 'multica_auth=GOOD')

    def test_refusal_and_missing_session(self):
        with self.assertRaises(MulticaAuthError) as ctx:
            self.api.me_by_session('BAD')
        self.assertEqual(ctx.exception.kind, 'unauthenticated')
        with self.assertRaises(MulticaAuthError):
            self.api.me_by_session('')

    def test_pat_goes_in_the_authorization_header(self):
        self.assertEqual(self.api.me_by_pat('mul_good').id, 'u1')
        with self.assertRaises(MulticaAuthError):
            self.api.me_by_pat('mul_bad')

    def test_pat_creation_with_csrf_and_body(self):
        pat = self.api.create_pat('GOOD', 'tok', 'Claude MCP', 90)
        self.assertEqual((pat.id, pat.token), ('p1', 'mul_new'))
        _, path, headers, body = self.seen[-1]
        self.assertEqual((path, headers['X-Csrf-Token'], json.loads(body)), ('/api/tokens', 'tok', {'name': 'Claude MCP', 'expires_in_days': 90}))
        with self.assertRaises(MulticaAuthError) as ctx:
            self.api.create_pat('GOOD', 'wrong', 'x', 90)
        self.assertEqual(ctx.exception.kind, 'unauthenticated')

    def test_redirects_are_not_followed_and_outage_is_unavailable(self):
        with self.assertRaises(MulticaAuthError) as ctx:
            self.api._request('GET', '/api/redirect', {})
        self.assertEqual(ctx.exception.kind, 'unavailable')
        self.assertEqual(len(self.seen), 1)
        dead = MulticaApi('http://127.0.0.1:1', timeout=1)
        with self.assertRaises(MulticaAuthError) as ctx:
            dead.me_by_pat('x')
        self.assertEqual(ctx.exception.kind, 'unavailable')


class CookieParsing(unittest.TestCase):
    def test_browser_cookie_parsing(self):
        from multica_mcp.multica_auth import session_from_cookie_header as parse
        self.assertEqual(parse('a=1; multica_auth=S; multica_csrf=old; multica_csrf_session=new'), ('S', 'new'))
        self.assertEqual(parse('multica_auth=S; multica_csrf=old'), ('S', 'old'))
        self.assertEqual(parse(''), ('', ''))
        self.assertEqual(parse('junk;;;='), ('', ''))

    def test_foreign_cookies_with_spaces_or_quotes_do_not_hide_the_session(self):
        from multica_mcp.multica_auth import session_from_cookie_header as parse
        for header in ('theme={"a": 1}; multica_auth=S; multica_csrf=c', 'a=b c; multica_auth=S; multica_csrf=c',
                       'a="unterminated; multica_auth=S; multica_csrf=c', 'multica_auth="S"; multica_csrf=c'):
            self.assertEqual(parse(header), ('S', 'c'), header)
        self.assertEqual(parse('multica_auth=S; multica_auth=other'), ('S', ''))


if __name__ == '__main__':
    unittest.main()
