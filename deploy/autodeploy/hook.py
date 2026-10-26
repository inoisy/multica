#!/usr/bin/env python3
"""GitHub webhook receiver that only wakes multica-autodeploy.

It verifies the X-Hub-Signature-256 HMAC, accepts a finished successful
workflow run on the deploy branch, and touches a trigger file watched by a
systemd path unit. The payload never reaches the deploy script: a forged or
replayed request can at most start a run that finds nothing new.
"""
import hashlib
import hmac
import json
import os
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

SECRET_FILE = Path(os.environ.get("AUTODEPLOY_HOOK_SECRET_FILE", "/etc/multica-autodeploy/webhook-secret"))
TRIGGER = Path(os.environ.get("AUTODEPLOY_TRIGGER", "/run/multica-autodeploy/trigger"))
BRANCH = os.environ.get("AUTODEPLOY_BRANCH", "main")
MAX_BODY = 5 * 1024 * 1024


def valid_signature(secret, body, header):
    if not secret or not header or not header.startswith("sha256="):
        return False
    expected = "sha256=" + hmac.new(secret, body, hashlib.sha256).hexdigest()
    return hmac.compare_digest(expected, header)


def should_trigger(event, payload):
    if event == "ping":
        return False
    if event != "workflow_run":
        return False
    run = payload.get("workflow_run") or {}
    return (payload.get("action") == "completed" and run.get("conclusion") == "success"
            and run.get("head_branch") == BRANCH and run.get("event") == "push")


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):  # noqa: N802 - http.server API
        length = int(self.headers.get("Content-Length") or 0)
        if length <= 0 or length > MAX_BODY:
            return self.reply(413, "bad length")
        body = self.rfile.read(length)
        if not valid_signature(SECRET_FILE.read_bytes().strip(), body, self.headers.get("X-Hub-Signature-256")):
            return self.reply(401, "bad signature")
        event = self.headers.get("X-GitHub-Event", "")
        try:
            payload = json.loads(body)
        except ValueError:
            return self.reply(400, "bad json")
        if should_trigger(event, payload):
            TRIGGER.parent.mkdir(parents=True, exist_ok=True)
            TRIGGER.write_text(payload["workflow_run"]["head_sha"] + "\n")
            return self.reply(202, "deploy check queued")
        return self.reply(200, "ignored")

    def reply(self, code, text):
        self.send_response(code)
        self.send_header("Content-Type", "text/plain")
        self.end_headers()
        self.wfile.write(text.encode())

    def log_message(self, fmt, *args):
        sys.stderr.write("hook: " + fmt % args + "\n")


def main():
    port = int(os.environ.get("AUTODEPLOY_HOOK_PORT", "8977"))
    HTTPServer(("127.0.0.1", port), Handler).serve_forever()


if __name__ == "__main__":
    main()
