# Выкладка выбора модели на сервере (ТЗ моделей, этапы 1–2)

Пакет заменяет только контейнер backend живого стека multica. PostgreSQL, frontend
и daemon хоста не останавливаются, не пересоздаются и не получают сигналов.

## Что в релизе

Модель рана выбирает backend при claim: профиль задачи → свой список агента → профиль
агента → штатные поля. Берётся первая строка, чей runtime онлайн и не на лимите. Ран с квотой
провайдера (включая отказ `subs-pick` «выше лимита агентам») сразу повторяется на следующей
модели, его runtime попадает в `runtime_unavailable` до сброса из текста ошибки (без времени —
на 60 мин). Весь список на лимите — запасной профиль или ожидание с уведомлением владельцу
workspace во входящие (одно на эпизод и «снова работает» при выходе). Задача планировщика
`model_routing` раз в 30 секунд снимает истёкшие лимиты и сама возвращает ждущие раны в очередь.

Агенты без профиля и без своего списка работают как раньше: их раны не маршрутизируются.

Схема меняется только добавлением: миграции 575–585 (таблицы `model_profile`,
`model_profile_option`, `agent_model_option`, `runtime_unavailable`, `model_idle_episode`,
колонки `agent.model_profile_id`, `agent.disallowed_tools`, `issue.model_profile_id`,
`agent_task_queue.model_position`, `agent_task_queue.model_choice`; индексы — отдельными
`CONCURRENTLY`-файлами).

## Сборка пакета (без root и docker)

```sh
deploy/stt-models/build-package.sh --revision <SHA> --daemon-pid <PID daemon>
```

`--base` (по умолчанию `570b73bb4`) — коммит форка с деревом живого backend, от него считается
диапазон миграций. `--previous-commit` (по умолчанию `b74f84e2e`) — коммит, который живой backend
отдаёт в `/health`: тот же код, собранный на сервере до того, как у форка появились свои SHA.

Создаёт `/home/agents/deploy/stt-models-<SHA9>/` с `source.tar.gz`, `manifest.json` и скриптами.

## Установка (root)

```sh
sudo /home/agents/deploy/stt-models-<SHA9>/preflight.sh --manifest …/manifest.json   # ничего не меняет
sudo /home/agents/deploy/stt-models-<SHA9>/install.sh   --manifest …/manifest.json --stop-provider-timer
```

`install.sh` запускает preflight и затем:

1. находит стек по меткам запущенного backend, теггирует текущий образ
   (`multica-backend:pre-stt-models-<суффикс>`), создаёт `/var/backups/multica-stt-models.<суффикс>/`
   с `state.env`, `rollback.sh`, `manifest.json`;
2. собирает образ `docker buildx build --builder stand-build --load` из `source.tar.gz`;
3. применяет миграции новым образом, пока старый backend работает (старый код новых таблиц не читает);
4. пересоздаёт только backend (`up -d --no-deps --pull never`), ждёт `/health` с новым коммитом;
5. с `--stop-provider-timer` — после ответа нового backend выключает полосу провайдеров сторожа
   (`systemctl disable --now multica-provider.timer`); прежнее состояние таймера пишется в `state.env`.
   Без флага таймер не трогается, и установщик напоминает: модели не должны менять двое;
6. сверяет контейнеры postgres/frontend и PID daemon с записанными до замены.

Коды выхода: `0` готово; `2` новый backend не ответил — прежний образ возвращён автоматически;
`3` backend заменён, но набор PID daemon или контейнер защищённого сервиса изменился.

После установки — перенос состояния пульта (раздел 7 ТЗ), сначала сухим прогоном:

```sh
multica profile import --file models-import.json          # план, ничего не пишет; unexplained должно быть 0
multica profile import --file models-import.json --apply
```

Формат документа — `docs/stt/models-import.md`.

## Аварийный выключатель

`MULTICA_MODEL_CHOICE=off` в окружении backend отключает выбор при claim и маршрутизацию:
новые claim идут по runtime и модели агента. Таблицы и профили остаются. Включается
пересозданием только backend с этой переменной.

## Откат

```sh
sudo /var/backups/multica-stt-models.<суффикс>/rollback.sh
```

Возвращает прежний образ только для backend и ждёт `/health` с прежним коммитом. Затем:

- неначатые раны, поставленные списком моделей на другой runtime, возвращаются на runtime
  своего агента (прежний код забирает ран только там) и теряют `model_choice`;
- таймер провайдеров включается снова, если установка его выключила и до неё он был включён.

Таблицы и колонки остаются: прежний код их не читает. Уже идущие раны доходят до конца.

## Что смотреть при сбое

- `multica agent models <агент>` — какой список, что выбрал бы сервер, журнал последних выборов;
- `multica runtime unavailable` — какие runtime на лимите и до какого времени;
- `docker compose logs backend | grep 'model choice'` — маршрутизация, лимиты, их снятие.

## Проверка пакета

```sh
deploy/stt-models/test/run-tests.sh   # preflight/install/rollback на подменённых docker, systemctl, curl, pgrep
bash -n deploy/stt-models/*.sh
```
