#!/bin/sh
# Start the recovery helper under systemd, without binding it to SSH.
set -eu
if [ "$(id -u)" -ne 0 ]; then
    echo "Run from the root console" >&2
    exit 1
fi
if [ "$#" -lt 1 ]; then
    echo "Usage: sh start-resume.sh STATE [--update-daemon] [--force-daemon]" >&2
    exit 1
fi
state=$1
shift
tool_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
revision=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["revision"])' "$state/package/manifest.json")
cd "$tool_dir"
sha256sum --check --strict SHA256SUMS
log=${STT420_RESUME_LOG:-/var/log/multica-stt420-resume.log}
exec systemd-run --no-ask-password --no-block \
    --unit=multica-stt420-resume \
    --description="Resume Multica STT-420 installation" \
    --property=Type=exec \
    --property=RemainAfterExit=yes \
    --property=UMask=0077 \
    --property="StandardOutput=append:$log" \
    --property="StandardError=append:$log" \
    /usr/bin/python3 "$tool_dir/resume.py" --state "$state" --revision "$revision" "$@"
