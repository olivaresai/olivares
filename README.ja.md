<div align="center">

<a href="https://olivares.ai"><img src=".github/assets/olivares-banner.png" alt="Olivares AI — Ground truth for enterprise AI" width="720"></a>

**言語:** [English](./README.md) · [Español](./README.es.md) · [简体中文](./README.zh.md) · [Русский](./README.ru.md) · **日本語** · [Deutsch](./README.de.md) · [Français](./README.fr.md)

**実際に運用する AI のためのコントロールプレーン。** 統合し、稼働させ、システムに接続し、その全体を統治する — ホームサーバーから規制対象のエンタープライズまで、1 つのセルフホスト型バイナリで。

[Install](#install) ·
[Quickstart](#quickstart) ·
[サンプル](examples/) ·
[Architecture](#architecture) ·
[Documentation](#documentation) ·
[セキュリティ](SECURITY.md) ·
[コントリビュート](CONTRIBUTING.md) ·
[olivares.ai](https://olivares.ai)

[![License: AGPL-3.0-only](https://img.shields.io/badge/license-AGPL--3.0--only-blue)](LICENSING.md)
[![SDK & connectors: Apache-2.0](https://img.shields.io/badge/SDK%20%26%20connectors-Apache--2.0-blue)](LICENSING.md)
[![Status: beta](https://img.shields.io/badge/status-beta-F08000)](CHANGELOG.md)
[![Contributor Covenant](https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa)](CODE_OF_CONDUCT.md)

<!-- OpenSSF Best Practices Badge (self-certification).
     Registration at https://www.bestpractices.dev is pending (a maintainer action); the
     evidence map is in docs/openssf-badge.md. Once a project ID is assigned, uncomment:
[![OpenSSF Best Practices](https://www.bestpractices.dev/projects/PROJECT_ID/badge)](https://www.bestpractices.dev/projects/PROJECT_ID)
-->

</div>

> ステータス: **beta**、活発に開発中です。エンジンはエンドツーエンドで動作します — コンソールを組み込んだ単一の静的バイナリが、AI が稼働するシステムから実際のシグナルを取り込みます。API、スキーマ、モジュールのサーフェスは 1.0 までに変更される可能性があり、一部のアクチュエーション・シーム（宣言済みの deny-closed 統合ポイント）は、プロビジョニングされるまで閉じたままです（[誠実さと限界](docs-site/src/content/docs/start/honesty-and-limits.md) を参照）。リリースはこのリポジトリから作成され、以下の[Install](#install)経路は最初のタグ付きリリースで公開されます。

> サプライチェーン: リリースは GitHub Actions でビルドされ、アーティファクト種別ごとに署名付きトラストチェーンを備えます — archive には SPDX SBOM と in-toto attestation が付き、container image は image SBOM attestation とともに cosign 署名され、すべての artifact（package と chart を含む）は cosign 署名済み checksums manifest でカバーされます。さらにセット全体に OpenVEX document と SLSA build provenance が付きます。任意のリリースは [`scripts/verify-release.sh`](scripts/verify-release.sh) で検証してください。アーティファクト種別ごとの正確なチェーン、air-gapped path、Helm chart は [`docs/RELEASE-VERIFICATION.md`](docs/RELEASE-VERIFICATION.md) と [`deploy/`](deploy/) に文書化されています。

## Olivares AI とは

AI はもう、ひとつのチャットウィンドウではありません。今実際に運用するのは小さな estate です。terminal の coding agent、MCP server、model endpoint、service account、scheduled job が、ひとつのシステムになるよう設計されていない machine に分散しています。それらをまとめるものはなく、だから普通の問いに答えるコストが高くなります — 何が稼働中か、誰が起動したか、何に到達したか、いくらかかったか、そして誰がそれに同意したか。

**Olivares AI はそれらをまとめるプレーンです。** これは 2 つの半分から成り、同じ binary で出荷されます。

- **実行して接続する。** 作業そのもののための durable plane です。owner、dependency、acceptance criteria、decision を持つ work item。ownership を、stale holder が使い続けられない authority にする lease。console から launch、attach、stop でき、live run への input を提供する session。A2A による remote peer への delegation。tool surface としての MCP。そして retrieval を供給する governed content source です。この半分は以下の[The work plane](#the-work-plane)で説明し、各要素の state を明確に示します。
- **可視化して統治する。** 発見されたすべてのものの inventory、各 agent と identity が実際に到達する対象の read/write access map、Cedar policy、deny-closed enforcement、spend を拒否できる budget、そして事後にそのすべてを証明する hash-chained signed ledger です。

どちらの半分も、もう一方の装飾ではありません。work plane のない統治は行動を起こす対象が何もない dashboard であり、統治のない work plane は後から誰も説明責任を果たせない work です。

**設計から multi-provider。** Claude Code は最も深いレベルで統合されています — `PreToolUse`/`PostToolUse` hook、managed settings、console からの launch と stop、subject ごとの model access — その傍らに Codex と Grok Build を first-class command surface として置き、gemini-cli、Cursor、opencode、goose、cline、OpenHands、OpenClaw、Hermes も専用 connector として扱います。それぞれが強制できることと観測しかできないことを明示し、どれも製品の重心ではありません。Ollama と他の self-hosted endpoint は、read-only を設計原則とする local connector を通じて inventory 化され attribution されます。policy と budget の rule は、inference が governed proxy を横断する場所で bind し、そこだけがそれらを bind できる場所です。

**誰が運用するか。** open build はこれらすべての規模で platform 全体です — commercial add-on はその上に追加される additive code であり、決して別製品ではありません:

| あなたは | その姿 |
|---|---|
| **home server または homelab network を運用している** | 1 つの binary、SQLite、Docker volume、loopback-bound、external service なし — 出荷される Compose topology は 1 CPU と 1 GiB の範囲で non-root・read-only で動作します（[`deploy/compose/docker-compose.yml`](deploy/compose/docker-compose.yml)） |
| **フリーランサーまたは独立コンサルタント** | client ごとに 1 tenant — すべてのモジュール操作は 1 つに固定される — 請求書が来る前に deny または throttle できる budget、そして引き渡せる posture export |
| **プロフェッショナルまたは上級ユーザー** | エンタープライズが運用するものと同じ engine を、何も差し引かずに使えます。open build は platform 全体なので、自分の環境で学ぶことが、職場で運用するものそのものです |
| **エンジニアリングチームまたは小規模事業者** | 共有 work item と lease により、2 つの agent — または 2 人 — が同じ work item を同時に保持することはできません。SSO、role、誰も手作業で組み立てる必要のない audit trail |
| **規制対象のエンタープライズ** | row-level security を備えた Postgres、single writer と standby による HA、air-gapped install、**26 framework catalogs**に map された evidence、immutable substrate 上の WORM archive |

どの行も同じ build です。SSO、HA、WORM archive、実際に deny する budget などの capability は、first boot で手に入る default ではなく、provision するものです。下の matrix と [誠実さと限界](docs-site/src/content/docs/start/honesty-and-limits.md) に、capability ごとにどちらなのかが記されています。

これは console を組み込んだ**単一のセルフホスト型 Go バイナリ**として、Linux、Docker、Kubernetes、on-prem、または完全な air-gapped 環境で動作します。mandatory telemetry はなく、既定では control-plane egress もありません。境界を越えるのは、越えるように設定したものだけです — model API への call、配線した SIEM/webhook output、provision した場合の external embedding provider。collector は既に運用する system（pgAudit、CloudTrail、eBPF、MCP、IdP）から読むため、障害が発生した collector であっても production の data path 上に置かれることは決してありません。

coverage と attribution には明示的な tier（`firm`/`approximate`/`unknown`、`clean`/`lossy`/`opaque`）があり、enforcement は配線済みの箇所では deny-closed、そうでない箇所では宣言済み seam です。documentation は今日動作するものと design-stage のものを率直に示します。製品は、証明できない確実性を捏造しません — [誠実さと限界](docs-site/src/content/docs/start/honesty-and-limits.md) を参照してください。

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-dark.png">
  <img src="docs-site/public/console/access-map-light.png" width="840"
       alt="アクセスマップ: 各エージェントがエステート全体で何を読み書きしているか。左に起点、右にアクセス先のリソースを示し、R/RW を色で表します。">
</picture>

<sub><b>アクセスマップ</b> — 各エージェントがエステート全体で何を読み書きしているか。左に起点、右にアクセス先のリソースを示し、R/RW を色で表します。</sub>

</div>

**2 つの command で確認する**（Go 1.26+、[Task](https://taskfile.dev)、pnpm — [prerequisites](#quickstart-prerequisites)）:

```sh
task build
./bin/olivares serve --seed-demo --insecure --listen 127.0.0.1:8901 \
  --grpc-listen 127.0.0.1:8902 --data-dir "$(mktemp -d)"
# open http://127.0.0.1:8901 — inventory, work, orchestration, access map + drift, policies, FinOps
```

CI も同じ path をたどります。`task smoke:quickstart` はこの demo estate を real binary に対して起動し、access-map と drift の count を assert します。install path と operational default は [Install](#install) と [Quickstart](#quickstart) を参照してください。

<a name="the-work-plane"></a>
<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/04-environments-dark.svg">
  <img src=".github/assets/04-environments-light.svg" width="840"
       alt="あらゆる規模で 1 つのバイナリ: ホームサーバーやホームラボ、顧客ごとにテナントを持つフリーランス、エンジニアリングチームや中小企業、そして規制対象の企業。Linux、Docker、Kubernetes、Helm、エアギャップ環境で動作し、ローンチ時にはマネージドクラウドも利用でき、モデルプロバイダー、クラウドとディレクトリ、統制されたコンテンツソース、出力コネクターに到達します。アクセスマップはその中の 1 つの機能であり、中心ではありません。">
</picture>

<sub>ホームラボから規制対象企業まで、同じビルド。</sub>
</div>

## 作業プレーン

作業を担うプレーンは、agent と人が共有する Olivares AI の部分であり、最も頻繁に、あらゆる場所で完成しているかのように説明される部分です。そうではないため、各要素について、実際に何が支え、今日どこまで到達するかを示します。

| 要素 | 状態 | 配置先 |
|---|---|---|
| **ワークアイテム** — brief、provenance、dependency、acceptance criteria、decision、owner、event history を持つ durable item。REST、CLI、in-process caller で共有される 1 つの command document | **live, public API** | [`modules/sessions/work_model.go`](modules/sessions/work_model.go)、route は [`modules/sessions/work_api.go`](modules/sessions/work_api.go) |
| **リース** — ownership を fenced、expiring authority として扱う: acquire、renew、release、take over、revoke。stale holder は行動し続けられず、concurrent acquisition の winner は必ず 1 つ | **live, public API** | [`modules/sessions/work_lease.go`](modules/sessions/work_lease.go) |
| **message、ack、handoff** — replay と stale-epoch rejection を備え、work item に結び付いた durable conversation | **orchestration workflow の背後で live; general public inbox は意図的に未配線** | [`modules/sessions/communication_model.go`](modules/sessions/communication_model.go)。public plane の配線を禁じる boot test は [`cmd/olivares/communicationauthorityboot_test.go`](cmd/olivares/communicationauthorityboot_test.go) |
| **作業のための launch** — reserve し、lease を take し、それから session を spawn し、work/epoch/fence/execution を永続化するため retry は safe | **orchestration を通じて live** | [`modules/sessions/runtime_work_launch.go`](modules/sessions/runtime_work_launch.go) |
| **A2A による remote execution** — durable receipt とともに authorized peer 上の work を plan、test、start、observe、cancel | **live、ただし destination が構成されている場合のみ**; authorized target がなければ seam はまったく mount されない | [`cmd/olivares/wire.go`](cmd/olivares/wire.go)、[`cmd/olivares/orchremote.go`](cmd/olivares/orchremote.go) |
| **shadow mode と final authority** — plane が authoritative になる前に existing system と comparator に対して dual-report | **未実装** | design のみ |

この表を「agent 同士が話す」の正直な説明として読んでください。work item と lease は今日操作できる通常の API surface です。agent 間の会話は実在し durable ですが orchestration workflow に限定され、任意の agent のための general message bus はありません。remote delegation は動作し、unknown peer を拒否します。存在しないものは interface で「近日公開」として載せず、ここで absent とします。

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/03-agent-communication-dark.svg">
  <img src=".github/assets/03-agent-communication-light.svg" width="840"
       alt="エージェントが協調する仕組み: エージェント面が、ワークアイテム、同時に 1 名の保持者だけが動作する囲い込みリース、作業のための起動、ワークスペース単位のメッセージと確認応答からなる永続的なワークプレーンに供給します。委譲は、その適用ゲートを通って認可されたピアに到達します。このプレーンはオーケストレーショングラフ、イベントバス、ドリフト付きのアクセスマップ、そして SIEM へ届く署名済み台帳を出力します。シャドウモードと最終権限は未構築のため破線の枠で描かれています。">
</picture>

<sub>エージェントは 1 つの永続的なワークプレーンを共有します。未構築のものは不在として描かれます。</sub>
</div>

## カバーする範囲

1 つの binary、**30 モジュール**、1 つの console — 単一の feature ではなく AI の footprint 全体にわたります。すべての capability は live、on-demand、observed、または宣言済み deny-closed seam という明示的な maturity state を持ち、[誠実さと限界](docs-site/src/content/docs/start/honesty-and-limits.md) に項目ごとに記載されています。

- **作業を実行する。** [The work plane](#the-work-plane)で説明した durable work item、lease、orchestrated launch、A2A delegation。console の Work view は同じ store の operator surface であり、Orchestration view は observed signal から delegation topology を描きます。
- **可視化する。** **発見された**すべての agent、session、model、MCP server、tool、identity の inventory — coverage は接続したものに従い、明示的な indicator を持ち、見えないものは推測せず `unknown` と mark します。それぞれが実際に到達する対象の read/write **access map** と Permitted-vs-Observed **drift** view、live session、orchestration graph、health、SLA。
- **統治して強制する。** Cedar authorization engine（RBAC + deny-overlay + positive scoped grant）と **4つのdeny-closedエンフォースメントポイント** — Claude Code `PreToolUse`/`PostToolUse` hook、inline `/v1/messages` inference proxy、MCP `tools/call` gate、A2A delegation gate — により unauthorized action は実行されません。block、two-person approval への送付、または実行前の rewrite が行われます。この形容詞は主張ではなく測定結果です。point は test がその*unconfigured* path — wired gate なし、empty policy document、応答しない policy store — を実行して refusal を assert している間だけ数えられます。seam-to-proof census は [`scripts/enforcement-seams.tsv`](scripts/enforcement-seams.tsv) です。proof を削除すれば count は下がり、build は失敗します。policy は session 自体に届きます。hook 内の path 別・subtree 別 allow/ask/deny rule、surface 別・group 別 context-window budget、session、agent、user、group、role までの source scoping。加えて scoped admin と custom role、dual control を備えた break-glass、fail closed する estate **kill-switch** があります。
- **Claude と agent ecosystem。** hook で Claude Code を統治し、console から Claude Code session と workspace を launch、attach、govern、stop します。enterprise managed-settings を配信し、各 subject がどの surface でどの model を使えるかを統治します。MCP（OAuth-gated resource server、posture、registry、`.mcpb`）、authorized peer 間の A2A v1、そしてチームが実際に運用する agent 向け surface — gemini-cli、Cursor、Codex CLI、opencode、goose、cline、OpenHands、OpenClaw、Hermes（surface が公開する場合は enforcement、そうでない場合は read-only posture observation。各 connector がどちらかを明記）— と approval deep-link 付き Teams notification を備えます。
- **統治して供給する。** 同じコインの context 側です。content source（SharePoint、Confluence、Google Drive、Notion、Salesforce、Snowflake、S3、Azure AI Search、SAP OData、PostgreSQL、さらに local/NFS/SMB mount 向け root-confined filesystem source）が、実用的な default を備えた governed RAG pipeline を供給します。zero-egress lexical retrieval、embedding provider（Voyage、OpenAI-compatible、self-hosted）を provision したときの model-backed semantic retrieval（`embed_policy=model_backed` は silent degrade せず fail closed）、source ごとの provenance、retrieval 時に deny-closed で強制される clearance と scoping、versioned contract と quality gate を備えた data-product catalog です。[Governed data for Claude](docs-site/src/content/docs/how-to/governed-data-for-claude.md) を参照してください。
- **アイデンティティとアクセス。** human identity（WebAuthn/FIDO2、PIV/CAC、AAL step-up）と**non-human identity** lifecycle。agent-identity federation（Entra Agent ID、AWS AgentCore、Google、SPIFFE/SPIRE）。SCIM を備えた AD/LDAP/Okta/Entra/Vault/Infisical からの roster reconciliation。
- **データを保護する。** inline guardrail（PII、prompt-injection、jailbreak）、DLP egress、3 つの KMS backend（AWS KMS、Google Cloud KMS、Azure Key Vault）にわたる BYOK/CMEK envelope encryption、privileged-session recording、verified key-shredding を伴う right-to-erasure、retention と legal-hold、residency attestation、TLS 1.3 hybrid post-quantum key establishment（peer が対応する場合は X25519MLKEM768。signature は現在も classical）。
- **証明する。** hash-chained、Ed25519-signed audit ledger。**26 framework catalogs**（EU AI Act、NIST AI RMF、ISO 42001、SOC 2、ISO 27001、GDPR…）に map された sealed append-only compliance evidence。SIEM/ITSM push（CEF/LEEF/syslog/OTLP/OCSF）。
- **適切に運用する。** spend を deny または throttle できる FinOps budget。blocking CI gate を備えた calibrated LLM-judge eval（on-demand — judge credential がなければ run は `SKIPPED` を報告し、silent pass は決してない）。OS-isolated red-team sandbox（gVisor/Firecracker。provisioned sandbox がなければ run は `DEGRADED` を報告し、fabricated pass は決してない）。public status page を備えた connector-health dashboard、console-managed backup と restore。

すでに運用している cloud、directory、secret store、model provider、agent surface、SIEM、pipeline にまたがる **158 integrations** — コードから導出され、各 push で [`scripts/check-public-counts.sh`](scripts/check-public-counts.sh) により強制される count です。unit は Go code を含む connector directory です。tree 内の 159 directory のうち 158 が該当し、gate は各 push でこの方法により figure を導出します。そのうち 12 は capability ではなく shared contract/library package ですが、count に含まれ、各 directory の完全な内訳は [`connectors/README.md`](connectors/README.md) に記されています。すべての capability と maturity の完全な map は [`docs-site/`](docs-site/) にあり、独自の test suite が gate します。

<a name="whats-open-whats-enterprise-whats-planned"></a>
## オープンなもの、enterprise のもの、予定されているもの

この表は各 capability area を、その出荷先 — open（AGPL）build または separate, optional commercial add-on — に対応付けます。capability ごとの maturity は [誠実さと限界](docs-site/src/content/docs/start/honesty-and-limits.md) に正直に示されています。reserved seam の完全な一覧は public tree 自体で宣言されています（[`cmd/olivares/wire_noenterprise.go`](cmd/olivares/wire_noenterprise.go)）。open binary が reserve する capability は `501` を返すか no-op で、comment にもそう記されています。何も隠されず、open のものが取り除かれることもありません。

| 領域 | Open（AGPL） | Commercial add-ons | Planned |
|---|---|---|---|
| Work & orchestration | durable work item（brief、dependency、acceptance、decision、event）、takeover と revoke を備えた fenced lease、work item に対する session の orchestrated launch、sessions API における work-fenced input と stop、durable receipt を備えた authorized peer への A2A delegation、workflow-scoped message/ack/handoff、console の Work と Orchestration view | — | shadow dual-report と、この plane を system of record にする authority switch |
| Visibility | agent/session/model/MCP server/tool/identity の inventory、Permitted-vs-Observed drift 付き read/write access map、live session、orchestration graph、health/SLA | — | — |
| Policy & enforcement | Cedar authorization engine（RBAC + deny-overlay + scoped grant）、4つのdeny-closedエンフォースメントポイント（Claude Code hook、inline `/v1/messages` proxy、MCP `tools/call` gate、A2A delegation gate）、two-person approval、dual control 付き break-glass、estate kill-switch | hook hardening、server-tool egress control、computer-use governance gate、MCP tool-definition pin（変更された definition では deny-closed）、kill-switch escalation 付き automatic circuit breaker | — |
| Claude & the agent ecosystem | hook で統治される Claude Code、console からの Claude Code session の launch/attach/govern/stop、enterprise managed-settings の配信、subject/surface ごとの model access、MCP（OAuth-gated resource server、posture、registry、`.mcpb`）、A2A v1、gemini-cli/Cursor/Codex CLI/opencode/goose/cline/OpenHands/OpenClaw/Hermes 向け surface（公開される場合は enforcement、そうでない場合は posture observation）、approval deep-link 付き Teams notification | MCP App render content inspection、elicitation/sampling mediation | — |
| Context & knowledge | 10 個の live content source（SharePoint、Confluence、Google Drive、Notion、Salesforce、Snowflake、S3、Azure AI Search、SAP OData、PostgreSQL）と root-confined filesystem source（local/NFS/SMB mount）、governed RAG（default は lexical retrieval、provisioned embedder があれば model-backed semantic retrieval — `embed_policy=model_backed` では fail closed）と retrieval 時の deny-closed clearance、source ごとの provenance、versioned contract と quality gate を備えた data-product catalog | — | — |
| Identity & access | single-IdP SSO（OIDC + SAML 2.0）、WebAuthn/FIDO2、PIV/CAC、AAL step-up、non-human identity lifecycle、agent-identity federation（Entra Agent ID、AWS AgentCore、Google、SPIFFE/SPIRE）、SCIM を備えた roster reconciliation（AD/LDAP/Okta/Entra/Vault/Infisical）、CAEP event receiver | multi-IdP federation、SSO-enforcement、managed SCIM、CyberArk Conjur NHI rotation、CAEP transmitter（SSF receiver への signed SET） | — |
| Data security | inline guardrail（PII、prompt-injection、jailbreak）、DLP egress、3 つの KMS backend（AWS KMS、Google Cloud KMS、Azure Key Vault）にわたる BYOK/CMEK、privileged-session recording、verified key-shred を伴う right-to-erasure、retention と legal-hold、residency attestation、TLS 1.3 hybrid PQC key establishment（X25519MLKEM768） | content firewall/DLP | — |
| Evidence & compliance | hash-chained Ed25519-signed audit ledger、sealed append-only evidence、26 framework catalogs、export/verify を備えた dir/S3 archive（dir は immutable substrate 上でのみ WORM、S3 は Object Lock を使用）、OSCAL export（3 つの open model）、open DORA ICT-risk view、SIEM/ITSM push（CEF/LEEF/syslog/OTLP/OCSF） | OSCAL profile/SSP ingestion + POA&M builder、regulatory retention floor + compliance-mode lock（SEC 17a-4/FINRA 4511/CFTC 1.31）、DORA Register-of-Information + major-incident report、long-horizon WORM legal hold + examiner-grade evidence bundle、Azure/GCS WORM sink、ISO 42001 AIMS pack、compliance-depth + NIS2 classification pack、enterprise reporting | — |
| Operations | spend を deny または throttle する FinOps budget、blocking CI gate を備えた calibrated LLM-judge eval（on-demand: judge credential 必須、なければ `SKIPPED`）、OS-isolated red-team sandbox（gVisor/Firecracker。unprovisioned run は `DEGRADED` を報告）、public status page 付き connector-health dashboard、console-managed backup と restore、open attack-path query | compiled threat-intel catalog、incident close-loop | — |
| Platform & deploy | console を組み込んだ single static binary、row-level security を備えた SQLite または Postgres、Docker/Kubernetes/Helm/air-gapped、Terraform provider、generated client SDK（Go、Java、Python、TypeScript）、open in-proc bus + Core-NATS bridge | durable JetStream bus（at-least-once + dedup） | Windows package（現在: Linux container または source から build）、v1 後の model fine-tuning、voice telemetry probe（現在は宣言済み deny-closed seam） |

AGPL build は platform 全体であり、内部から feature-cap されることはありません。commercial add-on は additive な新しい code であり、open product から取り除かれた feature ではありません。subscription は、disk 上に既にある code を unlock する key ではなく、signed artifact を download する credential — SUSE model — です。self-hosted engine では user account は unlimited です。この engine のどの edition も seat cap を適用せず、binary の seat seam は無条件の no-op です。唯一の例外は hosted Cloud tier で、その control plane は tenant ごとに seat を受け入れます。これはその service の性質であり、この binary の性質ではありません。[`LICENSING.md`](LICENSING.md) と [誠実さと限界](docs-site/src/content/docs/start/honesty-and-limits.md) を参照してください。

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/05-editions-dark.svg">
  <img src=".github/assets/05-editions-light.svg" width="840"
       alt="各エディションの内容: AGPL コアがプラットフォーム全体であり、アドオンはその上に加わるコードです。Community は利用者数無制限の完全な AGPL 製品です。Business はレポート、オンボーディング、脅威インテリジェンス、PQC ポスチャ、NIS2 に商用の深さを加えます。Regulated Operations は保持ガバナー、WORM 監査アーカイブ、リーガルホールド、消去の深さを加えます。Business Max は Business に 4 つのアドオンすべてを加えたものです。Cloud Standard はマネージドサービスで、サービスシートを含むプラン単位のクォータがあります。サブスクリプションは、署名済み成果物をダウンロードするための資格情報です。">
</picture>

<sub>構成によるエディション。パッケージングと価格はお問い合わせください。</sub>
</div>

## コンソールの内部

<div align="center">

<img src=".github/assets/olivares-reel.gif" width="720" alt="Olivares AI コンソールの実際の view を順に映す短いリール: access map、session、policy、FinOps、compliance。">

<sub>実際の console を数秒間ご覧ください。以下の各 still はすべて、running binary が提供する seeded demo estate の capture です — raw capture は <code>bash scripts/docs-captures.sh</code> で自分で再生成できます（ここにある curated set はその output から選ばれています）。</sub>

</div>

| | |
|---|---|
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/access-map-drift-dark.png"><img src="docs-site/public/console/access-map-drift-light.png" alt="最小権限ドリフト: 最小権限の差分をオーバーレイ表示します。想定外のアクセス（観測されたが許諾されていない）と未使用の付与をハイライトします。"></picture><br><sub><b>最小権限ドリフト</b> — 最小権限の差分をオーバーレイ表示します。想定外のアクセス（観測されたが許諾されていない）と未使用の付与をハイライトします。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/orchestration-dark.png"><img src="docs-site/public/console/orchestration-light.png" alt="オーケストレーションとA2A: エージェント間トポロジー — どのエージェントがどのエージェントに委譲するか、ライブの委譲フロー、宣言された実行間隔を示します。通信グラフの読み取りは特権操作であり、自己監査されます。"></picture><br><sub><b>オーケストレーションとA2A</b> — エージェント間トポロジー — どのエージェントがどのエージェントに委譲するか、ライブの委譲フロー、宣言された実行間隔を示します。通信グラフの読み取りは特権操作であり、自己監査されます。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/inventory-dark.png"><img src="docs-site/public/console/inventory-light.png" alt="インベントリ: エステート全体で検出された、すべてのエージェント・セッション・MCP・モデル・アイデンティティ。"></picture><br><sub><b>インベントリ</b> — エステート全体で検出された、すべてのエージェント・セッション・MCP・モデル・アイデンティティ。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/observability-dark.png"><img src="docs-site/public/console/observability-light.png" alt="可観測性とインターオペラビリティ: 標準準拠の取り込み健全性と、台帳と相関させたトレースのドリルダウンです。数値はエンジン全体（プロセスグローバル）のものであり、テナント単位ではありません。標準は上流団体が宣言したバージョンと成熟度に固定されています。"></picture><br><sub><b>可観測性とインターオペラビリティ</b> — 標準準拠の取り込み健全性と、台帳と相関させたトレースのドリルダウンです。数値はエンジン全体（プロセスグローバル）のものであり、テナント単位ではありません。標準は上流団体が宣言したバージョンと成熟度に固定されています。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/dashboards-dark.png"><img src="docs-site/public/console/dashboards-light.png" alt="エグゼクティブ概要: コスト、利用状況、リスク、コンプライアンスを一目で把握できます。詳細は運用ビューでドリルダウンしてください。"></picture><br><sub><b>エグゼクティブ概要</b> — コスト、利用状況、リスク、コンプライアンスを一目で把握できます。詳細は運用ビューでドリルダウンしてください。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/home-dark.png"><img src="docs-site/public/console/home-light.png" alt="概要: AIエステートを一目で把握 — インベントリ、アクティビティ、リスク、コンプライアンス、支出、ヘルスを集約します。"></picture><br><sub><b>概要</b> — AIエステートを一目で把握 — インベントリ、アクティビティ、リスク、コンプライアンス、支出、ヘルスを集約します。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/security-dark.png"><img src="docs-site/public/console/security-light.png" alt="セキュリティとフォレンジック: ガードレールの検出結果、エンフォースメント態勢、異常キュー、改ざん検知可能なインシデントフォレンジック。このプレーンはデフォルトで検出型です。記録は行いますが、エンフォースメントが有効化されガバナンス下にない限り、それ自体がブロックすることはありません。"></picture><br><sub><b>セキュリティとフォレンジック</b> — ガードレールの検出結果、エンフォースメント態勢、異常キュー、改ざん検知可能なインシデントフォレンジック。このプレーンはデフォルトで検出型です。記録は行いますが、エンフォースメントが有効化されガバナンス下にない限り、それ自体がブロックすることはありません。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/session-viewer-dark.png"><img src="docs-site/public/console/session-viewer-light.png" alt="セッション録画ビューア: 単一セッションのエージェント活動とガバナンス証拠の統合タイムライン。"></picture><br><sub><b>セッション録画ビューア</b> — 単一セッションのエージェント活動とガバナンス証拠の統合タイムライン。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/identity-dark.png"><img src="docs-site/public/console/identity-light.png" alt="アイデンティティと NHI: SSO、SCIM、アイデンティティインベントリ、NHI ライフサイクル、WIF グラフ、特権ログインを観測・統制・監査します。"></picture><br><sub><b>アイデンティティと NHI</b> — SSO、SCIM、アイデンティティインベントリ、NHI ライフサイクル、WIF グラフ、特権ログインを観測・統制・監査します。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/knowledge-dark.png"><img src="docs-site/public/console/knowledge-light.png" alt="データ・ナレッジ・コンテキスト: ガバナンス下のナレッジベース、検索来歴、プロンプトレジストリ、エージェントメモリ、コンテキストポリシー。"></picture><br><sub><b>データ・ナレッジ・コンテキスト</b> — ガバナンス下のナレッジベース、検索来歴、プロンプトレジストリ、エージェントメモリ、コンテキストポリシー。</sub> |
| <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/work-apply-refused-dark.png"><img src="docs-site/public/console/work-apply-refused-light.png" alt="計画: 変更を計画しています。この段階では何も書き込まれません。"></picture><br><sub><b>計画</b> — 変更を計画しています。この段階では何も書き込まれません。</sub> | <picture><source media="(prefers-color-scheme: dark)" srcset="docs-site/public/console/killswitch-dark.png"><img src="docs-site/public/console/killswitch-light.png" alt="キルスイッチ: エステートの緊急停止。ワンクリックで、統制下のあらゆる作動面を停止します。発動は意図的に低コストですが、復旧には別個の2つのユーザーアカウントと強制的な事後レビューが必要です。"></picture><br><sub><b>キルスイッチ</b> — エステートの緊急停止。ワンクリックで、統制下のあらゆる作動面を停止します。発動は意図的に低コストですが、復旧には別個の2つのユーザーアカウントと強制的な事後レビューが必要です。</sub> |

<a name="install"></a>
## インストール

すべての release は **cosign-signed trust chain** のもとで出荷されます — 全 artifact をカバーする cosign-signed checksums manifest、そこから間接的にカバーされる archive と static binary、archive ごとの SBOM in-toto attestation、container image の直接の cosign signature と SBOM attestation、Helm chart の cosign signature、そしてセット全体の SLSA build provenance です。security product では supply chain が trust model の一部なので、実行前に[検証してください](docs/RELEASE-VERIFICATION.md)。OS ごとの完全な matrix と production setup は [`INSTALL.md`](INSTALL.md) に、deployment tutorial（Compose、Kubernetes/Helm、air-gapped）は [`docs-site/`](docs-site/) にあります。

engine は**secure by default**です。loopback に bind し、初回 boot で self-signed certificate による HTTPS を提供し、default credential はなく、console に single-use setup token を出力します。最初に実行する command が secure な command です。

**source から**（最初の tagged release までの supported path）:

```sh
# Build the single binary (Go 1.26+, Task, pnpm — the web console is embedded).
task build

# Start it — one guided, secure-by-default command (TLS on, loopback-only, no
# default credentials). It prints your console URL and a one-time setup token.
./bin/olivares quickstart
```

**最初の release 以降**、推奨 path は 1 回の verified install になります — hardened systemd unit を備えた `.deb`/`.rpm`/`.apk` package、multi-arch Docker image、Homebrew cask、Helm chart。いずれも release の cosign-signed checksums manifest でカバーされます（image は直接署名）。すべて 1 step で install でき、引き続き secure by default です。これらはまだ公開されていません。tag が付くまでは、上記のように source から build してください。**Windows** はまだ build されていません — Linux container を実行するか source から build してください（[`INSTALL.md` の計画](INSTALL.md#windows)）。

> 最初に、real source を配線せず見て回りたいですか? synthetic estate が 1 command で loopback 上に起動します — 下の[Quickstart](#quickstart)を参照してください。

<a name="quickstart"></a>
## クイックスタート

2 つの入り方があります。すぐに synthetic estate を探索するか、engine を real source に向けるかです。どちらも同じ real binary を実行します。

### 5 分で評価する

1. `task build` で build します（Go 1.26+、Task、pnpm。[prerequisites](#quickstart-prerequisites)を参照）。
2. 以下の step 2a の正確な command で demo estate を boot します。
3. console で access map と Permitted-vs-Observed drift（20 nodes / 13 edges、8 unexpected accesses と 2 unused grants）、Cedar policy と approval flow、compliance evidence view（26 framework catalogs）、FinOps budget を確認します。
4. 次に何が real で何が planned かを読みます。上の feature matrix、[The work plane](#the-work-plane)、[誠実さと限界](docs-site/src/content/docs/start/honesty-and-limits.md)です。

<a name="quickstart-prerequisites"></a>
source から build する prerequisite は Go 1.26+、[Task](https://taskfile.dev)（go-task）、pnpm（web UI は embedded）です。完全な development setup は [`CONTRIBUTING.md`](CONTRIBUTING.md) を参照してください。

**1. Build:**

```sh
task build && ./bin/olivares version
```

**2a. demo estate を探索する** — real engine を通じた synthetic observation、loopback-only（non-loopback address は拒否）、real data なし:

```sh
./bin/olivares serve --seed-demo --insecure \
  --listen 127.0.0.1:8901 --grpc-listen 127.0.0.1:8902 \
  --data-dir "$(mktemp -d)"
```

`http://127.0.0.1:8901` を開き、boot banner の demo credential で login し、console を見てください — inventory、access map と drift、session、orchestration、policy、FinOps、compliance。demo seed は学習専用です（public source-tree password）。real data には決して向けないでください。

**2b. または real に起動する** — 1 つの guided・secure-by-default command:

```sh
./bin/olivares quickstart        # TLS on, loopback; prints the console URL + a one-time setup token
```

表示された URL で console を開き、token で最初の administrator を作成してください — curl も追加 step も不要です。（`olivares serve` は同じ engine を explicit flag で起動するもので、production と container 向けです。）次に source を接続します。[完全なクイックスタート](docs-site/src/content/docs/start/quickstart.md)は PostgreSQL audit log に対して**real pgAudit connector**を配線します — demo seed は使用しません — また production install path（systemd、Docker Compose、[`deploy/manifests/install.yaml`](deploy/manifests/install.yaml) による Kubernetes、air-gapped）にも link します。

demo estate は deterministic です。数字は願望ではありません。`task smoke:quickstart` は、この同じ path を real binary に対して（専用の port と data dir で）実行し、上記の access-map と drift count を assert するため、この section がコードからひそかに drift することはありません。

<a name="architecture"></a>
## アーキテクチャ

<div align="center">
<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/02-architecture-dark.svg">
  <img src=".github/assets/02-architecture-light.svg" width="840"
       alt="アーキテクチャ: エージェント面、監査ソース、MCP および A2A のピア、コンテンツソースが 3 つの方法で、コンソールを組み込んだ単一のセルフホスト Go バイナリへ収集されます。このバイナリは製品モジュール、ポリシーと適用の層、署名済みの証跡台帳を、テナント単位にスコープされたストアの上で保持します。コンソール、REST API、絞り込んだ gRPC サブセット、CLI、Terraform プロバイダーを提供し、クラウド制御プレーン（構築済み・未デプロイ）とライセンスポータル（デプロイ済み・提供は無効）は別のプレーンとして描かれています。">
</picture>
</div>

engine は web UI を embed する単一の static Go binary（`olivares`）で、文書化された coverage を持つ 4 つの surface を通じて capability を公開します。primary surface の REST API、stable core に絞った frozen gRPC mirror、`olivares` CLI 自身（`quickstart`、`serve` から `work`、`orchestration`、`agent`、`mcp`、`compliance` まで、68 の group 化された top-level command。新しい command が ungrouped で追加されないよう help group の合計を維持する test があります）、そして manage-as-code resource の Terraform provider です。collector は customer infrastructure 内で 3 つの mode で動作します。in-process fast-path source、engine が authenticated per-launch channel（AutoMTLS）で監督する out-of-process plugin、検証済み client certificate による mutual TLS を介する opt-in remote collector→core deployment です。core は data を SQLite（single-node、air-gap）または row-level security を備えた Postgres に保存します。すべてのモジュール操作は store API で tenant に固定され、Postgres が FORCE row-level security で再度強制します。これを黙って bypass できる特権の connection role（superuser または `BYPASSRLS`）は boot 時に拒否され、その拒否を越える唯一の方法は、cost を明示する explicit opt-in flag です。tenant をまたぐ system read は、tenant-scoped work には決して使用されない別個の least-privilege `BYPASSRLS` admin pool を通ります — 宣言された扉であり、存在しない扉ではありません。

概要: [`ARCHITECTURE.md`](ARCHITECTURE.md)。

## ディレクトリ単位のオープンコア

license は最初の commit から確定しています。**open core** — AGPL の下にある complete product、copyleft friction なしに ecosystem が成長できる permissive SDK と connector、そして reserved capability のための小さな**additive** commercial add-on 群です。これらは `-tags enterprise` でのみ build され、それぞれ commercial term の下で個別に license され、public binary には含まれません。AGPL build は governance platform 全体であり、upsell のために機能を削られることは決してありません。commercial add-on は open product には一度もなかった新しい code を*追加*します。そのため enterprise build は open build と同一ではありませんが、open で出荷されるものから何かが取り除かれることはありません。すべての source file は `SPDX-License-Identifier` header を持ち、CI が強制します。

| ディレクトリ | License | 内容 |
|---|---|---|
| `core/` | `AGPL-3.0-only` | Engine: ingest、event bus、data model、module runtime、API、authn/z、audit、multi-tenancy |
| `modules/` | `AGPL-3.0-only` | 30 の product module（inventory、access map、work と lease、identity、FinOps、eval、guardrail、…） |
| `web/` | `AGPL-3.0-only` | React UI、`go:embed` により binary に embed |
| `sdk/` | `Apache-2.0` | 安定した `SourceConnector` / `OutputConnector` / `Module` interface + gRPC contract + type |
| `connectors/` | `Apache-2.0` | first-party と community connector（Claude、MCP、pg-audit、eBPF、cloud、SIEM、…） |
| `clients/` | `Apache-2.0` | generated client SDK（Go、Java、Python、TypeScript） |
| Commercial add-ons *(separate private repo)* | `LicenseRef-Olivares-Commercial` | enforcement、MCP、identity、data security、compliance depth、operation、platform にまたがる additive・separately-licensed add-on family。上の[the matrix above](#whats-open-whats-enterprise-whats-planned)に領域別で列挙され、すべて [`cmd/olivares/wire_noenterprise.go`](cmd/olivares/wire_noenterprise.go) の宣言済み seam です — `-tags enterprise` でのみ build され、この repository にも public binary にも決して含まれません |
| `docs/`, `docs-site/` | — | design document と product documentation site |

connector は `sdk/` からのみ import でき、`core/` からは決して import できません。これは AGPL / Apache boundary を明確に保ち、third party が copyleft obligation なしに connector を書けるようにします — CI の [`scripts/check-boundary.sh`](scripts/check-boundary.sh) で強制されます。

## セキュリティとサプライチェーン

Olivares AI は customer host で動作し、各 agent が触れられる対象を map するため、security bar は設計上高く設定されています。read-first、observation plane の minimal data（access map は payload ではなく edge を保存し、governed Knowledge store は明示的に ingest した content のみを保持）、least privilege、mTLS、signed checkpoint を備えた append-only hash-chained audit、signed release です。access map 自体が privileged で audited な surface です。それを開くことも、agent-to-agent communication graph を読むことも recorded action です。

vulnerability の報告または disclosure policy を読むには [`SECURITY.md`](SECURITY.md) を参照してください（private report — public issue には絶対にしないでください）。advisory flow は [`docs/security-advisories.md`](docs/security-advisories.md) に文書化されています。supply-chain readiness evidence は [`docs/openssf-badge.md`](docs/openssf-badge.md) の Best Practices map にあります。

<a name="documentation"></a>
## ドキュメント

product documentation は [`docs-site/`](docs-site/) にあります。tested install tutorial（single node、Docker Compose、Kubernetes/Helm、air-gapped）、real console capture を備えた connector 別 guide、cookbook（deny-closed policy、budget、approval、kill-switch drill、SIEM push）、API reference、glossary を持つ Diátaxis site です。[What is Olivares AI](docs-site/src/content/docs/start/what-is-olivares-ai.md) と [誠実さと限界](docs-site/src/content/docs/start/honesty-and-limits.md) から始めてください。後者は今日動作するもの、design-stage のもの、製品が意図的に行わないものを率直に示す page です。

## コミュニティとガバナンス

adopter が期待する community-health と governance file は、すべて揃っており、最新の状態です。

- **意思決定の方法:** [`GOVERNANCE.md`](GOVERNANCE.md)（maintainer-led / open-core、project の stage について正直）と [`.github/CODEOWNERS`](.github/CODEOWNERS)（license frontier に map された review routing）。
- **Contributing:** [`CONTRIBUTING.md`](CONTRIBUTING.md)（setup、DCO/CLA、SPDX、connector boundary）— すべての change は [pull-request template](.github/PULL_REQUEST_TEMPLATE.md) 経由で送られます。
- **行動規範:** [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md)（Contributor Covenant 2.1）。
- **ヘルプを得る:** [`SUPPORT.md`](SUPPORT.md) — そして security issue を報告**しない**場所。
- **変更:** [`CHANGELOG.md`](CHANGELOG.md)（Keep a Changelog 1.1 + CalVer `vYY.M.PATCH`; beta）。

## ライセンス

product（`core/`、`modules/`、`web/`）は **GNU Affero General Public License, version 3**（`AGPL-3.0-only`）の下で license されています。connector SDK、connector、client SDK（`sdk/`、`connectors/`、`clients/`）は **Apache-2.0** の下で license されています。特定の file を規律する license は SPDX header に、release の license は SBOM に記されています。

> **無保証・無責任 — deploy 前にお読みください。** free software は**現状有姿**で提供され、**いかなる種類の保証もなく**、**data loss、corruption、business interruption、lost profit について責任を負いません**。これは control plane において形式的な文言ではありません。misconfiguration は正当な作業を block して production を中断させることも、止めるつもりだったものをそのまま通すこともあります。AGPL-3.0-only §§15–16 と Apache-2.0 §§7–8 が適用され、さらにこの project 固有の AGPL §7(a) に基づく supplemental term が適用されます。high-risk use、compliance outcome、third-party component を含む全文は [`DISCLAIMER.md`](DISCLAIMER.md) にあります。

**commercial license** は、その条件で運用できない組織に AGPL の private exception を提供します。additive `enterprise/` capability — [the matrix above](#whats-open-whats-enterprise-whats-planned)に領域別で列挙された add-on family で、すべて public tree の宣言済み seam — は、自身の commercial term の下で**個別の optional add-on**として提供されます。`-tags enterprise` でのみ build される closed code であり、open binary には決して存在しません。packaging と pricing はお問い合わせください。AGPL core 自体は complete であり、内部から feature-cap されることは決してありません。commercial license または enterprise に関するお問い合わせは `enterprise@olivares.ai` までご連絡ください。[`LICENSING.md`](LICENSING.md) を参照してください。

contribution には DCO sign-off（`git commit -s`）と Contributor License Agreement が必要です。[`CONTRIBUTING.md`](CONTRIBUTING.md) と [`CLA.md`](CLA.md) を参照してください。

## プロジェクトを支援する

Olivares AI は AGPL-3.0 かつ self-hosted です。core は free であり、これからも free のままです。役に立ち、作業を直接支援したい場合は、この repository の **Sponsor** button から sponsor になれます。

sponsorship は support contract では**なく**、priority も購入しません。質問と bug report の扱いは [`SUPPORT.md`](SUPPORT.md)、commercial term と enterprise add-on は [`LICENSING.md`](LICENSING.md) を参照してください。

---

<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset=".github/assets/olivares-mark-dark.svg">
  <img src=".github/assets/olivares-mark-light.svg" alt="Olivares AI" width="44">
</picture>

<sub><strong>エンタープライズ AI のグラウンドトゥルース。</strong> · <a href="https://olivares.ai">olivares.ai</a> · <a href="LICENSING.md">AGPL-3.0 + commercial</a></sub>

</div>
