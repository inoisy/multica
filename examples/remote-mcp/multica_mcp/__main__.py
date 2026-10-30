"""Entry point: `python3 -m multica_mcp`. Configuration comes from the environment; keys, PATs and OAuth tokens never reach the log."""
import json
import logging
import os
import signal
import sys
import threading

from .audit import AuditLog
from .config import Config, ConfigError
from .http import make_server
from .multica_auth import MulticaApi
from .oauth_http import OAuthHttp
from .oauth_service import OAuthService, load_key
from .oauth_store import OAuthStore
from .redaction import Redactor
from .runner import Runner
from .service import Service


class JsonFormatter(logging.Formatter):
    def format(self, record):
        entry = {'level': record.levelname, 'logger': record.name, 'message': record.getMessage()}
        entry.update(getattr(record, 'ctx', {}))
        if record.exc_info:
            entry['exception'] = self.formatException(record.exc_info).splitlines()[-1]
        return json.dumps(entry, ensure_ascii=False)


def main(env=None):
    env = os.environ if env is None else env
    handler = logging.StreamHandler(sys.stderr)
    handler.setFormatter(JsonFormatter())
    logging.basicConfig(level=logging.INFO, handlers=[handler])
    log = logging.getLogger('multica_mcp')
    try:
        config = Config.from_env(env)
    except (ConfigError, ValueError) as e:
        log.error('configuration: %s', e)
        return 2
    config.state_dir.mkdir(parents=True, exist_ok=True)
    redactor = Redactor(config.secrets)  # shared by audit and runner; PATs issued through OAuth are added at runtime
    audit = AuditLog(config.state_dir / 'audit.jsonl', redactor=redactor)
    oauth = None
    if config.oauth_key_file:
        try:
            key = load_key(config.oauth_key_file)
        except ConfigError as e:
            log.error('configuration: %s', e)
            return 2
        store = OAuthStore(config.state_dir / 'oauth.sqlite')
        oauth = OAuthHttp(OAuthService(config, store, MulticaApi(config.api_url), key, redactor), config)
    server = make_server(config, Service(Runner(config, redactor=redactor), audit), oauth=oauth)
    # shutdown() blocks until serve_forever returns, so the signal handler calls it from another thread
    signal.signal(signal.SIGTERM, lambda *_: threading.Thread(target=server.shutdown).start())
    log.info('listening on %s:%s', config.host, config.port)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
    return 0


if __name__ == '__main__':
    sys.exit(main())
