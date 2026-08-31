> 機械翻訳です。正式な情報源は英語版です。

# ADR-0028: マネージドクラウドの database — managed PostgreSQL と tenant boundary としての row-level security

- **Status:** accepted (managed cloud; this record creates no infrastructure)
- **Date:** 2026-08-02
- **Deciders:** Fran Olivares
- **References:** ADR-0005 (SQLite by default, PostgreSQL at scale), ADR-0027
  (managed-cloud ingress), ADR-0029 (managed-cloud regions), ADR-0022 (source-scoping
  subject axes); the platform decision record for the managed cloud; PostgreSQL
  documentation on row security policies and the AWS database guidance on multi-tenant
  isolation with row-level security, consulted 2026-08-02:
  `https://aws.amazon.com/blogs/database/multi-tenant-data-isolation-with-postgresql-row-level-security/`.

## 背景と課題

ADR-0005 によって、すでに scale 時の製品基盤は PostgreSQL となっており、製品には tenant scoping のための
row-level-security machinery もすでに備わっている。マネージドクラウドに新しい data model は必要ない。必要なのは、
**誰が database を運用するのか**、そして **tenant の row を別の tenant から隔離するために、正確には何に依存する
のか**についての決定である。

後半は前半より重要である。role が policy の実際の適用対象になるよう構成されるまで、「row-level security を使う」
ことは property ではない。PostgreSQL は 2 種類の caller を table policy の適用対象から除外する。superuser と、
`BYPASSRLS` attribute を持つ role である。さらに default では、**table owner にはその table の policy がまったく
適用されない**。ただし、その table を `FORCE ROW LEVEL SECURITY` で alter した場合はこの限りではない。
したがって schema を作成した role で接続する application には、tenant isolation があるように見えても、実際には
*まったく存在しない*。これはこの設計で起こしうる最も高くつく誤りであり、しかも気付かれない。

## 意思決定の要因

- tenant isolation は、将来のすべての query を慎重に書くことに頼るのではなく、**database によって** enforce
  されなければならない。
- 1 人だけの operator が PostgreSQL を運用すべきではない。patching、failover、point-in-time recovery は、
  マネージド offering がまさに取り除くために存在する作業である。
- recovery は platform の property でなければならず、誰かが実行を忘れずにいる必要のある runbook の property
  であってはならない。
- isolation について主張する内容は何であれ、**application の外部から test 可能**でなければならない。

## 検討した選択肢

- **A — virtual machine 上の self-managed PostgreSQL。** 完全な control と最も低い unit cost を得る代わりに、
  upgrade、failover drill、backup verification のすべてを我々が担う。
- **B — cloud provider の managed PostgreSQL service、multi-AZ。** automated backup と point-in-time
  recovery を備える。
- **C — provider の PostgreSQL-compatible cluster service。** shared-storage architecture で、standard
  configuration では request ごとの I/O billing を行う。
- **D — third-party PostgreSQL platform。** 同じ region から到達可能なもの。

## 決定の結果

選択した選択肢は **B — managed PostgreSQL、multi-AZ** であり、row-level security を tenant boundary とする。
以下の role layout は implementation detail ではなく、この決定の一部として扱う。

role layout は規範的である。

1. application は、tenant-scoped table を **所有せず**、**`BYPASSRLS` も保持しない** role として接続する。
2. すべての tenant-scoped table に **`FORCE ROW LEVEL SECURITY`** を設定し、ownership だけでは policy を
   bypass できないようにする。これは、将来の migration が table owner を変更した場合に備えるものである。
3. migration に使う administrative role は、application の connection string に指定する role とは別にする。
4. **scope を明示し、決して仮定にしない:** この record が規定するのは **tenant data plane**、すなわち
   tenant-owned row を保持する schema である。そこでは engine がすでに `ENABLE ROW LEVEL SECURITY`、
   `FORCE ROW LEVEL SECURITY`、および session setting に紐付く tenant ごとの policy を出力する。managed
   plane **自身の control metadata**（tenant registry、billing ledger、usage snapshot）は、**別の posture を持つ
   別 schema** である。現在は、単一の application role を使い、tenant-facing SQL を持たず、
   application-level scoping に依存している。これは control metadata にとって正しい答えである可能性は十分にある。
   しかし現時点では **継承されたものであり、決定されたものではない**。また、読者が
   「row-level security を使用している」という表現から受け取る意味とも異なる。managed plane を構築する者は、
   有料顧客の record をその schema に保持する前に、**その schema がどの posture を採るのか、またその理由を
   文書で明記しなければならない**。

### 帰結

- **良い点:** patching、multi-AZ failover、automated backup、point-in-time recovery が platform の
  property になる。製品が提供する disaster-recovery runbook は self-hosted deployment 用の artefact として残るが、
  managed plane における日々の operational duty ではなくなる。
- **良い点:** isolation は外部から test 可能になる。acceptance criterion は、**application role として**実行した
  query で別 tenant の row を読み取ろうとしても 1 行も返されないことであり、design document 内の assertion
  ではない。
- **悪い点 / トレードオフ:** 通常の virtual machine より固定月額が高くなり、engine-version upgrade は我々ではなく
  provider の calendar に従って到来する。
- **中立:** managed service の administrative role は privileged database role であり、PostgreSQL superuser
  では **ない**。operating-system access を持たず、host authentication configuration を書き換えることもできない。
  これは blast radius を縮小するうえで有用だが、row-level security を成立させるものではない。それを成立させるのは
  上記の role layout である。
- **明示的に未検証であり、仮定してはならない:** 稼働中の engine で、その administrative role が `BYPASSRLS` を
  保持するかどうか。これは実 instance に対して 1 つの query
  （`SELECT rolbypassrls FROM pg_roles WHERE rolname = current_user;`）で確認でき、最初に instance を作成する
  phase で実施する。それが実行されるまで、administrative role が tenant policy の適用対象になると、いかなる
  document にも記載してはならない。

## 代替案を却下した理由

- **A（self-managed PostgreSQL）** — managed plane が吸収するために存在する operational load を、1 人の operator に
  集中させてそのまま引き戻すため却下した。version upgrade、failover rehearsal、そして誰かが定期的に restore して
  初めて実効性を持つ backup verification を担うことになる。コスト上の優位は現実のものだが、絶対額では小さい。
  一方、operational exposure は決して小さくない。
- **C（PostgreSQL-compatible cluster service）** — 時期尚早であるため却下した。workload は write rate が穏当な
  小規模 transactional schema である。shared-storage architecture は、この workload にはない scaling problem を、
  より高い固定費と standard configuration での request ごとの I/O billing を伴って解決する。write rate が将来
  それを正当化するなら、自然な upgrade path であることに変わりはない。
- **D（third-party PostgreSQL platform）** — primary store としては却下した。row-level security の behaviour、
  superuser model、利用可能な role attribute は vendor ごとに異なり、上記の isolation property に照らしてそれぞれ
  再検証しなければならない。絶対に失敗してはならないこの 1 つの boundary について、vendor 固有の risk を負う理由は
  ない。
