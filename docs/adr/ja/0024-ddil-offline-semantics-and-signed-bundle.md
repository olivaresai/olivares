> 機械翻訳です。正式な情報源は英語版です。

# ADR-0024: plane ごとの DDIL offline semantics と単一の signed bundle format

- **Status:** accepted
- **Date:** 2026-07-09
- **Deciders:** Fran Olivares (ratified the three questions below during design, 2026-07-09)
- **References:** the DDIL work brief; the OTA update framework
  (`core/release/manifest.go`); ADR-0009
  (hash-chained audit ledger); ADR-0013 (embedded Cedar PDP); ADR-0021 (durable
  JetStream bus, enterprise add-on); ADR-0022 (source scoping, forbid-overrides);
  the durable bus seam (`core/eventbus`, ADR-0021); break-glass.

## 背景と課題

Olivares は tactical / disconnected edge（DoD DDIL: 「unit は少なくとも部分的に disconnected な状態で…
air-gapped network…および tactical edge 全体で運用されることを想定」）に deploy される。edge buyer が求めるのは
「satellite link の統合」ではない。pLEO/satellite bearer は断続的な IP にすぎず、app は変更なしにその上で動く。
必要なのは、link が数時間または数日 down して短い window（「submarine surfacing」）で戻る場合も governance が
機能し続けることである。

building block はすでに存在し、discovery 中に検証済みである。

- **audit ledger はすでに durable、per-tenant、hash-chained、signed local store** である
  （`core/internal/store/sqlstore/audit.go`; ADR-0009）。disconnection で gap は生じない。eventing platform が駆動する
  off-box **forward cursor**（`modules/siemforward`）の進行が止まるだけである。失われる in-RAM-only audit buffer はない。
- **PDP は LOCAL policy store に対して評価する**（embedded Cedar、ADR-0013）ため、policy はすでに offline で動く。
  未決定なのは *staleness*、すなわち disconnected node が refresh 不能な policy をいつまで信頼できるかである。
- **durable bus** は leader-only、at-least-once の JetStream overlay（ADR-0021）で、backend は非公開 enterprise build。
  OSS tree は seam だけを提供する。これは *distribution* backbone であり、local disk spool ではない。
- **OTA updater は air-gap update 用の signed bundle をすでに定義する。** JSON `manifest.json` と、domain-separated な
  verbatim byte（`tag || manifest`、tag `olivares.update-manifest.v1\n`）に対する detached Ed25519 signature の gzip tar
  で、parse の**前に** verify する（`core/release/manifest.go`）。別の `airgap-bundle.sh`（cosign、image + chart）と
  `core/dr/bundle.go`（AES-GCM-sealed DR snapshot）も存在する。

DDIL code を書く前に 3 つの問いを解決しなければならない。これらは mechanism ではなく fail-safe direction を定める。

## 意思決定の要因

- **正しい方向の fail-safe。** governance control plane は link を失ったために privilege を*escalate*してはならず、
  evidence を*黙って*失ってはならない。
- **edge での mission-safety。** 数時間の link outage が、safe answer がすでに local で判明しているのに mission-kill に
  なってはならない。
- **format sprawl なし。** 「検証可能な bundle format は 2 つではなく 1 つ」（DDIL design brief）。別の手作り signed-envelope
  implementation は domain separation を誤る場所を 1 つ増やす。OTA updater がすでに対処した cross-protocol key-reuse trap
  そのものである。
- **誠実さ。** silent truncation より、宣言・文書化された limit（disk budget、TTL、infinite outage で生き残らないもの）。

## 検討した選択肢

### Q1 — Offline policy trust

- **A. Asymmetric（deny は永続、allow は expire）。** restricting rule（ABAC deny、Cedar `forbid`）は offline で
  無期限に強制し続ける。positive grant（Cedar scoped `allow`、ADR-0019/ADR-0022）は signed
  `policy_max_staleness` 後に expire し、deny-closed で fail する。
- **B. TTL expiry 時に全面 deny-closed。** TTL 後、node は governing を完全に停止する。
- **C. expire せず warn だけ。**

### Q2 — local disk budget 枯渇時の audit behaviour

- **A. fail-closed が default、opt-in degrade。** default `block`: evidence を失う前に新しい governed action を拒否する。
  opt-in `degrade`: segment を seal し、**signed、in-chain gap marker**を append して loss を改ざん検知可能にし、決して
  silent にしない。
- **B. 常に fail-closed。**
- **C. 常に degrade。**

### Q3 — Bundle format unification

- **A. `core/sigbundle` + domain-tag registry を抽出。** OTA update envelope を shared package へ持ち上げる。
  `core/release` は byte-identical golden test の背後でそれを利用するよう refactor し、この DDIL work と
  security-advisories feed はそれぞれ独自 domain tag を追加する。
- **B. `core/release` は変更せず、各 session が pattern を copy する。**

## 決定の結果

**Q1 → 選択肢 A（asymmetric）。** offline で `policy_max_staleness` を超えた場合:

| Rule class | Offline, TTL expired | 理由 |
|---|---|---|
| ABAC deny | **引き続き強制** | stale restriction は restrict するだけで escalate しない |
| Cedar `forbid` (absolute, ADR-0022) | **引き続き強制** | 同様。forbid はすでにすべてを override する |
| Cedar positive grant / `allow` | **expired → deny-closed** | 「expired grant は決して authorize してはならない」 |
| Break-glass | available, its own 1h/24h expiry | sanctioned offline escape hatch |

`policy_max_staleness` は operator setting（default 72h）で、policy bundle に含まれ signed される。console/CLI は age と
expiry を目立つ形で表示する。

**Q2 → 選択肢 A（fail-closed default、opt-in degrade）。** Config `audit.spool.on_full`:

- `block`（default）: 新しい governed action を拒否する（`503`, deny-closed）。read は提供し続ける。console/CLI は
  「audit spool full — governance halted」を表示する。
- `degrade`（explicit opt-in）: current segment を seal し、signed in-chain `audit.gap` marker
  `{from_seq, to_seq, reason: "spool_full", count, at}` を append して chain を continuous に保ち、loss を証明可能にする。
  `audit.spool.max_bytes` は宣言・文書化する。

gap marker は chain 内で認められる唯一の discontinuity である。offline archive verifier
（`core/audit/archiveverify.go`）を拡張し、signed gap marker を `seq-gap` failure ではなく *declared* boundary として認識する。

**Q3 → 選択肢 A（`core/sigbundle` を抽出）。** 1 つの envelope:

```
core/sigbundle/
  SigningInput(tag, payload) = tag || payload           // verbatim, no canonicalization
  Sign(tag, payload, priv) / Verify(tag, bundle, sig, pub)   // Ed25519, detached, verify-BEFORE-parse
  Envelope: tar.gz{ manifest.json, manifest.json.sig, <payload files by sha256> }
  Manifest: schema_version, kind, created_at, expires?, entries[{name, sha256, size}]
```

`core/release` は tag `olivares.update-manifest.v1\n` で `sigbundle.SigningInput` を再利用するよう refactor し、
`release.ManifestSigningInput(b)` が byte-for-byte 変更されないことを assert する golden test で guard する
（発行済み release signature がすべて引き続き verify できる）。**domain-tag registry**（table + uniqueness/
no-prefix-collision test）はすべての tag を記録する。

| Tag | Owner | Note |
|---|---|---|
| `olivares.update-manifest.v1\n` | `core/release` (update manifest) | byte-identical after refactor |
| `olivares.ddil-bundle.v1\n` | this DDIL work | NEW — air-gap policy+audit+evidence bundle |
| `olivares.security-advisories.v1\n` | the security-advisories feed | NEW — signed OSV advisories feed |

`core/license`（bare `{`-leading JSON payload）と audit event/checkpoint domain（`olivares.audit.*`）は、各 tag と
provably disjoint のままである（tag は `{` で始まらず、audit domain は length-prefixed preimage で tar bundle ではない）。
`core/dr/bundle.go` は意図的に**そのまま**とする。これは *sealed*（AES-GCM）、unsigned DR snapshot で、異なる trust model
（publisher-authenticity ではなく confidentiality）であり、統合すれば両者を混同する。

### 帰結

- **良い点:** 両 plane で正しい方向に fail-safe。3 つではなく 1 つの audited envelope と 1 つの domain-separation
  discipline。長い outage 後も edge は常に deny されていたものを deny し続ける。evidence loss は default では不可能で、
  明示的に許可した場合は改ざん検知可能。
- **悪い点 / トレードオフ:** 真に長い outage では `policy_max_staleness` 後に positive grant が動かなくなる
  （break-glass と TTL を operator choice にすることで mitigation）。`degrade` mode は evidence と availability を交換し、
  意識的な opt-in が必要。`core/release` の refactor は新しく merge された OTA updater code に触れる
  （golden byte-identity test で mitigation）。
- **中立 / follow-up:** security-advisories feed は `core/sigbundle` と独自 tag に依存する。archive verifier は
  `declared-gap` vocabulary を得る。`docs/deploy/ddil.md` は disk budget、TTL、infinite outage で生き残らないものを記載する。

## 代替案を却下した理由

- **Q1-B（全面 deny-closed）:** mission-kill。TTL より長い downed link は deny rule に疑義がない場合でも edge unit を停止する。
- **Q1-C（expire しない）:** centre で revoke された grant が edge で永遠に live のままになる。unbounded authorization
  window は governance plane に許容できない。
- **Q2-B（常に fail-closed）:** 正当な operator trade-off（停止できない edge mission がある）を除去する。signed gap marker
  が degrade をすでに honest にする。
- **Q2-C（常に degrade）:** governance product として弱い default。policy により silent になる evidence loss は、ledger が
  防ぐために存在するものそのものである。
- **Q3-B（pattern を copy）:** 3 つの envelope implementation と domain separation を誤る 3 つの機会。cross-protocol
  key-reuse の教訓は、tag なしで 1 key を 2 message type に使うことが forgery vector になる点だった。

## 実装メモ（2026-07-10）

Q2 は ratified された通り実装済みである。gap marker は dropped range
`{from_seq, to_seq, count, reason, at}` を hash linkage が continuous のままの sequence hole として宣言し、live chain
verifier、archive exporter、offline archive verifier はすべて、correctly-declared、correctly-signed marker を declared
boundary（report の `declared_gaps`）として認識しつつ、undeclared または inconsistent な discontinuity では引き続き fail する。
budget は、保存された event value の正確な logical byte を、budgeted boot ごとに ledger から再計算される incremental counter
で測る。integrity machinery（checkpoint、archive anchor、marker 自体）は budget 超過でも受け入れるが完全に account し、
system plane も他の writer と同じく budget-governed である。

chain を gapless に保つ並行 implementation（sequence hole のない summary marker、physical page/relation measurement、
system-plane exemption）は同日に integrate されたが、reconciliation 中にこの実装が supersede した。ratified text は declared
range と verifier extension を規定し、exact counter は physical approach の measurement hysteresis と modified-v3-migration 問題を
除去する。superseded variant は参照用に history に残る。
