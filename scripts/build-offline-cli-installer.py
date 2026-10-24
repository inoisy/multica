#!/usr/bin/env python3
"""Package an already verified Linux amd64 CLI into a single offline installer."""

import argparse
import base64
import gzip
import hashlib
from pathlib import Path
import re


def build(binary: Path, output: Path, commit: str, expected_sha: str) -> None:
    if not re.fullmatch(r"[0-9a-f]{40}", commit):
        raise ValueError("commit must be a full lowercase Git SHA")
    if not re.fullmatch(r"[0-9a-f]{64}", expected_sha):
        raise ValueError("expected-sha256 must be a lowercase SHA256")
    content = binary.read_bytes()
    if hashlib.sha256(content).hexdigest() != expected_sha:
        raise ValueError("binary SHA256 does not match expected-sha256")
    template = Path(__file__).with_name("deploy-cli-offline.sh.in").read_text()
    template = template.replace("@PAYLOAD_SHA256@", expected_sha).replace(
        "@EXPECTED_COMMIT@", commit
    )
    payload = base64.encodebytes(gzip.compress(content, mtime=0)).decode("ascii")
    output.write_text(template + payload)
    output.chmod(0o755)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--expected-sha256", required=True)
    args = parser.parse_args()
    build(args.binary, args.output, args.commit, args.expected_sha256)
