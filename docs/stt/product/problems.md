# Проблемы и долги

Срез 26.10.2026. Отделяем действующий долг от исправленного дефекта и ограничения
проверки. Количества относятся к указанному наблюдению, а не к непрерывному
мониторингу. Источники — [sources.md](sources.md).

## Дополнение 27.10: запуск GitHub Actions восстановлен

В 07:29 UTC контрольный dispatch успешно создал
[CI run 40467400359](https://github.com/inoisy/multica/actions/runs/40467400359),
который завершился success. Прежний отказ 422 больше не воспроизводится;
причина и способ снятия прежнего запрета неизвестны. Ожидание Support/владельца
для запуска отменено, релиз продолжает Техдиректор. Проверки головы PR #103
не заменяются успешным CI main. Ниже — доказательства прежнего состояния.

### Диагностика до восстановления

В [STT-450](https://multica.yakutov.com/stt/issues/STT-450) Евгений повторил
`gh workflow run ci.yml --repo inoisy/multica --ref main` и получил HTTP 422
`Actions has been disabled for this repository`. Проверка API Продактом:
`enabled=true`, `allowed_actions=all`, CI workflow `395222620` активен,
`workflow_dispatch` присутствует; репозиторий публичный, не архивирован
и не отключён. Последний записанный успешный CI создан 26.10 в 21:43:31 UTC,
[run 40406744971](https://github.com/inoisy/multica/actions/runs/40406744971).

Это не root-блокер сервера. [Документация GitHub](https://docs.github.com/en/repositories/managing-your-repositorys-settings-and-features/enabling-features-for-your-repository/managing-github-actions-settings-for-a-repository)
описывает отдельное GitHub-controlled disabled state, которое не снимается
настройками Actions. Применимость и причина в нашем случае не были подтверждены;
до успешного dispatch следующим шагом была проверка владельцем сообщения
в Actions и подготовленного обращения в GitHub Support.
Биллинг недоступен текущему CLI credential, scope и лимиты расходов не менялись.
Покупка минут из одного ответа 422 не следует: стандартные hosted runners
публичных репозиториев [бесплатны](https://docs.github.com/en/actions/concepts/billing-and-usage).

Факты и готовый английский черновик обращения сохранены в комментарии
STT-450 `01a17c13-afa8-776b-bf92-694232980992`. В поддержку ничего не отправлено.
Прежнее ожидание ответа владельца после успешного dispatch отключено;
root-настройки и обязательные проверки не ослаблялись.

## Исходный срез 26.10

| Состояние | Проблема и доказательство | Следующее место работы |
| --- | --- | --- |
| Проработка | «Эпики» показывает только включённый stage-dispatch и дублирует контрол карточки задачи; название и место не объясняют назначение | [STT-455](https://multica.yakutov.com/stt/issues/STT-455), [кодовые доказательства и границы](reviews/2026-10-26-task-audit.md#страница-эпики). Перенос/удаление вкладки ещё не согласован |
| Требует решения автора | MCP нельзя включить в общий откат одним compose: autodeploy перечисляет backend/frontend; минимальные изменения сервисов/образов/health ограничены постановкой | [STT-454](https://multica.yakutov.com/stt/issues/STT-454), [подготовленные PR и блокер](reviews/2026-10-26-task-audit.md#открытый-вопрос-stt-454) |
| Открыто | Поля агента и строки профиля — два входа настройки. `UpdateAgent` пишет runtime/model; `SetAgentModelProfile` меняет только ссылку. В STT-433 зафиксировано ручное выравнивание 12 агентов | [STT-433](https://multica.yakutov.com/stt/issues/STT-433), [agent.sql](../../../server/pkg/db/queries/agent.sql), [model_profile.sql](../../../server/pkg/db/queries/model_profile.sql) |
| Уточнено до разработки | `reason_code` уже NOT NULL с 607; очистка требует непустого enum CHECK. API-key `DropModel` делает модель аккаунта приоритетнее непустой строки. Действующий `subscription_pool.all_workspaces` не относится к удаляемому наследию | [Разбор STT-433 с первичными доказательствами](reviews/stt-433.md) |
| Требует решения автора | `subscription_usage` хранит замеры квоты, отдельного журнала ручных команд нет. Требование `subscription run` нельзя считать покрытым этими замерами | [Вопрос и варианты STT-433](reviews/stt-433.md#единственный-вопрос-по-продукту) |
| Открыто | Состав пула редактируется из аналитики и среды одним компонентом. Технически одна мутация, но два пути управления | [PoolEditor](../../../packages/views/subscriptions/pool-editor.tsx), [SubscriptionsContent](../../../packages/views/subscriptions/subscriptions-page.tsx), [цель STT-433](https://multica.yakutov.com/stt/issues/STT-433) |
| Открыто | «Обслуживает» среды считает прямое поле агента и пропускает ссылки через строки общих и личных профилей | [runtime-detail.tsx](../../../packages/views/runtimes/components/runtime-detail.tsx), [STT-433](https://multica.yakutov.com/stt/issues/STT-433) |
| Отложено по условию | Старая схема сохранена для отката: `agent_model_option`, `reserve`, `subscription_limit_backup`, старые колонки пула. Последние миграции — до 612; файла 615 нет | [Миграции](../../../server/migrations), [контрольный ответ](acceptance.md), [STT-433](https://multica.yakutov.com/stt/issues/STT-433) |
| Открыто перед 615 | Спецификация §6 говорит, что код не пишет `agent_model_option`, но очистка и merge runtime всё ещё содержат DELETE/UPDATE: `DeleteRuntimeModelReferences`, `ReassignRuntimeModelReferences`, `DeleteWorkspaceAgents`. Бизнес-чтения уже нет; эти три остатка необходимо снять вместе с DROP | [Контрольный ответ и вызывающие обработчики](acceptance.md), [sqlc](../../../server/pkg/db/queries/model_profile.sql), [workspace_delete.sql](../../../server/pkg/db/queries/workspace_delete.sql) |
| Открыто | Старый конвейер сосуществует с Workflow; часть команды accept нужна обоим. Удаление без проверки связей сломает приёмку. Прогон команд в accept дублирует CI в репо, где он есть (решение 26.10) | [STT-429](https://multica.yakutov.com/stt/issues/STT-429), [handler accept](../../../server/internal/issueworkflow/issueworkflow.go) |
| Открыто | У встроенного или собственного входа без пула аккаунт запуска неизвестен; нельзя приписывать расход конкретной подписке | [STT-433](https://multica.yakutov.com/stt/issues/STT-433), [контракт account_fact](../../engineering/model-accounts-server-api.md) |
| Зависимость | Удаление `settings.cli`/`quota_row` требует остановить внешних читателей stt-agents; индекс этого репозитория их не видит | [STT-429](https://multica.yakutov.com/stt/issues/STT-429), [STT-433](https://multica.yakutov.com/stt/issues/STT-433) |
| Риск интеграции | Номера миграций 564–572 пересекаются с PR апстрима #8931 | [STT-435](https://multica.yakutov.com/stt/issues/STT-435), [PR #8931](https://github.com/multica-ai/multica/pull/8931) |
| Исправлено 26.10 | При STT-441 обязательных checks ещё не было. Позже включены `backend`, `frontend`, `commit-signatures`; повторное чтение branch protection 26.10 подтвердило этот список, `enforce_admins=false` | [Решение о CI](decisions.md), [тема CI](roadmap.md#ci). Старый замер в [журнале](roadmap-upstream-check.md) остаётся историей |

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
