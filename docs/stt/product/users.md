# Пользователи и сценарии

Срез и источники — [README](README.md), [sources.md](sources.md). Продуктовые роли
ниже выведены из кода и перечисленных задач; это не исследование аудитории.

| Пользователь | Задача | Действие и граница доступа | Источник |
| --- | --- | --- | --- |
| Евгений, владелец инстанса | Управлять ресурсами, принимать решения и релизы | Личный вход администратора меняет аккаунты, пулы и выдачу; права агента этого не заменяют | [Контракт пулов](../../engineering/model-accounts-server-api.md), [STT-433](https://multica.yakutov.com/stt/issues/STT-433) |
| Член команды | Назначить работу и проверить результат | Задача, комментарии, история запусков, PR; доступ к приватному агенту ограничен | [README](../../../README.md), [agent.go](../../../server/internal/handler/agent.go), [видимость моделей](../../../server/internal/handler/model_profile.go) |
| Владелец агента / администратор workspace | Выбрать поведение агента | Общий профиль, личный профиль или одна модель; на задаче — явное переопределение | [ModelProfileService](../../../server/internal/service/model_profile.go), [API](../../engineering/model-accounts-server-api.md) |
| Агент | Исполнить поручение и передать проверяемый результат | Task token работает в контексте задачи; операции instance admin in person ему недоступны | [pool.go](../../../server/internal/handler/pool.go), [Workflow](../../../server/internal/handler/issue_workflow.go) |
| Пользователь другого workspace | Работать со своими сущностями на том же инстансе | Видит только доступные ему выдачи пула и среды; наличие пула у другого workspace не раскрывается | [pool_directory.go](../../../server/internal/handler/pool_directory.go), [контракт](../../engineering/model-accounts-server-api.md) |
| Демон на машине | Запустить CLI с допустимым аккаунтом | Получает предложение при claim, подтверждает аккаунт до запуска; старый демон оставляет факт неизвестным | [subscription_daemon.go](../../../server/internal/handler/subscription_daemon.go), [контракт](../../engineering/model-accounts-server-api.md) |

Основной сценарий команды: назначение задачи → серверный выбор строки профиля →
claim и `account_offer` → подтверждение `account_fact` → выполнение → результат и
переход Workflow с SHA. Предварительный прогноз не резервирует аккаунт и не
является фактом состоявшегося запуска. Источники:
[model_routing.go](../../../server/internal/service/model_routing.go),
[model_profile.go](../../../server/internal/service/model_profile.go),
[контракт](../../engineering/model-accounts-server-api.md).

Сценарий изменения ресурсов сейчас: администратор редактирует пул из «Подписок»
или страницы среды через общий `PoolEditor`. Цель STT-433 — оставить управление
составом только на странице среды, аккаунты перенести во вкладку «Аккаунты»,
а аналитику оставить для наблюдения. Источники:
[PoolEditor](../../../packages/views/subscriptions/pool-editor.tsx),
[PoolAccountsSection](../../../packages/views/subscriptions/pool-accounts-section.tsx),
[решение 26.10](https://multica.yakutov.com/stt/issues/STT-433).
