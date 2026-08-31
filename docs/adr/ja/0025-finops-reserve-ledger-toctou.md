> 機械翻訳です。正式な情報源は英語版です。

# ADR-0025: FinOps の reserve→commit/release ledger が budget/spend-limit の TOCTOU を閉じる

- **Status:** accepted
- **Date:** 2026-07-17
- **Deciders:** Fran Olivares
- **References:** ADR-0023 (per-group window and spend ceilings — its FinOps budget dimensions are what this reservation ledger admits against), ADR-0001 (store abstraction — SQLite + Postgres, one descriptor), ADR-0009 (append-only hash-chained audit).

## 背景と課題

`finops.CheckBudget` と `finops.CheckSpendLimit` は read-only の pre-flight admission check である。cost read-model を
集計し、「この request は、それを scope する enforcing な budget / limit の範囲内か」に答える。その答えと、実際の
spend が書き戻される瞬間（connector の `CostSampled` → `onCost` ingest）との間には window がある。**N 個の並行
request が同じ pre-spend state を読み、すべて pass し、まとめて limit を突破する** — check→act（TOCTOU）の
double-spend である。以前の fail-closed hardening pass は `Truncated` degradation と availability posture を閉じたが、
race 自体は開いたままだった。

正しい fix は「ceiling を check し、headroom を consume する」を **atomic** にしなければならず、しかも単一 process
内だけでなく **Postgres 上の replica をまたいで** atomic でなければならない。したがって process-level mutex は
許容できない。

## 意思決定の要因

- **ceiling は settlement ではなく admission で consume しなければならない。** N 個の並行 request がすべて pass する
  ことを防げる唯一の方法は、各 admission が次の read より前に自身の headroom を durable に減算することである。
- **store をまたいで 1 つの contract。** 同じ mechanism が SQLite（embedded、single writer）でも Postgres HA
  （複数 connection、READ COMMITTED）でも成立しなければならない。store 自身の atomicity primitive を使い、
  in-memory lock は決して使わない。
- **実際の cost は a-posteriori にしか分からない。** output token（したがって cost）は call の前には不明である。
  admission は *estimate* を reserve し、completion で reconcile しなければならない。
- **正直な expiry。** crash した caller が headroom を永遠に保持してはならず、その回収が double-count を起こしては
  ならない。
- **新しい schema engine は作らない。** module の `ExtensionRegistry` descriptor と generic repo の optimistic
  concurrency を再利用する。

## 決定の結果

reserve→commit/release lifecycle を持つ **dynamic reserve ledger**（`finops.budget_reservation`、table
`finops_budget_reservation`）である。`ReserveBudget` / `ReserveSpendLimit` は request を scope するすべての enforcing
policy に対して estimate を atomic に reserve し、`CommitReservation` が actual cost で settle し、
`ReleaseReservation` が失敗時に headroom を返す。あらゆる箇所の ceiling（`CheckBudget`、`budgetStatus`、
`evaluateBudgets`）は、これで `committed_spend + static ReservedMicroUSD + Σ(active, unexpired reservations)` になる。

これは既存の **static** な `budgetSpec.ReservedMicroUSD`（limit に算入される Priority-Tier の capacity commitment）
とは **別物である**。両者は `effective` に合算され、この ADR は *dynamic かつ request ごと* の項を追加する。

### 1. atomicity: UNIQUE index の下で scope ごとに monotonic な `seq`（process lock なし）

各 reservation は **(policy, period_start, scope_key)** ごとに monotonic な `seq` を持ち、UNIQUE index
`finops_budget_reservation_seq_uniq (tenant_id, policy_ref, period_start, dim_key, seq)` の下に置かれる。reserve は
`max(seq)` を読み、現在の spend と active な reservation を読み、余地があれば `seq = max+1` で `INSERT` する。

- 2 つの並行 reserver は **同じ** 次の `seq` を計算する。UNIQUE index は厳密に **1 つ** の `INSERT` だけを commit
  させ、もう一方を `store.ErrConflict`（`mapWriteErr`）に写像する。敗者は **transaction 全体を retry** し、commit
  済みになった state を読み直す。これにより reserve-check-insert は **process lock を一切使わずに** serialize される。
- **SQLite:** `MaxOpenConns=1` がすでに single writer 上ですべての transaction を serialize するため、reserve はそれ
  自体で atomic である。seq index は belt-and-suspenders の backstop である。
- **Postgres READ COMMITTED（load-bearing な case）:** 別々の connection は互いの uncommitted row を見ないため、
  retry を強制するのは seq collision である。**順序の invariant:** reserve は reserved sum **より先に** `max(seq)` を
  読み、*その* seq で insert する。したがって insert が成功した（collision がなかった）ことは、読んだ seq が真の
  committed max だったことを証明し、ゆえに（厳密に後で読まれた）sum はそれ以前のすべての reservation を見ていた
  ことになる。2 つの read を逆順にすると race が再び開く（stale な sum と衝突しない新しい seq の組み合わせは
  over-admit する）。帰納法で証明できる。k 番目に成功した insert はそれ以前の k-1 個の reservation をすべて見て
  いるので、ちょうど `floor(headroom/estimate)` 件が admit される。

複数 policy にまたがる request は、すべての target を **1 つの** transaction で reserve する（all-or-nothing）。
後続 target の denial は先行 insert を rollback し、block は throttle に優先する。

### 2. reservation の粒度 — enforcing policy ごと、scope を key とする

reservation は **request が一致する enforcing policy ごとに 1 row** で、`(policy_ref, period_start, scope_key)` を
key とする。

- **Budgets:** `scope_key` = budget の dimension key（global では `""`）— policy ごとに 1 scope。request が一致する
  17 個の非 group dimension すべてにわたって reserve する（request ごとの一般的な case: model/provider/agent/
  workspace/identity/api_key/…）。
- **seat ごとの spend limit:** `scope_key` = **actor**。したがって org/group policy 由来の cap は、各 seat の headroom
  を **独立に** reserve する — `CheckSpendLimit` の actor ごとの semantics と一致する。
- **group dimension の budget（`user_group`/`agent_group`）はここでは NOT reserved である。** その spend は
  read-model に group column がない `actor`/`agent_ref` 上の member fan-out であり、fan-out の reservation はより
  大きな設計になる。これらは `CheckBudget` の既存 preventive path で引き続き enforce される。（未解決の
  follow-up — 下記参照。）

### 3. estimation — estimate を reserve し、commit で reconcile する

admission は `estimateMicroUSD`（seam の a-priori estimate — 例えば prompt に対する `count_tokens` に `max_tokens` の
output allowance を加えたもの）を reserve する。completion 時に `CommitReservation(handle, actualMicroUSD)` が actual
を刻んで row を `committed` に反転させ、active sum から取り除く。実際の spend は `onCost` 経由で別途着地する。
estimate が **低すぎた** 場合、その 1 request 分だけ budget は `actual − estimate` だけ一時的に超過しうる — 有界で
あり、actual spend が記録されれば自己修正する。**default の estimate policy は製品としての決定である（下記参照）。
mechanism は estimate に依存しない。**

**順序:** actual spend を ingest し、*その後で* reservation を commit する。これにより settlement 中に ceiling が
一時的に under-count することが決してない。

### 4. expiry — predicate であって、decrement では決してない

active-reserved sum は `state = active AND expires_at > now` で filter する。したがって expire した reservation は
**失効した瞬間に計上されなくなる** — decrement すべき counter が存在しないので、**double-counting は構造的に
不可能である**。`SweepExpiredReservations` は observability/GC のために terminal な `expired` state を刻むだけであり、
correctness はその実行に依存しない。TTL（`reservationTTL`、default **5 分**）は、reserve と commit/release の間で
死んだ caller に対する crash backstop である。まだ実行中の request が決して落とされないよう、最も遅い governed
actuation より長くなければならない。

### 帰結

- **良い点:** double-spend は両 engine で atomic に閉じられる。fix は additive である（新しい descriptor table —
  `applyModuleTables` が fresh な DB でも in-place な DB でも作成する。既存の migration には触れない）。
  `CheckBudget`/status/alert は in-flight な reservation を反映するようになり、pre-flight denial、hard-cap signal、
  status DTO が一致する。
- **コスト:** reserve は read-only な check に対して 2 回の write（reserve + settle）である。hot path では小さな
  transaction がいくつか増えるだけで、それが守る inference call に比べれば些細である。
- **配線されるまでは latent:** ledger が効くのは、actuation seam が read-only の `CheckBudget` ではなく（estimate を
  伴う）`ReserveBudget`/`Commit`/`Release` を呼ぶようになってからである。それまで dynamic-reserved は 0 で、挙動は
  変わらない。inference proxy / HITL gate の配線と default estimate の選択が、残る integration である。

## 未解決の問い（製品）

1. **default estimate。** seam が estimate を持たない場合、a-priori estimate は何か。選択肢: model の rate での
   `count_tokens(prompt)` + 設定された `max_tokens` output allowance、request ごとの一律の floor、または model ごとの
   p95 履歴 cost。過小見積もりは guarantee を弱め、過大見積もりは早期に throttle する。
2. **TTL。** 5 分は crash backstop として適切か、それとも model の最大 completion time に追随させるべきか /
   surface ごとにすべきか。
3. **group budget の reservation。** `user_group`/`agent_group` の budget も（member fan-out で）reserve すべきか、
   それとも group ceiling には preventive のみの enforcement で許容できるか。
4. **retry 枯渇時の posture。** `maxReserveRetries`（64）を使い切ると、reserve は（`CheckBudget` の contract に従って）
   **open** に fail する。hard な `block` budget では、極端な contention はむしろ **closed** に fail すべきか。
