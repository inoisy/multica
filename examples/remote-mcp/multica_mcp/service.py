"""Owns the behavior: command policy, the client's workspace, running the CLI with the client's token, audit.

The HTTP and JSON-RPC layers decide none of this; they call Service.call_tool.
"""
import json
import logging
import re
import threading
import time

from . import policy, tools
from .audit import text_digest
from .errors import InvalidParams, ToolError

log = logging.getLogger('multica_mcp')

UUID_RE = re.compile(r'[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}')
AUDIT_ARG_CHARS = 500
MEMBERSHIP_TTL = 30.0  # seconds: removal from a workspace takes effect within this time


class Service:
    def __init__(self, runner, audit, clock=time.monotonic, membership_ttl=MEMBERSHIP_TTL):
        self.runner, self.audit = runner, audit
        self.redact = audit.redact
        self._clock, self.membership_ttl = clock, membership_ttl
        self._workspaces = {}  # client cache_key -> (load time, [{id, slug, name}] from `workspace list`)
        self._meta = threading.Lock()

    def call_tool(self, name, arguments, client_ip='', client=None):
        """(is_error, payload). A policy violation or malformed arguments raise InvalidParams (-32602)."""
        if name not in tools.TOOLS:
            raise InvalidParams(f'unknown tool: {name}')
        args, stdin, workspace = tools.validate(arguments)
        entry = {'client': client.name, 'client_ip': client_ip, 'argv': [a[:AUDIT_ARG_CHARS] for a in args]}
        if client.ident:
            entry |= {'user_id': client.ident, 'auth': client.source}
        try:
            path = policy.check(args, client.read_only)
        except InvalidParams as e:
            self._audit(entry | {'denied': e.denied, 'exit_code': None})
            raise
        try:
            workspace_id = self._workspace_id(client, workspace) if policy.needs_workspace(path, args) else ''
        except InvalidParams as e:
            self._audit(entry | {'denied': e.message, 'exit_code': None})
            raise
        except ToolError as e:
            self._audit(entry | {'denied': None, 'exit_code': None, 'error': e.message})
            return True, e.to_dict()
        argv = args + ['--output', 'json'] if policy.wants_output_flag(args) else args
        result = self.runner.run(client.token, argv, stdin, workspace_id)
        entry |= {'denied': None, 'exit_code': result.exit_code, 'timeout': result.timeout,
                  'duration': result.duration, 'stdin_length': result.stdin_length, 'stdout_length': result.stdout_length}
        if stdin:
            entry['stdin'] = text_digest(stdin, self.audit.redact)
        self._audit(entry)
        return result.exit_code != 0, result.to_dict()

    def _audit(self, entry):
        try:
            self.audit.write(tool='multica', **entry)
        except OSError:
            log.exception('audit write failed')

    # --- client workspace ------------------------------------------------------------------------

    def _workspace_id(self, client, ref):
        wanted = list(client.workspaces)
        if ref is None:
            if client.any_workspace:
                known = self._membership(client)
                if len(known) != 1:
                    raise InvalidParams('workspace is required (' + ', '.join(_slugs(known)) + ')')
                return str(known[0]['id'])
            if len(wanted) != 1:
                raise InvalidParams('workspace is required for this key (' + ', '.join(wanted) + ')')
            ref = wanted[0]
        key = ref.lower()
        if UUID_RE.fullmatch(ref) and key in (w.lower() for w in wanted):
            return ref
        found = _match(self._membership(client), key)
        if found is None:
            # The person may have joined the workspace after the cache was filled: reload once before refusing
            found = _match(self._membership(client, refresh=True), key)
        if found is None:
            allowed = 'your workspaces' if client.any_workspace else ', '.join(wanted)
            raise InvalidParams(f'workspace {ref!r} is not allowed for this key ({allowed})')
        return str(found['id'])

    def _membership(self, client, refresh=False):
        """Workspaces the token owner belongs to (and the client may use): `workspace list` with the client's own token, cached with a TTL."""
        name = client.cache_key
        with self._meta:
            cached = self._workspaces.get(name)
            if cached and not refresh and self._clock() - cached[0] < self.membership_ttl:
                return cached[1]
        result = self.runner.run(client.token, ['workspace', 'list', '--output', 'json'])
        if result.exit_code != 0:
            raise ToolError('workspace_lookup_failed', 'workspace list failed: ' + result.stderr.strip()[:300])
        try:
            rows = json.loads(result.stdout)
        except ValueError:
            raise ToolError('workspace_lookup_failed', 'workspace list: response is not JSON') from None
        rows = rows.get('workspaces') if isinstance(rows, dict) else rows
        if not isinstance(rows, list):
            raise ToolError('workspace_lookup_failed', 'workspace list: unexpected response shape')
        rows = [w for w in rows if isinstance(w, dict)]
        if not client.any_workspace:
            wanted = {w.lower() for w in client.workspaces}
            rows = [w for w in rows if wanted & {str(w.get('id', '')).lower(), str(w.get('slug', '')).lower()}]
        with self._meta:
            self._workspaces[name] = (self._clock(), rows)
        return rows


def _match(known, key):
    for w in known:
        if key in (str(w.get('id', '')).lower(), str(w.get('slug', '')).lower(), str(w.get('name', '')).lower()):
            return w
    return None


def _slugs(known):
    return [str(w.get('slug') or w.get('id')) for w in known]
