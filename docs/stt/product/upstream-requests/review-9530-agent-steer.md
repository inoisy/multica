# Review notes for multica-ai/multica#9530 — agent-authored steer

Target: PR #9530 (comment), issue #9529. Status at drafting time (26.10.2026): open, no maintainer review yet.

Внутренняя пометка (не копировать в комментарий): перенос — PR форка #78 (STT-452).
Перед публикацией дописать в п. 1 итог живой проверки агент → агент после выкатки
или убрать строку «Live check», если она не состоялась.

---

We carried both commits of this PR into a self-hosted fork to try it before it lands. Notes from that:

1. **Applies cleanly, tests pass.** Both commits cherry-pick onto a recent `main` without conflicts. `go build ./...`, `go vet` and the full `internal/handler` and `internal/service` test packages pass against a fresh Postgres with all migrations, including the 7 new steer tests (agent author with `steer: true`, no intent, no run IDs, no running task, attachments, the 10-supplement cap, the recount after the lock wait). Not run on our side: `-race`.
2. **The CLI has no way to set the flag.** Agents talk to the server through `multica issue comment add`, so without a CLI option the new `steer: true` is unreachable for them. We added `--steer` to `issue comment add` (passes `steer: true`, prints to stderr which mentioned agents got the comment in their running turn and which were queued). Happy to open it as a follow-up PR if that helps.
3. **Report the outcome in the response.** The fail-open path is right (no running turn → normal queue), but the author cannot tell which path was taken. Returning per-agent delivery (`steered` / `queued`) in the `POST /comments` response would let the CLI and UI show it without a second request.
4. **`triggerTasksForComment` signature.** Adding a positional `steer bool` breaks every out-of-tree caller at compile time (we had one). An options struct, or a separate entry point for the steer path, would be gentler for forks; not a blocker.
5. **Guidance for agents.** It may be worth one line in the built-in agent brief: steer is for a correction to the current run on the same issue (wrong requirement, new constraint), new work still goes as a normal comment or issue. Without it agents tend to steer by default.

Live check (agent A running, agent B comments with `--steer` mentioning A → A receives it in the current turn, nothing queued; after A finishes the same comment is queued): _to be filled after deployment_.
