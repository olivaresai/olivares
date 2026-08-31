> 機械翻訳です。正式な情報源は英語版です。

# ADR-0022: subject 軸（session / agent / user / user-group / role）による source scoping、row-level effect、および versioned・dual-controlled な enforcement posture

- **Status:** accepted
- **Date:** 2026-07-07
- **Deciders:** Fran Olivares
- **References:** ADR-0003 (RRW map — permitted vs observed), ADR-0019 (Cedar scoped grants).

## 背景と課題

source binding（`modules/sourcescope`）は、接続された source（MCP server、model、provider、
knowledge base、data source）を `workspace`、`agent_group`、`folder` の 3 つの**包含** scope-tree の
厳密に 1 つへ bind する（`schema.go:52-62`, `binding.go:33`）。これは「この scope **内の** actor は
この source に到達できる」と答える。

製品ビジョンには、包含モデルでは自然に表現できない 4 つの追加軸が必要である。

- **「この SESSION が source X を見る」** — 実行中の単一 session。
- **「この USER / user group が source Y にアクセスする」** — 指定された human と directory group。
- **「この特定の AGENT（その group ではない）が Z だけを見る」** — 所属する agent-group ではなく 1 agent。

現在、これらの軸は raw Cedar grant の作成でしか*近似*できない。binding の操作性、listable/auditable な
row、access-map projection がなく、逆方向の問い「subject S はどの source に到達できるか」には未解決の
reverse-query problem がある（`accessmap.go:44`）。一方、**model** governance はすでに豊富な SUBJECT
model、すなわち `subject_kind ∈ {user, role, agent_group}` と allow/forbid row、および
`forbid-overrides-allow` algebra を備える（`modelgovernance.go:98-100`, `modelaccessgate.go:204`）。
ガバナンスには非対称性がある。**model は subject によって豊富に統治され、source は containment によって
狭く統治される。** この決定はその差を解消する。

第 2 の要件は incumbent 分析から生じる（vendor docs に対して 2026-07-07 に検証）。AWS Q Business は ACL
の*緩和*を専用の一方向かつ監査対象の IAM operation（`qbusiness:DisableAclOnDataSource`）にし、Google の
data-store ACL posture は**作成後に変更不能**である。私たちの差別化要因は**変更可能で、versioned かつ
audited**な posture だが、その緩和は**privileged、dual-control、audited** operation でなければならず、
silent toggle であってはならない。incumbent は per-agent または per-session source scoping を表現しない。
これは仮説ではなく、検証済みの white space である。

## 意思決定の要因

- **model-access との一貫性。** 同じ subject vocabulary と `forbid-overrides-allow` algebra を用い、operator が
  「誰が source に到達できるか」を「誰が model を使えるか」と同じ方法で理解できるようにする。
- **hot-path cost。** resolver は models の EXECUTE path（`ScopeGate`）と knowledge retrieval path
  （`RetrievalScopeGate`）で動作する。identity axis が resolve ごとの policy round-trip を追加してはならない。
- **監査可能性と reverse-query。** 「session S / user U / group G に scoped されたすべての source を列挙」は、
  Cedar reverse-walk（未解決）ではなく、単一の indexed query でなければならない。
- **UI。** console（follow-up）が描画・作成できる単一の binding shape。
- **後方互換性と security。** 新しい binding のない deployment は以前とまったく同じ決定を行う。identity axis は
  可能な限り caller-declared string ではなく**authenticated principal**に bind する。attack surface を小さく保つ
  ため、control plane に第 2 の authorization engine を追加してはならない。

## 決定の結果

**既存の source binding をその場で拡張する（候補 A1）。subject scope-tree と row-level `effect` を追加し、
`sourcescope` に subject-scoped allow/forbid algebra を持たせて自身の table 上で model-access を模倣する一方、
containment model と cross-scope Cedar override はそのまま残す。** 新しい軸のために raw Cedar を作成せず
（候補 B）、並列の model_access-twin decision plane も立てない（候補 C）。1 つの control plane、1 つの query
surface、authorization を正しくする 1 つの場所とする。

### 1. 既存の containment tree と統一された 5 つの新しい subject scope-tree

`scope_tree` を `{workspace, agent_group, folder}` から次も含むよう拡張する。

| tree | `scope_ref` | 一致する条件 | identity source | 偽装可能か |
|---|---|---|---|---|
| `session` | session `external_id` | acting session == ref | session-aware caller ref、agent-identity-hardened | route-gated（§4 参照） |
| `agent` | agent `external_id` | acting agent == ref | `principal.AgentIdentity` ∨ session's agent ∨ agent ref | route-gated / authenticated |
| `user` | user id | `principal.UserID` == ref | **authenticated principal** | いいえ |
| `user_group` | `UserGroup.ID` | ref ∈ `principal.GroupsIn(tenant)` | **authenticated principal**（directory-group-gated nested closure） | いいえ |
| `role` | tenant role name | `principal.RoleIn(tenant)` == ref | **authenticated principal** | いいえ |

`user_group` は**directory group**であり、`principal.GroupsIn(tenant)` に対して **group id** で照合する。
これは authenticated principal 上ですでに伝播し、nested-ancestor closure 全体を折り込んでいる
（`principal.go:67-77,151-164`）。resolve ごとの group read は追加しない。`UserGroup` には slug がない
（`model/auth.go:122`）ため、id が stable identifier である。`role` は**完全な model-access parity** のために
追加する（Fran Olivares, 2026-07-07）。tenant role による source governance は、model-access も公開する
粗粒度の「user group」lever である。

identity-of-one の 3 軸（`session`, `agent`, `user`）は退化した containment（equality）であり、
`user_group` と `role` は真の membership である。すべて actor 上の統一された**scope predicate** として評価し、
新しい decision engine は導入しない。

**validation は containment-vs-subject dichotomy に従う（検証済み制約）。** module の write handler は
business-tenant `store.Scope` を保持する。auth subject（`model.User`, `model.UserGroup`, role）は
`store.AuthScope`（system tenant）にあり、そこから**到達できない**（`core/store/store.go` vs `auth.go:24-36`）。
したがって:

- **Containment tree** `workspace` / `agent_group` / `folder` **および** store-resident subject tree は現在と同様、
  bind 時に存在を検証する（deny-closed、「dangling scope なし」）。ただし 1 つの統一ルールを保ち、ephemeral
  session より先に source を bind できるよう、この決定では**5 つすべての subject tree を authoring 時には
  shape-only** とする。正しい kind の空でない `scope_ref` を要求し、store lookup はしない。
- correctness は存在検証に依存しない。未知の subject ref は authenticated actor と一致しないため
  ⇒ deny-closed となり、これは**まさに model-access pattern**である（`modelaccessgate.go` は subject の
  *shape* だけを検証し、`validateGrantRefs` は store-resident TARGET だけを検査する）。typo 防止は console
  （directory/agent picker から author）の役割であり、binding layer の役割ではない。containment tree は既存の
  existence validation を維持する。

### 2. row-level `effect`（allow | forbid）と**絶対的な** forbid-overrides-allow

各 binding は `effect ∈ {allow (default; empty stored value = allow), forbid}` を持つ
（model-access の `normalizeEffect` と同じ慣例）。1 つの `(actor, source)` に対する resolver algebra は次になる。

```
1. If ANY enabled binding matching the actor has effect=forbid  → DENY   (absolute)
   — OR the cross-scope Cedar engine returns EffectForbid for a resource-anchored (workspace/folder) binding.
2. Else, if the source is UNCONFINED (no enabled ALLOW binding at all) → ALLOW   (global / back-compat),
   subject to the per-workspace connector-assignment gate for unbound connectors.
3. Else (confined), ALLOW iff the actor matches an ALLOW binding (its tree's containment),
   OR a cross-scope Cedar EffectGrant, OR tenant RBAC soft-isolation;
   the credential is taken from the MOST-SPECIFIC matching ALLOW (§3). Otherwise DENY-CLOSED.
```

**挙動変更、文書化済み（ADR-0019 が自身の変更を文書化したのと同様）。** 現在の source binding の forbid は
*binding ごと*である。ある binding の cross-scope `EffectForbid` は `continue` され、*別の* binding が allow
できる（`resolver.go:243-248`）。この決定では**すべての** forbid（row-level `effect=forbid` と cross-scope
`EffectForbid` の両方）を**絶対的**にする。一致する forbid は source を拒否し、containment、cross-scope grant、
**および** tenant RBAC を上書きする。これは model-access（`modelaccessgate.go:204`）および Cedar core
（`EffectForbid` は「OVERRIDES everything」、`authorizer.go:101`）と同じ algebra である。方向は厳密に安全側
（forbid は拒否しかできない）で、既存の single-binding forbid test は regression しない。変更が見えるのは
従来未規定だった multi-binding case だけである。

**Confinement trigger。** source は enabled **allow** binding を 1 つ以上持つ場合に限り *confined* である。
既存 binding はすべて allow なので、現在の「bound ⇔ has bindings」と同一である。**forbid だけ**を持つ source は、
その forbid が指名する subject を除き global のままになる。model-access の「特定 subject を制限する」posture が
source にも利用可能になる。connector-assignment gate は「binding なし」ではなく「allow binding なし」をキーに
するため、forbid-only source も connector assignment を尊重する。

### 3. precedence: forbid は絶対、credential は最も specific な allow から

forbid は絶対（§2）なので、precedence は allow-vs-deny を決めず、複数の allow binding が一致したときに
permitted actor が受け取る**credential**を決める。most-specific → least の順序は次の通り。

```
session > agent > user > user_group > role > agent_group > folder > workspace
```

identity-of-one、directory group、RBAC role、acting agent の group、resource containment の順である。
この total order により credential selection は決定的になり（`loadEnabledBindings` の lexical sort を置換）、
文書化された `session > agent > group > workspace` precedence を 5 軸向けに精緻化する。

### 4. 軸の可用性は enforcement point ごとであり、その差を正直に示す

resolver には異なる actor context を持つ 2 つの entrypoint がある。

| axis | `ResolveForSession` (models `ScopeGate`, runtime) | `ResolveForAgent` (knowledge `RetrievalScopeGate`) |
|---|---|---|
| session | ✅ acting session ref | ❌ context に session なし → 一致しない |
| agent | ✅ session's agent (agent-identity override) | ✅ agent ref (agent-identity override) |
| user / user_group / role | ✅ authenticated principal | ✅ authenticated principal |
| workspace / agent_group / folder | ✅ (existing) | ✅ (existing) |

knowledge base 上の `session` binding は session が存在しないため agent-only retrieval path では**強制されない**。
黙って「許可」されるのではなく、単にその actor の scope ではない。同じ source の別の binding/axis は引き続き
適用される。この非対称性は隠さず contract に記載する。`session`/`agent` axis は route-gated のままで、
caller-influenced ref は agent-identity check により強化される（`principal.AgentIdentity` が caller-declared ref を
上書きする）。`user`/`user_group`/`role` は**偽装不能な authenticated principal**に bind するため、より強い軸である。

### 5. enforcement posture は mutable、versioned、audited で、緩和は dual-controlled

source の *posture* は enabled binding とその effect の集合である。Fran Olivares（2026-07-07、
「robust without duplication」）により、**governance の `revision.go` と `approvals.go` は module-internal で、
`sourcescope` から再利用できない**（検証済み: unexported helper、独自 entity、REST approval flow）。それらを
`sourcescope` に fork すれば技術的負債が重複するため、posture control は**自己完結**させ、すでに存在する単一の
shared immutable primitive、すなわち audit ledger を再利用する。

- **audit chain による監査と versioning。** すべての posture mutation は、append-only、hash-chained audit ledger
  （ADR-0009）に posture **delta** を記録する。create/update/delete の `sourcescope.binding.*`
  （`auditBinding` を `effect` で拡張）と dual-control lifecycle の
  `sourcescope.posture.{propose,approve,reject}` である。ledger が immutable で sequenced な version history そのもの
  である。専用 numbered-revision *table with rollback* は意図的に追加しない（`governance/revision.go` の重複になる）。
  pending/decided **posture-request** row は、すべての*緩和*（誰が提案し、誰が承認したか）の first-class で queryable
  な record である。
- **緩和方向のみ dual-control、自己完結。** source に到達できる者を**広げ得る** mutation は *relaxation* である。
  actor は適用せず、pending `sourcescope_posture_request` として記録し、**2 人目の別の** principal が承認した場合のみ
  適用する（`proposer != approver` check が two-person integrity）。approver は admin-tier
  `sourcescope:posture:admin` permission を持つ（editor-tier proposer からの separation of duty）。

  > **Status 修正、2026-08-07。** 以下の列挙は修正済みである。当初の記述は *allow の broadening* と
  > *allow の移動* を挙げるだけで、`forbid` に対する scope 操作を**一つも**挙げておらず、さらに
  > 「より specific な tree への narrowing」を **effect で限定せずに** 単独 actor の通常 write に分類していた。
  > code はそれを忠実に実装していたため、enabled な `forbid` のまま覆う population だけを変える書き込みは
  > 単独 actor によって即座に適用された——同じ forbid の DELETE は 2 人を要したにもかかわらず。
  > 二人制の gate は、削除ではなく編集することで迂回できた。classifier を whitelist へ反転させたところ、
  > 同じクラスの漏れがさらに 3 つ露見した。「より specific な」tree へ移動した `allow`、
  > **最後の** enabled `allow` を `forbid` に変える書き込み、および **既に** confined な source への
  > `allow` の作成（作成は何によっても分類されていなかった）である。
  > この項目冒頭の一般規則は一度も変わっておらず、それがこの訂正を authorize する——列挙は、
  > 自らが精密化すると称した規則より常に狭かった。

  **classifier は WHITELIST である。** access を広げ得ないと証明できる write のみを列挙し、
  **それ以外はすべて——認識できない形も含めて——relaxation として扱う。** 緩和的な形の blacklist は
  構造上必ず漏れる。実際にこれは 4 箇所で漏れた。3 つは既存 binding への編集——scope を縮める `forbid`、
  「より specific な」tree へ移動した `allow`、そして `forbid` に変えられた**最後の** enabled `allow`——であり、
  4 つ目は何によっても分類されていなかった作成である。最初の 2 つは scope 操作を `allow` の極性で読んだことに、
  3 つ目は行の EFFECT だけを読み、その同じ行が担っていた CONFINEMENT を忘れたことに由来する。
  source は enabled な `allow` を持つ間だけ confined なので、「この行はもう拒否しかできない」と読める write が、
  同時に source を global にする write でもある。

  **`forbid` はあらゆる scope 操作の極性を反転させる。これが罠である。** `allow` では scope が小さいほど
  到達する actor が減る（tightening）。`forbid` では**守る** actor が減る——覆わなくなった者は全員、
  その 1 回の write で拒否を解かれる。

  **2 つの scope は、同一の scope であるときにのみ比較可能である。** `specificityRank`（`resolver.go`）は
  一致する allow binding の中から **CREDENTIAL を選ぶために tree を順序付ける**ものであり、
  **包含関係ではない**。決して包含関係として使ってはならない。`role:admin` と `user_group:g1`、
  `workspace:eng` と `agent_group:core`、folder とその子は異なる POPULATION であり、どちらも他方を含まない——
  folder binding に至っては包含の次元を一切持たない（cross-scope Cedar grant に乗る）。membership も固定ではない:
  今日 row を読んで証明した superset は、明日には superset ではない。したがって
  「この write は access を広げ得ない」ことの証明書は **scope の同一性であって、それより弱いものではない**。
  「この 2 つの scope を比較できない」は *relaxation* に解決する——false positive は承認 1 回の超過で済むが、
  false negative は二人制 gate の迂回である。

  **relaxation** は正確には（`classifyCreate`/`classifyUpdate`/`classifyDelete`）、enabled **forbid** の
  delete/disable、`forbid→allow` flip、**enabled forbid に対するあらゆる scope 変更**
  （その population の一部の拒否を解く）、allow の**有効化**、**最後の** enabled allow の disable/delete
  （source を unconfine → global）、**enabled allow に対するあらゆる scope 変更**——広い・狭い・横移動を問わず——、
  **既に confined な source への allow の作成**（到達できなかった population への grant）、および専用の一方向
  **`POST /sources/disable-scoping`** operation（AWS `qbusiness:DisableAclOnDataSource` の mirror）である。

  **tightening / neutral** mutation は通常の single-actor write であり、audited だが gated ではない:
  **forbid** の追加、`allow→forbid`、unconfined な source への **最初の** enabled allow の作成
  （source を governance 下に置く——module 最大の tightening であり、安全な操作が高くつかないよう意図的に
  gate しない）、**disabled** な row の作成、parked な **forbid** の有効化、**最後ではない** allow の
  delete/disable、そして effect・enabled・scope に触れない note/credential edit
  （credential locator は既に authorize された actor が受け取る参照を選ぶだけで、authorize されるか否かは決めない）。
  前後とも disabled な row は何も enforce しないため、そこへの write はすべて neutral である。

  この非対称性は AWS（緩和が privileged op）と一致し、Google の immutable posture を上回る。私たちは mutable かつ
  governed である。endpoint: relaxation の create/update/delete は既存の `POST /bindings` および
  `PUT`/`DELETE /bindings/{id}` から PROPOSE される
  （pending request とともに `202` を返す）。`POST /posture-requests/{id}/{approve,reject}` で決定し、
  `GET /posture-requests` が reviewer queue である。

### 6. access-map は新しい origin を project する（ADR-0003）

`publishBindingEdges` は RRW map の permitted side を project する。`EdgeObservation` はすでに
`OriginKind ∈ {agent, session, identity}` を support する（`sdk/model/observation.go:55`）ので、3 つの
identity-of-one axis はそれぞれ ONE edge を project する。`session` binding → `session`-origin edge
（per-session binding は**その** session の edge として現れる）、`agent` → `agent`-origin edge、`user` →
`identity`-origin edge である。GROUP subject axis（`user_group`, `role`）の edge projection には MEMBER の列挙が
必要だが、member は module の tenant `store.Scope` から到達できない auth-scope entity（directory group、user）
である。したがって folder binding の reverse-grant projection（reverse-query defer）と同様に **DEFER** し、log して
何も project しない。forbid binding は何も project しない（forbid は permitted edge ではない）。enforcement は
常に live principal に対する resolver の live decision であり、map は best-effort drift observability である。
deferred/absent edge が enforcement を弱めることはない。

## 帰結

- **良い点:** 4 つの vision axis（`role` を含めれば 5 つ）は表現可能で、両方の実 PEP で deny-closed に
  強制され、scope resolution と access-map に表示される。console のための auditable/listable な単一 binding shape。
  identity axis は authenticated principal に bind し（偽装不能）、第 2 authorization engine はない（小さな attack
  surface）。hot path は安価な membership check 1 回と identity axis のための新しい policy round-trip **zero**。
  AWS（一方向）および Google（immutable）に対する検証済み差別化要因である mutable-yet-governed posture。
- **悪い点 / トレードオフ:** `scope_tree` は「containment scope」と「subject identity」の両方の semantics を持つ
  （mitigation: contract は両者を統一された *scope predicate* として構成）。posture/dual-control machinery は、
  minimal deployment が relaxation を作成するまで使わない実際の surface を追加する。forbid を absolute にするのは
  文書化された挙動変更（安全側）。
- **中立:** `role` は既存の tenant-RBAC soft-isolation bypass（`rbacAllows`）と概念的に重なる。両者は compose する
  （`role` binding は positive scope、RBAC bypass は tenant-operator visibility rule）し、forbid は**両方**を上書きする。

## 代替案を却下した理由

- **(B) 新しい軸向けの Cedar policy を生成する high-level API。** 却下。(1) consistency target である model-access は
  Cedar を生成せず自身の row 上で決定する（`modelaccessgate.go:11-14`）のに、これだけが raw Cedar を author する
  plane になる。(2) hot path の resolve ごとに Cedar round-trip を支払う。(3) console が必要とする逆方向の問い
  （「subject S が到達できる source はどれか」）は未解決の Cedar reverse-query で、UI と access-map が block または
  approximate になる。(4) 「誰が何を scope したか」の監査が row ではなく policy text の読解になる。
- **(C) 既存 containment binding と compose する source-subject grant 用の別 model_access-twin table。** robustness を
  *低下*させる over-engineering として却下。2 つの decision plane をすべての PEP で compose し、一貫させる必要が
  あり、security drift の典型的原因となる（一方だけ更新、ambiguous cross-plane precedence）。「最も complete/
  enterprise」は plumbing の重複ではなく、**1 plane の depth**（全 axis + effect + versioned dual-controlled posture +
  full test matrix）で達成する。uniform algebra を持つ単一 control plane は監査しやすく（「source X を統治するすべて」
  = one query）、correctness を証明しやすい。
- **local enum ではなく custom-role scopeSpec vocabulary を拡張する。** 却下。`sourcescope` の `scope_tree` は
  custom-role catalog を*模倣*するだけの module-local constant である（`schema.go:49`）。shared catalog を広げると、
  source axis が custom role の target 可能範囲へ漏れる。新しい tree は `sourcescope` local のままにする。
