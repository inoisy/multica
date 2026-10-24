# Offline CLI deployment

This is a Linux amd64 **CLI-only** installer for a verified fork build. It
embeds that binary in one Bash file and requires no GitHub access, `gh`,
Docker, Go toolchain, saved Multica credential, or API request on the host.
The self-host server and existing daemon process are not restarted. New CLI
invocations use the installed file; the running daemon retains its executable.

Build the deliverable from a verified binary, pinning both its code revision
and its independently recorded checksum:

```bash
python3 scripts/build-offline-cli-installer.py \
  --binary ./artifacts/multica \
  --commit <full-code-revision> \
  --expected-sha256 <verified-binary-sha256> \
  --output ./artifacts/deploy-multica-workflows-cli.sh
```

Deliver the generated script as a file; binary payloads are not committed.
Save it on the target host, then run:

```bash
bash ./deploy-multica-workflows-cli.sh --check
# As root, install to /usr/local/bin/multica:
bash ./deploy-multica-workflows-cli.sh
```

`--check` verifies payload checksum, version and both Workflow command
groups without replacing the current CLI or creating a backup. Installation
uses a lock, a private staging directory on the target filesystem and atomic
rename. Version/help checks do not access the API or any user's profile.
`--target /absolute/path/multica` supports an explicit custom installation
directory. The directory must already exist and be writable.

An existing file is backed up with its mode, SHA256 and destination in
`<target-directory>/.multica-cli-backups/<unique-directory>/`. The installer
prints the exact rollback command before changing the executable. On a
failed activation or handled signal it automatically restores the previous
bytes, or removes its new file if this was a fresh installation. An abrupt
power loss/SIGKILL cannot execute a trap; use the printed manual command:

```bash
bash ./deploy-multica-workflows-cli.sh --rollback <printed-backup-directory>
```

Use the same `--target` for a custom destination. Rollback checks the saved
checksum and refuses to overwrite a different, later release. Reinstalling
the same healthy payload creates no additional backup. No configuration,
subscription, model profile or daemon lifecycle change is part of this script.

Verification (no root permissions or real services required):

```bash
bash -n scripts/deploy-cli-offline.sh.in
python3 -m unittest discover -s scripts -p test_offline_cli_installer.py -v
```
