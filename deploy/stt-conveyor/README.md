# Выкладка конвейера в multica (маршрут сквада, шаг accept, стадии, повторы)

Пакет к этапам 4 ТЗ моделей и 1–3 ТЗ `docs/stt/tz-conveyor-in-multica.md`. Две части:

1. **backend** — тем же пакетом, что выбор модели (`deploy/stt-models`): он собирает образ из любого коммита форка,
   применяет новые миграции (586 — шаблон маршрута сквада, 587–588 — стадии эпика, все аддитивные) и заменяет
   только контейнер backend, не трогая PostgreSQL, frontend и демон;
2. **демоны** — этот пакет: новый `multica` на каждом хосте демона, который должен исполнять шаг `accept`
   (runtime `accept` регистрирует каждый обновлённый демон; выключить на хосте — `MULTICA_ACCEPT_RUNTIME=off`).

Порядок: backend → демоны → настройка (`docs/stt/conveyor-accept.md`, `docs/stt/stage-dispatch.md`) → снятие сторожа
(`services/conveyor-retire` в монорепо). Старые демоны с новым backend работают как раньше: шаг `accept` просто
некому исполнять, пока демон не обновлён, — не включайте `accept` в маршруты до обновления.

## Backend

```sh
deploy/stt-models/build-package.sh --revision <SHA> --base <SHA, который сейчас в backend> \
  --previous-commit <коммит из /health backend> --daemon-pid <PID демона>
sudo /home/agents/deploy/stt-models-<SHA9>/preflight.sh --manifest …/manifest.json
sudo /home/agents/deploy/stt-models-<SHA9>/install.sh   --manifest …/manifest.json
```

`--stop-provider-timer` уже не нужен, если полоса провайдеров сторожа выключена при выкладке моделей.

## Демоны

Сборка (без root, нужен go):

```sh
deploy/stt-conveyor/build-cli.sh --revision <SHA>          # → /home/agents/deploy/stt-conveyor-<SHA9>/
```

На каждом хосте демона (root):

```sh
sudo /home/agents/deploy/stt-conveyor-<SHA9>/daemon-install.sh --check   # что и где заменит, ничего не меняет
sudo /home/agents/deploy/stt-conveyor-<SHA9>/daemon-install.sh
```

Скрипт находит демон по процессу `multica daemon`, заменяет файл, из которого он запущен (сверив `SHA256SUMS`),
перезапускает его юнит (system или user — по cgroup процесса; `--unit` задаёт явно) и ждёт, пока `/health` демона
(`127.0.0.1:19514`, `--port`) покажет runtime `accept`. Прежний `multica` — в `/var/backups/stt-conveyor-cli.<время>/`
с `rollback.sh`.

Идут раны — скрипт ничего не меняет и выходит с кодом `4`: перезапуск демона обрывает их все (21.10.2026 так
оборвались пять ранов маршрутов разом). Дождаться простоя — `/root/daemon-install-when-idle.sh`; оборвать
осознанно — `FORCE_RESTART=1`. Новый код приёмки и CLI агентов вступает в силу и без перезапуска: достаточно
заменить файл (`install` пакетного `multica` поверх старого) — раны `accept` запускают его заново.

Коды выхода: `0` готово; `2` перезапущенный демон не предложил `accept` — прежний `multica` возвращён и демон
перезапущен; `3` файл заменён, но юнит не найден — перезапустите демон вручную.

Откат: `sudo daemon-rollback.sh` (последний бэкап на хосте) или `rollback.sh` из нужного бэкапа; backend — откат
пакета `stt-models`. Перед откатом backend уберите `accept` из шаблонов сквадов и выключите стадии
(`multica issue stages <эпик> --off`), иначе после отката эпики останутся без назначения.

## Проверка пакета

`bash deploy/stt-conveyor/test/run-tests.sh` — установка на подменённых `/proc`, `systemctl`, `curl`, `pgrep`:
`--check`, установка с откатом, демон без `accept` (возврат), без юнита, подменённый пакет.
