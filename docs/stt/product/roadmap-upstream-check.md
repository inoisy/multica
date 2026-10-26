# Проверка апстрима для роадмапа

Проверено 26.10.2026 через GitHub Search API и REST API конкретных PR/issue.
Первый поиск начат в 10:18 UTC; статусы перечитаны после 10:20 UTC, дополнительные
поиски завершены около 10:29 UTC. Для трекера — [реестр](roadmap-inventory.md).
Для решения и очереди — [роадмап](roadmap.md).

## Метод и воспроизведение

Запросы ниже выполнялись в `repo:multica-ai/multica`, **без фильтра состояния**:
в выдачу входят открытые и закрытые issue и PR. Поиск — до 30 результатов,
сортировка по updated. В широких запросах не заявляется чтение всех результатов;
формулировки уточнялись по заголовкам и предмету, затем читались сами найденные
источники. Нулевой результат не доказывает отсутствия работы под другим названием.

```sh
gh api --method GET search/issues -f 'q=repo:multica-ai/multica quota pool' -f per_page=30 -f sort=updated
gh api repos/multica-ai/multica/pulls/9510
gh api repos/multica-ai/multica/issues/8410
gh api repos/multica-ai/multica/issues/9536/comments
```

Для PR состояние CLOSED разделено по `merged_at`: закрытый без слияния не
считается поставленным. Для issue CLOSED означает только закрытие обсуждения.
Результаты поиска на короткое время расходились с последующим REST-чтением:
#9553 успела влиться, #9536 — закрыться. В роадмап взят более поздний факт.
После исчерпания минутного search-limit часть уточняющих запросов была отклонена;
для решений использованы только успешно сохранённые ответы и чтение источников.
Неуспешные запросы не считаются отрицательной выдачей.

| Тема | Английские формулировки и всего совпадений |
| --- | --- |
| Модели и подписки | `quota pool` → 8; `runtime bindings` → 103; `execution policy` → 155 |
| Workflow и приёмка | `workflow acceptance` → 90; `workflow artifact` → 68; `child handoff` → 61; `project workflow in:title` → 8; `acceptance in:title` → 4; `stage enqueue` → 40 |
| Обвязка и доставка | `hot reload provider` → 2; `deployment approval` → 104; `self update supervisor` → 22 |
| CI и релизы | `required checks` → 280; `auto deploy` → 1045; `self hosted CI` → 229; `CI deploy in:title` → 1; `required checks in:title` → 0; `ci fork in:title` → 3 |
| Каналы и интеграции | `express BotX` → 0; `remote MCP OAuth` → 9; `Bitrix connector` → 0; `BotX` → 0; `eXpress channel` → 20; `express bot in:title` → 0 |
| Наблюдаемость | `accepted result usage` → 37; `run wait constraint` → 73; `watchdog retry` → 67 |
| QA и ёмкость | `disk space claim` → 10; `ENOSPC` → 12; `task environment cleanup` → 195; `QA manifest` → 4; `test services cleanup` → 20; `resource manifest cleanup` → 8 |
| Знания и контекст | `skill versioning` → 6; `long running state` → 358; `task complexity` → 31 |
| Мобильные вложения | `PWA attachment` → 3; `standalone download` → 44; `iOS preview` → 30 |
| Освоение продукта | `onboarding tutorial` → 2; `docs interactive` → 114; `documentation workflows` → 204 |

## Перечитанные источники

Updated — активность всего issue/PR, не обещание даты релиза и не обязательно
новый коммит. Открытые сторонние PR не являются обязательством мейнтейнеров.
Если срок не объявлен, следующая проверка — 02.11 или перед началом реализации.

| Источник | Фактическое состояние | Updated UTC |
| --- | --- | --- |
| [#6650: RFC: decouple agent identity from runtime — bindings, execution policies, quota-aware recovery](https://github.com/multica-ai/multica/issues/6650) | OPEN (issue) | 2026-10-26 06:15:32 |
| [#6000: [Feature]: Show each runtime's subscription plan quota (5h / weekly windows), not just tokens consumed](https://github.com/multica-ai/multica/issues/6000) | OPEN (issue) | 2026-10-26 03:03:10 |
| [#8931: MUL-7792: feat: enforce provider quota pools across agent dispatch](https://github.com/multica-ai/multica/pull/8931) | OPEN | 2026-10-26 06:54:45 |
| [#8579: MUL-7512: SE-37711 / SE-37873: Multi-runtime bindings and runtime-level fallback (Phase 2)](https://github.com/multica-ai/multica/pull/8579) | OPEN | 2026-10-26 06:54:45 |
| [#7413: feat(runtimes): show CLI plan quota per runtime](https://github.com/multica-ai/multica/pull/7413) | OPEN | 2026-08-21 15:10:09 |
| [#9493: [Feature]: Support atomic single-attempt enqueue for fallback runs](https://github.com/multica-ai/multica/issues/9493) | CLOSED (issue) | 2026-10-26 03:33:18 |
| [#8814: MUL-7420: Project workflows — columns and handoffs on the workflow](https://github.com/multica-ai/multica/pull/8814) | MERGED 2026-10-16 | 2026-10-16 08:49:50 |
| [#4325: [Feature]: Declarative per-project workflows with stage-transition automation](https://github.com/multica-ai/multica/issues/4325) | OPEN (issue) | 2026-10-21 12:16:03 |
| [#1998: [Feature]: Lightweight handoff primitives for multi-agent workflows (Dev → Reviewer → Tester)](https://github.com/multica-ai/multica/issues/1998) | OPEN (issue) | 2026-10-21 12:16:13 |
| [#7345: [RFC] Declarative pipeline templates: the missing declaration for staged sub-issues (#4835, #5856)](https://github.com/multica-ai/multica/issues/7345) | OPEN (issue) | 2026-10-22 05:34:04 |
| [#9540: MUL-8250: feat(issues): statuses follow the issue's workflow, Default included](https://github.com/multica-ai/multica/pull/9540) | OPEN | 2026-10-26 05:54:44 |
| [#9553: MUL-8094: feat(wakeups): accept blocked in --until-issue-state](https://github.com/multica-ai/multica/pull/9553) | MERGED 2026-10-26 | 2026-10-26 10:20:17 |
| [#9536: feat(wakeups): wake coordinators on child handoffs](https://github.com/multica-ai/multica/pull/9536) | CLOSED, без слияния | 2026-10-26 10:20:31 |
| [#9510: MUL-7490: feat(runs): persist and show each queued run's wait constraint](https://github.com/multica-ai/multica/pull/9510) | OPEN | 2026-10-26 06:54:39 |
| [#9184: MUL-7969: feat(server): record structured claim decision outcomes](https://github.com/multica-ai/multica/pull/9184) | MERGED 2026-10-23 | 2026-10-23 13:28:49 |
| [#9105: Per-task liveness: detect a hung running task on a live runtime (throttled heartbeat from the existing status poll)](https://github.com/multica-ai/multica/issues/9105) | OPEN (issue) | 2026-10-21 06:23:52 |
| [#9511: Idle watchdog force-stops runs during a long tool call and leaves the tool's child processes running](https://github.com/multica-ai/multica/issues/9511) | OPEN (issue) | 2026-10-25 02:26:44 |
| [#9314: fix: prevent delegated quota recovery loops (ZIC-343)](https://github.com/multica-ai/multica/pull/9314) | OPEN | 2026-10-17 14:16:57 |
| [#8934: fix(agent): MUL-7781 / MUL-7793 interrupt Codex turns after repeated Guardian 429s](https://github.com/multica-ai/multica/pull/8934) | MERGED 2026-10-12 | 2026-10-12 06:18:03 |
| [#9201: MUL-7851: fix(daemon): don't count host sleep as idle-watchdog silence](https://github.com/multica-ai/multica/pull/9201) | MERGED 2026-10-12 | 2026-10-12 09:19:59 |
| [#8410: [Feature]: Manage run-owned background processes as first-class resources](https://github.com/multica-ai/multica/issues/8410) | OPEN (issue) | 2026-10-11 02:49:29 |
| [#7457: [Feature]: Expose retained task environments and add safe manual cleanup](https://github.com/multica-ai/multica/issues/7457) | OPEN (issue) | 2026-09-28 12:03:50 |
| [#7590: MUL-6717: feat(daemon): explain retained task environments](https://github.com/multica-ai/multica/pull/7590) | OPEN | 2026-10-06 04:49:20 |
| [#7455: fix(runtime): classify storage exhaustion (MUL-6589)](https://github.com/multica-ai/multica/pull/7455) | OPEN | 2026-08-24 05:52:27 |
| [#5884: KAP-1094: park daemon task claims below the 15 GiB disk admission floor](https://github.com/multica-ai/multica/pull/5884) | CLOSED, без слияния | 2026-08-04 07:24:24 |
| [#8084: MUL-7096: WS-4954 fix(daemon): share per-run Codex temporary caches](https://github.com/multica-ai/multica/pull/8084) | OPEN | 2026-09-07 08:18:52 |
| [#9032: MUL-7856: fix(scripts): preserve check failures in exit cleanup](https://github.com/multica-ai/multica/pull/9032) | MERGED 2026-10-20 | 2026-10-20 04:47:44 |
| [#5745: feat(approval): add sensitive-operation approval flow (WS-721)](https://github.com/multica-ai/multica/pull/5745) | OPEN | 2026-07-21 20:21:02 |
| [#4096: MUL-3269: Follow out-of-band multica binary replacement; hot-refresh agent CLI versions](https://github.com/multica-ai/multica/pull/4096) | MERGED 2026-08-05 | 2026-08-05 11:54:22 |
| [#7621: BHI-15092: pilot agent-approval-check on Multica](https://github.com/multica-ai/multica/pull/7621) | OPEN | 2026-08-27 06:14:19 |
| [#8302: MUL-7280: ci(backend): cache race builds per commit and shard pkg/agent onto its own runner](https://github.com/multica-ai/multica/pull/8302) | MERGED 2026-09-11 | 2026-09-11 07:57:27 |
| [#9143: MUL-7918: fix(ci): require Go 1.26.9 security patches](https://github.com/multica-ai/multica/pull/9143) | MERGED 2026-10-09 | 2026-10-09 03:22:32 |
| [#1687: ci(release): skip homebrew-tap publish on forks](https://github.com/multica-ai/multica/pull/1687) | MERGED 2026-04-27 | 2026-04-27 09:47:12 |
| [#1963: chore(ci): bind fork Actions on push/PR + bake prod compose into deploy script](https://github.com/multica-ai/multica/pull/1963) | CLOSED, без слияния | 2026-04-30 22:13:41 |
| [#5126: Make fork releases and installs work without baking org into shared CI](https://github.com/multica-ai/multica/pull/5126) | CLOSED, без слияния | 2026-07-08 19:20:53 |
| [#9552: [Bug]: Opening an attachment in the iOS home-screen PWA is a dead end — no way back without relaunching](https://github.com/multica-ai/multica/issues/9552) | OPEN (issue) | 2026-10-26 09:18:32 |
| [#5970: Mobile UX: PWA navigation, tap targets, padding, and scroll shortcuts (iOS/Safari)](https://github.com/multica-ai/multica/issues/5970) | CLOSED (issue) | 2026-08-20 10:53:14 |
| [#7025: feat(web): make the mobile web app installable (MUL-6237)](https://github.com/multica-ai/multica/pull/7025) | MERGED 2026-08-16 | 2026-08-16 06:37:09 |
| [#8763: MUL-7642: full-window attachment viewer that pages through every file](https://github.com/multica-ai/multica/pull/8763) | MERGED 2026-09-23 | 2026-09-23 19:28:42 |
| [#1351: [Feature] MCP server for AI-native Multica orchestration from chat interfaces](https://github.com/multica-ai/multica/issues/1351) | OPEN (issue) | 2026-10-19 19:08:30 |
| [#9183: [Feature]: Support MCP Gateway integration with IDP-issued dynamic tokens per agent](https://github.com/multica-ai/multica/issues/9183) | OPEN (issue) | 2026-10-16 13:21:23 |
| [#6566: [Feature]: Outbound images/files on IM channels — the Channel abstraction has no media contract](https://github.com/multica-ai/multica/issues/6566) | OPEN (issue) | 2026-10-08 17:20:43 |
| [#9343: MUL-8089: UI Lab mock for skill versioning MVP](https://github.com/multica-ai/multica/pull/9343) | OPEN | 2026-10-19 23:25:56 |
| [#6227: Discussion: Should Multica be the unified SDLC standard, or an automation layer with fit criteria?](https://github.com/multica-ai/multica/issues/6227) | OPEN (issue) | 2026-10-15 06:17:49 |
| [#9498: MUL-8220: docs: interactive demos, structured pages and docs for undocumented features](https://github.com/multica-ai/multica/pull/9498) | MERGED 2026-10-26 | 2026-10-26 06:39:44 |

## Доказательства расхождений с постановками

| Проверка | Факт и следствие |
| --- | --- |
| Форк `main@8130ec095610cbb2dc218dd9198e29de36e35047` | Чтение исходников, без использования старого generated index |
| `packages/views/subscriptions/subscriptions-page.tsx:52` | `SubscriptionsPage` перенаправляет в usage с tab=subscriptions; dashboard переиспользует `SubscriptionsContent`. STT-295 частично уже покрыта, её редактирование заменено STT-433 |
| `gh api repos/inoisy/multica/pulls/45` | MERGED 24.10 16:12 UTC. Ранний отчёт STT-418 о неслитом CLI-PR уже исторический; факт установки бинаря проверяется отдельно |
| `gh api repos/inoisy/monorepo-for-multica/pulls/29` | MERGED 24.10 16:14 UTC. STT-272 в merge не означает отсутствие кода; ROOT-STEPS и живой MCP не подтверждены этим API |
| `gh api repos/inoisy/multica/pulls/54` | OPEN. Правка PWA существует, принимать и проверять iOS, не писать вторую |
| `gh api repos/inoisy/multica/pulls/53` | MERGED 26.10 09:58 UTC; схема таблиц уже включена в main |
| `multica issue pull-requests STT-272 / STT-418 / STT-437` | В таблице связей PR нет. Адреса взяты из отчётов и перечитаны через GitHub API; серверные условия ожидания PR и их snapshots по этим задачам этим не подтверждены. Причина отсутствия связей не исследовалась |
| `.github/workflows/ci.yml:252`, `:503` | Агрегаты frontend/backend существуют; файл stt.yml отсутствует |
| `gh api repos/inoisy/multica/branches/main/protection` | 404 Branch not protected — только classic protection, недостаточно для вывода обо всех правилах |
| `gh api repos/inoisy/multica/rules/branches/main` | Effective rules: deletion, non_fast_forward, update, creation; required_status_checks отсутствует. Проверки пока не обязательны по прочитанным правилам; настройки не менялись |
| Закрытие #9536 | Мейнтейнер явно выбрал #9553 и по одному условию для in_review и blocked. Новый агрегатный handoff-state не требуется |

## Все 15 сигналов STT-431

Источник — описание [STT-431](https://multica.yakutov.com/stt/issues/STT-431),
окно 25.10 03:00–26.10 03:00 UTC. Это разбор опубликованной сводки, а не новый
прогон digest. Время рана не переназначено в экономию.

| Сигнал | Решение для роадмапа |
| --- | --- |
| Отмена QA STT-424 | Разрешённая остановка приёмки; инженерный остаток cleanup уже в STT-430 |
| Пять квотных сбоев | Для четырёх есть история retry; пятому не приписывать успех. Новый дефект routing не доказан; наблюдение в теме моделей |
| 12 смешанных отмен/ошибок без issue | Разные причины, общего фикса и оценки экономии нет |
| Отмена STT-420 | Продолжение и релиз известны, причина конкретной отмены не установлена |
| Отмена STT-427 | Действие человека, не автоматический retry |
| Отмена STT-396 | Действие человека после retry, не новый дефект |
| ENOSPC до старта | Подтверждённая находка STT-430, тема QA |
| no such file | Исправление установщика уже в PR форка #51; прочие совпадения разнородны |
| timeout | Нет доказанного повторяющегося непочиненного дефекта; не повторять полный QA |
| permission denied | Известная граница Docker-доступа, не новое разрешение на права |
| not a git repository | Кандидат проверки контекста команды; пока причина не разобрана |
| docker.sock | Возможный дубль permission denied, не суммировать |
| HTTP 4xx/5xx | Без контекста код ответа не устанавливает причину |
| connection refused | Встречаются остановленная тестовая БД и Redis; причинность не установлена |
| Длительный completed QA | 58,8 мин сами по себе не доказывают лишнюю работу |

## Обновление среза

Перед новой разработкой повторить запросы по её теме, перечитать значимые
треды и сравнить поставленный SHA с upstream merge commit. На еженедельной
сверке обновить темы и кандидатов снятия работы; создавать будильник или
новый платный ран для одного формального подтверждения не требуется.
