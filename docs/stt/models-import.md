# Импорт моделей агентов из пульта (раздел 7 ТЗ моделей)

`multica profile import --file <документ.json>` переносит в базу multica текущее состояние внешнего пульта:
уровни (`tiers`), цепочки агентов (`chains`) и переопределения эпиков (`epics[].overrides`).
По умолчанию это **сухой прогон**: ничего не пишется, выводится план. Применяет `--apply`.
Нужна роль owner или admin в workspace. API: `POST /api/model-profiles/import`
с телом `{"document": {...}, "dry_run": true|false}`.

## Документ

Документ — объединение `controls.json` и конфига сторожа. Ключи верхнего уровня (все необязательны):

```json
{
  "tiers": [
    {"id": "strong", "name": "Сильная", "description": "сложная разработка и ревью",
     "chain": [{"kind": "codex", "model": "gpt-5.5", "thinking_level": "high", "custom_args": null}]}
  ],
  "providers": {
    "tiers": {"mid": {"name": "Средняя", "chain": [{"kind": "glm", "model": "glm-4.6"}]}},
    "agents": {"Ревьюер": [{"kind": "claude", "model": "opus"}]}
  },
  "chains": {
    "<id или имя агента>": {"tier": "strong", "chain": [{"runtime_id": "…"}], "mode": "auto", "selected": 0}
  },
  "epics": [
    {"id": "e-1", "overrides": {"STT-301": {"model": "gpt-5.5", "thinking_level": "high"}}}
  ]
}
```

- `tiers` и `providers.tiers` — список или объект «id → уровень». Уровень `controls.json` с тем же id важнее
  уровня конфига сторожа.
- Строка (`chain[]`) называет runtime одним из полей: `runtime_id`, `runtime` (имя или своё имя runtime)
  или `kind`. `kind` сопоставляется сначала с именем или своим именем runtime, затем с его провайдером;
  ноль или несколько совпадений — в отчёт, строка пропускается.
- `custom_args` строки: флаги запрета инструментов (`--disallowedTools …` у claude,
  `-c mcp_servers.<сервер>.disabled_tools=[…]` у codex) переводятся в запреты агента `сервер:инструмент`
  (объединение по всем строкам; расхождения — в отчёт). Остальные аргументы остаются явными `custom_args`
  строки — тоже с записью в отчёт. У строк профиля `custom_args` не хранятся: их выводит backend под агента.
- `chains` — по агенту (id или имя). Пустая `chain` берётся из `providers.agents` того же агента.
  `mode: "manual"` с `selected` (с нуля, как хранит пульт) — закрепление: свой список из одной строки.
- `epics[].overrides` — объект «задача → {model, thinking_level}» или список `{issue, model, thinking_level}`.
  Задача — `PREFIX-N`, номер или UUID. Открытая задача получает профиль, первая строка которого совпадает
  по модели и рассуждению; нет такого — в отчёт. Закрытые не переносятся.

## Что делает импорт

1. **Профили.** Каждый уровень — профиль с тем же именем. Есть профиль с таким именем — обновляется
   (`action: update`) или не трогается (`unchanged`); нет — создаётся (`create`). `when_exhausted_profile_id`
   не задаётся (поведение не меняется).
2. **Агенты.** Цепочка совпадает со строками уровня (без учёта аргументов) — у агента только
   `model_profile_id`. Не совпадает — профиль плюс свой список (`own_list_why: differs`), запись в отчёт:
   решение по каждому за владельцем. Закрепление — свой список из одной строки (`pinned`).
3. **Сухой прогон.** Для каждого агента — текущая модель (штатные поля агента: их сейчас выставляет сторож)
   и что выбрал бы сервер после импорта (`would_choose`). Расхождение объяснено, если текущая модель — строка
   того же списка, которая сейчас недоступна, или запасная строка при доступной основной. Остальные
   расхождения считает `unexplained`; цель — `0`.

## План (ответ)

```json
{
  "dry_run": true,
  "profiles": [{"key": "strong", "name": "Сильная", "action": "create", "options": [ … ]}],
  "agents": [{"agent_id": "…", "agent_name": "Ревьюер", "profile": "Сильная", "own_list": [],
              "own_list_why": "", "disallowed_tools": ["github:delete_repo"],
              "current": { … }, "would_choose": { … }, "matches": true}],
  "issues": [{"issue": "STT-301", "profile": "Сильная"}],
  "report": ["agent Ревьюер: chain differs from tier \"strong\"; own list kept — decide whether to keep it"],
  "unexplained": 0
}
```

Повторный импорт того же документа — без изменений (`unchanged` у всех профилей).
Тест на фикстуре: `TestModelProfileImportDryRunFixture` в `server/internal/handler/model_profile_test.go`.
