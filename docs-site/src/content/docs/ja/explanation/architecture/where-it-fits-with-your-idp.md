---
title: Olivares AI が IdP とどう組み合わさるか
description: >-
  Olivares AI はアイデンティティプロバイダーではありません。すでに運用しているレジストリ — Entra Agent
  ID、AWS AgentCore Identity、Google Agent Identity — からエージェントのアイデンティティをリードオンリーで
  フェデレートし、それを使ってアクセスマップに帰属させます。IdP、SSO/SCIM、SPIFFE/WIF、そして ID-JAG / XAA
  標準とどう構成されるか。
sidebar:
  order: 3
---

セキュリティアーキテクトからよく出る最初の質問は、*「これは私が運用しなければならないもう 1 つのアイデンティティシステムなのか?」* というものです。いいえ。**Olivares AI はアイデンティティプロバイダーではなく、アイデンティティを所有しません。** すでに発行しているアイデンティティを**消費**します — 人間については IdP から SSO/SCIM 経由で、エージェントについてはハイパースケーラーが一般提供したエージェントアイデンティティレジストリから — そしてそれらを使って [アクセスマップ](/ja/explanation/) の各エッジの背後にいる*誰または何*を帰属させます。このノートでは、その接合部が正確にどこに位置するかを説明します。

## レイヤリング

```
   Your IdP (Entra ID / Okta / Google)         ← humans: SSO + SCIM (unchanged)
   Agent-identity registries                    ← agents: Entra Agent ID,
     (Entra Agent ID / AgentCore / Google)        AgentCore Identity, Google Agent Identity
            │  read-only roster sync
            ▼
   Olivares AI  ── SPIFFE/WIF roster ──► R/RW access map (attributed edges)
            │                            └─ Permitted-vs-Observed drift
            └─ deny-closed gates (approvals, hooks PEP, MCP gating) — never an IdP
```

- **人間**は**あなたの** IdP を通じて認証します。Olivares AI は、オペレーターアカウントとグループからロールへのマッピングのために、標準的な **SSO と SCIM** と統合します。資格情報を保存したり、第 2 のディレクトリになったりはしません。
  → [SSO & SCIM アイデンティティ](/ja/how-to/connectors/sso-scim-identity/)
- **エージェント**は、すでに採用したレジストリからアイデンティティを取得します。Olivares AI はそれらのロスターを内部の **SPIFFE/WIF** ロスター上に**リードオンリー**でフェデレートし、観測されたすべてのアクセスを、匿名のプロセスではなく、ガバナンスされた名前付きのアイデンティティに結び付けられるようにします。

## エージェントアイデンティティフェデレーションが実際に行うこと

コントロールプレーンは、GA となったエージェントアイデンティティレジストリ向けのリードオンリーのロスターコネクタを出荷します。それぞれが一次ソースに対して検証され、**deny-closed** です (資格情報なし → 空のロスター、決して幻のエラーではない)。

- **Microsoft Entra Agent ID** — Microsoft Graph 経由でエージェントアイデンティティ、ブループリント、オーナー/スポンサーの関係をインポートします。レジストリが主張するオーファンを表面化します。長期有効なパスワード資格情報を持つブループリントは **long-lived-credential drift** の検出事項を発生させます。
- **AWS AgentCore Identity** — エージェントロスターをインポートします。サービスアイデンティティを持つエージェントはサービスアカウントのアイデンティティ種別にマッピングされます。
- **Google Agent Identity** — reasoning-engine のアイデンティティをインポートします。参照は完全な **SPIFFE ID** であるため、外部 id によって SPIFFE ロスターと収束します。

これらのマッピングは [アクセスマップの帰属](/ja/reference/glossary/#attributionconfidence) 軸 (`firm` / `approximate` / `unknown`) に供給されます — それらを再実装するわけではありません。フェデレーションは厳密にリードオンリーです。Olivares AI はリモートレジストリを**決して**変更しません。オーナーシップとオーファンのシグナルは non-human-identity ライフサイクルに転送され、レジストリが主張するオーファンが既存のガバナンス機構を通じて現れるようにします。

:::note[実験的かつ設計目標、そう明示]
クロスエコシステムのディスクリプタ (**OASF**) と **AGNTCY Agent Badges** は、検証可能な資格情報の適合性を満たすまで**実験的**として扱われます。まだプレビュー段階のロスター (例: Google の Gemini Enterprise Agent Platform) は**接合部 (seam)** として配線されており、ライブとして主張されてはいません。何が GA で、何がプレビューで、何が設計目標かを明示します — それらを曖昧にすることはありません。
:::

## ID-JAG、XAA、そして SPIFFE ベースのクライアント認証

*委任された、帰属可能な*エージェントアクセスのためのエンタープライズ標準は収束しつつあり、コントロールプレーンは独自のものを発明するのではなく、それらに乗るように構築されています。

- **ID-JAG** (Identity Assertion JWT Authorization Grant) と **XAA** (Cross-App Access) は、アプリケーションをまたいで動作するエージェントに対して IdP が**スコープ付きで帰属可能な**認可を発行するための新興パターンです — MCP 認可作業におけるエンタープライズ管理の認可拡張です。これらが定着するにつれて、帰属可能なトークンは、アクセスマップがガバナンスされたアイデンティティに結び付けられるもう 1 つの高忠実度シグナルになります。
- **SPIFFE ベースの OAuth クライアント認証** (`draft-ietf-oauth-spiffe-client-auth`) は、認可サーバーがサポートを公開した瞬間に、プレーン自身の OAuth フローが **SVID** で認証できるようにします — 既存の deny-by-default mTLS 上で。これは**設計目標**であり、ドラフトとサーバーのサポートが安定するまでは適合性の主張はありません。
- **デフォルトで短命。** エステート内で発見された長期有効な静的資格情報は、エージェントの資格情報は短命であるべきという **Five Eyes** のガイダンス (2026) に沿って、ドリフトクラスとしてフラグが立てられます。

## これがあなたにとって意味すること

- あなたは IdP、SSO、SCIM、そして標準化したいずれのエージェントアイデンティティレジストリも維持します。何も移行しません。
- Olivares AI は、それら**すべて**のアイデンティティがエステートの**観測された挙動**と出会う場所になります — 「このレジストリ由来の、この人間が所有するこのエージェントが、ポリシーが決して付与しなかったアクセスを使っている」と言える唯一のレイヤーです。
- フェデレーションはリードオンリーかつセルフホストであるため、その相関のために一律の
  データ転送を強制しません。必須のテレメトリはなく、デフォルトではコントロールプレーン
  からのエグレスもありません。あなたの境界を越えるのは、**あなた**がそのように設定した
  ものだけです。具体的には、あなたのモデル API への呼び出し、接続した SIEM／Webhook 出力、
  用意した場合の外部埋め込みプロバイダーです。

## 関連

- [Agent / Identity / NHI](/ja/reference/glossary/#identity--nhi) — 用語集の定義。
- [AI コントロールタワーとの比較](/ja/explanation/positioning/vs-control-towers/) — エコシステムの管理プレーンとの双方向統合。
