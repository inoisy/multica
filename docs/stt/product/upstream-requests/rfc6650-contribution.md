Comment proposal for [RFC #6650](https://github.com/multica-ai/multica/issues/6650)

We would like to contribute implementation experience and small patches along
the RFC's phases, rather than propose a separate execution-policy API.

Our self-hosted prototype separates reusable ordered execution profiles from
account pools. It records an execution choice and distinguishes the account
offered at dispatch from the account confirmed by the daemon. These concepts
overlap with bindings, quota pools, and attempt snapshots, but the schemas and
selection lifecycle are different. We are not proposing a direct schema import.

The intended invariants are:

- One writable source for primary execution configuration; other agent fields
  are a server-maintained projection, not an independent setting.
- Unknown or stale quota is explicit rather than reported as available or zero.
- Attempt history distinguishes proposed execution from confirmed execution and
  retains the reason a candidate was rejected.
- Account attribution is accepted only for the corresponding dispatch and
  confirmation; a configured account name is not evidence that it served a run.
- Recovery remains visible and policy-controlled. Adding another candidate must
  not silently authorize a different payer or move an in-flight session.

Possible contributions, subject to maintainer guidance:

1. Extend [#7413](https://github.com/multica-ai/multica/pull/7413)'s quota telemetry
   with freshness and explicit unknown states, followed by provider-specific
   measurements that can be tested without live model calls.
2. Specify a proposed-versus-confirmed account fact in the attempt snapshot,
   including dispatch identity, stale confirmation rejection, and idempotency.
3. Expose the chosen binding/account and rejection reasons in attempt history,
   using public identifiers rather than credential material.

We would coordinate with [#8931](https://github.com/multica-ai/multica/pull/8931)
and [#8579](https://github.com/multica-ai/multica/pull/8579) before changing
admission or multi-runtime routing. Migration numbering and API compatibility
would be reconciled against upstream at the time of each patch.

Following the direction in [#9493](https://github.com/multica-ai/multica/issues/9493),
attempt-budget controls should be considered inside this design rather than as
a standalone enqueue API. Which of the telemetry, confirmation, or history
pieces would be most useful as the first bounded contribution?
