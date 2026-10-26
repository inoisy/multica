# Таблицы моделей, сред и подписок: как устроено сейчас

Срез схемы на выкаченной линии STT-420: код `f4cd8d034`, последняя миграция `612_migrate_runtime_unavailable_codes`.
Колонки взяты из `server/pkg/db/generated/models.go`. Данные STT — снимок CLI от 26.10.2026 ~08:00 UTC.
Целевое устройство и план чистки — [model-selection-and-accounts.md](model-selection-and-accounts.md) и задача STT-433.

Внешних ключей нет (правило репозитория). Все связи ниже держит код приложения.

## Диаграмма

```mermaid
erDiagram
  agent {
    uuid id PK
    uuid runtime_id "копия строки 1 профиля (апстрим)"
    text model "копия строки 1 профиля (апстрим)"
    text thinking_level
    jsonb custom_args
    uuid model_profile_id "общий или личный профиль"
  }
  issue {
    uuid id PK
    uuid model_profile_id "переопределение профиля агента"
  }
  model_profile {
    uuid id PK
    uuid workspace_id
    text name
    uuid when_exhausted_profile_id "запасной профиль"
    uuid owner_agent_id "NULL — общий, иначе личный"
    uuid based_on_profile_id "от какого общего сделан личный"
  }
  model_profile_option {
    uuid profile_id
    int position
    uuid runtime_id "agent_runtime.id"
    text model "пусто — модель задаёт аккаунт"
    text thinking_level
    jsonb custom_args
  }
  agent_model_option {
    uuid agent_id
    int position
    uuid runtime_id
    text model "СТАРОЕ: своя цепочка, удаляется"
  }
  agent_runtime {
    uuid id PK
    uuid workspace_id
    uuid daemon_id
    text provider
    text status "online — демон отвечает"
    uuid profile_id "runtime_profile.id, NULL у встроенных"
    uuid_arr shared_with
  }
  runtime_profile {
    uuid id PK
    uuid workspace_id
    text display_name
    text protocol_family
    text command_name "claude, codex или обёртка"
    jsonb fixed_args
    bool enabled
    uuid pool_id "пул аккаунтов"
    jsonb subscriptions "СТАРОЕ: ключи пула"
    text spend_order "СТАРОЕ: копия пула"
    text share_key "СТАРОЕ"
    bool all_workspaces "СТАРОЕ"
  }
  subscription_pool {
    uuid id PK
    text name
    text runtime_type "claude или codex"
    text spend_order "first или balanced"
    bool all_workspaces
    int revision
    text legacy_share_key "СТАРОЕ"
  }
  subscription_pool_member {
    uuid pool_id
    uuid subscription_id
    int position
  }
  subscription_pool_grant {
    uuid pool_id
    uuid workspace_id
  }
  subscription {
    uuid id PK
    text key
    text provider
    text auth "claude_login, claude_oauth, codex_auth, api_key"
    text credential_path "файл входа на хосте"
    jsonb windows
    jsonb caps "лимит агентам по окнам"
    int reserve "СТАРОЕ: свёрнуто в caps"
    jsonb settings "env для api_key; cli и quota_row — метки"
    text account_group "общая квота"
    timestamptz disabled_until
  }
  subscription_usage {
    uuid subscription_id
    text window_key
    float used_percent
    timestamptz resets_at
    bool limit_reached
    timestamptz observed_at
  }
  subscription_host_state {
    uuid subscription_id
    uuid daemon_id
    text credential_state
    timestamptz checked_at
  }
  subscription_limit_backup {
    uuid id "СТАРОЕ: бэкап переноса 611"
  }
  runtime_unavailable {
    uuid runtime_id
    timestamptz until
    text reason_code
    uuid subscription_id "отказ конкретного аккаунта"
  }
  model_idle_episode {
    uuid agent_id
    timestamptz until
    jsonb runtimes
  }
  agent_task_queue {
    uuid id PK
    uuid agent_id
    uuid issue_id
    uuid runtime_id "фактическая среда"
    jsonb model_choice "что выбрал сервер"
    jsonb account_offer "что предложено демону"
    jsonb account_fact "какой аккаунт подтвердил демон"
  }

  agent }o--o| model_profile : "model_profile_id"
  agent ||--o| model_profile : "owner_agent_id (личный)"
  issue }o--o| model_profile : "model_profile_id"
  model_profile ||--o{ model_profile_option : "строки"
  model_profile |o--o{ model_profile : "based_on / when_exhausted"
  model_profile_option }o--|| agent_runtime : "runtime_id"
  agent ||--o{ agent_model_option : "СТАРОЕ"
  agent }o--|| agent_runtime : "runtime_id (копия)"
  agent_runtime }o--o| runtime_profile : "profile_id"
  runtime_profile }o--o| subscription_pool : "pool_id"
  subscription_pool ||--o{ subscription_pool_member : "порядок"
  subscription_pool ||--o{ subscription_pool_grant : "выдача workspace"
  subscription_pool_member }o--|| subscription : ""
  subscription ||--o{ subscription_usage : "замеры квоты"
  subscription ||--o{ subscription_host_state : "вход на машинах"
  agent_runtime ||--o{ runtime_unavailable : "отказы"
  subscription |o--o{ runtime_unavailable : "отказ аккаунта"
  agent ||--o{ model_idle_episode : "простой"
  agent_task_queue }o--|| agent_runtime : "runtime_id"
  agent_task_queue }o--o| subscription : "account_fact.subscription_id"
```

## Как выбирается запуск

1. Список строк: профиль задачи (`issue.model_profile_id`) → профиль агента (`agent.model_profile_id`, общий или
   личный). Поля агента (`runtime_id`, `model`, `thinking_level`) выбор не читают: это проекция строки 1, её пишет
   сервер в транзакции любой правки профиля. Только служебный `accept` без профиля идёт по своим полям. Агент, у
   которого профиль пуст или удалён, не запускается.
2. Строки по порядку. Строка доступна, если `agent_runtime` online, нет активной `runtime_unavailable`, и в пуле
   среды есть аккаунт ниже `caps` по свежим `subscription_usage`.
3. Сервер пишет выбор в `agent_task_queue.model_choice` и снимок пула в `account_offer`.
4. Демон берёт аккаунт по порядку пула, подставляет вход (`credential_path`, `settings.env`) и подтверждает
   `account_fact`.

## Что лежит в данных STT

| Таблица | Сейчас |
|---|---|
| `agent_runtime` | 13 сред на одной машине (`vps-135.106.225.250`). 8 с `profile_id` и 5 встроенных: Claude, Codex, Hermes, Pi, Accept |
| `runtime_profile` | 8. С `pool_id` — 7, `command_name` у них `claude` или `codex`. Без пула — Qwen Code Лоция (`qwen-loodsen`). «Claude · вторая» удалена 26.10 |
| `subscription_pool` | 7, один к одному со средами. У каждого одна выдача workspace |
| `subscription` | 10: Claude ×3, Codex ×2, ключи API ×5 (MiniMax, z.ai ×2, Лоция, OpenRouter). `zai` и `zai-pro` — один ключ, `account_group = zai` |
| `model_profile` | 6 общих (на «Креатив · ультра» никто не ссылается) и 2 личных (Техдиректор, MiniMax) |
| `agent` | 16 активных. 15 на профиле, «Бенч z.ai» — на полях. Поля `runtime_id`/`model` выровнены по строке 1 профиля 26.10 вручную |
| `subscription_host_state` | заполняет демон вместе с замерами квоты (`subscription_daemon.go`, `UpsertSubscriptionHostState`). API отдаёт `hosts` и `credential_path` только админу инстанса, остальным — пустой список. `login_state = logged_in` у всех аккаунтов подтверждает, что отчёты приходят |
| `agent_task_queue.account_fact` | пишется с 25.10 14:25 UTC; у более ранних запусков `NULL` |

## Где одна настройка хранится дважды

| Настройка | Где | Чем кончается |
|---|---|---|
| Среда и модель агента | `agent.runtime_id`/`model` и строка 1 профиля | С миграции 613 поля — проекция строки 1, которую сервер обновляет вместе с профилем; прямой `PATCH` этих полей при профиле — 409 |
| Состав пула | `subscription_pool_member` и `runtime_profile.subscriptions` | Правит только пул, старая колонка удаляется миграцией 616 |
| Лимит агентам | `subscription.caps` и `reserve` | `reserve` свёрнут в `caps` (611), колонка удаляется миграцией 616 |
| Своя цепочка агента | `agent_model_option` и личный профиль | Перенесено в личные профили (609), таблица удаляется миграцией 616 |
| Вход в аккаунт | `subscription.credential_path` и обёртки в `command_name` | Обёртки со своим входом тратят аккаунт без учёта. Обёртки встроенных сред убраны 26.10 |

Удаление старого — миграция 616: `agent_model_option`, `subscription_limit_backup`, `model_truth_backup`, `subscription.reserve`, `runtime_profile.subscriptions/spend_order/share_key/all_workspaces`, `subscription_pool.legacy_share_key`, индекс `runtime_profile_workspace_share_key_uidx`; у `runtime_unavailable.reason_code` остаётся закрытый набор `provider_limit`, `agent_cap_refusal`, `disabled_refusal`, `unknown`. Применять не раньше 01.11.2026, после прогона на копии прод-БД и только вручную (`multica-autodeploy --allow-destructive <sha>`). Откат данных — только из бэкапа: down возвращает пустую схему.
