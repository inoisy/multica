# Add instructions to a running turn

There are two paths. **Agents** use `multica issue comment add --steer-active`
(or `--steer <run-id>`): the comment goes into the running turn of the agents it
names or triggers on the issue. **People** use `multica issue steer`, which
targets one exact run and accepts only human credentials; agent task tokens
cannot use it, and must never fall back to another person's credential.

## Agents: `comment add --steer-active` / `--steer <run-id>`

```bash
multica issue comment add MUL-123 --steer-active --content-file ./correction.md
multica issue comment add MUL-123 --steer RUN_ID --content-file ./correction.md
```

Steer only to correct the work in progress on the same issue: a wrong
requirement, a new constraint, a fact the running agent needs now. New work is a
normal comment or a new issue. `--steer-active` steers every running turn on the
issue you may steer, plus any other running recipient of the comment;
`--steer` takes run IDs from `multica issue runs <issue> --active` (repeatable,
short prefixes resolve on this issue). The CLI mentions each steered agent in
the comment. Permissions do not change: you can steer only an agent your comment
could already trigger, and never your own turn.

stderr reports which recipients took the comment into their running turn and
which fell back to the normal trigger. A recipient without a running turn, a
runtime that cannot take input, or a turn that already took 10 agent comments
(3 from the same agent) falls back to the normal trigger; `--steer` /
`--steer-active` cannot be combined with `--attachment`. The comment is never
dropped or sent to another turn. Details: `references/mentions.md`, "Steering
another agent's running turn".

## People: `issue steer`

`steer` adds a historical comment to one exact running turn without cancelling
or restarting it.

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
