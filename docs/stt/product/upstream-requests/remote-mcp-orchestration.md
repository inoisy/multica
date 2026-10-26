Comment proposal for [#1351](https://github.com/multica-ai/multica/issues/1351)

The hosted remote endpoint discussed here matches a concrete use case: reading
the board, commenting, waking an assigned agent, and cancelling a run from a chat
client on a phone while the user's development machine is offline.

We have a small self-hosted Streamable HTTP prototype with a deliberately narrow
tool surface. It wraps the existing task API rather than owning scheduling or
execution. Its credential scheme is not an implementation of the OAuth flow
requested in this thread; we would align authentication with the maintainers'
chosen contract before proposing it as an official endpoint.

The reusable pieces we could contribute are:

- Workspace-scoped projections for issue, agent, and run reads, with bounded
  response sizes and no credential or configuration dumps.
- Comment and issue updates through existing authorization checks; no agent,
  workspace, credential, or instruction administration in the initial tool set.
- An idempotent wake operation that reuses an existing queued/running execution
  and accepts canonical issue identity consistently across input forms.
- A cancellation operation restricted to the selected workspace and the caller's
  existing run-control rights.
- Write audit records and consistent secret redaction before truncating output.
- Fake-API tests for denied access, duplicate wake requests, invalid protocol
  versions, oversized input, and failures without leaking upstream secrets.

Before a public implementation, the remaining design questions are OAuth client
registration and token audience, member consent and workspace selection, remote
client compatibility, and the exact audit identity exposed to other members.
We would test the live connector separately from the synthetic transport suite.

Is there a preferred upstream authentication or hosting direction we should
target? We would rather contribute a small adapter and test matrix here than
start a second orchestration API.
