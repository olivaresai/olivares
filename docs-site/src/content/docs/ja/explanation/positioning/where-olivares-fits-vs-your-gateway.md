---
title: Olivares はあなたの AI ゲートウェイと Guardrails に対してどこに位置するか
description: >-
  あなたはすでに AI ゲートウェイ（LiteLLM、Portkey、Cloudflare）やハイパースケーラーの
  Guardrails（Bedrock、Azure）を運用しています。よいことです——そのまま使い続けてください。
  Olivares AI はゲートウェイではなく、ルーティングやキャッシュで競合しません。それらの傍らに
  位置し、それらが残す隙間を埋める、ガバナンスとエビデンスのプレーンです。
sidebar:
  order: 7
---

すでに **AI ゲートウェイ**やハイパースケーラーの **Guardrails** に投資しているなら、
誠実に最初に言うべきことはこうです。**そのまま使い続けてください。Olivares AI はそれらを
置き換えようとしていません。** ゲートウェイの仕事はモデル呼び出しです——ルーティングし、
キャッシュし、バランスし、予算をつける。Guardrails の仕事はその呼び出しに対するコンテンツ
セーフティです。どちらも現実的で、どちらも得意分野で優れており、いずれも Olivares が
そうであるものではありません。

:::tip[要点]
**Olivares AI は AI ゲートウェイではありません。** モデルトラフィックのホットパス上で
ルーティング、キャッシュ、ロードバランス、あるいはそこに居座ることはなく、これからも
決してありません。それはあなたのゲートウェイの**傍らに、そしてその背後に**、*ガバナンスと
エビデンスのプレーン*として位置します。エージェントランタイム内のプロセス内強制、改ざん
検知可能なエビデンス台帳、非人間アイデンティティのライフサイクル、そして**ライブ
セッション**に対する human-in-the-loop／ブレークグラス／キルスイッチ。あなたのゲートウェイは
*リクエスト*を統制します。Olivares は*エージェントと、それが触れるすべて*を統制し、それを
監査人に証明します。
:::

## ゲートウェイと Guardrails が得意とすること（これにはそれを使う）

これらはコモディティであり、よく理解された能力であり、ベンダーはそれを率直に記述しています。

- **AI ゲートウェイ**はモデル呼び出しのためのリクエストパス管理者です。LiteLLM は
  *"OpenAI Proxy Server (LLM Gateway) to call 100+ LLMs in a unified interface &
  track spend, set budgets per virtual key/user"*
  （[LiteLLM](https://docs.litellm.ai/docs/simple_proxy)）です。Cloudflare AI Gateway は
  *"Connect to any model, dynamically route requests, and manage usage,
  billing, and logs from one unified gateway"*を可能にします
  （[Cloudflare](https://www.cloudflare.com/products/ai-gateway/)）。Portkey は
  *"records real-time API requests, including cost"*します
  （[Portkey](https://portkey.ai/features/ai-gateway)）。ルーティング、フォールバック、
  キャッシュ、仮想キー、キーごとの予算、リクエストロギング——これが彼らのレーンです。
- **ハイパースケーラーの Guardrails** はコンテンツセーフティのフィルターです。Bedrock
  Guardrails は*"provides configurable safeguards to help you build safe generative AI
  applications"*を提供し、それは*"detect and filter undesirable content and protect
  sensitive information that might be present in user inputs or model responses"*します
  ——コンテンツフィルター、禁止トピック、ワードフィルター、PII リダクト、コンテキスト
  グラウンディングと自動推論のチェック
  （[AWS](https://docs.aws.amazon.com/bedrock/latest/userguide/guardrails.html)）。

あなたの問題が*「私のアプリに多数のモデルへの 1 つのエンドポイントを、予算・キャッシュ・
コンテンツフィルタリングとともに与える」*ことなら、そのスタックがそれを解決し、それを行うのに
コントロールプレーンは必要ありません。私たちはそのパターンと統合します。それを再実装する
ことはしません。

## それらが残すガバナンスの隙間

ゲートウェイは**リクエスト**を見ます。Guardrails は**コンテンツ**を見ます。いずれも
**エージェント**——時間をまたいだそのアイデンティティ、それがあなたのデータプレーンを横断して
何に到達したか、誰がリスクのあるアクションを承認したか、そしてそのいずれかを後で証明できるか
——を見ません。それが Olivares が埋める隙間です。

| ゲートウェイ／Guardrails が残す隙間 | なぜ重要か | Olivares AI が提供するもの |
|---|---|---|
| **エージェントランタイムでの強制** | ゲートウェイは*リクエスト境界*で強制する。それを横断しないローカルの Claude Code のツール呼び出しを止められない | エージェントにおける deny-closed の[プロセス内 PEP](/ja/how-to/connectors/claude-code-hooks-pep/)：確固たるアイデンティティのゲート、ポリシーの処理結果、ライブポリシーのオーバーレイ——すべてツールが実行される前に |
| **改ざん検知可能なエビデンス** | ゲートウェイと Guardrails は*ログ*を発する——可変のリクエスト記録。監査人は不変の証明を求める | 追記専用、ハッシュチェーン、[Ed25519 署名付き台帳](/ja/reference/glossary/#audit-ledger監査台帳)、ボックス外で検証可能、OSCAL エビデンスとしてエクスポート可能 |
| **非人間アイデンティティのライフサイクル** | ゲートウェイの「仮想キー」は予算バケットであって、プロビジョニング・帰属・ローテーション・オフボーディングされるアイデンティティではない | [NHI ライフサイクル](/ja/reference/glossary/#identity--nhi)：陳腐化 → ブロック、オフボーディングのカスケード、ローテーションのデュアルコントロール、アクセスマップに束縛 |
| **ライブセッションへの介入** | ログと予算は事後的。これら調査対象のツールはどれもセッションを途中で止められない | [HITL 承認](/ja/reference/glossary/#approvalhitl)、[ブレークグラス](/ja/reference/glossary/#break-glass緊急昇格)、そしてデュアルコントロールでの再有効化まですべての統制下アクチュエーションを拒否する[キルスイッチ](/ja/reference/glossary/#kill-switch) |
| **エステート全体のグラウンドトゥルース** | ゲートウェイはそれを通過する呼び出しだけを見る。エージェントは DB、オブジェクトストア、MCP、ファイルにも直接触れる | リードファーストな [R/RW アクセスマップ](/ja/explanation/#アクセスマップ-read-firstminimal-datapermitted-vs-observed)と Permitted-vs-Observed ドリフト、ネイティブ監査と突き合わせて裏づけ |
| **主権** | SaaS ゲートウェイとクラウド Guardrails はそのトラフィックを彼らのクラウドで処理する | セルフホスト／エアギャップ。データプレーンは境界を決して離れない |

これらのいずれもルーティング機能ではありません。それが要点です。隙間は*よりよいルーティング*
ではなく、**リクエストパスが提供するように設計されていなかったガバナンス**です。

## 特に Guardrails について：コンテンツセーフティは競合相手ではなくフック

Bedrock Guardrails は 2 通りに適用できます——Bedrock 推論呼び出し中にインラインで、または
*"directly through the `ApplyGuardrail` API without invoking the
foundation models"*で。後者は*"with any foundation model whether hosted on
Amazon Bedrock or self-hosted models"*で機能します
（[AWS](https://aws.amazon.com/bedrock/guardrails/)）。それは真に有用であり、Olivares は
コンテンツセーフティを**あなたが差し込むディテクター**として扱い、Guardrails の*代わりに*
選ぶよう求める壁としては決して扱いません。2 つの誠実で異なる事実があります。

- インライン推論プロキシは**コンテンツ検査シーム**を露出させます——コンテンツ／DLP
  ディテクターが、deny-closed の決定器が作用する評定を返す、プラグ可能なポイントです。
  コンテンツセーフティは、競合するフィルターとして再実装されるのではなく、*そこに*、
  パイプライン内に属します。
- Olivares はあなたの Guardrails の**自身の決定**をリードファーストで読みます。AWS
  コネクターは Bedrock のガードレール決定をその CloudWatch／S3 ログからポスチャとエビデンス
  として取り込みます。それは意図的に、有料の `ApplyGuardrail` ランタイムそのものを
  呼び出し**ません**。あなたのコンテンツ評定が改ざん検知可能な記録の一部になります。

ですからコンテンツセーフティは、あなたがすでに運用しているものと組み合わさります。
Guardrails が文書化*していない*もの——そしてガバナンスの隙間が開いたままになるところ——は、
エージェントの生のそれ以外の部分です。Bedrock のページはエージェントのアイデンティティ、
セッション管理、人間の承認、コストガバナンスを一切文書化していません（それらのページには
not documented、2026-06-21 確認）。Olivares はまさにその補完物です。アイデンティティ、
セッション制御、承認、エビデンスを担います。コンテンツフィルターはすでにある場所にとどまります。

## それらがどう組み合わさるか

健全な配置は、すべてのツールをそのレーンにとどめます。

- **あなたのゲートウェイを使い続ける**（LiteLLM／Portkey／Kong／Cloudflare）。モデル呼び出しの
  プレーンとして——リクエストに対するルーティング、キャッシュ、仮想キー、予算。
- **あなたの Guardrails を使い続ける**（Bedrock／Azure Content Safety）。コンテンツ
  セーフティのディテクターとして——Olivares の PEP はそのコンテンツ検査シームでプラグ可能な
  ディテクターを実行し、あなたの Guardrails の自身の決定をリードファーストでエビデンスとして
  読みます。それは `ApplyGuardrail` そのものを呼び出しません。
- **その傍らに Olivares を加える**。ガバナンスとエビデンスのプレーンとして——あなたの
  ゲートウェイに決して当たらないエージェント上のプロセス内 PEP、エステート全体にわたる
  アクセスマップ、改ざん検知可能な台帳、そしてライブの HITL／ブレークグラス／キルスイッチ
  制御。

Olivares が推論に触れる唯一の場所は狭く明示的です——生の SDK/`curl` 呼び出し側のための
**API キー専用**のゲートウェイ経路であり、[サブスクリプション認証されたエージェントを統制する](/ja/explanation/positioning/governing-subscription-authed-agents/)で
記述されています。それは、あなたの他のツールが到達できないトラフィックを統制するために存在し、
ルーティングで彼らと競合するためでは決してなく、そして**決して**サブスクリプション資格情報を
運びません。

## あなたのゲートウェイで十分な場合

誠実さは双方向に働きます。あなたのエージェントが常にあなたのゲートウェイ**を通じて**のみ
モデルを呼び出し、あなたのコンテンツセーフティのニーズが Guardrails で満たされ、データベース
／オブジェクトストア／MCP に直接到達する**セルフホストまたはラップトップ常駐のエージェントが
ない**、そして**主権や改ざん検知可能なエビデンスの要件がない**——その場合、あなたの
ゲートウェイとそのログと Guardrails があなたに必要なすべてかもしれず、それ自体のために
コントロールプレーンを加えるべきではありません。

Olivares がその地位を得るのは、問いが*エステート全体かつ敵対的（adversarial）*になるときです。
どのエージェントが存在し、それぞれが実際に何に到達したか、悪意あるアクションを
**エージェントにおいて** deny-closed で止められるか、誰がリスクのあるものを承認したか、そして
監査人に**不変の証明**を手渡せるか——そのすべてを、他者のクラウドへその全体像を送ることなく。
隣接する 2 つの比較のより深い扱いについては、[vs AI コントロールタワー](/ja/explanation/positioning/vs-control-towers/)と
[vs LLM ゲートウェイ・オブザーバビリティ](/ja/explanation/positioning/vs-llm-observability/)を
参照してください。
