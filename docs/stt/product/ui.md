# Интерфейс и визуальные источники

Кодовый срез — `f12d2020e`, схема 612. Архив приёмки и макет не подменяют свежую
съёмку прода. Источники: [sources.md](sources.md), [манифесты](generated/screenshots.json).

## Навигация сейчас

Workspace — контекст пути. Общие web/desktop-экраны находятся в `packages/views`,
маршрутизация — в приложениях, пути — в
[packages/core/paths/paths.ts](../../../packages/core/paths/paths.ts). Это правило
границ пакетов из [AGENTS.md](../../../AGENTS.md).

«Агенты», «Команды», «Навыки», «Среды выполнения» — разделы команды;
«Аналитика» — наблюдение. Подписи брать из
[ru/layout.json](../../../packages/views/locales/ru/layout.json),
[ru/agents.json](../../../packages/views/locales/ru/agents.json),
[ru/runtimes.json](../../../packages/views/locales/ru/runtimes.json).
«Подписки» сейчас открываются на `/{slug}/usage?tab=subscriptions`, старый
`/{slug}/subscriptions` перенаправляет туда. Источники:
[SubscriptionsPage](../../../packages/views/subscriptions/subscriptions-page.tsx),
[DashboardPage](../../../packages/views/dashboard/components/dashboard-page.tsx).

У агента настройки выполнения в инспекторе ещё редактируются через
`RuntimePicker` и `ModelPicker`. Вкладка моделей имеет общий/личный/однострочный
профиль и старый fallback `fields`. Цель STT-433 — инспектор только для просмотра,
без отдельного режима штатных полей. Источники:
[inspector](../../../packages/views/agents/components/agent-detail-inspector.tsx),
[agent-models.tsx](../../../packages/views/agents/components/agent-models.tsx),
[STT-433](https://multica.yakutov.com/stt/issues/STT-433).

## Размещение целевого управления

Пул редактируется на странице среды; реестр аккаунтов и вход — во вкладке
«Среды выполнения → Аккаунты»; аналитика содержит квоты, историю, расхождения и
ссылку на среду. Нового отдельного пути создания пула нет: пул появляется со
средой через аккаунты. Это принятое направление, ещё не меню схемы 612.
Источник: [STT-433](https://multica.yakutov.com/stt/issues/STT-433), комментарий
`01a17777-90af-7766-b714-1c1e1cb7459f`,
[v5](https://art-multica-kiss-v5-0da4e4.stand.yakutov.com).

## Контролы и проверка макета

Использовать существующий UI kit: [Button](../../../packages/ui/docs/button.md),
[Dialog](../../../packages/ui/docs/dialog.md),
[Select](../../../packages/ui/components/ui/select.tsx), семантические токены
[tokens.css](../../../packages/ui/styles/tokens.css). Для действий — Button,
для переходов — ссылки. Права и причина недоступности должны быть видны рядом
с действием, повторяющий заголовок поясняющий текст не нужен. Источник:
[AGENTS.md](../../../AGENTS.md), разделы UI Copy и Web/Desktop UI Rules.

Проверять скриншотами светлую и тёмную темы × 1440/375, отдельно открытые формы
и списки. Высота соседних контролов согласована, текст не обрезается и не
выходит за край. У native select — `appearance: none`, свой шеврон, отступ
справа не менее 28 px и различимость в обеих темах. Это workspace-правило
приёмки, основание — инструкции задачи STT-436 и архивы STT-424.

## На каких снимках основано описание

В STT-439 подготовлен v6 макета: раздельные строка 1/прогноз/факт, настоящий
личный режим в демо, черновик пула/выдачи/ревизии, просмотр без прав и мобильные
карточки без скрытия сведений. Исходники и матрица снимков переданы через
STT-439; публикация ждёт MCP stand. Публичная ссылка выше пока остаётся v5.
Это проверка прототипа, не свежая съёмка продукта. Доказательства и границы:
[reviews/stt-433.md](reviews/stt-433.md#визуальная-проверка).

| Источник | Конкретные файлы | Что доказывают / предел |
| --- | --- | --- |
| STT-423, `gallery/shots` | `08-agent-personal-match-*.webp`, `10-agent-single-*.webp`, `18-profile-edit-*.webp`, `22-agent-profile-select-open-*.webp`, `24-subscription-limit-edit-*.webp` | Общий/личный/однострочный режим, редактор профиля, открытый список, cap. Историческая приёмка, не новый замер |
| STT-424, `workdir/shots` | `profiles-{light,dark}-{1440,375}.png`, `runtimes-{light,dark}-{1440,375}.png`, `subs-{light,dark}-{1440,375}.png` | Состав страниц, обе темы и ширины |
| STT-424, формы | `pooledit-dark-375.png`, `poolnew-light-375.png`, `profile-editor-{light,dark}-375.png`, `profile-row-select-{light,dark}-375.png`, `wizard1-light-375.png`, `wizard2-dark-375.png` | Высота и переносы контролов, списки, длинные формы. Папка смешивает несколько прогонов; SHA каждого файла отдельно не восстановлен |
| STT-436, новая съёмка макета STT-433 v5 | `runtime-pool-{light,dark}-{1440,375}.png`, `accounts-{light,dark}-{1440,375}.png`, `subscriptions-{light,dark}-{1440,375}.png` | Целевое размещение управления, 12 снимков; не фактическая реализация |

Новая съёмка v5: нет document overflow и pageerror в 12 состояниях; два select
на странице пула имеют `appearance: none`, `padding-right: 34px`, шевроны видны
на скриншотах. Значения квот и состояния аккаунтов в макете — данные самого
макета, не свежая телеметрия. Снимки и аудиты перечислены в
[generated/screenshots.json](generated/screenshots.json).

Скрипт [capture.mjs](../../../tools/product/capture.mjs) умеет съёмку URL с
проверкой готовности, действиями над формой и матрицей тем/ширин, а также импорт
архива с SHA256. Проверен на v5 и архиве STT-424. Для свежего продуктового стенда
нужны изолированные web/backend/БД с известным SHA, тестовый пользователь и его
Playwright storage state. В этом ране MCP stand не был доступен; продовые
настройки и учётные данные не менялись. Пользоваться архивом здесь разрешено
критериями STT-436; свежий live smoke не заявлен.
