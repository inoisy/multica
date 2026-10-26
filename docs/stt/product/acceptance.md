# Контрольные ответы STT-436

Срез `f12d2020efe929ac670ba1bf10be67c8013b65f5`, `origin/runtime/0.6.1-live`.
Воспроизведение: `python3 tools/product/verify.py`. Полный вывод:
[generated/control-answers.txt](generated/control-answers.txt). Все три вопроса
выполняются за несколько секунд, меньше минуты; время записано в конце вывода.

## 1. Кто пишет agent.runtime_id/model и откуда их читает страница

Основной пользовательский путь — `CreateAgent` и `UpdateAgent`:
[handler/agent.go](../../../server/internal/handler/agent.go),
[agent.sql:55 и :146](../../../server/pkg/db/queries/agent.sql). Существуют также
системные создания и перепривязки: индекс находит 9 sqlc-запросов записи
`runtime_id` и 5 записи `model`, включая builder, accept, системного пользователя,
очистку доступа и merge runtime. Полный список дают `writers agent runtime_id`
и `writers agent model`.

Смена профиля идёт через `SaveAgentModels` / `savePersonalProfile` →
`SetAgentModelProfile`, который обновляет только `model_profile_id` и время.
Автоматической проекции строки 1 в runtime/model здесь нет; это цель STT-433.
Источники: [service/model_profile.go](../../../server/internal/service/model_profile.go),
[model_profile.sql:91](../../../server/pkg/db/queries/model_profile.sql),
[STT-433](https://multica.yakutov.com/stt/issues/STT-433).

Чтение: `AgentDetailPage` → `agentDetailOptions(wsId, agentId)` →
`api.getAgent` → `GET /api/agents/{id}` → `Handler.GetAgent` /
`loadAgentForView` → sqlc `GetAgentInWorkspace` / `GetAgent` → таблица `agent`.
`agentToResponse` переносит сохранённые `RuntimeID` и `Model` в ответ.
Инспектор получает объект `agent` и показывает/меняет `agent.runtime_id` и
`agent.model`; он не подменяет их строкой 1 профиля. Источники:
[AgentDetailPage](../../../packages/views/agents/components/agent-detail-page.tsx),
[queries.ts:67](../../../packages/core/workspace/queries.ts),
[ApiClient.getAgent](../../../packages/core/api/client.ts),
[handler/agent.go](../../../server/internal/handler/agent.go),
[inspector](../../../packages/views/agents/components/agent-detail-inspector.tsx).

## 2. Какие экраны вызывают API пулов

В текущем коде основные поверхности — «Аналитика → Подписки» и детали среды.
Первый использует `SubscriptionsContent`, второй — `PoolAccountsSection`.
Общий `PoolEditor` вызывает `useUpdatePool` и `useSetPoolGrants`;
`NewPoolDialog` и `AddSubscriptionDialog` умеют создавать пул.
Источники: [SubscriptionsContent](../../../packages/views/subscriptions/subscriptions-page.tsx),
[PoolAccountsSection](../../../packages/views/subscriptions/pool-accounts-section.tsx),
[PoolEditor](../../../packages/views/subscriptions/pool-editor.tsx),
[NewPoolDialog](../../../packages/views/subscriptions/new-pool-dialog.tsx),
[AddSubscriptionDialog](../../../packages/views/subscriptions/add-subscription-dialog.tsx).

`poolListOptions` / мутации → `ApiClient.listPools/createPool/updatePool/setPoolGrants`
→ `/api/pools` → `handler/pool.go` → sqlc `subscription_pool.sql`.
Данные включают пул, участников, выдачу, среды, аккаунты, квоты и состояние входа.
Источники: [core queries](../../../packages/core/subscriptions/queries.ts),
[API client](../../../packages/core/api/client.ts),
[router.go:1728](../../../server/cmd/server/router.go),
[pool.go](../../../server/internal/handler/pool.go),
[sqlc](../../../server/pkg/db/queries/subscription_pool.sql).

`screens /api/pools` также находит web/desktop-оболочки и условно подключаемые
компоненты: это возможные зависимости, не утверждение о запросе при каждом
открытии экрана. STT-433 переносит управление из аналитики, но на этом SHA
перенос ещё не выполнен.

## 3. Что удаляет 615 и кто ещё зависит от старого

**Файла 615 в этой ветке нет.** План STT-433 удаляет две таблицы:
`agent_model_option` и `subscription_limit_backup`. `reserve` — колонка
`subscription`, не таблица. Также планируются удаление старых колонок пула
`runtime_profile.subscriptions/share_key/all_workspaces`,
`subscription_pool.legacy_share_key`, индекса 592 и строк `pool_verdict`, затем
обязательный `reason_code` с CHECK. Источники:
[STT-433](https://multica.yakutov.com/stt/issues/STT-433),
[спецификация, §7](../../engineering/model-selection-and-accounts.md).

У `agent_model_option` не осталось SELECT-пути выбора модели. Но остались
SQL-зависимости, которые сломаются после DROP:

| Запрос | Операция | Кто вызывает |
| --- | --- | --- |
| `DeleteRuntimeModelReferences`, `model_profile.sql:173` | DELETE старых строк | `DeleteAgentRuntime`, `UnbindAgentsAndDeleteRuntime`, `DeleteRuntimeProfile` |
| `ReassignRuntimeModelReferences`, `model_profile.sql:193` | UPDATE runtime_id | `mergeLegacyRuntime` в `handler/daemon.go` |
| `DeleteWorkspaceAgents`, `workspace_delete.sql:687` | DELETE старых строк | `DeleteWorkspace` |

Источники: [model_profile.sql](../../../server/pkg/db/queries/model_profile.sql),
[workspace_delete.sql](../../../server/pkg/db/queries/workspace_delete.sql),
[handler/runtime.go](../../../server/internal/handler/runtime.go),
[runtime_profile.go](../../../server/internal/handler/runtime_profile.go),
[daemon.go](../../../server/internal/handler/daemon.go),
[workspace.go](../../../server/internal/handler/workspace.go).
Исторические миграции 609 up/down тоже используют таблицу; старые down-пути
потребуют пересмотра. `subscription_limit_backup` встречается только в 611
up/down; текущих sqlc-читателей индекс не находит.

Дополнительный `rg` по `server/` вне миграций, generated и тестов подтвердил
только три перечисленных остатка. Внешние читатели `settings.cli`/`quota_row`
в stt-agents этим индексом не покрыты — их снятие связано со STT-429.
Удаление 615 допустимо не раньше 01.11.2026, после отдельного разрешения Евгения
и проверки копии БД; STT-436 ничего из этого не исполняет.
