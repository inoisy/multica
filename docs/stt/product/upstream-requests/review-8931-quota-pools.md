# Review notes for multica-ai/multica#8931 — provider quota pools

Target: PR #8931 (comment). Status at drafting time (26.10.2026): open, conflicts with `main`, author asked for maintainer review, none yet.

---

We operate account quota pools in a self-hosted fork (several provider accounts per pool, one usage cap per window, availability derived from the accounts). A few observations that may be useful for this design:

1. **Migration numbers `564–567`** may need renumbering against current `main`.
2. **Record the account actually used per attempt.** A pool mapping says which accounts *may* serve an agent; it does not prove which one did. We store an offered account and a daemon-confirmed account per run, and keep "unknown" as an explicit value instead of inferring it from pool order. That made cost and incident analysis trustworthy.
3. **Empty pool ≠ unpooled.** Treating a pool with no members as "no pool" silently changes billing behavior. We found it safer to keep an empty pool as a pool that refuses with a visible reason.
4. **One cap per window, not per agent.** A single per-window threshold on the account (e.g. stop agents at N% so humans keep the rest) was easier to reason about than per-agent caps.
5. **Relation to #8579.** Both PRs hold dispatch on quota failures (account-scoped here, runtime-scoped there). It would help to say which one is authoritative when both apply, so a task is not parked twice with different reset times.
6. **RFC #6650 alignment.** The RFC lists a first-class quota-pool identity as an open decision; linking this PR there would let the discussion converge in one place.

Glad to share test cases for these scenarios if helpful.
