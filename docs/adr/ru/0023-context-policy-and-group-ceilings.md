> Машинный перевод. Авторитетным источником является английская версия.

# ADR-0023: Enforcement context-policy в трёх точках транзита с потолками window и spend для каждой группы

- **Status:** accepted
- **Date:** 2026-07-08
- **Deciders:** Fran Olivares
- **References:** ADR-0022 (source scoping by subject axis — its subject resolver and `most-specific` precedence are mirrored here), ADR-0009 (append-only hash-chained audit), ADR-0003 (RRW map — permitted vs observed).

## Контекст и постановка проблемы

Context-policy (размер window и стратегия compaction) сохранялась как governed data, но **ни один
потребитель её не применял**: обещанного комментарием потребителя не существовало, политика была мёртвой
metadata. Отдельно token ceilings inference proxy были только **per-tenant / per-request**, а FinOps
содержит budget dimension `team`, которая работает **detective и fail-open**. Нельзя было сказать «эта
группа users (или agents) может потребить не более такого window / spend» и обеспечить это.

Видение продукта требует двух вещей, недостижимых сохранённой, но неиспользуемой политикой:

1. **Context-policy ПРИНИМАЕТ РЕШЕНИЕ во всех трёх transit points**, где платформа касается model request:
   session runtime, inline inference proxy и knowledge retrieval — вместо инертных данных.
2. **Обеспеченные потолки на группу** — `user_group` и `agent_group` — для **context window** и **spend**,
   deny-closed там, где требует policy, с **честной degradation** без silent clamp или silent allow.

## Движущие факторы решения

- **Согласованность с source scoping (ADR-0022).** Тот же словарь subject и тот же precedence
  `most-specific`, чтобы операторы одинаково рассуждали о context governance и source scoping; без
  второго decision engine, с малой attack surface.
- **Ceiling должен быть ceiling.** Числовой предел, который более specific scope может *ослабить*, не
  является потолком; цель — именно «enforced ceilings».
- **Честная degradation.** Если платформа не может полностью учесть величину (approximate group spend),
  она должна fail в безопасную сторону и сообщить об этом: не запретить ошибочно и не разрешить молча.
- **Переиспользование primitives.** Audit ledger, существующая per-subject cost attribution и текущий
  proxy deny path предпочтительнее новой cross-cutting machinery.

## Результат решения

### 1. Композиция `Apply`: qualitative most-specific, security floors restrictive, `max_tokens` через MIN

`Module.Apply` (`modules/knowledge/context.go:263`) вычисляет effective policy запроса:

- **Qualitative** fields (`strategy`) — **most-specific-wins**, как в ADR-0022.
- **Security floors** компонуются **restrictively**: `forbid` абсолютен; `redaction_required` через OR;
  `excluded_sources` через union.
- **`max_tokens` компонуется через MIN** (most-restrictive; поле в `context.go:62,73`, ограничение в
  `context.go:124`). Это сознательное уточнение числового limit: ceiling, повышаемый более specific scope,
  не является ceiling. Поведение обратимо примерно двумя строками, если deployment предпочитает
  most-specific для limit.

### 2. Agent identity в proxy: закрыть достижимый остаток (E3-lite), остальное честно отложить

Session-inference WIF credential (`sk-ant-oat`) **не** проходит через inline inference proxy, который
аутентифицирует лишь собственные токены платформы `olvs` / `olvk`. Полное закрытие agent-identity
federation для *session* traffic требует переработки inference credential (многодневная работа, часть
ephemeral-WIF mint posture) и **отложено в отдельную работу (E3-full)**.

Достижимая часть закрывается сейчас (**E3-lite**): `authToken` передаёт `AgentRef` → `AgentIdentity`, а
resolver actor scope моделей учитывает **authenticated principal**, не caller-declared value (исправление
ошибки), что включает axis `agent_group` в proxy для agent-on-behalf-of callers. Agent ref всегда берётся
из authenticated credential, никогда из тела request (`context.go:278-279`, `query.go:110-111`).

### 3. SPEND ceiling на группу: preventive, по природе fail-open, с granular fail-closed knob

Budget получает dimensions `user_group` / `agent_group`, preventively обеспеченные через `CheckBudget`;
spend группы суммируется посредством **member fan-out** по существующей per-subject cost attribution
(group column нет; безразборное суммирование строк было бы mis-attribution bug —
`modules/finops/ingest.go:75,361`).

Posture **fail-open** — природа budget check и соответствует разделению продукта *security = deny-closed*
и *budget = fail-open* (`modules/models/api.go:639,656`) — с per-budget knob **`fail_closed`** для hard stop
(`modules/finops/budgets.go:102,166,182`). Это заявляется **честно**: preventive group spend —
*приближение*, не точный accounting. Coverage растёт с attribution; ещё не атрибутированный spend лишь
under-counts группу — безопасное направление, никогда не дающее ложный deny. Detective ingest/finding
FinOps backstop для групп и local degradation counters — **документированный follow-up**, намеренно не
half-wired.

### 4. Отказ proxy при превышении window: 413, без mutation клиентского payload

При превышении effective policy/group window proxy **отказывает с HTTP 413** и detail
(`cmd/olivares/inferenceproxy.go:449`); он **никогда не изменяет opaque payload клиента**, а отказывает
вместо silent clamping (`inferenceproxy.go:550`). Compaction и signalled truncation применяются только
там, где контекст собирает сама платформа (retrieval и session runtime), никогда поверх prompt caller.
Silent degradation отсутствует.

Подключены три enforcement points: retrieval (`modules/knowledge/query.go:167` → `:354`), session runtime
(`modules/sessions/runtime.go:285,623`) и inference proxy выше.

## Решения и фиксация состояния (в утверждённом направлении)

- **Девять scope-kinds context-policy** — `session > agent > user > user_group > role > agent_group > kb > workspace > tenant`
  — валидируются write handler (`modules/knowledge/context.go:102-103`), с nullable expand-only `effect`
  (устоявшийся reconcile module-column, без numbered migration).
- **`surface` и `model` не являются scope-kinds.** У retrieval нет surface, а proxy уже сворачивает
  per-surface window через MIN; добавление было бы неиспользуемой общностью (YAGNI).
- **«OTel metric» для функции = auditable events + native findings**, не in-module meter. Product telemetry
  идёт по bus как findings в observability; новый meter — cross-cutting architecture change вне scope.

## Рассмотренные альтернативы

- **Most-specific composition для `max_tokens`**: отклонено — числовой ceiling, который more-specific scope
  может поднять, не является ceiling. Решение остаётся легко обратимым.
- **Отдельный in-module meter context/group telemetry:** отклонён как cross-cutting architecture change;
  audit-events + bus-findings path уже переносит сигнал.
- **Сумма всех per-subject spend rows группы без member fan-out:** отклонено — over-count и
  mis-attribution; fan-out по authenticated membership даёт корректную безопасную attribution.

## Последствия

- Context-policy превращается из мёртвой metadata в **живое решение** на retrieval, proxy и session runtime.
- Group **window** ceilings — **жёсткие и MIN-composed**; group **spend** ceilings — **preventive и честно
  approximate**, с opt-in `fail_closed`.
- **Зарегистрированный долг, ничего half-wired:** E3-full (перенаправление session inference через governed
  identity), detective group-spend backstop FinOps + local degradation counters и передача principal
  (`user` / `user_group`) в launch gate. Всё документировано как follow-up.
