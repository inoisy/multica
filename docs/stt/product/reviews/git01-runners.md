# Раннеры git01 для EPUS

Срез 26.10.2026. По поручению Евгения выясняем возможности раннеров Лоции
для тестов frontend, Bitrix backend и Laravel configurator. Исследование
передано Девопсу в [STT-456](https://multica.yakutov.com/stt/issues/STT-456).
Это инфраструктура EPUS, а не новая функция Multica; путь относительно апстрима
— только у нас. Изменения CI и запуск pipeline в исследование не входят.

## Что подтверждено исходниками

Репозитории прочитаны через `multica repo checkout`. Это локальные снимки:
живой SSH-мост git01 при проверке ответил `Connection refused`. Теги и команды
в YAML не подтверждают, что раннер сейчас доступен или последняя проверка зелёная.

| Репозиторий и SHA | Факт в исходниках |
| --- | --- |
| `gaz-group/epus/frontend`, `origin/stage@41653954df265b64b66f6cc3c046a39693ba76ea` | `.gitlab-ci.yml`: этапы build/deploy; сборка с тегом `docker1`, часть k8s job с `$RUNNER_TAG`, остановка demo с `k8s-99`. Отдельного этапа lint/typecheck/Vitest/Playwright нет. `test_cdn` относится к deploy, это не тест кода |
| `gaz-group/epus/backend`, `ebf2bab2bc81b24828c846a40a7e06b43a65e41a` | `.gitlab-ci.yml`: job `tests`, этап test, образ `composer:latest`, тег `docker1`; установка зависимостей и `vendor/bin/phpunit --colors=never`. Правила включают MR и develop/stage/master |
| `gaz-group/epus/configurator`, dev-срез `76388031735e150ae7199c15b3e6025a4185cd14` | `.gitlab-ci.yml`: build/deploy с тегом `docker1`; отдельного этапа тестов нет |

В `frontend/package.json` есть команды `lint:js`, `lint:style`, `typecheck`
и `test` (Vitest). Комментарий в CI описывает недоступность pypi.org из раннера;
это старое наблюдение в исходнике, а не текущая проверка сетевых ограничений.
Конфиги и секретные переменные в отчёт не копируются.

## Что нужно выяснить

- Владелец раннеров, scope, tags, executor, OS/архитектура, protected/shared
  и доступность для MR в каждом из трёх репозиториев.
- CPU/RAM/диск, timeout, concurrency и очередь; разделение тестов и деплоя.
- Допустимые образы и `services`, доступные registries/зеркала; возможности
  MySQL, Kafka, Elasticsearch и Chromium/Playwright. Наличие Docker socket,
  DinD и privileged выясняется без включения этих прав.
- Последний результат существующего PHPUnit job backend; реальные настройки
  проверки pipeline перед merge и права на них. Автослияние EPUS не включаем.

Результат STT-456 — таблица «проверка → команда → runner/образ/services →
ограничение → владелец», со статусом подтверждено/неизвестно/требует доступа
для каждого поля. При отсутствии доступа нужен готовый вопрос ответственному
Девопсу Лоции; внешний запрос отправляется после согласования с Евгением.
Повторять недоступное SSH-соединение вместо исследования не нужно.

## Связи и границы

[STT-112](https://multica.yakutov.com/stt/issues/STT-112) и
[STT-125](https://multica.yakutov.com/stt/issues/STT-125) требуют проверки
импортера конфигуратора: unit/integration/contract с настоящей MySQL/VIEW,
отдельно E2E с Kafka/MySQL/ES. У них есть и другие блокеры; раннеры нельзя
объявлять единственной причиной остановки. Существующий PHPUnit job backend
следует проверить, а не создавать повторно.

[STT-438](https://multica.yakutov.com/stt/issues/STT-438) отвечает за доступ
через мост, [STT-409](https://multica.yakutov.com/stt/issues/STT-409) — за общий
аудит изоляции. STT-456 не подменяет эти задачи и не меняет права или инфраструктуру.

Решение по РП сохраняется: приватный `inoisy/resource-planning`, GitHub Free,
свой runner и слияние из Actions после CI и review одного SHA —
[STT-453](https://multica.yakutov.com/stt/issues/STT-453). Исследование GitLab
Лоции не переносит EPUS на GitHub и не отменяет согласованный переход РП.
