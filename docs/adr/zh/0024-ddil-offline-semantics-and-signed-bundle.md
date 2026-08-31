> 机器翻译。英文版本为权威来源。

# ADR-0024: 按 plane 定义 DDIL 离线语义，并采用一种 signed bundle format

- **Status:** accepted
- **Date:** 2026-07-09
- **Deciders:** Fran Olivares (ratified the three questions below during design, 2026-07-09)
- **References:** the DDIL work brief; the OTA update framework
  (`core/release/manifest.go`); ADR-0009
  (hash-chained audit ledger); ADR-0013 (embedded Cedar PDP); ADR-0021 (durable
  JetStream bus, enterprise add-on); ADR-0022 (source scoping, forbid-overrides);
  the durable bus seam (`core/eventbus`, ADR-0021); break-glass.

## 背景与问题陈述

Olivares 部署于 tactical / disconnected edge（DoD DDIL：“预计 units 至少部分时间在断网状态下运行……
跨 air-gapped networks……以及 tactical edge”）。edge buyer 并不要求我们“集成卫星链路”——pLEO/satellite
bearer 只是间歇性 IP，app 可在其上不加修改地运行。他们要求的是：当 link 中断数小时或数日，并在短暂
窗口恢复（“submarine surfacing”）时，governance 仍能继续工作。

基础构件已经存在，并在 discovery 中得到验证：

- **Audit ledger 已是 durable、per-tenant、hash-chained、signed local store**
  （`core/internal/store/sqlstore/audit.go`; ADR-0009）。断网不会造成 gap，只会使 off-box
  **forward cursor**（`modules/siemforward`，由 eventing platform 驱动）停止前进。不存在会丢失的
  in-RAM-only audit buffer。
- **PDP 针对 LOCAL policy store 求值**（embedded Cedar，ADR-0013），所以 policy 已可离线工作。
  未决问题是 *staleness*：disconnected node 可在多长时间内继续信任无法刷新的 policy？
- **Durable bus** 是 leader-only、at-least-once JetStream overlay（ADR-0021），backend 来自私有
  enterprise build；OSS tree 只提供 seam。它是 *distribution* backbone，而非 local disk spool。
- **OTA updater 已为 air-gap update 定义 signed bundle**：包含 JSON `manifest.json` 的 gzip tar，
  以及对 domain-separated verbatim byte（`tag || manifest`，tag `olivares.update-manifest.v1\n`）的
  detached Ed25519 signature，在 parse **之前**验证（`core/release/manifest.go`）。另有
  `airgap-bundle.sh`（cosign、images + chart）和 `core/dr/bundle.go`（AES-GCM-sealed DR snapshot）。

在编写任何 DDIL code 前必须解决三个问题，因为它们定义 fail-safe direction，而非机制。

## 决策驱动因素

- **向正确方向 fail-safe。** Governance control plane 绝不能因为失去 link 而*提升* privilege，也绝不能
  *悄然*丢失 evidence。
- **edge 上的 mission-safety。** 若安全答案已在本地明确，持续数小时的 link outage 不应成为 mission-kill。
- **不扩散格式。** “一种可验证 bundle format，而非两种”（DDIL design brief）。第二套手工 signed-envelope
  implementation 会增加一个可能错误实现 domain separation 的位置，正是 OTA updater 已解决的 cross-protocol
  key-reuse trap。
- **诚实性。** 声明并记录 limit（disk budget、TTL、何者无法经受无限 outage），而非 silent truncation。

## 考虑过的选项

### Q1 — Offline policy trust

- **A. 非对称（deny 永久，allow 过期）。** Restricting rule（ABAC deny、Cedar `forbid`）离线时永久
  强制；positive grant（Cedar scoped `allow`，ADR-0019/ADR-0022）在 signed
  `policy_max_staleness` 后过期，并 fail deny-closed。
- **B. TTL expiry 后全面 deny-closed。** TTL 后 node 完全停止 governing。
- **C. 永不过期，只 warn。**

### Q2 — local disk budget 耗尽时的 audit 行为

- **A. 默认 fail-closed，可 opt-in degrade。** 默认 `block`：在丢失 evidence 前拒绝新的 governed action。
  opt-in `degrade`：seal segment 并 append 一个 **signed、in-chain gap marker**，使证据丢失可被检测，
  绝不静默。
- **B. 始终 fail-closed。**
- **C. 始终 degrade。**

### Q3 — Bundle format 统一

- **A. 提取 `core/sigbundle` + domain-tag registry。** 将 OTA update envelope 提升为 shared package；
  refactor `core/release` 以在 byte-identical golden test 保护下使用它；本 DDIL 工作和
  security-advisories feed 各自添加 domain tag。
- **B. 保持 `core/release` 不变；每个 session 复制此 pattern。**

## 决策结果

**Q1 → 选项 A（非对称）。** 离线超过 `policy_max_staleness` 后：

| Rule class | Offline, TTL expired | 理由 |
|---|---|---|
| ABAC deny | **仍然强制** | stale restriction 只会限制，绝不提升 privilege |
| Cedar `forbid` (absolute, ADR-0022) | **仍然强制** | 同理；forbid 已覆盖一切 |
| Cedar positive grant / `allow` | **expired → deny-closed** | “expired grant 绝不能 authorize” |
| Break-glass | available, its own 1h/24h expiry | 受认可的 offline escape hatch |

`policy_max_staleness` 是 operator setting（默认 72h），随 policy bundle 携带并签名；console/CLI
醒目显示 age 与 expiry。

**Q2 → 选项 A（默认 fail-closed，opt-in degrade）。** Config `audit.spool.on_full`：

- `block`（默认）：拒绝新的 governed action（`503`，deny-closed）；read 继续服务；console/CLI 显示
  “audit spool full — governance halted”。
- `degrade`（显式 opt-in）：seal current segment，并 append signed in-chain `audit.gap` marker
  `{from_seq, to_seq, reason: "spool_full", count, at}`，让 chain 保持 continuous 并使 loss 可证明。
  `audit.spool.max_bytes` 会被声明并记录。

gap marker 是 chain 中唯一受认可的 discontinuity；offline archive verifier
（`core/audit/archiveverify.go`）得到扩展，将 signed gap marker 识别为 *declared* boundary，而非
`seq-gap` failure。

**Q3 → 选项 A（提取 `core/sigbundle`）。** 一种 envelope：

```
core/sigbundle/
  SigningInput(tag, payload) = tag || payload           // verbatim, no canonicalization
  Sign(tag, payload, priv) / Verify(tag, bundle, sig, pub)   // Ed25519, detached, verify-BEFORE-parse
  Envelope: tar.gz{ manifest.json, manifest.json.sig, <payload files by sha256> }
  Manifest: schema_version, kind, created_at, expires?, entries[{name, sha256, size}]
```

`core/release` 被 refactor 为使用 tag `olivares.update-manifest.v1\n` 调用
`sigbundle.SigningInput`，并由 golden test 断言 `release.ManifestSigningInput(b)` 逐字节不变
（所有已发布 release signature 仍可验证）。**domain-tag registry**（table + uniqueness/
no-prefix-collision test）记录每个 tag：

| Tag | Owner | Note |
|---|---|---|
| `olivares.update-manifest.v1\n` | `core/release` (update manifest) | byte-identical after refactor |
| `olivares.ddil-bundle.v1\n` | this DDIL work | NEW — air-gap policy+audit+evidence bundle |
| `olivares.security-advisories.v1\n` | the security-advisories feed | NEW — signed OSV advisories feed |

`core/license`（bare `{`-leading JSON payload）与 audit event/checkpoint domain（`olivares.audit.*`）
和每个 tag 保持可证明的互斥（tag 不以 `{` 开头，audit domain 是 length-prefixed preimage 而非 tar bundle）。
`core/dr/bundle.go` 有意**保持原状**：它是 *sealed*（AES-GCM）、unsigned DR snapshot，采用不同 trust
model（confidentiality 而非 publisher-authenticity）；将其纳入会混淆二者。

### 后果

- **好处：** 两个 plane 都向正确方向 fail-safe；使用一种 audited envelope 与一套 domain-separation
  discipline，而非三种；即使长期 outage，edge 仍拒绝一直被拒绝的事项；默认不可能丢失 evidence，
  明确许可时，证据丢失也可被检测。
- **坏处 / 权衡：** 真正长期 outage 中，positive grant 在 `policy_max_staleness` 后停止工作
  （通过 break-glass 和让 TTL 成为 operator choice 缓解）；`degrade` mode 用 evidence 换 availability，
  必须有意识地 opt-in；refactor `core/release` 会触及刚 merge 的 OTA updater code（由 golden
  byte-identity test 缓解）。
- **中性 / 后续：** security-advisories feed 依赖 `core/sigbundle` 及自己的 tag；archive verifier
  增加 `declared-gap` vocabulary；`docs/deploy/ddil.md` 记录 disk budget、TTL，以及无法经受无限 outage 的内容。

## 为何否决了其他选项

- **Q1-B（全面 deny-closed）：** mission-kill。link down 超过 TTL 会停止 edge unit，即使其 deny rule
  从未存在疑问。
- **Q1-C（永不过期）：** 在 centre 撤销的 grant 会永远在 edge 有效；治理 plane 无法接受无界
  authorization window。
- **Q2-B（始终 fail-closed）：** 移除合理的 operator trade-off（某些 edge mission 不得停止）；signed
  gap marker 已使 degrade 诚实。
- **Q2-C（始终 degrade）：** 对治理产品而言默认值过弱；policy 允许的静默 evidence loss 正是 ledger
  所要防止的事情。
- **Q3-B（复制 pattern）：** 三种 envelope implementation，三次搞错 domain separation 的机会；
  cross-protocol key-reuse 的教训正是：一个 key 对两种没有 tag 的 message type 签名会产生 forgery vector。

## 实施说明（2026-07-10）

Q2 已按批准内容实施。Gap marker 将 dropped range `{from_seq, to_seq, count, reason, at}` 声明为
hash linkage 保持 continuous 的 sequence hole；live chain verifier、archive exporter 与 offline archive
verifier 都将 correctly-declared、correctly-signed marker 识别为 declared boundary（报告中的
`declared_gaps`），同时继续在任何 undeclared 或 inconsistent discontinuity 上 fail。Budget 使用 incremental
counter 精确测量已存 event value 的 logical byte，并在每次 budgeted boot 时从 ledger 重新计算；integrity
machinery（checkpoint、archive anchor、marker 本身）可以超过 budget 写入但会被完整核算，system plane 也像其他
writer 一样受 budget 治理。

同日还集成了保持 chain gapless 的平行 implementation（无 sequence hole 的 summary marker、physical
page/relation measurement、system-plane exemption），但在 reconciliation 中被本实现 supersede：批准文本
明确要求 declared range 与 verifier extension，而 exact counter 消除了 physical approach 的 measurement
hysteresis 和 modified-v3-migration 问题。被取代的 variant 仍保留在 history 中供参考。
