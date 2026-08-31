> 機械翻訳です。正式な情報源は英語版です。

# ADR-0023: 3 つの transit point における context-policy enforcement と group ごとの window・spend ceiling

- **Status:** accepted
- **Date:** 2026-07-08
- **Deciders:** Fran Olivares
- **References:** ADR-0022 (source scoping by subject axis — its subject resolver and `most-specific` precedence are mirrored here), ADR-0009 (append-only hash-chained audit), ADR-0003 (RRW map — permitted vs observed).

## 背景と課題

context-policy（window size と compaction strategy）は governed data として永続化されていたが、**consumer は一度も
適用しなかった**。code comment が約束した consumer は存在せず、policy は死んだ metadata だった。別途、inference
proxy の token ceiling は**tenant ごと / request ごと**だけで、FinOps は `team` budget dimension を持つものの
**detective かつ fail-open** だった。「この user（または agent）の group は window / spend を最大この量まで消費
できる」と定めて強制する方法がなかった。

製品ビジョンは、保存されているが使われていない policy では実現できなかった 2 点を必要とする。

1. **context-policy が 3 つすべての transit point で DECIDE する。** platform が model request に触れる session
   runtime、inline inference proxy、knowledge retrieval で inert data ではなく decision として働く。
2. **group ごとの enforced ceiling。** `user_group` と `agent_group` の両方について **context window** と
   **spend** を制限し、policy が要求する箇所では deny-closed、かつ**正直な degradation**（silent clamp も silent
   allow もなし）を行う。

## 意思決定の要因

- **source scoping（ADR-0022）との一貫性。** 同じ subject vocabulary と `most-specific` precedence を再利用し、
  operator が context governance を source scoping と同様に理解できるようにする。第 2 decision engine はなく、
  attack surface は小さい。
- **ceiling は実際に ceiling でなければならない。** more-specific scope が数値 limit を*緩められる*なら、それは
  ceiling ではない。「enforced ceilings」が目的である。
- **正直な degradation。** platform が完全に account できない場合（approximate group spend）は、安全な方向に
  fail し、そのことを明示する。誤って deny せず、黙って allow しない。
- **既存 primitive の再利用。** 新しい cross-cutting machinery より audit ledger、既存の per-subject cost attribution、
  既存 proxy deny path を優先する。

## 決定の結果

### 1. `Apply` composition — qualitative は most-specific、security floor は restrictive、`max_tokens` は MIN

`Module.Apply`（`modules/knowledge/context.go:263`）は request の effective policy を resolve する。

- **Qualitative** field（`strategy`）は ADR-0022 と同様に **most-specific-wins**。
- **Security floor** は**restrictively** compose する。`forbid` は absolute、`redaction_required` は OR、
  `excluded_sources` は union。
- **`max_tokens` は MIN で compose**する（most-restrictive。field は `context.go:62,73`、bound は
  `context.go:124`）。more-specific scope が増やせる ceiling は ceiling ではないため、数値 limit を意図的に精緻化した。
  deployment が limit に most-specific を望む場合、この挙動は約 2 行で reversible である。

### 2. proxy の agent identity — 到達可能な残余を閉じ（E3-lite）、残りは正直に defer

session-inference WIF credential（`sk-ant-oat`）は、platform 自身の `olvs` / `olvk` token だけを authenticate する
inline inference proxy を通らない。*session* traffic の agent-identity federation を完全に閉じるには inference
credential の再設計（multi-day、ephemeral-WIF mint posture の一部）が必要であり、**専用 effort（E3-full）へ defer**する。

到達可能な部分は現在閉じる（**E3-lite**）。`authToken` が `AgentRef` → `AgentIdentity` を伝播し、models
actor-scope resolver は caller-declared value ではなく**authenticated principal**を尊重する（bug fix）。これにより
agent-on-behalf-of caller について proxy の `agent_group` axis が可能になる。agent ref は常に authenticated credential
から取得し、request body からは取得しない（`context.go:278-279`, `query.go:110-111`）。

### 3. group ごとの SPEND ceiling — preventive、本質的に fail-open、granular な fail-closed knob 付き

Budget に `user_group` / `agent_group` dimension を追加し、既存 per-subject cost attribution 上で member fan-out により
group spend を合計して `CheckBudget` から**preventively** enforce する（group column はない。すべての row を無差別に
合計すると mis-attribution bug になる — `modules/finops/ingest.go:75,361`）。

posture は **fail-open** である。これは budget check の性質であり、製品の *security = deny-closed* と
*budget = fail-open* の分離（`modules/models/api.go:639,656`）とも一致する。hard stop を望む deployment のため、
budget ごとに **`fail_closed`** knob を持つ（`modules/finops/budgets.go:102,166,182`）。これは正直に表明する。
preventive group spend は exact accounting ではなく*近似*である。coverage は attribution とともに拡大し、まだ attribution
されていない spend は group を under-count するだけで、安全な方向である（誤って deny しない）。group 向けの detective
ingest/finding FinOps backstop と local degradation counter は**文書化された follow-up**であり、意図的に half-wire しない。

### 4. window 超過時の proxy denial — 413、client payload は決して mutate しない

request が effective policy/group window を超えると proxy は detail 付き **HTTP 413 で deny** する
（`cmd/olivares/inferenceproxy.go:449`）。client の opaque payload を**決して mutate せず**、silent clamp ではなく deny
する（`inferenceproxy.go:550`）。compaction と signalled truncation は platform 自身が context を assemble する retrieval
と session runtime にのみ置き、caller prompt 上では行わない。silent degradation はない。

3 つの enforcement point は配線済みである。retrieval（`modules/knowledge/query.go:167` → `:354`）、session runtime
（`modules/sessions/runtime.go:285,623`）、inference proxy（上記）。

## 決定し明記する事項（承認済み方向の範囲内）

- **9 つの context-policy scope-kind** — `session > agent > user > user_group > role > agent_group > kb > workspace > tenant`
  — write handler で validation（`modules/knowledge/context.go:102-103`）。nullable、expand-only の `effect`
  （確立済み module-column reconcile、numbered migration なし）。
- **`surface` と `model` は scope-kind ではない。** retrieval には surface がなく、proxy はすでに per-surface window を
  MIN に折り込むため、追加すれば未使用の一般性になる（YAGNI）。
- この feature の **「OTel metric」= auditable event + native finding** であり、in-module meter ではない。製品 telemetry は
  finding として bus を流れて observability に入る。新しい meter は cross-cutting architecture change であり scope 外。

## 検討した代替案

- **`max_tokens` の most-specific composition**（qualitative field と統一）: 却下。more-specific scope が増やせる数値
  ceiling は ceiling ではなく、目的を損なう。deployment が異議を持つ場合に容易に reversible なままとする。
- **context/group telemetry 専用の in-module meter:** cross-cutting architecture change として却下。audit-events +
  bus-findings path がすでに signal を運ぶ。
- **member fan-out なしで group の per-subject spend row をすべて合計:** over-count と mis-attribution になるため却下。
  authenticated membership 上の fan-out が正しく安全な attribution である。

## 帰結

- context-policy は死んだ metadata から、retrieval、proxy、session runtime における**生きた decision**へ移行する。
- group ごとの **window** ceiling は **hard かつ MIN-composed**。group ごとの **spend** ceiling は
  **preventive かつ正直に approximate**で、opt-in `fail_closed` を持つ。
- **登録済み debt、half-wired はなし:** E3-full（session inference を governed identity 経由へ再ルーティング）、FinOps による
  detective group-spend backstop + local degradation counter、principal（`user` / `user_group`）を launch gate へ渡すこと。
  すべて文書化された follow-up である。
