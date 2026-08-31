---
title: サブスクリプション認証された Claude Code と Codex を統制する
description: >-
  Olivares AI が、サブスクリプションで認証するコーディングエージェント——Pro/Max の
  Claude Code、ChatGPT の Codex——を、そのサブスクリプションの中間に入ることなく
  どのように統制するか。3 つのメカニズム（observe、managed-settings + フック、
  API キーゲートウェイ）と、1 本のレッドライン——あなたのサブスクリプション資格情報を
  決してルーティングしない。
sidebar:
  order: 6
---

統制が最も難しいエージェントは、開発者が個人または会社の**サブスクリプション**で
ログインしたものです。Pro/Max でサインインした Claude Code、あるいは ChatGPT で
サインインした Codex。同じ形は Grok Build にも、ワークロードではなく**人**を認証する
あらゆる CLI エージェントにも当てはまります。以下のメカニズムが扱うのは、
特定ベンダーではなく、そのログインの*形*です。それはラップトップ上で動作し、
OAuth 資格情報で認証します。
そしてそれは、推論パス内のクラウドプロバイダーのガードレールが決して目にすることの
ない、まさにその面（surface）です（[ウェッジ](/ja/explanation/positioning/where-olivares-fits-vs-your-gateway/)
を参照）。魅力的に見える「解決策」——サブスクリプションを保持しそのトラフィックを
ルーティングするサービスをその前段に置く——は、Olivares AI が**構築しない**ものです。
なぜならモデルプロバイダーがそれを禁じており、また私たちのコントロールプレーンを
資格情報侵害の単一障害点にしてしまうからです。

このページは、これらのエージェントを**サブスクリプションを一切ブローカーすることなく**
どのように統制するかについての誠実な説明です——何を観察するか、どこで強制するか、
そしてゲートウェイが適切となる唯一の狭い経路（それは決してサブスクリプションのもの
ではありません）。

:::danger[レッドライン：あなたのサブスクリプションを決してルーティングしない]
Olivares AI は、**サードパーティのサブスクリプション資格情報を保持・プロキシ・
ルーティングすることは決してありません。** Anthropic 自身のポリシーはこう述べています。
*"Anthropic does not permit third-party developers to offer Claude.ai login or to route
requests through Free, Pro, or Max plan credentials on behalf of their users"*
（[Claude Code legal & compliance](https://code.claude.com/docs/en/legal-and-compliance)、
2026-06-21 取得 — この禁止条項は 3 つのコンシューマープラン **Free, Pro, Max** を
名指ししています）。OpenAI の規約も、コンシューマー版 ChatGPT/Codex ログインについて
同様に機能します。私たちの姿勢はこの一線そのものよりも厳格です。私たちは**いかなる**
プランの、**いかなる**サブスクリプション OAuth も**ルーティングしません**。ガバナンスは
エージェントの*周囲*で起こるのであって、その資格情報の*内側*では決して起こりません。
:::

## なぜサブスクリプションのブローカーが選択肢にないのか

このルールについて正確であることには価値があります。なぜなら購買担当者の法務はそれを
確認するからです。Anthropic のポリシーは、混同してはならない 2 つのリストを描いています。

- **そもそも誰が OAuth を使ってよいか** — 5 つのプラン：*"OAuth authentication is intended
  exclusively for purchasers of Claude Free, Pro, Max, Team, and Enterprise
  subscription plans and is designed to support ordinary use of Claude Code and
  other native Anthropic applications."*
- **サードパーティが行ってはならないこと** — ユーザーに代わってルーティングすること：
  *"Anthropic does not permit third-party developers to offer Claude.ai login or to route
  requests through Free, Pro, or Max plan credentials on behalf of their users."*

禁止条項は明示的に**コンシューマー**プラン（Free, Pro, Max）を名指ししています。一方で
このページは、Team や Enterprise のシートをルーティングする許可を誰にも与えてはいません
——それについては沈黙しており、私たちは沈黙を許諾とは読みません。*ツールを構築する
開発者*については、Anthropic 自身のガイダンスはサブスクリプション OAuth から完全に
離れる方向を指し示しています。*"Developers building products or services that
interact with Claude's capabilities, including those using the Agent SDK, should
use API key authentication through Claude Console or a supported cloud provider."*
（[出典](https://code.claude.com/docs/en/legal-and-compliance)。規約によるプラン分割：
Team/Enterprise/API は Commercial Terms、Free/Pro/Max は Consumer Terms。）

私たちの Codex コネクターは、設計上、同一の規律をコードで体現しています。自動化資格情報は
OpenAI の **API キー**または**ワークスペースアクセストークン**であり、個人の ChatGPT
サブスクリプションでは決してありません——*"proxying it for third-party/programmatic
use violates OpenAI's terms exactly as a consumer Claude subscription does for
Anthropic. There is no subscription config field by design"*
（`connectors/codex/codex.go`）。つまりレッドラインは後から取り付けたマーケティング上の
約束ではありません。それは製品の形そのものです。

## 3 つのメカニズム、そのいずれもサブスクリプションではない

私たちはサブスクリプション認証されたエージェントを、3 つの独立したチャネルを通じて
統制します。最初の 2 つは推論に一切触れません。3 つ目が触れるのは、**API キー**で
認証するトラフィックに対してのみであり、サブスクリプションには決して触れません。

### 1. Observe — テレメトリ、使用状況、ポスチャ

Claude Code は OpenTelemetry を発し、管理者は管理ティアからフリート全体に対してそれを
有効化できます。*"Administrators can configure OpenTelemetry settings
for all users through the managed settings file"*
（[Claude Code monitoring](https://code.claude.com/docs/en/monitoring-usage)）。
私たちはその **gen-ai シグナル**——セッション、トークン、コスト、ツール活動——を取り込み、
それをアクセスマップとポスチャ検出結果に変えます。決定的に重要なのは、これが **Claude Code
側でも構造的に最小データである**ことです。プロンプトの内容は*"redacted by
default"*であり、ツールの詳細、ツールの内容、生の API ボディはそれぞれ*"(default:
disabled)"*です（同出典）。私たちは使用状況とメタデータを消費するのであって、会話を
消費するのではありません。

Codex については、同じ observe チャネルは、コネクターによる Analytics および
Compliance/Audit API の取り込みです——使用状況、導入状況、不変の監査レコードを
コストサンプルと改ざん検知可能なエビデンスに変え、*"never prompt/diff
content or key values"*を保ちます（`connectors/codex/codex.go`）。

→ [OpenTelemetry GenAI を取り込む](/ja/how-to/connectors/otel-genai/) ·
[Claude Code 向けエンタープライズ OTel](/ja/how-to/claude-code-enterprise-otel/)

### 2. Managed settings + フック — プロセス内 PEP

観察は強制ではありません。Claude Code の強制チャネルは、OS ポリシーティアにある
**managed settings** ファイルであり、これはオーバーライド不可能な `PreToolUse` フックを
携え、すべてのツールが実行される前に Olivares の決定点へコールバックします。Anthropic は
私たちが依拠する性質を文書化しています。*"Environment variables defined
in the managed settings file have high precedence and cannot be overridden by
users"*、そして managed settings は*"can be distributed via MDM"*
（[monitoring](https://code.claude.com/docs/en/monitoring-usage)）。

Olivares はそのファイルを（`olivares agent managed-settings`）`allowManagedHooksOnly`
付きでレンダリングするため、開発者自身のフックが統制されたフックに先行したり、それを
切り崩したりすることは決してできません。そしてセッションごとのエンドポイントとベアラーは
起動時に注入されます——静的ファイルに書き込まれるのではありません。決定そのものは
**すべてのエッジで deny-closed** です。ツール呼び出しが許可されるのは、確固たる
アイデンティティが解決し、ポリシーの処理結果が `deny` でなく、ライブのポリシーエンジンが
それを禁じておらず、そして——`ask` については——人間の承認がまさにそのプランハッシュに
束縛されている場合に限られます。緊急停止（[キルスイッチ](/ja/reference/glossary/#kill-switch)）は、
アクティブなブレークグラス付与を含め、すべてに優先します。

これは[Claude Code フック PEP](/ja/how-to/connectors/claude-code-hooks-pep/)のページが
運用的に文書化しているメカニズムであり、私たちがローカルの開発エージェントを単に
見張るのではなく*統制*できるようにするもの——[この語彙が指し示す 3 つのレーン](/ja/explanation/positioning/analyst-vocabulary/#この語彙が指し示す-3-つのレーン)
の 2 番目——です。

### 3. API キーのためのゲートウェイ — OAuth のためでは決してない

Olivares が推論リクエストラインに位置する経路はちょうど 1 つだけ存在し、それは
Claude Code の managed-settings チャネルを**使わない**呼び出し側のためにのみ存在します。
**API キー**（または Bedrock/Vertex 相当）で認証された、生の SDK または `curl` の
トラフィックです。Claude Code はそのようなリクエストを `ANTHROPIC_BASE_URL` で
ルーティングし——*"To route requests through a custom API endpoint, set the
`ANTHROPIC_BASE_URL` environment variable instead"*——`ANTHROPIC_AUTH_TOKEN` 経由の
ベアラーでゲートウェイを認証します。*"when routing through an LLM gateway or proxy
that authenticates with bearer tokens rather than Anthropic API keys"*
（[Claude Code IAM](https://code.claude.com/docs/en/iam)）。Olivares のインライン推論
プロキシに向けられると、そのトラフィックは転送される前に統制されたパイプライン——
レジデンシー、モデルアクセス、コンテキストウィンドウ、DLP、予算、記録——を通ります。

境界は絶対的です。**この経路は API キー／ベアラーのトラフィックを運ぶのであって、
サブスクリプションの OAuth 資格情報を運ぶことは決してありません。** これは managed
settings が到達できない SDK/`curl` の呼び出し側のための強制シームであり、それ以上の
ものではありません。

## 誠実ボックス：検証済みデプロイであって、回避不能ではない

:::caution[*デプロイされている*と証明できる強制であって、*回避できない*強制ではない]
managed-settings + フックの PEP は **deny-closed** であり、**設定を通じてユーザーが
オーバーライドできません**——しかしそれは魔法ではありません。`ANTHROPIC_BASE_URL` を
自分自身のエンドポイントに向ける開発者は、推論をまったく別の場所へ送ります。私たち自身の
エンジニアリングノートがそれを率直に述べています。*"a custom
`ANTHROPIC_BASE_URL` bypasses server-managed-settings entirely"*
（`modules/inferenceproxy/doc.go`）。ですから私たちは PEP が回避不可能であるとは
決して主張しません。代わりに、私たちが裏づけられる 2 つのことを主張します。

1. **それは検証済みデプロイである。** Olivares は、managed settings と PEP フックが
   実際にホスト上に存在することをアテストします——プロビジョニングされていないホストは
   統制されないが観察される（ungoverned-but-observed）状態で動作し、それは隠されるのでは
   なく可視化されます。
2. **バイパスそのものが検出結果になる。** ホスト上の非デフォルトの `ANTHROPIC_BASE_URL` は
   ポスチャ検出結果として表面化し、認可された Olivares ゲートウェイから逸脱したベース URL を
   固定する管理環境は**ドリフト**検出結果を生じさせます
   （`connectors/claude-config`、`connectors/managedsettings`）。回避は静かにやり過ごされる
   のではなく、明るく点灯します。

「検証済みデプロイ、回避は検出結果」は、開発者が制御するマシン上で動作するあらゆる
エージェントについての誠実な強制の物語です。私たちはあなたに「回避不能（unbypassable）」を
売り込むことはしません。
:::

## Codex の非対称性を、誠実に述べる

Claude Code と Codex は対称ではなく、その違いは重要です。ChatGPT で認証された Codex に
ついては、**`ANTHROPIC_BASE_URL` の文書化された相当物は存在しません**——OpenAI の
[managed-configuration ページ](https://developers.openai.com/codex/enterprise/managed-configuration)
は、カスタムベース URL やゲートウェイを通じて推論をルーティングするための設定や環境変数を
一切文書化していません（fetch により検証、2026-06-21。それはそのページ上の不在であって、
他のどこにも存在しないことの証明ではありません）。ですから私たちは Codex をその推論を
傍受することによっては**統制しません**。

代わりに、OpenAI が*実際に*管理者に強制された制御を与えている場所で統制します。Codex の
managed configuration は、エンタープライズが*"Requirements: admin-enforced
constraints that users can't override"*——*"constrain security-sensitive
settings (approval policy, approvers reviewer, automatic review policy, sandbox
mode, permission profiles, web search mode, managed hooks, and optionally which
MCP servers users can enable)"*——を設定することを可能にします（同出典）。Olivares は
それらの requirements を作成・アテストし（`connectors/codex-managed-config`）——承認
ポリシー、サンドボックスモード、MCP の許可リスト、リダクトされたテレメトリ
（`log_user_prompt = false`）——Codex の Analytics および Compliance のエビデンスを
取り込みます。モデル呼び出しに対する中間者（man-in-the-middle）を通じてではなく、
構成とエビデンスを通じたガバナンスです。

## 一覧表で

| チャネル | 何をするか | 推論に触れるか？ | 資格情報 |
|---|---|---|---|
| **Observe** | 使用状況・コスト・ツール活動 → アクセスマップ + ポスチャ；Codex Analytics/Compliance → 台帳 | いいえ | なし — テレメトリのみ、内容はデフォルトでリダクト |
| **Managed settings + フック** | Claude Code 上の deny-closed `PreToolUse` PEP、設定経由ではオーバーライド不可 | いいえ | エージェント自身のもの。私たちは決して目にしない |
| **ゲートウェイ（API キーのみ）** | `ANTHROPIC_BASE_URL` 経由の生の SDK/`curl` 呼び出し側のための統制されたパイプライン | はい | **API キー／ベアラー — サブスクリプション OAuth では決してない** |
| **Codex managed-config** | 管理者強制の requirements（承認/サンドボックス/MCP）+ エビデンス取り込み | いいえ | 組織のもの。傍受ではなく構成 |

## 関連

- [Olivares はあなたのゲートウェイ／Guardrails に対してどこに位置するか](/ja/explanation/positioning/where-olivares-fits-vs-your-gateway/)
  — なぜこのいずれもがあなたの AI ゲートウェイと競合しないのか。
- [Olivares AI vs WitnessAI](/ja/explanation/positioning/vs-witnessai/) — IDE 内で
  エージェントを統制することについての直接対決。
- [Claude Code フックと PEP](/ja/how-to/connectors/claude-code-hooks-pep/) と
  [Olivares と一緒に Claude Code を実行する](/ja/how-to/run-claude-code-with-olivares/) — 運用上の
  ハウツー。
- [誠実さと限界](/ja/start/honesty-and-limits/) — このページが書かれている根底にある
  恒常的なコミットメント。
