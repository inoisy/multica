Репозиторий inoisy/multica — форк multica с русской локализацией и своими правками. Работай от ветки `runtime/0.6.1-live`, а не от default: `git fetch origin && git checkout -B <твоя ветка> origin/runtime/0.6.1-live`.

Контекст. Self-hosted multica (multica.yakutov.com, workspace stt) с агентами сквада «Разработка». 19.10.2026 выкачен backend STT-258 (серверный opt-in маршрут задачи, дедуп wake-up); он слит в `runtime/0.6.1-live` squash-коммитом 570b73b, дерево совпадает с продом. Решено: логику моделей агента (сейчас внешний пульт multica-control + controls.json + полоса провайдеров внешнего сторожа + CLI mconv) перенести в сам форк и упростить: профиль моделей — отдельная сущность workspace, агенты и задачи ссылаются на него (правка профиля действует на всех), свой список у агента — явное исключение; модель выбирает backend при claim на каждый ран, на лимите провайдера — сразу следующая модель списка, всё на лимите — запасной профиль или ожидание с уведомлением владельцу. Цель — задачи идут от назначения до слияния без человека и без простоев, без потери качества.

ТЗ: `docs/stt/tz-model-tiers.md` (версия 3) в ветке `runtime/0.6.1-live`. Прочитай целиком. Делай этапы 1–3 из раздела 6. Этапы 4–5 и раздел 10 не трогай.

Базы:
- backend — `runtime/0.6.1-live`. Это прод, включая серверный маршрут STT-258: `server/internal/service/issue_conveyor*.go`, миграции 564–574.
- веб — ветка `ru-v0.6.1-fallback`. Там экран фоллбеков `packages/views/agents/components/agent-fallback-chain.tsx` (ходит во внешний пульт `/control/api` — это и заменяем на штатный API), страницы «Подписки» и «Конвейер». Ветки веба и backend расходятся: веб-PR делай от `ru-v0.6.1-fallback` в `ru-v0.6.1-fallback`, backend-PR — в `runtime/0.6.1-live`.

Что уже проверено в коде:
- runtime ран получает при постановке: 8 мест в `server/internal/service/task.go` берут `agent.RuntimeID`;
- модель, рассуждение и `custom_args` берутся из агента при claim, в сборке `AgentData` в `server/internal/handler/daemon.go` (~2490–2545);
- квоту классифицирует `taskfailure.Classify`, переход маршрута делает `OnTaskTerminal` (STT-258);
- scheduler — по образцу `server/internal/scheduler/jobs_issue_conveyor.go`;
- `waiting_provider` маршрута сейчас выходит только внешним `issue rerun`. По ТЗ повтор после квоты делает сам backend — согласуй с `RerunGuard`: один преемник, тот же `conveyor_action_id`.

PR — по этапу, каждый от предыдущего:
1. backend, только чтение;
2. включение и пакет установки/отката по образцу `deploy/stt-258/` (только backend; PostgreSQL, frontend и демон не трогать; сборка через `docker buildx build --builder stand-build --load`, не голый `docker build`);
3. веб.

Коммиты в формате `feat(models): … T-12345`, `fix(models): …` и т.п. Ни в коммитах, ни в PR никаких упоминаний ИИ-ассистентов: без Co-Authored-By, без «Generated with». В описании PR первой строкой — https://multica.yakutov.com/stt/issues/STT-258.

Правила:
- миграции только аддитивные, индексы через `CONCURRENTLY` отдельными файлами с записью в `concurrentIndexCleanups`, как 566–574;
- `sqlc generate` чистый;
- тесты на реальной PostgreSQL, демоны и агенты подменены, реальных агентских запусков нет;
- в диффе 0 `as any`, `@ts-ignore`, `eslint-disable`;
- переводы через i18n, как у текущего экрана фоллбеков.

Готово:
- PR этапов 1–3 открыты;
- тесты из раздела 8 ТЗ зелёные, вывод — в описании PR;
- к этапу 2 — пакет `deploy/<имя>/` с README, preflight, install, rollback и тестами скриптов;
- к этапу 3 — скриншоты экрана «Модели» и страницы «Профили» на 1440px, если окружение позволяет поднять веб; иначе Storybook или тест рендера и пометка, что скриншоты за выкаткой.

Сервер, выкатка и `controls.json` — не твоя зона: данные для импорта (раздел 7) описывай форматом и тестами на фикстурах. Вопрос по смыслу, который ТЗ не решает, — запиши в PR как допущение и иди дальше. Не останавливайся.
