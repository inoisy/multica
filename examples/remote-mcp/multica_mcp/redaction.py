"""Masks secrets in nested data before fields are truncated or JSON-encoded.

One Redactor is shared by the audit log, the runner and the service: secrets issued at runtime (PATs created through
OAuth sign-in) are added with add() and are masked everywhere from then on.
"""
import re
import threading


class Redactor:
    def __init__(self, secrets=()):
        self._lock = threading.Lock()
        self._secrets = set()
        self._pattern = None
        self.longest = 0
        self.add(*secrets)

    def add(self, *secrets):
        with self._lock:
            fresh = {s for s in secrets if s and s not in self._secrets}
            if not fresh:
                return
            self._secrets |= fresh
            self.longest = max(self.longest, *(len(s) for s in fresh))
            # A long secret may contain a short one: replace the longest match in a single pass.
            ordered = sorted(self._secrets, key=len, reverse=True)
            self._pattern = re.compile('|'.join(re.escape(s) for s in ordered))

    def __call__(self, value):
        if isinstance(value, str):
            pattern = self._pattern
            return pattern.sub('[redacted]', value) if pattern else value
        if isinstance(value, dict):
            return {self(k): self(v) for k, v in value.items()}
        if isinstance(value, list):
            return [self(v) for v in value]
        return value
