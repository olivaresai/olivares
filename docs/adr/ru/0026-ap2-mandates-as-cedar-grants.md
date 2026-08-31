> Машинный перевод. Авторитетным источником является английская версия.

# ADR-0026: Payment mandates AP2 как Cedar scoped grants (governed procurement)

- **Status:** proposed (design only; the enterprise build lands in a separate phase)
- **Date:** 2026-07-20
- **Deciders:** Fran Olivares
- **References:** ADR-0019 (Cedar scoped grants), ADR-0022 (source-scoping subject axes),
  ADR-0025 (FinOps reserve→commit/release ledger, TOCTOU-safe), ADR-0009 (append-only
  hash-chained audit); the companion AP2 governed-payment threat-model spec; the AP2 v0.2.0
  specification (github.com/google-agentic-commerce/AP2, verified 2026-07-20).

## Контекст и постановка проблемы

Agentic payments приходят как протокольный слой. **AP2 (Agent Payments Protocol)** от Google — один
из самых заметных; его текущая спецификация — **v0.2.0 (выпущена 2026-04-28)**, и в тот же день он
был передан FIDO Alliance. AP2 позволяет пользователю делегировать подписанный **mandate** shopping
agent, который затем привязывает его к конкретной транзакции, проверяемой **Verifiers** (merchant,
credential provider, network, payment processor).

Форму этого решения задают два факта:

1. **Актуальность (измеренная реальность важнее плана).** Раннее планирование опиралось на AP2 v0.1 и
   описывало тройку mandate *Intent / Cart / Payment*, подписанную «verifiable credentials». Эта
   модель **вытеснена**. v0.2 определяет ровно **два** типа mandate — **Checkout Mandate** и
   **Payment Mandate** — каждый в состоянии **Open** (несущем ограничения, подписанном пользователем)
   и в состоянии **Closed** (привязанном к транзакции; agent порождает Key Binding JWT /
   Proof-of-Possession над ключом из claim `cnf` открытого mandate). Mandates — это **SD-JWTs**
   (RFC 9901); **binding hash / Key Binding JWT MUST использовать недетерминированную схему
   (ES256/ECDSA) и NOT детерминированную (Ed25519)** — спецификация утверждает, что это защищает
   hash binding. Этот ADR ориентируется на **v0.2**, закреплённый по опубликованным суффиксам схемы
   `vct` (по спецификации v0.2 — `mandate.checkout.1` / `mandate.payment.1`; проверять по `docs/ap2/*`
   спецификации на этапе сборки).

2. **Что такое Olivares — и чем он не является.** Olivares — это **governance control plane**: Policy
   Decision Point (PDP) и журнал доказательств с обнаружением подделки. Он **не** является payment processor, PSP,
   карточной сетью, wallet или хранителем средств, и этот ADR его таковым не делает. Сам AP2
   находится в **pre-1.0**, с **ранним, во многом декларативным внедрением** (собственные страницы
   PayPal упоминают AP2 лишь таксономически и подчёркивают ACP от OpenAI + UCP от Google; «Agent Pay»
   от Mastercard — отдельная программа; цифра «60+ организаций» — это счёт на запуске в сентябре
   2025; список подписантов FIDO — около 12). Честная маркировка запрещает заявлять поддержку AP2
   сверх того, что проверяемо.

Проблема: **как Olivares управляет agentic-покупкой, опосредованной AP2, с помощью примитивов,
которые у него уже есть, рождаясь вместе с конкретным enterprise use case и покрывая пробелы, которые
AP2 намеренно оставляет вышестоящему слою, — не внося при этом authorization fall-through или
молчаливое ослабление ограничений?**

Конкретный use case, с которым рождается этот дизайн: **governed procurement agent** — предприятие
покупает через агента, работающего под открытым AP2 mandate, чьи ограничения кодируют закупочную
policy (budget ceiling, разрешённые поставщики, лимиты на позицию, повторяемость, окно исполнения);
Olivares авторизует каждую конкретную покупку по этой policy, эскалирует дорогостоящие человеку и
запечатывает mandate+receipt как non-repudiable evidence.

**Предусловие (in-path gate).** Каждая гарантия ниже действует только там, где deployment
маршрутизирует покупку **через Olivares как in-path gate**: agent MUST получить свежую авторизацию
Olivares, прежде чем предъявить closed mandate уровню settlement. В роли side/advisory PDP Olivares
может дотянуться до closed mandate, уже переданного merchant, не больше, чем AP2. Реализация MUST
документировать это требование к развёртыванию.

## Движущие факторы решения

- **Переиспользовать существующую authorization plane, не форкать её** — но только там, где семантика
  действительно совпадает (см. поправку Abstain-vs-deny ниже).
- **Закрыть заявленные пробелы AP2 на нашем слое** (см. сопутствующую spec threat-model): у AP2
  **нет revocation**, отклонение double-spend на стороне verifier он делает **необязательным (MAY)**,
  он **не** доказывает личность человека / SCA, **молчит о доверии к часам** и оставляет
  хранение/извлечение evidence и ответственность вне scope. PDP, который «предполагает, что все
  agents — потенциальные атакующие» (собственная threat model AP2), должен сделать это обязательным.
- **Fail closed на всём, что не моделируется.** Ограничение, которое мы не можем закодировать,
  disclosure, которую agent утаивает, неизвестный алгоритм — каждое из этого должно отклонять
  mandate, никогда не расширять его.
- **Честный scope и риск pre-1.0.** Проектировать сейчас, закрепляться по `vct`, не выпускать
  утверждения, которые мы не можем проверить, держать Olivares строго на стороне PDP/evidence.

## Рассмотренные варианты

- **Вариант A — mandates AP2 как Cedar scoped grants; Olivares как управляющий Verifier/PDP.**
  Моделировать **open mandate** AP2 как авторский **Cedar grant** (ADR-0019), привязанный к этому
  одному mandate, чьи условия `when` — это ограничения mandate; трактовать **closed mandate** как
  **authorization request** (principal = ключ агента в `cnf`; action = `purchase`/`pay`; resource =
  получатель платежа / checkout), вычисляемый **deny-by-default для платёжных actions**. Olivares
  исполняет правила верификации AP2 в роли PDP, пропускает дорогостоящие через одноразовое HITL
  approval, резервирует FinOps budgets (ADR-0025) fail-closed и запечатывает полный подписанный
  mandate+receipt как evidence.
- **Вариант B — самописный engine mandate AP2 параллельно Cedar.**
- **Вариант C — только наблюдать.**

## Результат решения

Выбранный вариант: **Вариант A** — потому что модель ограничений отображается на условия Cedar grant,
а окружающие механизмы (approvals, reserve ledger, подписанная audit chain) уже существуют, —
**при условии, что сделаны три семантические поправки ниже**, без которых переиспользование
небезопасно.

### Три семантические поправки, которые делают переиспользование корректным

1. **Платёжные actions — DENY-BY-DEFAULT, а не abstain-defers-to-RBAC.** Engine scoped-grant
   возвращает **`EffectAbstain`** (не deny), когда ни один permit не совпал: «нет grant», «grant
   истёк» и «нет scoped grants для tenant» — всё это Abstain, а Abstain означает, что *базовое
   решение RBAC остаётся в силе* (`modules/governance/grants.go:31-38`, инвариант обратной
   совместимости с RBAC). Наивно приравнивать «нет подходящего mandate» к «deny» — **неверно**:
   несовпадение cnf, истёкший mandate или отозванный grant дадут Abstain и могут провалиться в
   **RBAC allow**. Поправка: `purchase`/`pay` авторизуются **только** совпавшим, валидным,
   привязанным к mandate grant, **без RBAC fallback**. Реализация MUST обеспечить это либо
   (i) доказав, что базовый authorizer не выдаёт permit `purchase`/`pay` ни одной role (тогда
   Abstain→deny), либо (ii) платёжным overlay, который трактует Abstain на платёжном action как deny.
   Присутствующий, но невалидный mandate дополнительно порождает явный **`forbid`**. Conformance test
   MUST утверждать, что один только RBAC никогда не авторизует платёж.

2. **Транслятор mandate→grant FAILS CLOSED на любом немоделируемом ограничении.** «Unknown constraint
   MUST fail» — обязательство **времени трансляции**, а не то, что даёт deny-by-default в Cedar: если
   транслятор молча опускает ограничение, которое не может закодировать, он порождает grant **шире,
   чем подписал пользователь**, и Cedar разрешает, потому что никогда не видел этого ограничения.
   Поправка: транслировать по **allowlist** распознаваемых ключей ограничений, операторов и единиц;
   при любом нераспознанном элементе — **отклонить mandate целиком и не порождать никакого grant**.

3. **Полное раскрытие обязательно; недоверенный agent не может утаить ограничение.** В SD-JWT именно
   *holder* (недоверенный agent) выбирает, какие disclosures раскрыть. Он мог бы предъявить только те
   disclosures, которые проходят, и утаить более жёсткое ограничение. Поправка: адаптер верификации
   перечисляет digests `_sd` и, если хоть один digest для policy-значимого claim **не раскрыт**,
   считает его невычислимым ограничением и **fails closed**.

### Соответствие (с применёнными поправками)

| Понятие AP2 v0.2 | Примитив Olivares (file:line) |
|---|---|
| Open mandate (ограничения, подписан пользователем) | Cedar scoped **grant**, привязанный к `jti`/`sd_hash` этого mandate (`modules/governance/grants.go:67`, ADR-0019) |
| Closed mandate | Authorization **request**, вычисляемый **deny-by-default для `purchase`/`pay`** (поправка 1) |
| «Verification and Processing Rules» | Chain-verify адаптера + проверка полного раскрытия (поправка 3) + fail-closed трансляция (поправка 2) + решение PDP |
| `payment.budget` (кумулятивный) / `amount_range` (на транзакцию) | FinOps reserve ledger (`modules/finops/budgets.go`, `spendlimits.go`, ADR-0025) с **совершенно новым per-mandate ключом reservation**; резервировать против cap самого mandate И всех scopes Olivares атомарно (NOT `min()`) |
| `payment.agent_recurrence` (count/velocity) | **Совершенно новый** limiter count/velocity (TOCTOU-safe в рамках ADR-0025) — NOT существующий budget по суммам |
| `allowed_payees` / `allowed_merchants` / `allowed_payment_instruments` | Условия `when` Cedar по членству в множестве |
| `execution_date` {not_before,not_after} | Временное условие против **доверенных подписанных dead-man часов DDIL** (`modules/governance/ddiladopt.go`), внедряемых также в адаптер SD-JWT |
| Одобрение пользователя; gating дорогостоящих | Списание **одноразового HITL** approval (`modules/governance/approvals.go`) |
| Checkout/Payment Mandate + Receipt (evidence для споров) | Hash-chained **runtime audit ledger** с ключом `transaction_id` (`modules/sessions/runtime_ledger.go`, `sc.Audit().Append`, ADR-0009) — см. решение 1 о том, ЧТО хранится |

### Решения, которые принимает этот ADR

1. **Представление mandate — authority и evidence суть разные хранилища.**
   - **Authority** — это **Cedar grant** (вычисляемая policy), привязанный к стабильному id
     конкретного open mandate (`jti`/`sd_hash`), так что closed mandate может вычисляться только
     против grant, созданного из *его* open mandate (предотвращает **подмену mandate**: agent,
     держащий мягкий mandate A, не может добиться, чтобы closed mandate от B вычислялся против
     grant-A). Grant **никогда** не является сырым blob, трактуемым как самозаявленный authority.
   - **Evidence** — это **полный подписанный артефакт**: открытый SD-JWT, закрытый Key Binding
     JWT и **фактически предъявленные disclosures** — сохраняемые (зашифрованно, с контролем
     доступа), чтобы спор мог *воспроизвести последовательность проверки подписей AP2*, чего hash не
     позволяет. Эти evidence несут PII (суммы, получатели платежей), поэтому это **зашифрованные
     минимально необходимые evidence, а не «никогда PII»** — правило минимизации данных относится к
     *authority/grant* и к операционным логам, но не к запечатанной записи спора.

2. **Проверка подписей — цепочкой, с закреплёнными алгоритмами и разделёнными trust roots.**
   Проверить цепочку SD-JWT и связь open→closed через привязанный к `cnf` Key Binding JWT (PoP),
   убедиться, что closed mandate сохраняет claims открытого mandate неизменными, и вычислить каждое
   ограничение (поправки 2 и 3). Два правила усиления, которых сама спецификация не даёт:
   - **Закрепление алгоритмов.** Привязать каждый ключ trust root к разрешённому для него набору
     алгоритмов и проверять строго по нему; **игнорировать `alg`, объявленный в токене**. Отклонять
     `alg:none`, путаницу HS/ES и понижение кривой/стойкости — запрет Ed25519 в AP2 это одно узкое
     правило внутри управляемой заголовками поверхности согласования, которой распоряжается
     недоверенный agent.
   - **Разделённые trust roots.** Root **User-Credential** (OpenID4VP) проверяет, что *человек
     авторизовал* open mandate; список **Trusted-Agent-Provider** управляет только тем, какая agent
     identity может **держать/привязывать** ключ `cnf`. Они удостоверяют разные факты и **требуются
     оба, каждый по своему обязательству** — никогда не взаимозаменяемое OR (аттестация
     agent-provider не заменяет подпись авторизации пользователя). Deny-closed, если требуемый root
     отсутствует.

3. **Истечение, одноразовость и отзыв (в пределах потоков, проходящих через Olivares).** У AP2
   **нет revocation**. Olivares закрывает это для **in-path** развёртываний: (a) привязанный к
   mandate grant **отзываем как first-class** — его отзыв делает каждую *будущую авторизацию
   Olivares* для этого mandate deny-by-default (поправка 1); он не может дотянуться до closed
   mandate, уже отпущенного в settlement (то же ограничение, что и у AP2, — заявлено честно).
   (b) Дорогостоящий closed mandate списывает **одноразовое approval**, поэтому approval нельзя
   переиграть повторно. (c) `exp`/`execution_date`/повторяемость обеспечиваются по **доверенным
   подписанным часам DDIL**, и адаптер SD-JWT берёт свой `now` с тех же часов, поэтому два слоя не
   могут разойтись.

4. **Replay / double-spend — де-дубликация на стороне verifier ОБЯЗАТЕЛЬНА (in-path).** AP2
   возлагает MUST против double-spend на *shopping agent* (атакующего в его же threat model), а
   проверку на стороне verifier делает лишь MAY. PDP Olivares отслеживает предъявленные nonces /
   `transaction_id` закрытых mandate по каждому open mandate и отказывает в перекрывающихся или
   повторных предъявлениях — для авторизаций, которые маршрутизируются через Olivares (предусловие
   in-path).

5. **Что Olivares NOT делает.** Никакого хранения средств, никакого исполнения платежей, никакой
   эмиссии карт/токенов, никакой работы в роли PSP/network/wallet. Olivares — это **PDP**,
   авторизующий agentic-покупку по policy, и **evidence plane**, запечатывающий mandate/receipt.
   Settlement остаётся за merchant/PSP/network.

### Последствия

- **Плюсы:** переиспользует Cedar/reserve-ledger/approvals/audit-chain там, где семантика
  действительно совпадает; пробелы AP2 превращаются в обеспеченные гарантии; запечатанные
  non-repudiable evidence; честное, проверяемое позиционирование.
- **Минусы / компромиссы:** переиспользование **условно** — ему нужны overlay deny-by-default для
  платёжных actions, fail-closed транслятор, обеспечение полного раскрытия, per-mandate ключ
  reservation и совершенно новый limiter повторяемости (ничто из этого не бесплатно); AP2 находится
  в pre-1.0 (v0.3 заставит пересобрать отображение, изолированное за адаптером и закреплённое по
  `vct`); хранение подписанных evidence с PII добавляет обязательство по шифрованию и срокам
  хранения.
- **Нейтрально / follow-ups:** делегирование mandate от agent к agent **вне scope AP2** → вне
  нашего; x402 (crypto-rail расширение AP2) и ACP (OpenAI/Stripe) — отдельные вещи, отслеживаемые,
  но не строящиеся здесь.

## Почему альтернативы были отклонены

- **Вариант B (самописный engine)** — отклонён: он дублирует machinery reserve-ledger/approval/audit
  ради pre-1.0 протокола; поправки выше показывают, что переиспользование корректно, как только на
  месте окажутся deny-by-default для платёжных actions и fail-closed трансляция.
- **Вариант C (только наблюдать)** — отклонён: ратифицированное направление — проектировать сейчас и
  начать enterprise build рано, *не блокируя публичный релиз*. Наблюдение лишь лишило бы нас отличия
  (governed agentic spend с запечатанными evidence), пока стандарт консолидируется в FIDO. Опасение о
  честной маркировке снимается тем, что **дизайн** выпускается сейчас, а **build** ставится за
  проверенную потребность, а не бездействием.
