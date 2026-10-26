# Проблемы и долги

Срез 26.10.2026. Отделяем действующий долг от исправленного дефекта и ограничения
проверки. Количества относятся к указанному наблюдению, а не к непрерывному
мониторингу. Источники — [sources.md](sources.md).

| Состояние | Проблема и доказательство | Следующее место работы |
| --- | --- | --- |
| Открыто | Поля агента и строки профиля — два входа настройки. `UpdateAgent` пишет runtime/model; `SetAgentModelProfile` меняет только ссылку. В STT-433 зафиксировано ручное выравнивание 12 агентов | [STT-433](https://multica.yakutov.com/stt/issues/STT-433), [agent.sql](../../../server/pkg/db/queries/agent.sql), [model_profile.sql](../../../server/pkg/db/queries/model_profile.sql) |
| Уточнено до разработки | `reason_code` уже NOT NULL с 607; очистка требует непустого enum CHECK. API-key `DropModel` делает модель аккаунта приоритетнее непустой строки. Действующий `subscription_pool.all_workspaces` не относится к удаляемому наследию | [Разбор STT-433 с первичными доказательствами](reviews/stt-433.md) |
| Требует решения автора | `subscription_usage` хранит замеры квоты, отдельного журнала ручных команд нет. Требование `subscription run` нельзя считать покрытым этими замерами | [Вопрос и варианты STT-433](reviews/stt-433.md#единственный-вопрос-по-продукту) |
| Открыто | Состав пула редактируется из аналитики и среды одним компонентом. Технически одна мутация, но два пути управления | [PoolEditor](../../../packages/views/subscriptions/pool-editor.tsx), [SubscriptionsContent](../../../packages/views/subscriptions/subscriptions-page.tsx), [цель STT-433](https://multica.yakutov.com/stt/issues/STT-433) |
| Открыто | «Обслуживает» среды считает прямое поле агента и пропускает ссылки через строки общих и личных профилей | [runtime-detail.tsx](../../../packages/views/runtimes/components/runtime-detail.tsx), [STT-433](https://multica.yakutov.com/stt/issues/STT-433) |
| Отложено по условию | Старая схема сохранена для отката: `agent_model_option`, `reserve`, `subscription_limit_backup`, старые колонки пула. Последние миграции — до 612; файла 615 нет | [Миграции](../../../server/migrations), [контрольный ответ](acceptance.md), [STT-433](https://multica.yakutov.com/stt/issues/STT-433) |
| Открыто перед 615 | Спецификация §6 говорит, что код не пишет `agent_model_option`, но очистка и merge runtime всё ещё содержат DELETE/UPDATE: `DeleteRuntimeModelReferences`, `ReassignRuntimeModelReferences`, `DeleteWorkspaceAgents`. Бизнес-чтения уже нет; эти три остатка необходимо снять вместе с DROP | [Контрольный ответ и вызывающие обработчики](acceptance.md), [sqlc](../../../server/pkg/db/queries/model_profile.sql), [workspace_delete.sql](../../../server/pkg/db/queries/workspace_delete.sql) |
| Открыто | Старый конвейер сосуществует с Workflow; часть команды accept нужна обоим. Удаление без проверки связей сломает приёмку | [STT-429](https://multica.yakutov.com/stt/issues/STT-429), [handler accept](../../../server/internal/issueworkflow/issueworkflow.go) |
| Открыто | У встроенного или собственного входа без пула аккаунт запуска неизвестен; нельзя приписывать расход конкретной подписке | [STT-433](https://multica.yakutov.com/stt/issues/STT-433), [контракт account_fact](../../engineering/model-accounts-server-api.md) |
| Зависимость | Удаление `settings.cli`/`quota_row` требует остановить внешних читателей stt-agents; индекс этого репозитория их не видит | [STT-429](https://multica.yakutov.com/stt/issues/STT-429), [STT-433](https://multica.yakutov.com/stt/issues/STT-433) |
| Риск интеграции | Номера миграций 564–572 пересекаются с PR апстрима #8931 | [STT-435](https://multica.yakutov.com/stt/issues/STT-435), [PR #8931](https://github.com/multica-ai/multica/pull/8931) |
| Открыто, проверено в STT-441 | В `ci.yml` есть агрегаты backend/frontend, но effective rules `main` не содержат required_status_checks. Classic protection возвращает 404; ruleset защищает update/creation/deletion/non-fast-forward. `stt.yml` пока отсутствует | [Тема CI](roadmap.md#ci), [команды и факты](roadmap-upstream-check.md). Настройки и workflows этим исследованием не менялись |

Приёмка STT-424 выявляла 500 на `/profiles`, утечку личных профилей, ошибку caps
с null и неизвестным окном, проблемы форм на 375 px и локализации. Поздние отчёты
фиксируют исправления; не переносим ранний отрицательный результат как текущий
баг. Основание: [STT-424](https://multica.yakutov.com/stt/issues/STT-424), комментарии
`01a17077-dd4e-7572-a24d-bb15a8b24ea3` и
`01a17227-12f6-7cfd-9cb7-20ee8bff0c11`; [контракт прав и caps](../../engineering/model-accounts-server-api.md).

На прод-копии STT-424 у 154 из 156 контекстов прогноз совпал; два контекста дали
14 отличий из-за обработки недельной квоты 100% как исчерпанной. Это отдельное
описанное изменение поведения, не доказательство ошибки переноса. Часть Cursor
тестов ограничена ядром хоста <6.9; результат тех прогонов нельзя переименовывать
в полный зелёный тест. Источник: [STT-424](https://multica.yakutov.com/stt/issues/STT-424),
комментарий `01a17244-db12-789d-af1d-b5fa2d2b7eb5`.

Ограничение базы: свежей съёмки работающего backend/web в STT-436 нет. UI описан
по коду и архивам приёмки, цель — по v5. Готовый индекс также статический: ошибки
разбора и ложные связи возможны, поэтому ответ требует ссылки на исходник.
См. [ui.md](ui.md), `engine` и `limitations` в [индексе](generated/index.json).
