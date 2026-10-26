# Add instructions to a running turn

`steer` adds a historical comment to one exact running turn without cancelling
or restarting it. The server accepts only human credentials. Agent task tokens
cannot use it, even on behalf of a person; do not read or fall back to another
person's credential. An agent can prepare the message and commands for a person
to execute in their own authenticated terminal.

```bash
multica issue steer MUL-123 --content-file ./instructions.md --output json
multica issue steer MUL-123 RUN_ID --content-stdin --wait --timeout 1m --output json
multica issue steer retry MUL-123 RUN_ID COMMENT_ID --wait --timeout 1m --output json
```

Omitting RUN_ID selects the only `running` turn. Zero or several running turns
fail; the latter lists IDs to choose from. An explicit RUN_ID accepts a full
UUID or an unambiguous short prefix, scoped to the issue. COMMENT_ID is a full
UUID. Content uses exactly one of `--content`, `--content-file`, or
`--content-stdin`; file and stdin preserve literal backslashes.

Before submitting, the CLI prints the resolved run ID and a generated
`client_request_id` UUID to stderr. If the connection breaks or the result is
uncertain, repeat the same content with that explicit run ID and
`--client-request-id UUID`. This returns the original comment rather than
creating another one. A new command with no supplied request ID creates a new
message, so it is not a safe retry after an uncertain submission. `steer retry`
redelivers an existing failed comment; it does not create another comment.

JSON stdout is a receipt with `comment_id`, `task_id`, `status`, and, when
available, `client_request_id`, `failure_reason`, and `delivered_at`. Statuses
are `pending`, `delivering`, `delivered`, and `failed`. `--wait` polls just that
comment and run until delivered, failed, or `--timeout` (default one minute).
Failure and timeout return nonzero and still print the last receipt. A timeout
does not cancel delivery. `delivered` acknowledges input delivery, not completion
of the requested work. To verify the same turn received the input, inspect
`multica issue run-messages RUN_ID --issue MUL-123 --output json`.

HTTP 409 `task_supplement_turn_ended` means the run has already ended; HTTP 412
`task_supplement_unsupported` means its runtime cannot accept additions and
requires a restart for new instructions. The CLI never restarts automatically.
Other conflicts, including a reused request UUID, preserve the server's reason.
