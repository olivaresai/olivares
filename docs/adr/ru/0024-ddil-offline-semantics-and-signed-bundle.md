> Машинный перевод. Авторитетным источником является английская версия.

# ADR-0024: Офлайн-семантика DDIL по каждой plane и единый формат signed bundle

- **Status:** accepted
- **Date:** 2026-07-09
- **Deciders:** Fran Olivares (ratified the three questions below during design, 2026-07-09)
- **References:** the DDIL work brief; the OTA update framework
  (`core/release/manifest.go`); ADR-0009
  (hash-chained audit ledger); ADR-0013 (embedded Cedar PDP); ADR-0021 (durable
  JetStream bus, enterprise add-on); ADR-0022 (source scoping, forbid-overrides);
  the durable bus seam (`core/eventbus`, ADR-0021); break-glass.

## Контекст и постановка проблемы

Olivares развёртывается на tactical / disconnected edge (DoD DDIL: «предполагается, что units работают
хотя бы частично отключёнными… в air-gapped networks… и на tactical edge»). Edge buyer не просит нас
«интегрировать satellite link»: pLEO/satellite bearer — лишь прерывистый IP, приложение работает поверх
него без изменений. Требуется, чтобы governance продолжало работать, когда link не доступен часы или
дни и возвращается короткими окнами («submarine surfacing»).

Строительные блоки уже существуют и проверены во время discovery:

- **Audit ledger уже является durable, per-tenant, hash-chained, signed local store**
  (`core/internal/store/sqlstore/audit.go`; ADR-0009). Отключение не создаёт gap: просто перестаёт
  продвигаться off-box **forward cursor** (`modules/siemforward`, движимый eventing platform). Теряемого
  in-RAM-only audit buffer нет.
- **PDP вычисляет по LOCAL policy store** (embedded Cedar, ADR-0013), поэтому policy уже работает offline.
  Не решена *staleness*: как долго disconnected node доверяет policy, которую не может обновить?
- **Durable bus** — leader-only at-least-once JetStream overlay (ADR-0021), чей backend — закрытая
  enterprise build; OSS tree содержит только seam. Это *distribution* backbone, не local disk spool.
- **OTA updater уже определяет signed bundle** для air-gap updates: gzip tar с JSON `manifest.json` и
  detached Ed25519 signature над domain-separated verbatim bytes (`tag || manifest`, tag
  `olivares.update-manifest.v1\n`), проверяемой ДО parse (`core/release/manifest.go`). Также существуют
  отдельные `airgap-bundle.sh` (cosign, images + chart) и `core/dr/bundle.go` (AES-GCM-sealed DR snapshot).

Перед любым DDIL-кодом нужно решить три вопроса: они определяют fail-safe direction, не механизм.

## Движущие факторы решения

- **Fail-safe в правильном направлении.** Governance control plane не должна *повышать* privilege из-за
  потери link и не должна *молча* терять evidence.
- **Mission-safety на edge.** Outage link на часы не должен стать mission-kill, если безопасный ответ уже
  известен локально.
- **Без разрастания форматов.** «Один проверяемый bundle format, не два» (DDIL design brief). Вторая
  самописная signed-envelope implementation — второе место для ошибки domain separation, то есть именно
  cross-protocol key-reuse trap, уже оплаченная OTA updater.
- **Честность.** Объявленные документированные limits (disk budgets, TTL, что не переживает бесконечный
  outage) вместо silent truncation.

## Рассмотренные варианты

### Q1 — Offline policy trust

- **A. Асимметрично (deny вечен, allow истекает).** Restricting rules (ABAC deny, Cedar `forbid`)
  обеспечиваются offline бесконечно; positive grants (Cedar scoped `allow`, ADR-0019/ADR-0022) истекают
  после signed `policy_max_staleness` и fail deny-closed.
- **B. Полный deny-closed при TTL expiry.** После TTL node полностью прекращает governing.
- **C. Никогда не истекать, только warn.**

### Q2 — Поведение audit при исчерпании local disk budget

- **A. Fail-closed по умолчанию, opt-in degrade.** Default `block`: отказать в новых governed actions до
  потери evidence. Opt-in `degrade`: seal segment и append **signed, in-chain gap marker**, чтобы потеря
  evidence становилась обнаружимой и никогда не оставалась скрытой.
- **B. Всегда fail-closed.**
- **C. Всегда degrade.**

### Q3 — Унификация bundle format

- **A. Извлечь `core/sigbundle` + registry domain tag.** Поднять OTA update envelope в shared package;
  refactor `core/release` для использования за byte-identical golden test; DDIL work и feed
  security-advisories добавляют собственные domain tags.
- **B. Не трогать `core/release`; каждая session копирует pattern.**

## Результат решения

**Q1 → вариант A (асимметрично).** Offline после `policy_max_staleness`:

| Rule class | Offline, TTL expired | Обоснование |
|---|---|---|
| ABAC deny | **по-прежнему обеспечивается** | stale restriction может лишь ограничить, не повысить privilege |
| Cedar `forbid` (absolute, ADR-0022) | **по-прежнему обеспечивается** | то же; forbid уже переопределяет всё |
| Cedar positive grant / `allow` | **expired → deny-closed** | «expired grant никогда не должен authorize» |
| Break-glass | available, its own 1h/24h expiry | разрешённый offline escape hatch |

`policy_max_staleness` — operator setting (default 72h), переносимый в policy bundle и подписанный;
console/CLI заметно показывают age и expiry.

**Q2 → вариант A (fail-closed default, opt-in degrade).** Config `audit.spool.on_full`:

- `block` (default): новые governed actions отклоняются (`503`, deny-closed); reads продолжаются;
  console/CLI показывают «audit spool full — governance halted».
- `degrade` (явный opt-in): seal current segment и append signed in-chain marker `audit.gap`
  `{from_seq, to_seq, reason: "spool_full", count, at}`, чтобы chain оставалась continuous, а loss была
  доказуема. `audit.spool.max_bytes` объявляется и документируется.

Gap marker — ЕДИНСТВЕННАЯ разрешённая discontinuity chain; offline archive verifier
(`core/audit/archiveverify.go`) расширяется, чтобы воспринимать signed gap marker как *declared* boundary,
а не failure `seq-gap`.

**Q3 → вариант A (извлечь `core/sigbundle`).** Один envelope:

```
core/sigbundle/
  SigningInput(tag, payload) = tag || payload           // verbatim, no canonicalization
  Sign(tag, payload, priv) / Verify(tag, bundle, sig, pub)   // Ed25519, detached, verify-BEFORE-parse
  Envelope: tar.gz{ manifest.json, manifest.json.sig, <payload files by sha256> }
  Manifest: schema_version, kind, created_at, expires?, entries[{name, sha256, size}]
```

`core/release` рефакторится для использования `sigbundle.SigningInput` с tag
`olivares.update-manifest.v1\n`; golden test подтверждает, что `release.ManifestSigningInput(b)` побитно
не изменился, поэтому все уже выпущенные signatures продолжают проверяться. **Registry domain tags**
(таблица + uniqueness/no-prefix-collision test) содержит каждый tag:

| Tag | Owner | Note |
|---|---|---|
| `olivares.update-manifest.v1\n` | `core/release` (update manifest) | byte-identical after refactor |
| `olivares.ddil-bundle.v1\n` | this DDIL work | NEW — air-gap policy+audit+evidence bundle |
| `olivares.security-advisories.v1\n` | the security-advisories feed | NEW — signed OSV advisories feed |

`core/license` (bare `{`-leading JSON payload) и domains audit event/checkpoint (`olivares.audit.*`)
остаются provably disjoint от каждого tag (tag не начинается с `{`, audit domains — length-prefixed
preimages, не tar bundles). `core/dr/bundle.go` намеренно **не меняется**: это *sealed* (AES-GCM), unsigned
DR snapshot с иной trust model (confidentiality, не publisher-authenticity), объединение смешало бы их.

### Последствия

- **Плюсы:** fail-safe в верном направлении в обеих planes; один audited envelope и одна дисциплина
  domain separation вместо трёх; edge продолжает запрещать всегда запрещённое даже после долгого outage;
  потеря evidence по умолчанию невозможна, а при явном разрешении обнаружима.
- **Минусы / компромиссы:** positive grants перестают работать после `policy_max_staleness` при истинно
  долгом outage (смягчается break-glass и выбором TTL оператором); `degrade` меняет evidence на availability
  и требует осознанного opt-in; refactor `core/release` касается свежего OTA updater code (смягчение —
  golden byte-identity test).
- **Нейтрально / follow-ups:** feed security-advisories зависит от `core/sigbundle` и своего tag; archive
  verifier получает словарь `declared-gap`; `docs/deploy/ddil.md` документирует disk budgets, TTL и то,
  что не переживает бесконечный outage.

## Почему альтернативы были отклонены

- **Q1-B (полный deny-closed):** mission-kill. Downed link дольше TTL остановил бы edge unit, хотя deny
  rules не вызывали сомнений.
- **Q1-C (никогда не истекать):** grant, отозванный в центре, навсегда остался бы live на edge; unbounded
  authorization window неприемлемо для governance plane.
- **Q2-B (всегда fail-closed):** устраняет легитимный operator trade-off — некоторые edge missions нельзя
  останавливать; signed gap marker уже делает degrade честной.
- **Q2-C (всегда degrade):** слабый default для governance product; silent-by-policy evidence loss —
  именно то, что ledger должен предотвращать.
- **Q3-B (копировать pattern):** три envelope implementations и три шанса испортить domain separation;
  урок cross-protocol key reuse именно в том, что один key для двух message types без tag создаёт forgery vector.

## Примечание о реализации (2026-07-10)

Q2 реализован согласно ратификации. Gap marker объявляет dropped range
`{from_seq, to_seq, count, reason, at}` как sequence hole с непрерывной hash linkage; live chain verifier,
archive exporter и offline archive verifier распознают корректно объявленный и подписанный marker как
declared boundary (`declared_gaps` в отчётах), продолжая fail при любой undeclared или inconsistent
discontinuity. Budget измеряет точные logical bytes сохранённых event values через incremental counter,
пересчитываемый из ledger при каждом budgeted boot; integrity machinery (checkpoints, archive anchors,
сам marker) допускается сверх budget, но полностью учитывается, а system plane budget-governed, как любой writer.

Параллельная implementation, сохранявшая gapless chain (summary marker без sequence hole, physical
page/relation measurement, exemption system plane), была интегрирована в тот же день и вытеснена этой
при reconciliation: ратифицированный текст требует declared range и verifier extension, а exact counter
устраняет measurement hysteresis и проблемы modified-v3-migration физического подхода. Вытесненный
вариант остаётся в history для справки.
