> Машинный перевод. Авторитетным источником является английская версия.

# ADR-0022: Scoping источников по оси subject (session / agent / user / user-group / role), с row-level effect и versioned, dual-controlled posture enforcement

- **Status:** accepted
- **Date:** 2026-07-07
- **Deciders:** Fran Olivares
- **References:** ADR-0003 (RRW map — permitted vs observed), ADR-0019 (Cedar scoped grants).

## Контекст и постановка проблемы

Привязка источника (`modules/sourcescope`) связывает подключённый source — MCP server, model,
provider, knowledge base, data source — ровно с одним из трёх деревьев области **вложенности**:
`workspace`, `agent_group` или `folder` (`schema.go:52-62`, `binding.go:33`). Она отвечает:
«актор **внутри** этой области может обращаться к этому источнику».

Видение продукта требует ещё четыре оси, которые модель вложенности не может удобно выразить:

- **«эта SESSION видит source X»** — один работающий session.
- **«этот USER / group-of-users обращается к sources Y»** — конкретный человек и directory group.
- **«этот конкретный AGENT (не его группа) видит только Z»** — один агент, а не его agent-group.

Сегодня эти оси лишь *приближённо* задаются raw Cedar grant: нет удобства binding, перечисляемой и
аудируемой строки, проекции access-map, а для обратного вопроса «какие sources доступны subject S?»
остаётся нерешённая reverse-query (`accessmap.go:44`). Между тем governance **моделей** уже имеет
богатую SUBJECT-модель: `subject_kind ∈ {user, role, agent_group}`, строки allow/forbid и алгебру
`forbid-overrides-allow` (`modelgovernance.go:98-100`, `modelaccessgate.go:204`). Возникает асимметрия:
**модели богато управляются по subject, источники узко — по containment.** Это решение её устраняет.

Требование второго порядка следует из анализа incumbents, проверенного по vendor docs 2026-07-07:
AWS Q Business превращает *ослабление* ACL в отдельную одностороннюю и аудируемую IAM operation
(`qbusiness:DisableAclOnDataSource`); posture ACL data-store у Google **неизменяема после создания**.
Наше отличие — posture, которая **изменяема, versioned и audited**, но её ослабление должно быть
**привилегированной, dual-control, audited** operation, а не silent toggle. Ни один incumbent не выражает
per-agent или per-session scoping источника — это проверенное white space, не гипотеза.

## Движущие факторы решения

- **Согласованность с model-access.** Одинаковый словарь subject и алгебра
  `forbid-overrides-allow`, чтобы оператор одинаково рассуждал о доступе к source и model.
- **Стоимость hot path.** Resolver работает на EXECUTE path моделей (`ScopeGate`) и knowledge retrieval
  path (`RetrievalScopeGate`). Identity axis не должна добавлять policy round-trip на каждый resolve.
- **Аудируемость и reverse-query.** «Перечислить все sources для session S / user U / group G» должно
  быть одним indexed query, не обратным обходом Cedar (он не решён).
- **UI.** Одна форма binding, которую console (последующая работа) может показывать и создавать.
- **Обратная совместимость и security.** Без новых bindings решение остаётся прежним; identity axis по
  возможности связывается с **authenticated principal**, а не строкой caller. Control plane не должен
  получить второй authorization engine: attack surface остаётся малой.

## Результат решения

**Расширить существующую привязку source на месте (кандидат A1): добавить subject scope-trees и
row-level `effect`, дав `sourcescope` алгебру allow/forbid по subject поверх собственной таблицы,
зеркальную model-access, но оставить containment model и cross-scope Cedar override неизменными.**
Не создавать raw Cedar для новых осей (B) и не поднимать параллельную model_access-twin decision plane
(C). Одна control plane, одна query surface, одно место корректной авторизации.

### 1. Пять новых subject scope-tree, единообразных с существующими containment tree

`scope_tree` расширяется с `{workspace, agent_group, folder}`:

| tree | `scope_ref` | совпадает, когда… | identity source | подделываемо? |
|---|---|---|---|---|
| `session` | session `external_id` | acting session == ref | session-aware caller ref, agent-identity-hardened | route-gated (см. §4) |
| `agent` | agent `external_id` | acting agent == ref | `principal.AgentIdentity` ∨ session's agent ∨ agent ref | route-gated / authenticated |
| `user` | user id | `principal.UserID` == ref | **authenticated principal** | нет |
| `user_group` | `UserGroup.ID` | ref ∈ `principal.GroupsIn(tenant)` | **authenticated principal** (directory-group-gated nested closure) | нет |
| `role` | tenant role name | `principal.RoleIn(tenant)` == ref | **authenticated principal** | нет |

`user_group` — это **directory group**, сопоставляемая по **group id** с
`principal.GroupsIn(tenant)`, уже передаваемым в authenticated principal и включающим полное замыкание
nested ancestors (`principal.go:67-77,151-164`); чтения группы на каждый resolve нет. У `UserGroup` нет
slug (`model/auth.go:122`), поэтому id — стабильный идентификатор. `role` добавляется для **полного
паритета model-access** (Fran Olivares, 2026-07-07): governance source по tenant role — грубый рычаг
«группы пользователей», также доступный model-access.

Три оси identity-of-one (`session`, `agent`, `user`) — вырожденные containments (равенство), а
`user_group` и `role` — настоящее членство. Все вычисляются как единый **scope predicate** над actor,
без нового decision engine.

**Validation следует дихотомии containment-vs-subject (проверенное ограничение).** Write handlers
модуля имеют business-tenant `store.Scope`; auth subjects (`model.User`, `model.UserGroup`, roles`)
живут в `store.AuthScope` (system tenant) и **недостижимы** из него (`core/store/store.go` vs
`auth.go:24-36`). Поэтому:

- **Containment trees** `workspace` / `agent_group` / `folder` **и** store-resident subject trees
  проверяются на существование при bind, как сейчас (deny-closed, без dangling scope). Но для единого
  правила и привязки source до ephemeral session это решение считает **все пять subject trees
  shape-only** при authoring: непустой `scope_ref` правильного kind, без store lookup.
- Корректность не зависит от проверки существования: неизвестный subject ref не совпадает с
  authenticated actor ⇒ deny-closed — **ровно паттерн model-access** (`modelaccessgate.go` проверяет
  только *shape* subject; `validateGrantRefs` — лишь store-resident TARGET). Предотвращение опечаток —
  задача console (directory/agent picker), не binding layer. Containment trees сохраняют текущую
  existence validation.

### 2. Row-level `effect` (allow | forbid) с **абсолютным** forbid-overrides-allow

Каждая binding имеет `effect ∈ {allow (default; empty stored value = allow), forbid}` (та же
конвенция, что `normalizeEffect` в model-access). Алгебра resolver для `(actor, source)`:

```
1. If ANY enabled binding matching the actor has effect=forbid  → DENY   (absolute)
   — OR the cross-scope Cedar engine returns EffectForbid for a resource-anchored (workspace/folder) binding.
2. Else, if the source is UNCONFINED (no enabled ALLOW binding at all) → ALLOW   (global / back-compat),
   subject to the per-workspace connector-assignment gate for unbound connectors.
3. Else (confined), ALLOW iff the actor matches an ALLOW binding (its tree's containment),
   OR a cross-scope Cedar EffectGrant, OR tenant RBAC soft-isolation;
   the credential is taken from the MOST-SPECIFIC matching ALLOW (§3). Otherwise DENY-CLOSED.
```

**Изменение поведения, документированное, как в ADR-0019.** Сейчас forbid привязки source действует
*на одну binding*: cross-scope `EffectForbid` одной строки получает `continue`, и *другая* binding может
разрешить (`resolver.go:243-248`). Решение делает **все** forbids **абсолютными** — и row-level
`effect=forbid`, и cross-scope `EffectForbid`: любой совпавший forbid запрещает source поверх containment,
cross-scope grant **и** tenant RBAC. Это та же алгебра, что в model-access (`modelaccessgate.go:204`) и
Cedar core (`EffectForbid` «OVERRIDES everything», `authorizer.go:101`). Направление строго безопаснее
(forbid может только запретить), существующие single-binding tests не регрессируют; изменение заметно
только в ранее неопределённом multi-binding case.

**Триггер confinement.** Source *confined*, только если имеет ≥1 enabled **allow** binding. Все прежние
bindings — allow, поэтому это идентично «bound ⇔ has bindings». Source только с forbids остаётся global,
кроме названных ими subjects — posture model-access «ограничить отдельные subjects» теперь доступна и
источникам. Connector-assignment gate использует «нет allow binding» вместо «нет binding», поэтому
forbid-only source по-прежнему учитывает connector assignments.

### 3. Precedence: forbid абсолютен; credential от наиболее specific allow

Абсолютный forbid (§2) означает, что precedence выбирает не allow-vs-deny, а **credential** для
разрешённого actor при нескольких совпавших allow bindings. Порядок most-specific → least:

```
session > agent > user > user_group > role > agent_group > folder > workspace
```

Сначала identity-of-one, затем directory group, RBAC role, группа acting agent и resource containment.
Полный порядок делает credential selection детерминированным (заменяет lexical sort в
`loadEnabledBindings`) и уточняет документированный `session > agent > group > workspace` для пяти осей.

### 4. Доступность осей зависит от enforcement point — без сокрытия различий

У resolver две точки входа с разным actor context:

| axis | `ResolveForSession` (models `ScopeGate`, runtime) | `ResolveForAgent` (knowledge `RetrievalScopeGate`) |
|---|---|---|
| session | ✅ acting session ref | ❌ нет session в context → никогда не совпадает |
| agent | ✅ session's agent (agent-identity override) | ✅ agent ref (agent-identity override) |
| user / user_group / role | ✅ authenticated principal | ✅ authenticated principal |
| workspace / agent_group / folder | ✅ (existing) | ✅ (existing) |

Binding `session` на knowledge base **не обеспечивается** на agent-only retrieval path, поскольку там
нет session: это не молчаливое «разрешение», ось просто не входит в scope этого actor; другие bindings
того же source продолжают действовать. Асимметрия раскрыта в contract. Оси `session`/`agent` остаются
route-gated; зависящие от caller refs укреплены проверкой agent identity (`principal.AgentIdentity`
переопределяет caller-declared ref). `user`/`user_group`/`role` связаны с **неподделываемым authenticated
principal**, поэтому сильнее.

### 5. Posture enforcement изменяема, versioned и audited; ослабление dual-controlled

*Posture* source — набор enabled bindings и их effects. По решению Fran Olivares (2026-07-07, «robust
without duplication»), **`revision.go` и `approvals.go` в governance внутренние для модуля и НЕ могут
переиспользоваться из `sourcescope`** (проверено: unexported helpers, собственные entities, REST approval
flow). Их fork в `sourcescope` дублировал бы техдолг, поэтому posture controls **самодостаточны** и
переиспользуют единственный общий immutable primitive — audit ledger:

- **Аудит и версии через audit chain.** Каждая mutation posture записывает её **delta** в append-only,
  hash-chained audit ledger (ADR-0009): `sourcescope.binding.*` для create/update/delete (расширяя
  `auditBinding` полем `effect`) и `sourcescope.posture.{propose,approve,reject}` для dual-control
  lifecycle. Ledger и есть immutable sequenced version history; отдельная numbered-revision *table with
  rollback* намеренно не добавляется (дублировала бы `governance/revision.go`). Pending/decided строки
  **posture-request** — first-class queryable record каждого *ослабления*, его автора и одобрившего.
- **Dual-control только для ослабления, самодостаточно.** Mutation, способная **расширить** доступ к source,
  — *relaxation*: actor её не применяет; создаётся pending `sourcescope_posture_request`, применяемый
  только после одобрения **ВТОРЫМ, ОТЛИЧНЫМ** principal (`proposer != approver` обеспечивает two-person
  integrity), у которого есть admin-tier permission `sourcescope:posture:admin` (separation of duty от
  editor-tier proposer).

  > **Поправка к статусу, 2026-08-07.** Перечень ниже ИСПРАВЛЕН. В исходной редакции он называл
  > *broadening allow* и *перенос allow*, не называл **ни одной** операции над scope у `forbid`, а
  > «narrowing к более specific tree» помещал в обычные single-actor writes **без уточнения по effect**.
  > Код реализовывал это буквально, поэтому `forbid`, остававшийся enabled `forbid` и лишь менявший
  > покрываемую population, применялся немедленно одним actor — тогда как УДАЛЕНИЕ того же forbid требовало
  > двоих. Барьер двух человек обходился редактированием вместо удаления. Инверсия классификаторов в
  > whitelist вскрыла ещё три утечки того же класса: `allow`, перенесённый в «более specific» tree;
  > ПОСЛЕДНИЙ enabled `allow`, превращённый в `forbid`; и создание `allow` на УЖЕ confined source
  > (создание не классифицировалось вообще ничем). Общее правило в
  > начале этого пункта не менялось никогда — именно оно authorize это исправление: перечень всегда был уже
  > того правила, которое он якобы уточнял.

  **Классификаторы — это WHITELIST.** Они перечисляют writes, которые доказуемо не могут расширить доступ,
  и трактуют **всё остальное — включая любую форму, которую они не распознают, — как relaxation**. Blacklist
  ослабляющих форм протекает по построению, и наш blacklist протёк в четырёх местах. Три — правки
  существующего binding: `forbid`, сужающий свой scope; `allow`, перенесённый в «более specific» tree; и
  ПОСЛЕДНИЙ enabled `allow`, превращённый в `forbid`. Четвёртое — создание, которое не классифицировалось
  ничем. Первые два появились оттого, что операцию над scope прочитали с полярностью `allow`. Третье — оттого,
  что прочитали EFFECT строки, забыв про CONFINEMENT, который несла та же строка: source confined лишь пока у
  него есть enabled `allow`, поэтому запись, читающаяся как «эта строка теперь может только запрещать», — это
  та же запись, которая делает source глобальным.

  **`forbid` ИНВЕРТИРУЕТ ПОЛЯРНОСТЬ любой операции над scope — в этом и ловушка.** У `allow` меньший scope
  достаёт до меньшего числа actors: ужесточение. У `forbid` он ЗАЩИЩАЕТ меньшее число actors: все, кого он
  перестаёт покрывать, оказываются раз-запрещены этой единственной записью.

  **Два scope сравнимы, только когда это ОДИН И ТОТ ЖЕ scope.** `specificityRank` (`resolver.go`)
  **упорядочивает trees для выбора CREDENTIAL** среди подходящих allow-bindings; это **не отношение
  вложенности**, и использовать его как таковое нельзя. `role:admin` и `user_group:g1`, `workspace:eng` и
  `agent_group:core`, папка и её потомок — это разные POPULATIONS, ни одна не содержит другую, а у folder
  binding измерения вложенности нет вовсе (folder binding едет на cross-scope Cedar grant). Членство тоже не зафиксировано:
  superset, доказанный чтением строк сегодня, завтра уже не superset. Поэтому сертификат «эта запись не
  может расширить доступ» — это **тождество scope и ничто более слабое**, а «я не умею сравнить эти два
  scope» разрешается в *relaxation*: false positive стоит одного лишнего одобрения, false negative — это
  обход барьера двух человек.

  **Relaxations** точно (`classifyCreate`/`classifyUpdate`/`classifyDelete`): delete или disable enabled
  **forbid**; `forbid→allow`; **любое изменение scope у enabled forbid** (раз-запрещает часть его
  population); **enable** allow; disable/delete **последнего** enabled allow (source становится unconfined →
  global); **любое изменение scope у enabled allow** — шире, уже или вбок, безразлично; **создание allow на
  УЖЕ confined source** (grant для population, которая не могла до него дотянуться); отдельная односторонняя
  operation **`POST /sources/disable-scoping`**, зеркальная AWS `qbusiness:DisableAclOnDataSource`.

  **Tightening / neutral** mutations — обычные single-actor writes: аудируются, но не gated: добавление
  **forbid**; `allow→forbid`; создание **ПЕРВОГО** enabled allow на unconfined source (он ставит source под
  governance — крупнейшее ужесточение в модуле, намеренно без барьера, чтобы безопасный шаг никогда не был
  дорогим); создание **disabled** строки; включение припаркованного **forbid**; delete/disable **не**
  последнего allow; и правка note/credential, не трогающая effect, enabled и scope (credential locator
  выбирает, КАКУЮ ссылку получит уже авторизованный actor, а не БУДЕТ ли он авторизован). Строка, disabled
  до и после, ничего не enforce — любая запись в неё нейтральна.

  Асимметрия соответствует AWS (ослабление — privileged op) и превосходит неизменяемую
  posture Google: наша изменяема *и* governed. Endpoints: ослабляющие create/update/delete ПРЕДЛАГАЮТСЯ
  через существующие `POST /bindings` и `PUT`/`DELETE /bindings/{id}` (возвращают `202` с pending request);
  `POST /posture-requests/{id}/{approve,reject}` решают; `GET /posture-requests` — reviewer queue.

### 6. Access-map проецирует новых инициаторов (ADR-0003)

`publishBindingEdges` проецирует permitted-сторону RRW map. `EdgeObservation` уже поддерживает
`OriginKind ∈ {agent, session, identity}` (`sdk/model/observation.go:55`), поэтому каждая identity-of-one
axis проецирует ОДНО ребро: `session` binding → ребро с инициатором `session` именно этой session; `agent` →
ребро с инициатором `agent`; `user` → ребро с инициатором `identity`. Для групповых axes (`user_group`, `role`) понадобилось бы
перечислить MEMBERS, но они — auth-scope entities (directory groups, users), недостижимые из tenant
`store.Scope` модуля. Поэтому, как reverse-grant projection folder binding, они **DEFER**: логируют и
ничего не проецируют. Forbid bindings ничего не проецируют: forbid не permitted edge. Enforcement всегда
является live decision resolver по live principal; map — best-effort observability drift, и отсутствующее
или deferred edge никогда не ослабляет enforcement.

## Последствия

- **Плюсы:** четыре vision axes (пять с `role`) выразимы, deny-closed обеспечены на обоих реальных PEP и
  видимы в scope resolution/access-map; единая auditable/listable форма binding для console; identity
  axes связаны с authenticated principal (неподделываемы); нет второго authorization engine; hot path
  платит за одну дешёвую проверку membership и **ноль** новых policy round-trips; mutable-yet-governed
  posture — проверенное отличие от AWS (one-way) и Google (immutable).
- **Минусы / компромиссы:** `scope_tree` несёт semantics и containment scope, и subject identity
  (смягчение: contract представляет оба как единый *scope predicate*); machinery posture/dual-control
  добавляет реальную поверхность, не используемую минимальным deployment до relaxation; absolute forbid
  — документированное изменение в безопасную сторону.
- **Нейтрально:** `role` концептуально пересекается с tenant-RBAC soft-isolation bypass (`rbacAllows`),
  но они compose (`role` binding — positive scope, RBAC bypass — правило видимости tenant operator), а
  forbid переопределяет **оба**.

## Почему альтернативы были отклонены

- **(B) High-level API, генерирующий Cedar policies для новых осей.** Отклонено: (1) это был бы
  *единственный* plane, авторящий raw Cedar, тогда как цель согласованности model-access решает по своим
  строкам (`modelaccessgate.go:11-14`); (2) Cedar round-trip на каждый resolve hot path; (3) нужный console
  обратный вопрос остаётся нерешённым Cedar reverse-query, блокируя или приближая UI/access-map; (4) аудит
  «кто что scoped» потребовал бы читать policy text, а не rows.
- **(C) Отдельная model_access-twin table для source-subject grants, составленная с containment binding.**
  Отклонено как over-engineering, *снижающий* robustness: две decision planes надо compose на каждом PEP
  и синхронизировать — классический источник security drift и ambiguous precedence. «Наиболее complete/
  enterprise» достигается **глубиной одной plane** (все axes + effect + versioned dual-controlled posture +
  полная test matrix), не дублированием plumbing. Единая plane с общей algebra проще для аудита
  («всё governing source X» = один query) и доказательства корректности.
- **Расширение словаря custom-role scopeSpec вместо local enum.** Отклонено: `scope_tree` в `sourcescope`
  — module-local constant, лишь *зеркалящий* каталог custom-role (`schema.go:49`); расширение общего
  каталога протащило бы source axes в допустимые targets custom roles. Новые trees остаются локальными для
  `sourcescope`.
