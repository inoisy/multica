Comment proposal for [PR #5745](https://github.com/multica-ai/multica/pull/5745)

Could the approval registry expose a supported adapter contract for an operation
executed by a trusted host helper, after preparation and validation have finished?
The motivating case is activating a verified component version. The approval UI
should represent one concrete action, while the helper retains authority over
which actions and versions it can execute.

A proposed request would bind the approval to:

- The original initiating member, workspace, and narrowly scoped operation.
- The exact immutable artifact digest, validation result, and policy revision.
- An expiry and single-use operation identity, with durable execution status.

The card could show the prepared version, its verification result, the expected
effect, and recovery behavior before offering approve once or reject. Changing
the artifact or trusted policy would invalidate the pending request. Approval
would neither grant a general shell nor reveal a password or provider credential
to the agent.

After approval, an authenticated helper would execute only a registered operation
and report health or rollback on the same operation record. Duplicate delivery,
an expired request, changed validation, or replay of an already consumed approval
must not execute it again. Caller disconnects should not lose the result.

The helper's installation, allowed component list, and host privileges remain an
operator responsibility. Updating the daemon supervising the initiating run also
needs an independent lifecycle owner; approval alone cannot preserve active work
through an unsafe self-restart.

Would this fit the current registry and event model? We can first contribute
synthetic replay/policy-change tests and a small adapter specification, without
introducing a parallel approval engine or adding host deployment to this PR's
initial destructive-operation scope.
