"""OAuth storage: SQLite in the state directory (0600) that survives restarts.

Codes and tokens are stored only as hashes (SHA-256 of 256 random bits). PATs are stored encrypted; the operator keeps
the key separately. Redeeming a code and rotating a refresh token are atomic: "not used yet" is part of the UPDATE.
"""
import json
import os
import sqlite3
import threading

SCHEMA = '''
CREATE TABLE IF NOT EXISTS clients (client_id TEXT PRIMARY KEY, name TEXT NOT NULL, redirect_uris TEXT NOT NULL, created REAL NOT NULL);
CREATE TABLE IF NOT EXISTS pending (id TEXT PRIMARY KEY, client_id TEXT NOT NULL, redirect_uri TEXT NOT NULL, state TEXT,
    challenge TEXT NOT NULL, resource TEXT NOT NULL, scope TEXT NOT NULL, expires REAL NOT NULL);
CREATE TABLE IF NOT EXISTS grants (id TEXT PRIMARY KEY, client_id TEXT NOT NULL, user_id TEXT NOT NULL, user_name TEXT NOT NULL,
    pat_id TEXT NOT NULL, pat_cipher BLOB NOT NULL, scope TEXT NOT NULL, created REAL NOT NULL, expires REAL NOT NULL,
    revoked INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS codes (hash TEXT PRIMARY KEY, grant_id TEXT NOT NULL, client_id TEXT NOT NULL, redirect_uri TEXT NOT NULL,
    challenge TEXT NOT NULL, resource TEXT NOT NULL, expires REAL NOT NULL, used INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS tokens (hash TEXT PRIMARY KEY, kind TEXT NOT NULL, grant_id TEXT NOT NULL, expires REAL NOT NULL,
    used INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS tokens_grant ON tokens (grant_id);
'''


class OAuthStore:
    def __init__(self, path):
        self.path = path
        fd = os.open(path, os.O_RDWR | os.O_CREAT, 0o600)
        os.close(fd)
        os.chmod(path, 0o600)
        self._db = sqlite3.connect(path, check_same_thread=False, isolation_level=None)
        self._db.row_factory = sqlite3.Row
        self._lock = threading.Lock()
        with self._lock:
            self._db.execute('PRAGMA journal_mode=DELETE')
            self._db.execute('PRAGMA synchronous=FULL')
            self._db.executescript(SCHEMA)

    def close(self):
        with self._lock:
            self._db.close()

    def _one(self, sql, args=()):
        with self._lock:
            row = self._db.execute(sql, args).fetchone()
        return dict(row) if row else None

    def _write(self, sql, args=()):
        """Runs one write and returns the number of affected rows."""
        with self._lock:
            return self._db.execute(sql, args).rowcount

    # --- clients (DCR) ----------------------------------------------------------------------------

    def count_clients(self):
        return self._one('SELECT COUNT(*) AS n FROM clients')['n']

    def add_client(self, client_id, name, redirect_uris, now):
        self._write('INSERT INTO clients VALUES (?,?,?,?)', (client_id, name, json.dumps(redirect_uris), now))

    def get_client(self, client_id):
        row = self._one('SELECT * FROM clients WHERE client_id=?', (client_id,))
        return row and {**row, 'redirect_uris': json.loads(row['redirect_uris'])}

    # --- authorization requests -------------------------------------------------------------------

    def put_pending(self, req_id, client_id, redirect_uri, state, challenge, resource, scope, expires):
        self._write('INSERT INTO pending VALUES (?,?,?,?,?,?,?,?)',
                    (req_id, client_id, redirect_uri, state, challenge, resource, scope, expires))

    def get_pending(self, req_id, now):
        return self._one('SELECT * FROM pending WHERE id=? AND expires>?', (req_id, now))

    def take_pending(self, req_id, now):
        """The request or None. Takes it atomically, so a resubmitted consent form cannot issue a second PAT."""
        with self._lock:
            self._db.execute('BEGIN IMMEDIATE')
            try:
                row = self._db.execute('SELECT * FROM pending WHERE id=? AND expires>?', (req_id, now)).fetchone()
                self._db.execute('DELETE FROM pending WHERE id=?', (req_id,))
                self._db.execute('COMMIT')
            except BaseException:
                self._db.execute('ROLLBACK')
                raise
        return dict(row) if row else None

    # --- grants, codes, tokens --------------------------------------------------------------------

    def create_grant_with_code(self, grant, code_hash, code):
        """A grant and its one-time code are created together or not at all."""
        with self._lock:
            self._db.execute('BEGIN IMMEDIATE')
            try:
                self._db.execute('INSERT INTO grants (id,client_id,user_id,user_name,pat_id,pat_cipher,scope,created,expires) '
                                 'VALUES (:id,:client_id,:user_id,:user_name,:pat_id,:pat_cipher,:scope,:created,:expires)', grant)
                self._db.execute('INSERT INTO codes (hash,grant_id,client_id,redirect_uri,challenge,resource,expires) '
                                 'VALUES (?,?,?,?,?,?,?)',
                                 (code_hash, grant['id'], code['client_id'], code['redirect_uri'], code['challenge'],
                                  code['resource'], code['expires']))
                self._db.execute('COMMIT')
            except BaseException:
                self._db.execute('ROLLBACK')
                raise

    def get_grant(self, grant_id):
        return self._one('SELECT * FROM grants WHERE id=?', (grant_id,))

    def redeem_code(self, code_hash):
        """(row, replayed): replayed is True when the code was already redeemed, and the grant must be revoked."""
        with self._lock:
            self._db.execute('BEGIN IMMEDIATE')
            try:
                row = self._db.execute('SELECT * FROM codes WHERE hash=?', (code_hash,)).fetchone()
                fresh = bool(row) and self._db.execute('UPDATE codes SET used=1 WHERE hash=? AND used=0', (code_hash,)).rowcount == 1
                self._db.execute('COMMIT')
            except BaseException:
                self._db.execute('ROLLBACK')
                raise
        return (dict(row) if row else None), bool(row) and not fresh

    def put_tokens(self, grant_id, access_hash, access_expires, refresh_hash, refresh_expires):
        with self._lock:
            self._db.execute('BEGIN IMMEDIATE')
            try:
                self._db.execute('INSERT INTO tokens (hash,kind,grant_id,expires) VALUES (?,?,?,?)',
                                 (access_hash, 'access', grant_id, access_expires))
                self._db.execute('INSERT INTO tokens (hash,kind,grant_id,expires) VALUES (?,?,?,?)',
                                 (refresh_hash, 'refresh', grant_id, refresh_expires))
                self._db.execute('COMMIT')
            except BaseException:
                self._db.execute('ROLLBACK')
                raise

    def get_token(self, token_hash):
        return self._one('SELECT * FROM tokens WHERE hash=?', (token_hash,))

    def rotate_refresh(self, old_hash, grant_id, access_hash, access_expires, refresh_hash, refresh_expires):
        """True: the old refresh token is spent and new tokens are stored; False: it was already used (theft or a race)."""
        with self._lock:
            self._db.execute('BEGIN IMMEDIATE')
            try:
                swapped = self._db.execute('UPDATE tokens SET used=1 WHERE hash=? AND kind=? AND used=0',
                                           (old_hash, 'refresh')).rowcount == 1
                if swapped:
                    self._db.execute('DELETE FROM tokens WHERE grant_id=? AND kind=?', (grant_id, 'access'))
                    self._db.execute('INSERT INTO tokens (hash,kind,grant_id,expires) VALUES (?,?,?,?)',
                                     (access_hash, 'access', grant_id, access_expires))
                    self._db.execute('INSERT INTO tokens (hash,kind,grant_id,expires) VALUES (?,?,?,?)',
                                     (refresh_hash, 'refresh', grant_id, refresh_expires))
                self._db.execute('COMMIT')
            except BaseException:
                self._db.execute('ROLLBACK')
                raise
        return swapped

    def revoke_grant(self, grant_id):
        with self._lock:
            self._db.execute('BEGIN IMMEDIATE')
            try:
                self._db.execute('UPDATE grants SET revoked=1 WHERE id=?', (grant_id,))
                self._db.execute('DELETE FROM tokens WHERE grant_id=? AND kind=?', (grant_id, 'access'))
                self._db.execute('COMMIT')
            except BaseException:
                self._db.execute('ROLLBACK')
                raise

    def purge(self, now):
        """Drops expired rows. A revoked grant stays until it expires, so a replayed refresh token still gets invalid_grant."""
        with self._lock:
            self._db.execute('DELETE FROM pending WHERE expires<=?', (now,))
            self._db.execute('DELETE FROM codes WHERE expires<=?', (now,))
            self._db.execute('DELETE FROM tokens WHERE expires<=?', (now,))
            self._db.execute('DELETE FROM grants WHERE expires<=?', (now,))
