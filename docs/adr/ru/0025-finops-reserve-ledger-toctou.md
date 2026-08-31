> Машинный перевод. Авторитетным источником является английская версия.

# ADR-0025: Ledger reserve→commit/release в FinOps закрывает TOCTOU для budget/spend-limit

- **Status:** accepted
- **Date:** 2026-07-17
- **Deciders:** Fran Olivares
- **References:** ADR-0023 (per-group window and spend ceilings — its FinOps budget dimensions are what this reservation ledger admits against), ADR-0001 (store abstraction — SQLite + Postgres, one descriptor), ADR-0009 (append-only hash-chained audit).

## Контекст и постановка проблемы

`finops.CheckBudget` и `finops.CheckSpendLimit` — read-only pre-flight admission checks: они агрегируют
cost read-model и отвечают на вопрос «укладывается ли этот запрос в обеспечивающие budgets/limits,
которые его scope'ят?». Между этим ответом и моментом, когда фактический spend записывается обратно
(ingest `CostSampled` → `onCost` коннектора), есть окно. **N параллельных запросов читают одно и то же
pre-spend состояние, все проходят и совместно превышают limit** — double-spend типа check→act (TOCTOU).
Более ранний проход fail-closed hardening закрыл degradation `Truncated` и availability posture, но сама
race осталась открытой.

Корректное исправление обязано сделать «проверить ceiling, затем потребить headroom» **атомарным**, причём
атомарным **между репликами на Postgres**, а не только внутри одного процесса — поэтому process-level
mutex неприемлем.

## Движущие факторы решения

- **Ceiling должен потребляться на admission, а не на settlement.** Единственный способ не дать N
  параллельным запросам пройти всем — чтобы каждый admission durable вычитал собственный headroom до того,
  как прочитает следующий.
- **Cross-store, один контракт.** Один и тот же механизм должен работать на SQLite (embedded, single
  writer) и на Postgres HA (несколько соединений, READ COMMITTED). Использовать собственные primitives
  атомарности store, никогда — in-memory lock.
- **Фактический cost известен только апостериори.** Output tokens (а значит, и cost) неизвестны до вызова.
  Admission должен резервировать *estimate* и сверять его по завершении.
- **Честный expiry.** Упавший caller не должен удерживать headroom вечно, а возврат headroom никогда не
  должен приводить к double-count.
- **Без нового schema engine.** Переиспользовать descriptor `ExtensionRegistry` модуля + optimistic
  concurrency общего repo.

## Результат решения

**Динамический reserve ledger** (`finops.budget_reservation`, таблица `finops_budget_reservation`) с
жизненным циклом reserve→commit/release. `ReserveBudget` / `ReserveSpendLimit` атомарно резервируют
estimate против каждой обеспечивающей policy, которая scope'ит запрос; `CommitReservation` закрывает
резерв фактическим cost; `ReleaseReservation` возвращает headroom при сбое. Ceiling везде (`CheckBudget`,
`budgetStatus`, `evaluateBudgets`) теперь равен
`committed_spend + static ReservedMicroUSD + Σ(active, unexpired reservations)`.

Это **отличается от** уже существующего **статического** `budgetSpec.ReservedMicroUSD` (обязательство по
ёмкости Priority-Tier, засчитываемое в limit). Оба слагаемых суммируются в `effective`; этот ADR добавляет
*динамическую, per-request* составляющую.

### 1. Атомарность: монотонный per-scope `seq` под UNIQUE index (без process lock)

Каждая reservation несёт `seq`, монотонный в разрезе **(policy, period_start, scope_key)**, под UNIQUE
index `finops_budget_reservation_seq_uniq (tenant_id, policy_ref, period_start, dim_key, seq)`. Reserve =
прочитать `max(seq)`, прочитать текущий spend + active reservations и, если место есть, сделать `INSERT`
с `seq = max+1`.

- Два параллельных reserver вычисляют **один и тот же** следующий `seq`; UNIQUE index позволяет
  закоммитить ровно **один** `INSERT`, а второй отображает в `store.ErrConflict` (`mapWriteErr`).
  Проигравший **повторяет транзакцию целиком** и перечитывает уже закоммиченное состояние. Это
  сериализует reserve-check-insert **без какого-либо process lock**.
- **SQLite:** `MaxOpenConns=1` уже сериализует каждую транзакцию на единственном writer, поэтому reserve
  атомарен сам по себе; seq index — дублирующая подстраховка.
- **Postgres READ COMMITTED (несущий случай):** отдельные соединения не видят незакоммиченные строки друг
  друга, поэтому именно коллизия seq вынуждает retry. **Инвариант порядка:** reserve читает `max(seq)`
  **до** суммы зарезервированного и вставляет именно с *этим* seq — поэтому успешная вставка (без
  коллизии) доказывает, что прочитанный seq был истинным закоммиченным максимумом, а значит, сумма
  (прочитанная строго позже) увидела все предыдущие reservations. Перестановка этих двух чтений снова
  открыла бы race (устаревшая сумма в паре со свежим неколлидирующим seq привела бы к over-admit).
  Доказано по индукции: k-я успешная вставка видела все k-1 предыдущих reservations, поэтому проходят
  ровно `floor(headroom/estimate)`.

Запросы к нескольким policy резервируют все targets в **одной** транзакции (all-or-nothing): отказ более
позднего target откатывает предыдущие вставки; block имеет приоритет над throttle.

### 2. Гранулярность reservation — на каждую обеспечивающую policy, с ключом по scope

Одна reservation — **одна строка на каждую обеспечивающую policy, под которую попадает запрос**, с ключом
`(policy_ref, period_start, scope_key)`:

- **Budgets:** `scope_key` = dimension key бюджета (`""` для global) — один scope на policy. Резервируется
  по всем 17 не-групповым dimensions, под которые попадает запрос (типичный per-request случай:
  model/provider/agent/workspace/identity/api_key/…).
- **Per-seat spend limits:** `scope_key` = **actor**, поэтому cap, происходящий из org/group policy,
  резервирует headroom каждого seat **независимо** — в соответствии с per-actor семантикой
  `CheckSpendLimit`.
- **Budgets по групповым dimensions (`user_group`/`agent_group`) здесь NOT резервируются.** Их spend — это
  member fan-out по `actor`/`agent_ref`, без group column в read-model; fan-out reservation — более
  крупный дизайн. Они по-прежнему обеспечиваются существующим preventive path `CheckBudget`. (Открытый
  follow-up — см. ниже.)

### 3. Оценка — резервировать estimate, сверять на commit

Admission резервирует `estimateMicroUSD` (априорная оценка seam — например, из `count_tokens` по prompt
плюс output allowance `max_tokens`). По завершении `CommitReservation(handle, actualMicroUSD)` фиксирует
фактическое значение и переводит строку в `committed`, что убирает её из active sum; реальный spend
приходит отдельно через `onCost`. Если estimate оказалась **слишком низкой**, budget может кратковременно
превысить лимит на `actual − estimate` для этого одного запроса — величина ограничена и самокорректируется,
как только фактический spend записан. **Дефолтная политика estimate — продуктовое решение (см. ниже);
механизм не зависит от способа оценки.**

**Порядок:** сначала ingest фактического spend, *затем* commit reservation, чтобы ceiling никогда не
занижался кратковременно во время settlement.

### 4. Expiry — предикат, никогда не декремент

Сумма active-reserved фильтруется по `state = active AND expires_at > now`. Поэтому истёкшая reservation
**перестаёт учитываться в тот же миг, когда истекает** — декрементировать нечего, поэтому
**double-counting структурно невозможен**. `SweepExpiredReservations` лишь проставляет терминальное
состояние `expired` для observability/GC; корректность не зависит от того, запускается ли он. TTL
(`reservationTTL`, по умолчанию **5 мин**) — это crash backstop для caller, умершего между reserve и
commit/release; он должен превышать самое медленное governed actuation, чтобы ещё выполняющийся запрос
никогда не был отброшен.

### Последствия

- **Плюсы:** double-spend закрыт атомарно на обоих engines; исправление аддитивно (новая
  descriptor-таблица — `applyModuleTables` создаёт её и на свежих, и на существующих БД; ни одна
  существующая migration не затронута); `CheckBudget`/status/alerts теперь отражают in-flight
  reservations, поэтому pre-flight denial, сигнал hard-cap и status DTO согласованы.
- **Цена:** reserve — это две записи (reserve + settle) против read-only check; на hot path это несколько
  дополнительных мелких транзакций, ничтожных на фоне inference call, который они защищают.
- **Латентно до подключения:** ledger начинает действовать только тогда, когда actuation seams вызывают
  `ReserveBudget`/`Commit`/`Release` (с estimate) вместо read-only `CheckBudget`. До этого dynamic-reserved
  равен 0, а поведение не меняется. Подключение inference proxy / HITL gate + выбор дефолтной estimate —
  оставшаяся интеграция.

## Открытые вопросы (продукт)

1. **Дефолтная estimate.** Какова априорная оценка, когда у seam её нет? Варианты: `count_tokens(prompt)` +
   сконфигурированный output allowance `max_tokens` по тарифу модели; фиксированный per-request минимум;
   либо историческая стоимость p95 по каждой модели. Занижение ослабляет гарантию; завышение приводит к
   раннему throttling.
2. **TTL.** 5 мин — правильный crash backstop, или он должен следовать максимальному времени завершения
   модели / быть per-surface?
3. **Reservation для group-budget.** Должны ли budgets `user_group`/`agent_group` тоже резервироваться
   (member fan-out), или для групповых ceilings приемлемо enforcement только preventive?
4. **Posture при исчерпании retry.** При исчерпании `maxReserveRetries` (64) reserve fail **open**
   (согласно контракту `CheckBudget`). Должна ли для жёсткого budget `block` экстремальная конкуренция
   вместо этого приводить к fail **closed**?
