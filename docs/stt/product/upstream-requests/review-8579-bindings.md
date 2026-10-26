# Review notes for multica-ai/multica#8579 — multi-runtime bindings and fallback

Target: PR #8579 (comment, not a code suggestion). Status at drafting time (26.10.2026): open, conflicts with `main`, no maintainer review.

---

Thanks for pushing this forward. We run a self-hosted fork where agents already fall back across ordered (runtime, model) options and across several provider accounts behind one runtime, in daily use since October. Some notes from operating that model, in the hope they help this PR land:

1. **Migration numbers.** `502–505` are now taken on `main` by `channel_reply_delivery`. A rebase with renumbering would let maintainers try the branch.
2. **Reset parsing is the right call to keep strict.** Treating anything other than an explicit UTC/GMT reset, or anything beyond the 7-day horizon, as unknown has matched what we see in practice: weekly-limit messages carry only a wall-clock time. We'd suggest exposing the "unknown reset" backoff as a named constant in the visible audit, so users can tell "waiting for a known reset" from "backing off blindly".
3. **Auth must never switch silently.** Agreed with the no-switch rule for `provider_auth_or_access`. In our experience the useful addition is surfacing the held runtime to the *binding owner* (not only in task history), because they're the only person who can log in again.
4. **Quota scope is the account, not the runtime.** When the same provider account is used from two runtimes, a runtime-scoped circuit opens one and keeps hammering the other. This is where #8931 (account quota pools) and this PR meet. It would help reviewers to state which one owns the hold when both are merged.
5. **Probe after reset.** The exactly-one half-open probe is valuable; without it we saw all queued tasks retry the moment a window reopened. Please keep it on the claim path too, not only for autopilot.
6. **Primary projection.** Keeping `agent.runtime_id` as a server-written projection of binding #1 (never independently editable) avoided a whole class of "the inspector shows X but Y ran" bugs for us. A short note in the PR description on whether direct edits of those agent fields are rejected after this change would answer that for reviewers.

Happy to test a rebased branch against our setup and report results.
