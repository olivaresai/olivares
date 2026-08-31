---
title: Olivares AI vs WitnessAI
description: >-
  WitnessAI との誠実で出典のある比較 — IDE や開発者ツールの内側で AI エージェントを
  統制することについての、最も近い直接対決。エージェントの発見と MCP 許可リストでは
  本物の同等性があり、規制下・セルフホストの購買担当者にとっては明確で擁護可能な違いが
  ある——プロセス内強制、暗号的なエビデンス台帳、そして境界を決して離れないデータプレーン。
sidebar:
  order: 8
---

Olivares AI への「競合」のほとんどは隣接するレーンに位置しており——コントロールタワー、
ゲートウェイ、オブザーバビリティ——[他の位置づけページ](/ja/explanation/positioning/market-context-and-sources/)が、
なぜそれらが *or* ではなく *and* なのかを説明しています。**WitnessAI は本物の直接対決
です。** それは開発者環境の内側で AI エージェントを統制します——コーディングエージェントを
発見し、承認済みツールのリストを強制し、エージェントが行うことにポリシーを適用します。
ですからこのページはより高い基準で律せられます——以下の WitnessAI に関するすべての主張は、
彼ら自身のサイト（2026-06-21 取得）からの逐語的な引用であり、彼らのサイトが沈黙している
ところでは*"not documented"*と述べ、決して*"absent"*とは述べません。

:::note[このページの読み方]
私たちは機能チェックリストではなく、**アーキテクチャとデプロイモデル**で比較します。
なぜなら、違いが現実的で持続的なのはそこだからです。私たちが真に重複する機能については、
そう述べ、**優位性を主張しません**。差別化要因はある特定の購買担当者のためのものです——
ガバナンスデータを他者のクラウドへ送ることのできない、規制下またはエアギャップの組織。

:::

## 私たちが同等であるところ（そしてそれ以外を主張しない）

WitnessAI は、Olivares もカバーする 2 つの領域で本物の仕事をしています。私たちはこれらを
**同等（parity）**として扱い、自分たちが優れているとは主張しません。

- **エージェント／シャドー AI の発見。** WitnessAI は*"Find and catalog
  thousands of AI applications, agents, and MCP servers"*を、そして開発者向けには
  *"Discover apps like GitHub Copilot, Cursor, and hundreds of other AI dev tools
  across your network"*を謳っています（[witness.ai](https://witness.ai/)）。Olivares も
  エージェント、モデル、MCP サーバー、ツールを発見・インベントリ化します。視点は異なります
  ——彼らのネットワーク、私たちのリードファーストなテレメトリ＋監査——が、*発見*という
  成果は比肩可能であり、私たちは自分たちのカタログがカテゴリー的に優れているふりはしません。
- **MCP 許可リスト／承認済みツールのガバナンス。** WitnessAI：*"Enforce control of
  approved MCP servers and tools across every agent, IDE, and agentic app"*そして
  *"Maintain an organization-wide approved-tool list of MCP servers and tools"*
  （witness.ai）。Olivares も MCP ツールアクセスを統制します
  （[MCP ガバナンス](/ja/how-to/connectors/mcp-governance/)）。同等です。このページの
  いずれの箇条書きも「私たちは彼らよりうまく MCP を許可リスト化する」ではありません。

エージェントの発見と MCP の許可リスト化があなたの要件のすべてであれば、これは能力の面では
僅差であり、他の要因（デプロイモデル、価格、既存のフットプリント）が決め手となるべきです。
私たちは過大主張するより、そう述べることを選びます。

## WitnessAI とは何か、彼らの言葉で

WitnessAI のモデルは**ネットワークレベルかつクラウド配信**であり、明示的に*intent-based*
（意図ベース）の制御哲学を持っています。

- **ネットワークレベル、クライアントレス。** *"See AI activity across your entire network
  without relying on browser extensions or endpoint clients"*、そして
  *"operates at the network level—no new SDKs, additional clients, or added
  exposure"*なプラットフォーム（witness.ai）。
- **意図ベースのポリシー。** *"Traditional security sees text; WitnessAI sees
  intent"*、*"intent-based ML engines that understand context, not just
  keywords"*とともに（witness.ai）。これは現実的で独自の設計選択であり、インラインで
  コンテンツを意識するユースケースにとっての強みです。
- **人間に帰属するエージェントガバナンス。** *"every agent action maps back to a human
  identity"*、*"a single policy engine [that] governs both human and agent
  workforces"*のもとで（witness.ai）。
- **SaaS の主権ストーリー。** 彼らはデータ制御に取り組んでいます——*"a secure,
  single-tenant environment that ensures data sovereignty"*、*"single-tenant
  environment with your own key encryption"*、そして*"regional sandboxes"*
  （witness.ai）。これは**クラウド側、シングルテナント、顧客キー**のモデルです。
  データレジデンシーに対する現実的な答えであり——そしてそれは私たちのものとは*異なる*
  答えであって、それが以下の核心です。

これらは能力であり、出典付きで公正に述べられています。比較は「彼らは弱い」ではありません。
「私たちは異なるアーキテクチャの上に、異なる購買担当者のために構築されている」です。

## Olivares が構造的に異なるところ

| 観点 | WitnessAI（彼らのサイトによる） | Olivares AI |
|---|---|---|
| **デプロイ** | ネットワークレベル、クラウド配信。顧客キーとリージョナルサンドボックスを備えたシングルテナント。セルフホスト／オンプレ／エアギャップは**not documented** | デフォルトでセルフホスト。[エアギャップ](/ja/how-to/air-gap-install/)対応。データプレーンは境界を決して離れない |
| **ライセンス** | プロプライエタリ SaaS。オープンソースは**not documented** | オープンコア **AGPL**、ソース提供 — 監査可能、コンプライアンス経路に SaaS コントロールプレーンなし |
| **強制ポイント** | ネットワークレベルで、*"enforcement at the tool call and MCP server level"*とともに | エージェントランタイムでプロセス内 — [Claude Code 内の deny-closed PEP](/ja/how-to/connectors/claude-code-hooks-pep/)、加えて MCP とアクチュエーションのゲート |
| **エビデンス** | *"detailed logging keeps you audit-ready"* — 暗号的／不変の台帳は**not documented** | 追記専用、ハッシュチェーン、[Ed25519 署名付き台帳](/ja/reference/glossary/#audit-ledger監査台帳)、ボックス外で検証可能、OSCAL エクスポート |
| **ライブ介入** | Human-in-the-loop 承認／ブレークグラスは**not documented** | ライブセッションに対する [HITL 承認](/ja/reference/glossary/#approvalhitl)、[ブレークグラス](/ja/reference/glossary/#break-glass緊急昇格)、[キルスイッチ](/ja/reference/glossary/#kill-switch)、deny-closed |
| **アイデンティティモデル** | *"every agent action maps back to a human identity"* — NHI ライフサイクルは**not documented** | エージェントをファーストクラスの[非人間アイデンティティ](/ja/reference/glossary/#identity--nhi)として扱い、プロビジョニング、陳腐化ブロック、ローテーション、オフボーディングを伴う |

上記の各*"not documented"*はまさに次のことを意味します。それは私たちが読んだ WitnessAI の
ページに現れない、ということです。それは彼らの製品がその能力を**欠いている**という主張では
**ありません**——ただ、彼ら自身のサイトが述べていないことを彼らに代わって私たちが
主張することはしない、というだけです。

## 擁護可能なウェッジ：規制下・セルフホストの購買担当者

表を削ぎ落とすと、1 つの違いが荷重を担っています。WitnessAI のデータ制御はあなたのキーを
伴う**シングルテナントクラウド**であり、Olivares のそれは、あなた自身のインフラ — Linux、
Docker、Kubernetes、オンプレ、またはエアギャップ — で動く**セルフホストのコントロール
プレーン**です。必須のテレメトリはなく、デフォルトではコントロールプレーンからのエグレスも
ありません。あなたの境界を越えるのは、**あなた**がそのように設定したものだけです。具体的には、
あなたのモデル API への呼び出し、接続した SIEM／Webhook 出力、用意した場合の外部埋め込み
プロバイダーです。多くの購買担当者にとってこれらのモデルは同等です。しかし
**契約上または法律上、サードパーティのクラウドが禁じられている**購買担当者——防衛、機密、
ソブリンクラウド、特定の規制下の金融・医療——にとっては、SaaS やシングルテナントクラウドの
モデルは、機能比較が始まる前に失格となります。そして、ソース提供で、セルフホスト可能で、
デフォルトではコントロールプレーンからのエグレスがない製品こそが、調達を通過できる唯一の
種類です。

それが誠実なウェッジです。「私たちはエージェントをよりうまく統制する」ではなく、
**「クラウドをまったく使えない購買担当者のために、あなたが完全に制御するインフラ上で、
暗号的なエビデンスとプロセス内強制とともに、エージェントを統制する」**です。プロセス内 PEP と
改ざん検知可能な台帳と組み合わさることで、それはネットワークレベルの SaaS が機能を追加する
ことでは占有できない地位になります。

## WitnessAI のほうが適している場合

私たちはあなたが私たちを選ぶことより、よく選ぶことを望みます。WitnessAI はおそらく次の
場合により適しています。

- コントロールプレーンを**デプロイ・運用することなくネットワークレベルの可視性**を望み、
  シングルテナント SaaS があなたのデータレジデンシー基準を満たす場合。
- 優先事項が、一般的なエンタープライズ AI トラフィック全体にわたる**インラインで意図ベースの
  コンテンツ分類**である場合（Olivares が中心に据える、統制されたコーディングエージェントと
  改ざん検知可能なエビデンスの問題に特化したものではなく）。
- **セルフホスト、AGPL のソース提供、暗号的なエビデンス台帳、ライブセッションに対する
  ブレークグラス／HITL の要件がない**場合——それらは彼らのサイトが文書化しておらず、
  Olivares がそれを中心に構築されているものです。

Olivares がその決定に値するのは、エステートが**セルフホストまたはエアギャップ**であるとき、
エビデンスが**改ざん検知可能でボックス外で検証可能**でなければならないとき、そして強制が
エージェントの**内側に**、deny-closed で存在しなければならないとき——そのいずれもが
他社のクラウドへ越境することなく——です。

:::caution[出典と限界]
ここでのすべての WitnessAI の主張は、2026-06-21 時点で取得された彼らの公開サイト
（ホームページ、製品、開発者、コンプライアンス、制御の各ページ）から引用されています。
私たちは彼らが公開するすべてのページを読んだわけではなく、*"not documented"*は私たちが
読んだページに限定されます。マーケティングコピーはアーキテクチャ文書ではなく、製品の能力は
変化します。両者を評価しているなら、現在の状態を各ベンダーに直接確認してください——それが、
この[位置づけセクション](/ja/explanation/positioning/market-context-and-sources/)全体が
自らに課している基準です。
:::

## 関連

- [サブスクリプション認証された Claude Code と Codex を統制する](/ja/explanation/positioning/governing-subscription-authed-agents/)
  — プロセス内強制が実際にどう機能するか。
- [Olivares はあなたのゲートウェイ／Guardrails に対してどこに位置するか](/ja/explanation/positioning/where-olivares-fits-vs-your-gateway/)
  — 同じ「リクエストパスでは競合しない」という規律。
- [Olivares は IdP とどこで組み合わさるか](/ja/explanation/architecture/where-it-fits-with-your-idp/)
  — NHI モデルの背後にあるリードオンリーのアイデンティティフェデレーション。
