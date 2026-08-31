> 機械翻訳です。正式な情報源は英語版です。

# ADR-0026: Cedar scoped grant としての AP2 payment mandate（governed procurement）

- **Status:** proposed (design only; the enterprise build lands in a separate phase)
- **Date:** 2026-07-20
- **Deciders:** Fran Olivares
- **References:** ADR-0019 (Cedar scoped grants), ADR-0022 (source-scoping subject axes),
  ADR-0025 (FinOps reserve→commit/release ledger, TOCTOU-safe), ADR-0009 (append-only
  hash-chained audit); the companion AP2 governed-payment threat-model spec; the AP2 v0.2.0
  specification (github.com/google-agentic-commerce/AP2, verified 2026-07-20).

## 背景と課題

agentic payment は protocol layer として到来しつつある。Google の **AP2（Agent Payments Protocol）** は最も
可視性の高いものの 1 つであり、現行の specification は **v0.2.0（2026-04-28 リリース）** で、同日に FIDO
Alliance へ寄贈された。AP2 では user が署名された **mandate** を shopping agent に委任でき、agent は後にそれを
具体的な transaction へ bind し、**Verifier**（merchant、credential provider、network、payment processor）がそれを
check する。

この決定の形を決める事実が 2 つある。

1. **currency（計測された現実が計画に優先する）。** 以前の計画は AP2 v0.1 に基づき、「verifiable credentials」で
   署名された *Intent / Cart / Payment* という mandate の三つ組を記述していた。その model は **superseded** である。
   v0.2 は mandate type をちょうど **2 つ** — **Checkout Mandate** と **Payment Mandate** — 定義し、それぞれが
   **Open** state（constraint を持ち、user が署名する）と **Closed** state（transaction に bind される。agent が open
   mandate の `cnf` claim にある key に対して Key Binding JWT / Proof-of-Possession を生成する）を取る。mandate は
   **SD-JWT**（RFC 9901）である。**binding hash / Key Binding JWT は non-deterministic な scheme（ES256/ECDSA）を
   使わなければならず（MUST）、deterministic なもの（Ed25519）を使ってはならない（NOT）** — spec はこれが hash
   binding を守るためだと述べている。この ADR は **v0.2** を対象とし、公開された `vct` schema suffix に pin する
   （v0.2 spec に従い `mandate.checkout.1` / `mandate.payment.1`。build 時に spec の `docs/ap2/*` に対して検証する
   こと）。

2. **Olivares が何であり、何でないか。** Olivares は **governance control plane** である。すなわち Policy Decision
   Point（PDP）であり、改ざん検知可能な evidence ledger である。payment processor、PSP、card network、wallet、
   fund custodian では **ない** し、この ADR がそれらにするわけでもない。AP2 自体は **pre-1.0** であり、**採用は
   初期段階で、大部分は願望的である**（PayPal 自身の page は AP2 を分類上言及するだけで、OpenAI の ACP と Google
   の UCP を強調している。Mastercard の「Agent Pay」は別個の program である。「60+ organizations」という数字は
   2025 年 9 月の launch 時点の count である。FIDO の署名者リストは約 12 である）。正直な labelling は、検証可能な
   範囲を超えて AP2 support を主張することを禁じる。

問題はこうである。**Olivares は、すでに持っている primitive を用い、具体的な enterprise use case とともに生まれ、
AP2 が意図的に上位 layer に委ねている gap を埋めながら、authorization の fall-through や silent な constraint
downgrade を導入することなく、AP2 を介した agentic purchase をどう統治するのか。**

この設計が伴って生まれる具体的な use case は **governed procurement agent** である。enterprise は、constraint が
purchasing policy（budget ceiling、許可された supplier、item ごとの limit、recurrence、execution window）を符号化
した AP2 open mandate の下で動作する agent を通じて購買する。Olivares は具体的な purchase ごとにその policy に
照らして authorize し、高額なものは人間へ escalate し、mandate+receipt を否認不可能な evidence として封緘する。

**前提条件（in-path gate）。** 以下のすべての guarantee は、deployment が purchase を **in-path gate としての
Olivares 経由で** route する場合にのみ成立する。agent は closed mandate を settlement layer へ提示する前に、新しい
Olivares authorization を取得しなければならない（MUST）。side/advisory な PDP としての Olivares は、すでに merchant
に渡された closed mandate には、AP2 と同じく手が届かない。build はこの deployment 要件を文書化しなければならない
（MUST）。

## 意思決定の要因

- **既存の authorization plane を再利用し、fork しない** — ただし semantics が実際に一致する箇所に限る（下記の
  Abstain と deny の訂正を参照）。
- **AP2 が明示する gap を我々の layer で埋める**（companion の threat-model spec を参照）。AP2 には **revocation が
  なく**、verifier 側の double-spend rejection を **optional（MAY）** にし、human identity / SCA を証明 **しない** し、
  **clock trust については沈黙**しており、evidence の retention/retrieval と liability を scope 外に置いている。
  「すべての agent を潜在的な攻撃者とみなす」（AP2 自身の threat model）PDP は、これらを mandatory にしなければ
  ならない。
- **model 化できないものには fail closed。** 符号化できない constraint、agent が伏せる disclosure、未知の algorithm
  — いずれも mandate を reject しなければならず、決してそれを広げてはならない。
- **正直な scope と pre-1.0 の risk。** 今は設計し、`vct` に pin し、検証できない主張は出荷せず、Olivares を厳密に
  PDP/evidence 側にとどめる。

## 検討した選択肢

- **選択肢 A — Cedar scoped grant としての AP2 mandate。Olivares は統治する Verifier/PDP。**
  AP2 の **open mandate** を、その 1 つの mandate に bind された authored な **Cedar grant**（ADR-0019）として
  model 化し、その `when` 条件を mandate の constraint とする。**closed mandate** は **authorization request**
  （principal = `cnf` 内の agent key、action = `purchase`/`pay`、resource = payee / checkout）として扱い、
  **payment action については deny-by-default** で評価する。Olivares は PDP として AP2 の verification rule を実行し、
  高額なものは single-use の HITL approval で gate し、FinOps budget（ADR-0025）を fail-closed に reserve し、
  署名された mandate+receipt 全体を evidence として封緘する。
- **選択肢 B — Cedar と並行する専用の AP2 mandate engine。**
- **選択肢 C — 監視のみ。**

## 決定の結果

選択した選択肢は **選択肢 A** である。constraint model が Cedar grant の条件へ写像でき、周辺の control（approval、
reserve ledger、signed audit chain）がすでに存在するからである — **ただし下記の 3 つの semantic な訂正を行うことが
条件**であり、それなしでは再利用は安全ではない。

### 再利用を健全にする 3 つの semantic な訂正

1. **payment action は DENY-BY-DEFAULT であり、abstain が RBAC に委ねるのではない。** scoped-grant engine は、
   一致する permit がないとき（deny ではなく）**`EffectAbstain`** を返す。「grant なし」「expired grant」「tenant に
   scoped grant なし」はいずれも Abstain であり、Abstain は *base の RBAC decision がそのまま成立する* ことを意味
   する（`modules/governance/grants.go:31-38`、RBAC back-compat の invariant）。「一致する mandate がない」を素朴に
   「deny」と同一視するのは **誤り**である。cnf の不一致、expire した mandate、revoke された grant はいずれも
   Abstain となり、**RBAC の allow** へ fall through しうる。訂正: `purchase`/`pay` は、一致する有効な
   mandate-bound grant によって **のみ** authorize され、**RBAC fallback はない**。build は次のいずれかでこれを
   強制しなければならない（MUST）。(i) base authorizer がどの role にも `purchase`/`pay` permit を与えないことを
   証明する（したがって Abstain→deny）、または (ii) payment action 上の Abstain を deny として扱う payment overlay
   を設ける。存在するが無効な mandate は、さらに明示的な **`forbid`** を author する。conformance test は、RBAC
   単独では決して payment を authorize しないことを assert しなければならない（MUST）。

2. **mandate→grant translator は、model 化できない constraint があれば FAILS CLOSED する。** 「unknown constraint
   MUST fail」は **translation-time** の義務であり、Cedar の deny-by-default が与えてくれるものではない。translator
   が符号化できない constraint を黙って省略すると、**user が署名したより広い** grant を生成し、Cedar はその
   constraint を一度も見ていないので allow してしまう。訂正: 認識済みの constraint key、operator、unit の
   **allowlist** に対して translate し、認識できない要素があれば **mandate 全体を reject し、grant を一切 author
   しない**。

3. **full disclosure は mandatory であり、untrusted な agent が constraint を伏せることはできない。** SD-JWT では
   *holder*（untrusted な agent）がどの disclosure を開示するかを選ぶ。pass する disclosure だけを提示し、より
   厳しい constraint を伏せることもできてしまう。訂正: verification adapter は `_sd` digest を列挙し、policy に
   関係する claim の digest が 1 つでも **undisclosed** であれば、それを評価不能な constraint として扱い
   **fail closed** する。

### 対応関係（訂正を適用したもの）

| AP2 v0.2 の概念 | Olivares の primitive（file:line） |
|---|---|
| Open mandate（constraint 付き、user が署名） | その mandate の `jti`/`sd_hash` に bind された Cedar scoped **grant**（`modules/governance/grants.go:67`、ADR-0019） |
| Closed mandate | authorization **request**。**`purchase`/`pay` については deny-by-default** で評価（訂正 1） |
| 「Verification and Processing Rules」 | adapter の chain-verify + full-disclosure check（訂正 3）+ fail-closed な translate（訂正 2）+ PDP decision |
| `payment.budget`（累積） / `amount_range`（transaction ごと） | **mandate ごとの net-new な reservation key** を持つ FinOps reserve ledger（`modules/finops/budgets.go`、`spendlimits.go`、ADR-0025）。mandate cap AND Olivares の全 scope に対して atomic に reserve する（NOT `min()`） |
| `payment.agent_recurrence`（count/velocity） | **net-new** な count/velocity limiter（ADR-0025 の下で TOCTOU-safe）— 既存の amount-based budget ではない（NOT） |
| `allowed_payees` / `allowed_merchants` / `allowed_payment_instruments` | Cedar の set-membership な `when` 条件 |
| `execution_date` {not_before,not_after} | **DDIL の trusted signed dead-man clock**（`modules/governance/ddiladopt.go`）に対する temporal 条件。SD-JWT adapter にも注入する |
| user approval、高額時の gating | **single-use HITL** approval の consume（`modules/governance/approvals.go`） |
| Checkout/Payment Mandate + Receipt（dispute 用の evidence） | `transaction_id` を key とする hash-chained な **runtime audit ledger**（`modules/sessions/runtime_ledger.go`、`sc.Audit().Append`、ADR-0009）— 何が保存されるか（WHAT）は decision 1 を参照 |

### この ADR が行う decision

1. **mandate の表現 — authority と evidence は別々の store である。**
   - **authority** は **Cedar grant**（評価される policy）であり、特定の open mandate の安定した id
     （`jti`/`sd_hash`）に bind される。したがって closed mandate は *その* open mandate から author された grant に
     対してのみ評価されうる（**mandate substitution** を防ぐ。緩い mandate A を持つ agent は、B の closed mandate を
     grant-A に対して評価させることができない）。grant が self-asserted な authority として扱われる raw blob になる
     ことは **決してない**。
   - **evidence** は **署名された artifact 全体** である。open な SD-JWT、closed な Key Binding JWT、そして
     **実際に提示された disclosure** であり、dispute が *AP2 の signature-verification sequence を replay* できるよう
     （暗号化し、access-controlled にして）保持する。hash ではそれができない。この evidence は PII（金額、payee）を
     含むため、**「PII は決して持たない」ではなく、暗号化された minimal-necessary な evidence** である —
     minimal-data rule が適用されるのは *authority/grant* と operational log であって、封緘された dispute record では
     ない。

2. **signature verification — chain を検証し、algorithm を pin し、trust root を分離する。** SD-JWT chain と、`cnf` に
   bind された Key Binding JWT（PoP）経由の open→closed link を verify し、closed mandate が open mandate の claim を
   変更せず保持していることを確認し、すべての constraint を評価する（訂正 2 と 3）。raw な spec が与えない
   hardening rule が 2 つある。
   - **algorithm pinning。** 各 trust-root key を許可された algorithm 集合に bind し、それに対して厳格に verify する。
     **token が広告する `alg` は無視する。** `alg:none`、HS/ES confusion、curve/strength の downgrade を reject する
     — AP2 の Ed25519 禁止は、untrusted な agent が駆動する header 制御の negotiation surface の中にある 1 つの
     狭い rule にすぎない。
   - **分離された trust root。** **User-Credential** root（OpenID4VP）は、*人間が open mandate を authorize した*
     ことを verify する。**Trusted-Agent-Provider** list が統治するのは、どの agent identity が `cnf` key を
     **hold/bind** してよいかだけである。両者は異なる事実を attest しており、**それぞれ自身の義務について両方とも
     必須**である — 交換可能な OR には決してしない（agent-provider の attestation は user の authorization 署名の
     代わりにはならない）。必要な root が欠けていれば deny-closed とする。

3. **expiry、single-use、revocation（Olivares が gate する flow に限定）。** AP2 には **revocation がない**。Olivares
   は **in-path** な deployment についてこれを閉じる。(a) mandate-bound grant は **first-class に revocable** である
   — revoke すると、その mandate に対する *以後の Olivares authorization* はすべて deny-by-default になる（訂正 1）。
   すでに settlement へ release された closed mandate には手が届かない（AP2 と同じ限界 — 正直に明示する）。(b) 高額
   な closed mandate は **single-use approval** を consume するため、approval を replay できない。(c)
   `exp`/`execution_date`/recurrence は **DDIL の trusted signed clock** に対して enforce し、SD-JWT adapter も `now`
   を同じ clock から取るので、2 つの layer が食い違うことはない。

4. **replay / double-spend — verifier 側の de-dup は MANDATORY である（in-path）。** AP2 は anti-double-spend の MUST
   を *shopping agent*（AP2 自身の threat model では攻撃者）に課し、verifier の check は MAY にとどめている。
   Olivares PDP は open mandate ごとに提示された closed mandate の nonce / `transaction_id` を追跡し、重複する提示や
   繰り返しの提示を拒否する — Olivares を経由する authorization について（in-path の前提条件）。

5. **Olivares が行わないこと（NOT）。** fund custody なし、payment execution なし、card/token の issuance なし、
   PSP/network/wallet として振る舞うこともない。Olivares は、policy に照らして agentic purchase を authorize する
   **PDP** であり、mandate/receipt を封緘する **evidence plane** である。settlement は merchant/PSP/network に残る。

### 帰結

- **良い点:** semantics が本当に一致する箇所で Cedar / reserve ledger / approval / audit chain を再利用する。AP2 の
  gap は enforce された guarantee になる。封緘された否認不可能な evidence。正直で検証可能な positioning。
- **悪い点 / トレードオフ:** 再利用は **条件付き** である — payment action の deny-by-default overlay、fail-closed な
  translator、full-disclosure の enforcement、mandate ごとの reservation key、net-new な recurrence limiter が必要で
  ある（いずれも無料ではない）。AP2 は pre-1.0 である（v0.3 が出れば re-map を強いられるが、adapter の背後に隔離
  され `vct` に pin されている）。PII を含む signed evidence の保持は、encryption/retention の義務を追加する。
- **中立 / follow-up:** agent 間の mandate delegation は **AP2 の scope 外** であり → 我々の scope 外でもある。
  x402（crypto-rail の AP2 extension）と ACP（OpenAI/Stripe）は別物として追跡しており、ここでは構築しない。

## 代替案を却下した理由

- **選択肢 B（専用 engine）** — 却下。pre-1.0 の protocol のために reserve ledger / approval / audit の machinery を
  重複させることになる。上記の訂正は、payment action の deny-by-default と fail-closed な translation が入れば
  再利用が健全であることを示している。
- **選択肢 C（監視のみ）** — 却下。ratify された方向性は、今設計し、*public release を block することなく*
  enterprise build を早期に開始することである。監視のみでは、標準が FIDO で固まっていく間に差別化要因（封緘された
  evidence を伴う governed agentic spend）を手放すことになる。honest-labelling の懸念は、今 **design** を出荷し
  **build** は検証された need の背後で gate することによって満たされるのであって、何もしないことによっては
  満たされない。
