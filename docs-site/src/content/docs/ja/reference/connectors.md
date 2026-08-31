---
title: コネクタカタログとカバレッジ階層
description: >-
  control plane が現在配線できるファーストパーティコネクタを、それぞれがサポートする正直な
  カバレッジ階層（clean、lossy、impossible-passively、cooperative、approximate-by-attribution）
  ごとに分類したもの。出力先も含む。
---

このページはファーストパーティコネクタの**カタログ**であり、それぞれについて、サポート可能な
**正直なカバレッジ階層**を示す。これはコネクタの*モデル*（observe-only、minimal-data、3 種類の
観測種別）を説明する [ソースを接続する](/ja/how-to/connect-a-source/) の姉妹編なので、まずそちらを
読んでほしい。本ページはその次の問い、つまり*どのソースが存在し、それぞれのシグナルはどれほど良質
なのか*に答える。

カバレッジは、私たちがそうあってほしいと願う度合いではなく、常に**システムの監査面が正直に何を
教えてくれるか**によって**階層化**される。本ドキュメント全体で用いる階層は次のとおり。

- **Cooperative（協調的）** — 自分が何をしたかを報告するエージェントまたはプラットフォーム
  （OpenTelemetry、ベンダーの admin API）。*存在すれば*最高の忠実度を持つが、ソースが協調する
  ことに依存する。
- **Clean（クリーン）** — 読み取りと書き込みを**ネイティブに**分類し、それ自身の監査証跡から逐語的に
  取得するストア（SQL 監査、オブジェクトストア／ウェアハウスのデータアクセスログ）。
- **Lossy（劣化あり）** — 監査が読み取りと書き込み、あるいは呼び出し元同士を明確に分離できないストア
  （ドキュメントストア、リネージ）。エッジは得られるが、しばしば `approximate` になる。
- **Impossible passively（受動的には不可能）** — 利用可能な受動的監査面を持たないシステム
  （インメモリキャッシュ、組み込み型の単一ファイルデータベース）。正直な read-first シグナルは存在せず、
  本製品はそうであるかのように振る舞わない。
- **Approximate-by-attribution（帰属による近似）** — アクセスは実在するが、その帰属が解決済みの
  エージェントではなくロール、プロセス、または共有クレデンシャルに対してなされるため、エッジは
  `approximate` になる。
- **Untrusted hint（信頼できないヒント）** — 宣言された能力（MCP のツールアノテーション）。
  裏付けは取るが、それ単独では決して信頼しない。

:::caution[このカタログが反映するもの: 現在のビルドに配線されたコネクタ]
これは現在**デフォルトバイナリのコネクタセットに登録された**コネクタ、つまり
`OLIVARES_SOURCES_CONFIG` で名前を指定してエンジンに配線させられる種別を列挙したものである。
本製品は 1.0 以前である。正準の R/RW access map コネクタ —— **pgAudit**、
**S3/CloudTrail**、**eBPF/Tetragon** のバックストップ、**runtime** インベントリ、**MCP**
イントロスペクション —— と**ナレッジドキュメントソース**は、いずれも標準の `serve` で配線済みかつ
設定可能になった。一部は下記の [デプロイ要件](#デプロイ要件と正直な帰属)
で扱う**デプロイ要件**（Tetragon センサー、ホストアクセス）を伴う。カバレッジは**正直に階層化**
される。あるコネクタがここに存在することは、確固たるエージェント単位の帰属の主張ではない。それは
依然として難しい依存事項であり続ける（共有アカウントは clean 階層のストアですら `approximate` に
崩壊させる）。
:::

## Cooperative — Claude とベンダーテレメトリ

存在すれば最高忠実度のソース。Claude Code ランタイムソースは組み込みプラグインとして
**プロセス外**で動作する（素の開発ビルドではこれが省かれ、ブートは健全に見せかけるのではなく
正直に警告する）。

| Kind | 観測対象 | 備考 |
|---|---|---|
| `claude` | Claude Code OTLP ツールテレメトリ + MCP イントロスペクション → エッジ / コスト / findings | プロセス外プラグイン。エージェント単位のアイデンティティが存在すれば `attributed`、なければ `approximate` |
| `claude-api` | Claude Admin-API のコストサンプル + ガバナンス姿勢の findings | プロセス内。オフライン時（admin key なし）は no-op |
| `claude-compliance` | Claude Compliance アクティビティフィードの証跡 → findings | 構造上 GET のみ。オフライン時は no-op |
| `claude-config` | 静的な Claude 設定ツリー（subagents / Skills / plugins）→ **宣言された能力**のエッジ | メタデータのみ —— 能力面であって、観測されたアクセスではない |
| `claude-console` | Claude org IAM → SSO/SCIM 姿勢の findings（アイデンティティ名簿 + ソース） | |
| `claude-wif` | Anthropic の非人間アイデンティティ／ワークロードアイデンティティ名簿 + 許可スコープのエッジ | オペレーターが宣言したフェデレーションをモデル化し、静的キーの落とし穴を警告する |
| `claude-managed-agents` | Claude managed agents のインベントリ + thread events（webhook receiver + GET pollers） | ストリーミングソース（`poll_seconds: 0`）。オフラインでは no-op |
| `claude-projects` | Claude Organization Projects のインベントリ（メンバーシップ / API キー）+ オペレーター宣言のプロジェクトポリシー | 読み取り専用 Admin API。オフラインでは no-op |
| `claude-apps-gateway` | Claude apps gateway の姿勢、宣言された model grants、audit event ingest → トポロジ + findings | 既存の `gateway.yaml` と任意の JSONL audit export を読む |
| `claude-batch` | Anthropic Message Batches + Files API のインベントリ、batch policy enforcement、upload retention expiry | payload も file content も決して読まない。admin key がない場合は正直な offline finding |
| `claude-routines` | Claude Code Routines（スケジュール済みトリガー）のインベントリ → edges + cadence/review findings | GET のみ。prompt content は hash 化だけされる。ストリーミング（`poll_seconds: 0`） |
| `cowork` | Claude Cowork OTLP/HTTP logs receiver → activity evidence | プロセス外プラグイン（OTel-proto 依存を分離） |
| `cowork-analytics` | Claude Cowork エンゲージメント分析 | プロセス内（modelprovider client のみ） |
| `codex` | OpenAI Codex のコストサンプル、usage/auth/admin-audit 証跡、adoption findings | 読み取り専用 Admin API。営業経由で制限される面は posture finding に劣化する |
| `cursor` | Cursor Admin API の請求コスト、team audit logs、member inventory、budget posture | プラン制限の 403/404 は finding に劣化し、決して失敗しない |

### ベンダー中立な GenAI フレームワークプロファイル（`gen_ai.*`）— オプトイン

カタログが約束するエージェントフレームワーク —— **LangGraph / LangChain、CrewAI、
AutoGen / Microsoft Agent Framework、Google ADK**（さらに OpenAI SDK、LlamaIndex、
Pydantic-AI、Strands、…）—— は Claude の `claude_code.*` スキーマを**出力しない**。それらは
[OpenTelemetry の **GenAI** セマンティック規約](https://github.com/open-telemetry/semantic-conventions-genai)
（`gen_ai.*`）に収束する。同じ `claude` ソースがこのプロファイルも取り込むため、OTel で計装された
フリートはフレームワークごとの専用コネクタではなく単一の取り込みを通じて **access map** と
**FinOps** に供給される —— 最もレバレッジの高い統合である。

**このプロファイルはオプトインであり、正直に experimental と表示される。** `gen_ai` 領域全体は
OpenTelemetry の **Development** ステータス（Stable ではない、2026 年 6 月時点）であるため、
仕様自身のゲートをミラーしたときにのみ有効化される。コネクタの `semconv_opt_in` を、トークン
`gen_ai_latest_experimental` を含むカンマ区切りリストに設定する（`OTEL_SEMCONV_STABILITY_OPT_IN`
をミラーする）。デフォルトでは無効であり、`gen_ai.*` シグナルは沈黙ウォッチドッグには供給されるが
エッジ／コストはマップしない —— 規約が持たない安定性を私たちが主張することは決してない。

規約が変動の途上にあるため、取り込みは**デュアルネーム**（現在のキー*と*、いまだ実地で出力され続けて
いる非推奨の前身の両方を読む）かつ**マルチシグナル**（トレース**スパン**、
`gen_ai.client.inference.operation.details` ログ**イベント**をマップし、クライアント**メトリクス**を
認識する）である。

| 読み取る対象 | 現在のキー | 受け入れ済み（非推奨、依然として出力するもの） |
|---|---|---|
| Provider | `gen_ai.provider.name` | `gen_ai.system`（v1.36.0 以前のデフォルト。**Google ADK**、例: `gcp.gemini`） |
| 入力トークン | `gen_ai.usage.input_tokens` | `gen_ai.usage.prompt_tokens`（**OpenLLMetry/Traceloop** → LangChain/LangGraph/CrewAI） |
| 出力トークン | `gen_ai.usage.output_tokens` | `gen_ai.usage.completion_tokens`（同上） |

| gen_ai 属性 | マップ先 | 確信度 |
|---|---|---|
| `gen_ai.usage.*`（トークン） | `CostSample`（provenance は **estimated** —— 請求コストではなくトークン） | — |
| `gen_ai.provider.name` / `request.model` / `response.model` | コストの provider + model（response を優先） | — |
| `gen_ai.operation.name = execute_tool` + `gen_ai.tool.name` | agent→tool の**アクセスエッジ**（mode `unknown`） | `attributed` |
| `gen_ai.conversation.id` + `gen_ai.agent.{name,id}` | conversation→agent の**帰属エッジ** + セッション参照 | `attributed` |

#### サポートする方言マトリクス（複数世代の正規化器）

GenAI 規約は、実際の 2026 年のフリートで**共存する 3 つの世代**にわたって変化した。取り込みは
世代固有のマーカーから**シグナルごとに**世代を検出し、正規化したイベントに対応する semconv ピンを
刻印する（`genai.semconv` 姿勢 finding が実行ごとに有効なセットを記録し、実行ごとに 1 件の info
`drift` finding が見つかった各**非推奨**方言にフラグを立てるため、どのフリートが計装のアップグレードを
必要としているか分かる）。メッセージの**内容はどの世代からも決して読まれない** —— 内容キーは方言
マーカーとしてのみ機能する（minimal-data の姿勢）。

| 検出された方言 | 刻印されるピン | 排他マーカー（検証済み） | 出力するもの（2026 年 6 月検証済み） |
|---|---|---|---|
| レガシー **OpenLLMetry/Traceloop**（semconv 以前） | `openllmetry` | インデックス化された `gen_ai.prompt.{i}.*` / `gen_ai.completion.{i}.*`、`gen_ai.usage.prompt_tokens`/`completion_tokens`、`llm.usage.total_tokens`、`llm.request.type`、`llm.vendor`、`traceloop.span.kind` | **openllmetry v0.55.0 未満**（2026-03-29 リリース）にピン留めされた Traceloop 計装の LangChain / LangGraph / CrewAI。大文字始まりの provider（`OpenAI`、`Langchain`）は小文字化され、FinOps が大文字小文字で分割しないようにする |
| **v1.36 以前のイベント**（仕様自身の名前） | `1.36.0` | `gen_ai.system`、メッセージごとの 5 つのログイベント `gen_ai.{system,user,assistant,tool}.message`、`gen_ai.choice`（**名前で**認識される —— 唯一の属性はオプション） | Google ADK の LLM スパン（`gcp.vertex.agent`）、AutoGen（`autogen`）、Microsoft Agent Framework —— いずれも依然として `gen_ai.system` を出力する |
| **v1.37 以降のメッセージ**（現在） | `1.41.1` | `gen_ai.provider.name`、`gen_ai.input.messages` / `gen_ai.output.messages` / `gen_ai.system_instructions`、`gen_ai.client.inference.operation.details` イベント、`gen_ai.workflow.name` | OTel 公式計装、openllmetry **v0.55.0 以降** |

世代をまたいで名前が同一のキーのみを持つシグナル（例: ADK の `invoke_agent` スパン: operation +
agent + conversation、provider キーが一切ない）は現在のピンの下で正規化される —— 適用される
マッピングはバイト単位で同一であり、生産者の真のリリースは wire からは知り得ないからである。

#### MCP 規約（`mcp.*`、semconv v1.39 — Development）

上流には正確に 4 つの `mcp.*` 属性が存在する（`mcp.method.name`、`mcp.protocol.version`、
`mcp.resource.uri`、`mcp.session.id`）。ツールは `gen_ai.tool.name` に、プロンプトは
`gen_ai.prompt.name` に乗る。取り込みは、Claude パスが出力するのと同じリソース種別を再利用して、
これらのトレースを本製品自身の MCP ガバナンス事実と結合する。

| MCP シグナル | マップ先 |
|---|---|
| `server.address` を持つ任意のクライアント側 `mcp.*` スパン | session→`mcp.server` エッジ（`claude_code.mcp_server_connection` エッジと結合） |
| `tools/call` + `gen_ai.tool.name` | `mcp.tool` アクセスエッジ（エンドポイントが既知なら `server.address/tool`）—— Claude の `mcp__server__tool` 呼び出しと同じ種別 |
| `resources/read` / `resources/subscribe` + `mcp.resource.uri` | **read-mode** の `mcp.resource` エッジ（URI はサニタイズ: クレデンシャル／クエリを除去） |
| `prompts/get` + `gen_ai.prompt.name` | **read-mode** の `mcp.prompt` エッジ（プロンプト面） |
| SERVER 種別のスパン / `mcp.client|server.*.duration` メトリクス | liveness のみ（クリーンな劣化 —— サーバーのビューはエージェントアイデンティティを帰属しない） |

#### エージェントスパン（`invoke_agent` の client/internal 分割 + `invoke_workflow`、semconv v1.41 — Development）

v1.41.0 は `invoke_agent` を **CLIENT** バリアント（リモートのエージェントサービス）と
**INTERNAL** バリアント（プロセス内）に分割した。現実のフレームワークは今日その種別に違反している
（AutoGen と Microsoft Agent Framework はプロセス内エージェントに CLIENT をハードコードし、
Google ADK は INTERNAL を使う）ため、取り込みは、スパンが CLIENT であり**かつ** `server.address` を
持つときにのみ呼び出しを**リモート**として分類する —— それが conversation→`genai.agent.remote` の
委任エッジを生む。それ以外はすべて、conversation→`genai.agent` 帰属エッジでカバーされるプロセス内
呼び出しのままである。すなわち、捏造された「リモート」ではなく、クリーンに劣化する。`invoke_workflow`
（v1.41 で新規。CrewAI 形式の crew）は conversation→`genai.workflow` エッジをマップする。エージェント
スパンは上流で **Development**（experimental）のままであり —— いかなる安定性も主張されない。

**安定 vs experimental、正直に:** **メカニズム**（オプトインゲート、方言検出 + デュアルネーム読み取り、
span/event/metric マッピング、封印された `CostSample`/`EdgeObservation` の形状）は本製品では安定して
いる。それがマップする**語彙**（`gen_ai.*`/`mcp.*` キー、operation の enum）は上流で **Development**
であり、再びリネームされる可能性がある。だからこそ取り込みは 1 つにピン留めするのではなく、あらゆる
世代を正規化する。v1.41.1 は gen-ai 規約の最後の*バージョン付き*リリースである（それらは
`open-telemetry/semantic-conventions-genai` に移行し、そこには 2026 年 6 月時点でリリースがない）。
注記:

- **コストは W3C span id によって重複排除される。** ある operation がそのスパン*と*その
  `operation.details` イベントの*両方*に usage を報告した場合（それらは span id を共有する）、
  二重ではなく一度だけ課金される。
- **メトリクスは liveness に供給され、決してコストにはならない。** `gen_ai.client.token.usage` は
  集計値である。スパン／イベントが operation 単位の権威ある usage であるため、メトリクスも課金すると
  二重計上になる。v1.39 の `mcp.*` duration ヒストグラムも同様に認識される。
- **Provider は `unknown` でありうる。** スパンが model を持つが provider/system を持たない場合、
  コストは model id から推測されるのではなく `unknown` に帰属される。
- **合計のみのトークン数は分割されない。** prompt/completion の分割を持たないレガシーな
  `llm.usage.total_tokens` は決して input/output に推測されない（コストを捏造しない）。
- **OpenInference（Arize/Phoenix）は別の規約**であり、このプロファイルでは*取り込まれない* ——
  ここで読む `llm.*` キー（`llm.request.type`、`llm.usage.total_tokens`、`llm.vendor`）は
  **OpenLLMetry のレガシーマーカー**であって、OpenInference の `llm.*` 名前空間ではない。

## Cooperative — ローカルなエージェント面の設定

これらのソースはローカルエージェントが宣言した設定を読み、**permitted** エッジと
posture findings を出力する。実行のライブトレースではない。フレームワークがネイティブ OTEL を持つとき、
ライブ使用状況は引き続き上記の `gen_ai.*` ingest を通じて到着する。

| Kind | 観測対象 | 正直なカバレッジ |
|---|---|---|
| `opencode` | ローカルな `opencode.json` / `opencode.jsonc` JSONC レイヤー → permission posture、managed/admin override posture、MCP/tool/custom-agent permitted edges、credential-in-config/share/autoupdate/OTEL findings、authoring fragment | 設定宣言のみ。managed layer はローカルで検出されるが、不変のロックではない。runtime `OPENCODE_PERMISSION`、test directory redirection、remote organization config はこの reader の範囲外である。有効化時は native OTEL が out-of-band `OTEL_*` exporter 経由で live `gen_ai.*` usage を供給できる |
| `gemini-cli` | Gemini CLI `settings.json` レイヤー（system/user/workspace）→ permitted MCP/tool edges、enforcement-gap posture、effective-config inventory | 設定宣言のみ。ライブ使用は `gen_ai.*` ingest に乗る（CLI がネイティブに出力する）。Gemini API ではない（それは hosted-provider 面） |
| `openhands` | OpenHands `config.toml` + env → sandbox/model-pinning/credential/telemetry posture、permitted MCP/action edges | 設定宣言のみ。native OTEL `gen_ai.*` 経由のライブ使用 |
| `goose` | Goose (Block) `profiles.yaml` + env → admin-settings/model-pinning/extension/tool-approval posture、permitted extension edges | 設定宣言のみ |
| `cline` | Cline / Kilo Code VSCode `settings.json` 名前空間 → auto-approve/MCP-allowlist/credential/model-pinning posture | 設定宣言のみ。上流にネイティブ OTEL はない |
| `grok` | Grok Build (xAI) —— ローカル設定から読むターミナル用コーディングエージェント: hook 配線、文書化された veto 付き events、宣言可能な governance posture | **xAI API コネクタではない**（`xai` はカタログとコストを読み、モデルに `grok-build-0.1` を含む）。これはエージェントを読み、両者は重複しない。観測側は Grok Build がすでに出力する OTLP ingest を通る。`PostureEnforced` を主張するのは、文書化された veto を持つ唯一の event `PreToolUse` だけで、その他は `observed` |
| `openclaw` | OpenClaw `openclaw.json`（JSON5 discovery、制限された `$include`）→ エージェントごとの gateway/channel/tool/sandbox/skill/model posture、宣言 channel/skill/model edges | 設定宣言のみ。上流で inline PEP hook は検証されていない |
| `hermes` | Hermes Agent `config.yaml` + profile trees + managed scope → terminal/channel/skill/security/model/MCP posture、宣言エッジ | 設定宣言のみ。上流で inline PEP hook も native OTEL も検証されていない |
| `google-adk` | export された Google ADK 2.0 Session JSON → agent/app inventory、sub-agents、tool function calls、transfers、approved-tool drift、Vertex `reasoningEngine` correlation | 読み取り専用 export。message content は決して読まない。`google-agent` platform 面とは別 |
| `agents-md` | agent instruction files（AGENTS.md とエージェント別の memory/instruction files）の repo walk → SHA-256 baseline drift + instruction-injection / hidden-Unicode / secret scan | minimal-data: sanitized paths + hashed details。content は決して読まない |
| `mcpb` | install/distribute された `.mcpb` desktop extensions → manifest posture scan、enterprise allowlist drift、PKCS#7 signature verification | extension 面の PERMITTED-vs-OBSERVED |
| `codex-managed-config` | OpenAI Codex managed-config files → enforcement posture + authored baseline との drift | observation-only: 開発者が managed layer を迂回することを防げない（Codex 用の `managed-settings` 対応物） |

## Clean — ネイティブストア監査（逐語的な読み取り/書き込み）

これらはストア**自身**の監査証跡を読み、読み取り/書き込みの分類を逐語的に取得する —— クエリテキストから
推論することは決してない。`pgaudit` と `s3cloudtrail` は [access map](/ja/reference/modules/iii-access-map/)
が構築される中心となる正準の R/RW ソースである（ハイフン付きの `pg-audit` / `s3-cloudtrail` エイリアスも
解決される）。

| Kind | 観測対象 |
|---|---|
| `pgaudit` | PostgreSQL **pgAudit** 証跡（csvlog/jsonlog）→ R/RW テーブルアクセス、pgAudit の CLASS から `READ`/`WRITE` を逐語的に取得 |
| `s3cloudtrail` | AWS **CloudTrail** S3 イベント → オブジェクト R/RW、CloudTrail の `readOnly` フラグから読み取り/書き込みを取得（Claude-on-Bedrock のモデル呼び出しも表面化する） |
| `snowflake-audit` | Snowflake ネイティブのアクセス履歴 |
| `databricks-uc` | Databricks Unity Catalog 監査 |
| `bigquery-audit` | BigQuery データアクセス監査 |
| `redshift-audit` | Amazon Redshift 監査 |
| `mssql-audit` | SQL Server 監査 |
| `oracle-audit` | Oracle 統合監査 |
| `gcs-audit` | Google Cloud Storage データアクセス監査 |
| `azure-blob-audit` | Azure Blob Storage 監査 |

## Cloud management plane — org/tenant インベントリ + control-plane アクティビティ

**管理**プレーンのトライクラウド対応 —— 上記のストア監査コネクタがカバーするリソースごとの
**データ**プレーンとは別物である。それぞれがクラウドの org/tenant control plane に対するライブの
**読み取り専用** API クライアントである。リソースの**トポロジー**を発見し（インベントリエッジ、
`mode=unknown`、attributed）、クラウドのネイティブな**監査フィード**を読んで control-plane の
**アクティビティ**を取得する（`identity→…api` エッジ、読み取り/書き込み分類済み）。これらは AWS が
すでに `s3cloudtrail`（データプレーン）とアカウントレベルの IAM/CloudTrail `aws` コネクタで支える
マトリクスを完成させる。両者とも**プロセス内**で動作し、**オフラインセーフ**である（クレデンシャルが
なければ Gather は no-op）。両者とも control plane のみを観測する —— ペイロード、シークレット、キー、
リソースプロパティを決して観測しない。

| Kind | 観測対象 | 正直なカバレッジ |
|---|---|---|
| `gcp-audit` | GCP **Resource Manager / IAM**（org→folder→project→service-account トポロジー）+ **Cloud Audit Logs**（Admin Activity + Data Access）→ `identity→gcp.api` | ログがある箇所では **Clean**: Admin Activity はログ種別の定義により書き込み、Data Access は標準メソッド動詞により読み取り/書き込み。Data Access ログが無効（GCP ではデフォルトで無効）の箇所、またはメソッド動詞が非標準（`unknown`、決して推測しない）の箇所では **Lossy**。宣言された共有プリンシパルは `approximate`。`principalEmail` は SPIFFE/SA 名簿に収束する |
| `azure-activity` | Azure **Resource Graph**（tenant→subscription→resource トポロジー）+ **Azure Monitor Activity Log**（control-plane operations）→ `identity→azure.api` | control-plane の書き込み/削除は **Clean**（RBAC アクションから逐語的）。汎用の `action` サフィックスは **Lossy**（`unknown` —— 読み取りも書き込みもありうる）。データプレーンの**読み取りは Activity Log に含まれない**（`azure-blob-audit` / `azurekeyvault` データプレーンがそれらをカバーする）。共有呼び出し元は `approximate`。呼び出し元の `objectId`/`appId` は Entra 名簿に収束する |
| `cloudflare` | Cloudflare edge estate —— REST API v4 経由の **Workers、R2 buckets、Logpush jobs** → topology edges | inventory のみ（この connector に audit feed はない）。スコープ済み読み取り専用 token。`cloudflare-ai-gateway` / MCP-portals AI 面とは別 |

GCP の **Data Access** オプトインと Azure の **read-not-logged** ギャップは、このプレーンの正直な
**opaque** なエッジである。これらのログが無効な箇所では、アクティビティエッジの不在はアクセスが
なかった証明にはならない。クラウドごとの完全な階層テーブルは、同梱の
cloud-management コネクタ契約 `docs/contracts/S165-connectors-cloud-management.md` にある。

## Hosted model providers —— カタログ、姿勢、メータリング

これらのソースはホスト型 model-provider のアカウントとカタログをガバナンスする。推論は
proxy **しない**。プロバイダーに利用可能な usage API がない場合、費用は集約 billing feed から
取り出すのではなく、推論パスを囲むコネクタの `Meter` で見積もられる。

| Kind | 観測対象 | 正直なカバレッジ |
|---|---|---|
| `openai` | OpenAI platform の利用量とコスト（org API）+ model/API-key catalog | 読み取り専用 org/admin key。data-plane payload なし。OpenAI-org path ではなく実際の Azure 面を扱う `azure-openai` とは別 |
| `gemini` | Gemini（Google）ホスト型 model catalog + オペレーター配線の usage export | hosted-provider 面。local CLI settings を観測する `gemini-cli`、enterprise Vertex 面をカバーする `vertex` とは別。Google はこのパスで aggregate usage API を公開しないため、usage はオペレーターが配線したもの |
| `deepseek` | DeepSeek hosted catalog、account balance availability、PRC sovereignty posture | aggregate usage API なし。宣言された価格から推論を囲んで cost を計測 |
| `mistral` | Mistral catalog + governance posture | public usage/billing/spending-cap API なし。list price から推論を囲んで cost を計測 |
| `xai` | xAI/Grok live catalog、billing endpoints、key/ACL inventory、credit/spending-limit posture | 読み取り専用 management billing endpoints を cost に使う。management と inference credentials は別 |
| `glm` | Zhipu GLM / Z.ai declared catalog、USD list-price `Meter`、entitlement probe、sovereignty posture | catalog-only + Meter: GLM には検証済み usage/billing/balance/admin/key/organization API がない。PRC nexus / Entity List の注意事項は `z.ai` と `bigmodel.cn` の両面に適用 |
| `vertex` | Google Vertex AI catalog、model ごとの token usage（Cloud Monitoring）、opt-in billed cost（billing export）、opt-in Model Armor safety posture | AI Studio パスがカバーしない enterprise Google 面。GCP に real-time cost API はない |
| `azure-openai` | Azure OpenAI / AI Foundry deployments + models（ARM）、Azure Monitor token usage/cost 面 | 読み取り専用 management-plane client。data-plane payload なし |
| `openrouter` | OpenRouter live catalog（USD/MTok pricing）、account usage/limit posture、approved-model policy drift | exported `MeterCall` 経由の billed cost。オフラインでは no-op |
| `cohere` | Cohere live model catalog（cursor-paginated Models API） | public usage/billing/org API なし（dashboard のみ）—— 正直な coverage caveat。list price から推論を囲んで cost を計測 |
| `fal` | fal.ai API-key lifecycle inventory + rotation posture、queue API を囲む cost metering | public usage/audit API なし —— governance は key lifecycle による。深い面は営業経由に限定され UNVERIFIED と表示 |

## Self-hosted inference —— ローカルカタログと使用状況

Self-hosted inference は常にスコープ内であるため、gateway の後付けではなく第一級のソースとする。
この階層はローカル runtime が実際に何を提供しているかを観測する。

| Kind | 観測対象 | 正直なカバレッジ |
|---|---|---|
| `local` | Ollama model catalog（`/api/tags`）、**Ollama residency（`/api/ps`）** —— 現在ロードされているモデルと GPU/CPU 分割・unload deadline ——、OpenAI 互換面での vLLM token usage | residency は posture として報告され、severity は PLACEMENT である。完全に VRAM 上の model は informational、CPU 上または CPU/GPU に分割された model は、オペレーターが知らされずに latency を負担するため flag される。Ollama は aggregate token metrics を公開せず metering に寄与しない。この source から local inference の per-call identity/policy は依然得られず、governance には gateway または OTel path が必要。localhost の Ollama は credential 不要なため、空の config が動作する read-only default。server の無効化は明示的な空 URL であり、両方が空なら no-op |

## Kernel backstop — eBPF / Tetragon（clean シグナル、approximate 帰属）

moat の**非協調的**な半分。協調パスがエージェントが*報告する*ものを見るのに対し、これはカーネルが
*行った*ことを見る —— ファイルの読み取り/書き込みと外向き接続を、エージェントが自身のテレメトリを
無効化したときでさえ見る。**アクセス**はカーネルの ground-truth である（*何が起きたか*の clean 階層の
シグナル）。**帰属**はその限界について意図的に正直である —— カーネルはランタイムアイデンティティ
（プロセス/cgroup/コンテナ）に帰属し、解決済みのエージェントには決して帰属しないため、すべての eBPF
エッジは `approximate` である。ペイロードを復号も検査も決してしない（TLS の本文には盲目である）。

| Kind | 観測対象 | 正直な限界 |
|---|---|---|
| `ebpf` | Tetragon カーネルイベント → ファイル R/RW（`MAY_*` マスク）とネットワークエッジ。エージェントが協調的テレメトリなしにカーネルで動作したときのオプションの回避防止 finding | エージェント匿名 → 常に `approximate`。エージェント単位の台帳ではなくストリーミングバックストップ |

これは eBPF プログラムを自身でロードは**しない**。カーネルキャプチャは
[Tetragon](https://tetragon.io/)（独立した堅牢化された DaemonSet）が行う。
[デプロイ要件](#デプロイ要件と正直な帰属) を参照。

## Lossy — エッジは得られるが、しばしば approximate

| Kind | 観測対象 | なぜ lossy か |
|---|---|---|
| `mongo-audit` | MongoDB 監査 | ドキュメントストア。呼び出し元の分離が弱い |
| `openlineage` | OpenLineage 実行イベント → データセットリネージ | リネージは呼び出し単位の監査ではない |
| `delta-sharing` | Delta Sharing 受信者アクティビティ | 共有受信者の帰属 |

## Approximate-by-attribution & 許可側ソース

これらは**許可**側（宣言された grant）か、解決済みのエージェントではなくロール／プロセス／共有
クレデンシャルに帰属するアクセスのいずれかを出力する。

| Kind | 観測対象 | 階層 |
|---|---|---|
| `iceberg-catalog` | Iceberg REST カタログ → 許可された grant + vend されたクレデンシャルのアイデンティティ | permitted |
| `inference-gateway` | K8s Gateway API Inference-Extension のルーティング → 許可された推論ルート | permitted |
| `aws-kms` / `gcp-kms` / `azure-key-vault` | クラウド KMS 監査 → キーアクセスエッジ（キー材料は決して含まない） | approximate |
| `external-secrets` / `sops` / `kmip` | シークレット管理マニフェスト / KMIP locate → プロビジョニング/カストディエッジ | approximate（存在であって、使用ではない） |
| `istio-telemetry` | Istio Telemetry CRD → L7 メッシュエッジ | approximate（パースされた CRD であって、ライブフローではない） |
| `egress-proxy` | Egress プロキシの判定ログ → L7 egress エッジ | approximate |
| `kong-audit` | Kong 監査ログ → 設定変更の findings | approximate |
| `ai-gateway` | Envoy AI Gateway の使用記録 → **コスト**サンプル（FinOps） | コストストリーム |
| `github` | エージェントのデータソースとしての GitHub repositories → observed R/RW access edges（webhook-first、API-poll reconciliation）+ permitted ACL edges | observed + permitted。ストリーミング（`poll_seconds: 0`） |
| `gitlab` | GitLab repositories → observed R/RW access edges + permitted ACL edges | observed + permitted。ストリーミング（`poll_seconds: 0`） |

## Posture observers — アクセスエッジではなく findings

姿勢（同期/健全性/ドリフト、認証異常）を findings として表面化する read-first オブザーバー。estate を
決してミューテートしない。

| Kind | 観測対象 |
|---|---|
| `runtime` | AI ワークロードが動作する場所（Linux procfs、Docker デーモン、Kubernetes API）→ コンテインメントエッジ + 健全性 findings（ホストアクセスが必要 —— [デプロイ要件](#デプロイ要件と正直な帰属) を参照） |
| `argocd` / `flux` / `crossplane` | GitOps / control-plane CRD → 同期、健全性、ドリフト、コンポジション姿勢 |
| `kerberos` | KDC 認証テレメトリ → Kerberoasting findings |
| `aaa` | RADIUS / TACACS+ AAA 観測 |
| `ssf` | Shared-Signals / CAEP レシーバー（エージェント kill-switch） |
| `edugain` / `openidfed` | フェデレーション集約 / OpenID-Federation 信頼チェーン → フェデレーション姿勢 |
| `managed-settings` | Claude `managed-settings` ポリシー → 許可エッジ + ドリフト findings |
| `envoy-ai-gateway` | Envoy AI Gateway **宣言済み設定** export → gateway posture + gateway-vs-Olivares policy drift（`ai-gateway` usage stream の config 側） |
| `kong-agent-gateway` | Kong agent-gateway 宣言済み設定 export → posture + policy drift |
| `litellm` | LiteLLM proxy 宣言済み設定 export → posture + policy drift |
| `bedrock-kb` | Amazon Bedrock Knowledge Bases の retrieval health/config（Agent Runtime Retrieve health-check）→ KB ごとの posture findings + KB→data-source edges。`RetrieveAndGenerate` は決して行わず（課金推論なし）、完全な document content も決して読まない |
| `tak` | TAK Server `CoreConfig.xml` posture（+ 任意の mTLS probe）と、govern された minimal-data Cursor-on-Target ingest（position は digest、uid は hash 化） |
| `a2a` | Agent2Agent (A2A) v1.0 peers → Agent Card discovery + JWS/JCS signature verification（peer trust level）と、observed task/message interactions の agent↔agent edges。Observe-only —— task を決して dispatch しない。signed cards の出力は別機能 |

## Untrusted hint — MCP イントロスペクション

`mcp` ソースは MCP サーバー（stdio + Streamable HTTP）をイントロスペクトし、サーバーの*宣言された*
R/RW ヒントを伴う**能力エッジ**を、プロトコルリビジョン、機能面、レジストリ来歴の findings とともに
出力する。MCP 仕様によれば、ツールアノテーションは**信頼できない**宣言である —— 観測されたソースに
対して裏付けの取れる能力の*主張*であって、**それ単独では決して信頼されない**。（協調的な `claude`
ソースも OTLP パスの一部として MCP をイントロスペクトする。`mcp` は、サーバーリストや `.mcp.json` に
向ける単独のイントロスペクターである。）

| Kind | 観測対象 | 階層 |
|---|---|---|
| `mcp` | MCP サーバーの tools/resources/prompts → 宣言された能力エッジ + 姿勢 findings | untrusted hint |

## Out-of-process なブローカー & メッシュオブザーバー

これらは重いワイヤープロトコルの依存ツリーを抱えるため、それぞれ**プロセス外**で動作する（依存が
コアにリンクされることは決してない）。1 つのコネクタが多数のターゲットに到達する。

| Kind | 観測対象 |
|---|---|
| `kafka` | Kafka / Event Hubs / Redpanda / MSK のトピックアクティビティ |
| `amqp` | AMQP ブローカー（RabbitMQ、Azure Service Bus） |
| `nats` / `mqtt` / `cloudqueue` | NATS、MQTT、クラウドキューのアクティビティ |
| `debezium` | Debezium change-data-capture ストリーム |
| `envoy` | Envoy ALS / ext_authz / ext_proc の観測サービス |
| `hubble` | Cilium Hubble のフローデータ |

## アイデンティティ名簿プロバイダ

これらは帰属を鋭くする（`approximate` エッジを `attributed` に変える）非人間アイデンティティ
**名簿**を満たす。grant 面を持つ各ソースは、permitted-vs-observed 差分の PERMITTED 側として、
`Gather` から**許可アクセス**（`SignalPolicy`）エッジも出力する。

| Kind | 名簿 | 許可エッジ |
|---|---|---|
| `vault` | entities、groups、policies | ACL policy のパス grant（`vault.path`）、バインドされた entity ごとに展開 |
| `ldap` | users、service/computer アカウント、groups | 特権グループメンバーシップ → ディレクトリ grant（`ldap.directory`） |
| `idp`（Okta / Entra） | users、apps/service principals、groups | app 割り当て / スコープ grant（`okta.app` / `entra.app`） |
| `infisical` | machine identities、org members、projects | project grant（`infisical.project`） |
| `keycloak` | realms、clients、roles、groups、users | 名簿のみ（no-op `Gather`） |
| `pingone` / `forgerock` | 同じ multi-provider reader 経由の PingOne / ForgeRock directory roster（kind が対応する `provider` を設定。`ping` は `pingone` の alias） | 名簿のみ（no-op `Gather`） |
| `spiffe` | SPIRE 登録エントリ | 名簿のみ（no-op `Gather`） |

ブートごとの一度きりの許可 grant パスには `identity` エントリに `as_source: true` を、または定期的な
再スキャンには `poll_seconds` を伴う別の `sources` エントリを配線する —— 1 つの kind に対して
両方は決して配線しない（`okta`/`entra` は単一の `idp` コネクタを共有するため、プロセスごとに
ソースとして登録できる idp ファミリーのインスタンスは 1 つのみ）。グループ/ロールのメンバーシップは
型付き名簿スナップショットのみを通って移動し、決してエッジとしては移動しない。

### エージェントアイデンティティフェデレーション

ハイパースケーラーの**エージェントレジストリ**は、プレーンの SPIFFE/WIF 名簿に対して読み取り専用で
フェデレートする。それらのエージェント単位の行（`agent_identity` / `workload_identity` 種別）は専用の
共有されないアイデンティティであるため、access map はそれらを**確固たる**エージェント単位の帰属として
扱う。同じソースからの補助的な行（blueprint プリンシパル、クレデンシャルプロバイダ、サービス
アカウント裏付けのエージェント）は approximate のままである。フェデレーションはレジストリへ決して
書き込まない。control tower への*エクスポート*は別個の後発の能力である。

| Kind | フェデレート対象 | Gather |
|---|---|---|
| `entra-agent` | Microsoft Entra Agent ID（agent identities、agent users、blueprints、blueprint principals、owners/sponsors、スナップショット内 orphan 計算、opt-in soft-deleted）を Graph v1.0 経由で | `nhi_longlived_credential` drift findings、CA/risky-agent/governance/sponsorless posture findings、opt-in beta `auditLogs/signIns` observed agent access edges —— `poll_seconds` 付き `sources` entry を追加 |
| `agentcore` | AWS Bedrock AgentCore Identity（workload identities、token-vault クレデンシャルプロバイダ）+ AgentCore Policy エンジン/Cedar ポリシーをコレクションとして | `nhi_longlived_credential` ドリフト findings（静的 API-key プロバイダ）—— `poll_seconds` を伴う `sources` エントリを追加 |
| `google-agent` | Google Agent Identity（Agent Runtime reasoning engines、SPIFFE-based agent identities）+ Agent Registry / Agent Gateway posture。行は**完全な SPIFFE ID**を ref に使い `spiffe` roster に収束。Gather は帰属のない registry agents、読み取り可能な registry 外の shadow reasoning engines、リスクのある MCP tool annotations、gateway registry posture を検出 | registry/gateway posture findings + shadow-agent detection —— `poll_seconds` 付き `sources` entry を追加 |
| `agent365` | Microsoft Agent 365 registry（Entra identity が*ない* agents も含む package-level inventory）。Graph v1.0、app-permission client credentials または delegated token、opt-in package details | registry-hygiene findings（blocked deployed packages、全ユーザーに deploy された external/shared packages）—— `poll_seconds` 付き `sources` entry を追加 |
| `foundry-agents` | Microsoft Foundry projects、agent applications/deployments、ARM + Foundry Agent Service v1 経由の現在の Agent Service agents。app identity links を `entra-agent` と相関 | ARM 由来の application posture findings（Entra agent identity の欠落、enabled app 上の failed deployment）—— `poll_seconds` 付き `sources` entry を追加 |
| `ai-control-tower` | ServiceNow AI Control Tower のデジタルアセットインベントリ（Table API、読み取り専用） | no-op（名簿のみ） |
| `oasf` | AGNTCY/OASF エージェント記述子 + Agent Badge 検証 —— アイデンティティ仕様が VCDM 2.0 準拠になるまで **EXPERIMENTAL** | badge findings —— `poll_seconds` を伴う `sources` エントリを追加 |
| `onepassword` | 1Password アカウントを `secret_store` カストディアンとして | アイテム使用のシークレットアクセスエッジ —— `poll_seconds` を伴う `sources` エントリを追加 |

再ポーリング可能な Gather を持つ 7 つの kind（`entra-agent`、`agent365`、`agentcore`、
`foundry-agents`、`google-agent`、`oasf`、`onepassword`）
については、**名簿**側を `as_source` *なし*の `identity` エントリとして、**エッジ/findings** 側を
`poll_seconds` を伴う別個の `sources` エントリとして配線する —— `as_source: true` 経由で両方ではない。
それはブートごとに一度しかスキャンを実行しない（また、同じ kind の重複登録は拒否される）。

レジストリが宣言した**owner/sponsor** は名簿同期中に NHI ライフサイクルレコードに着地し
（`PUT /nhi/{ref}/ownership` と同じ意味論）、レジストリが主張した**orphan**（blueprint が消失した
Entra エージェント）は同じレコードの `registry_orphaned` フラグに着地する —— ライフサイクルスイープが
それを `orphaned` に OR で取り込み、`nhi_orphaned` finding を出力するため、orphan 検出は追加の配線
ゼロでフェデレートされたエージェントを監視する。`vault-audit` *ソース*（`identity` ではなく `sources`
配下）は Vault のファイル監査デバイスを tail し、同じ `entity:<name>` ref に対して `vault` の許可
grant の OBSERVED 対応物を出力する。

## ナレッジドキュメントソース（access-map カバレッジではない）

これらは access map ではなく **knowledge** モジュール（モジュール VIII）に供給する。ガバナンス付き
検索のために*ドキュメント内容*を取り込み、R/RW エッジを**出力せず**、バス上に観測を**生成しない**。
モジュールが取り込みリクエスト（`POST /v1/m/knowledge/kbs/{id}/ingest {"source":"<name>"}`）で
それらを*プル*する（List → Fetch）ため、それらはそのモジュールに配線される —— `OLIVARES_SOURCES_CONFIG`
の `sources` ではなく `documents` の下で名前を付ける。各々は読み取り専用かつ minimal-data である:
ソースの ACL と来歴を運ぶ（個人のメールは決して運ばない。モジュールは永続化前に本文を秘匿化する）。

| Kind | 取り込み対象 |
|---|---|
| `gdrive` | Google Drive ドキュメント（Docs/Sheets/Slides/ファイル） |
| `confluence` | Atlassian Confluence のスペース & ページ |
| `notion` | Notion ワークスペース、データベース & ページ |
| `sharepoint` | Microsoft SharePoint / OneDrive のサイト & ドキュメント |
| `s3content` | オブジェクトストレージの内容（S3 / R2 / GCS オブジェクト） |
| `sap_odata` | SAP OData service entities を governed documents として |
| `salesforce` | Salesforce objects/records を governed documents として |
| `snowflake` | Snowflake tables/rows を governed documents として（`snowflake-audit` R/RW observer とは別） |
| `azure_ai_search` | Azure AI Search index documents |
| `postgres` | PostgreSQL rows を governed documents として —— 構造上 read-only、行ごとの宣言 ACL、列ごとの classification（`pgaudit` R/RW observer とは別。NL-to-SQL ではない）。[Postgres を governed context source とする](/ja/how-to/govern-postgres-content/) を参照。 |
| `filesystem` | file-server content（local / NFS / SMB）—— 構造上 root 内に読み取りを制限、POSIX owner/group/ACL を Document ACL にマップ、xattr classification（`filelog` log sink とは別）。[ファイルサーバーをガバナンスする](/ja/how-to/govern-your-file-server/) を参照。 |

```jsonc
// OLIVARES_SOURCES_CONFIG — document sources live under "documents", never "sources"
{
  "documents": [
    { "name": "eng-wiki", "kind": "confluence",
      "config": { "export_path": "/var/lib/olivares/confluence" } }
  ]
}
```

## 出力先（カバレッジではない）

出力コネクタは findings と通知を**配信**する。何も観測せず、カバレッジ階層を持たない。ソースとは別に
配線される。

プロセス内の出力先 kind: `slack`、`teams`、`pagerduty`、`opsgenie`、`webhook`、`siem`、
`splunkhec`、`syslog`、`servicenow`、`jira`、`email`、`twilio`、`chronicle`、`datadog`、
`elastic`、`snmp`、`filelog`、`otlplog`（OTLP/HTTP logs）、`s3archive`（S3 Object Lock WORM sink ——
通知ごとに不変の lock-verified object を 1 つ）。

3 つの broker egress kind は組み込みプラグインとして**プロセス外**で実行する（プラグインソースと
同様、wire-protocol 依存ツリーが engine に link されることはない）: `kafka`、`amqp`、`cloudqueue` ——
ソース側の双子と同じ kind 名であり、出力先としては通知を CloudEvent として設定済み broker/queue へ
配信する。`task build:connectors` のない素の開発ビルドは、存在を装うのではなく、正直なブート警告を
出してそのような出力先をスキップする。

:::note[外向き webhook は出力先であって、API webhook ではない]
`webhook` は control plane がプッシュする出力チャネルであって、本製品の REST API に対して登録する
コールバックではない —— OpenAPI ドキュメントは `webhooks` を定義していない。
[正直さと限界](/ja/start/honesty-and-limits/) を参照。
:::

## デプロイ要件と正直な帰属

R/RW 差分コネクタはデフォルトバイナリに配線されているが、2 つは他にはない**デプロイ要件**を伴う ——
コネクタのコードはホスト非依存だが、それが消費する*データ*はそうではない:

- **`ebpf`** は [Tetragon](https://tetragon.io/) のカーネルイベントエクスポートを消費する。
  **コネクタはカーネル能力を必要としない** —— Tetragon が所有する `0600` のファイル/FIFO/`stdin`
  （`events_path`、デフォルト `-`）を読む。Tetragon 自身は、最小限の `CAP_BPF` + `CAP_PERFMON` を
  保持し、seccomp/AppArmor 付きで非 root として動作し、インバウンドリスナーを持たない、**独立した
  堅牢化された DaemonSet** である。したがってデプロイは: Tetragon を特権で実行し（バンドルされた
  ファイルアクセス + TCP-connect の TracingPolicy）、次に `ebpf` をそのエクスポートに向ける。
  最小 Tetragon: v1.0。
- **`runtime`** はホストの procfs（`proc_root`、デフォルト `/proc`）、Docker デーモンソケット
  （`docker_socket`、**デフォルトで無効** —— `docker.sock` への読み取りアクセスは root 相当。
  意図的にオプトインし、理想的には GET 許可リスト化したソケットプロキシ経由で）、および／または
  Kubernetes API（デフォルトはクラスタ内 ServiceAccount）を読む。有効化したものだけをマウントする。
- **`gcp-audit`** は GCP サービスアカウント（key JSON または WIF/ADC が発行する `access_token`）
  として認証し、**読み取り専用の管理**ロールのみを必要とする:
  `roles/resourcemanager.organizationViewer` + `roles/iam.serviceAccountViewer` +
  `roles/logging.viewer` —— **Data Access** エントリの読み取りには加えて
  `roles/logging.privateLogViewer` が必要。`organization_id`（org ウォーク + org スコープ監査）
  および／または `projects` をスコープする。Data Access 監査ログは **GCP ではデフォルトで無効**:
  IAM/data-access 設定に従って有効化すること。さもなければアクティビティフィードは正直に過小報告する。
- **`azure-activity`** は Entra サービスプリンシパル（client-credentials）または
  managed-identity の `access_token` として認証し、tenant ルート（またはサブスクリプションごと）で
  **Reader** ロールのみを必要とする —— その単一ロールが Resource Graph、サブスクリプション一覧、
  Activity Log をカバーする。`subscriptions` が未設定のとき、サブスクリプションは自動列挙される。

両者とも依然として**プロセス内**（transport A）で動作する。それらをホスト近傍のプロセス外
**コレクター**デプロイに隔離したい場合のために、`cmd/{pg-audit,s3-cloudtrail,ebpf-source}` の
go-plugin バイナリが存在する。

すべてのソースは**オプトインかつ deny-closed** である: `log_path`/`path`/`events_path` の欠落は
起動時の設定エラーであり（ソースは配線されない）、決して静かな no-op にはならない。デモ estate
（[quickstart](/ja/start/quickstart/)）は、ライブソースを配線する前に clean 階層のシグナルをエンドツーエンド
で確認できるよう、本物のバスを通じて同等の合成観測をシードする。

:::caution[すべての階層にわたる正直な限界]
- カバレッジが lossy、impossible、またはソースが配線されていない箇所では、**エッジの不在はアクセスが
  なかった証明ではない**。access map は自身の到達範囲について正直である。
- **エージェント単位のアイデンティティは難しい依存事項である。** コネクションプールの背後にある共有
  サービスアカウントは、clean 階層のストアですら帰属を `approximate` に崩壊させる ——
  [ガバナンスと承認](/ja/how-to/govern-and-approve/) を参照。
- **MCP ツールアノテーションは MCP 仕様によって信頼できない**: 観測されたソースに対して裏付けの取れる
  宣言された能力ヒントであり、決してそれ単独では信頼されない。
:::

## 関連

- [ソースを接続する](/ja/how-to/connect-a-source/) —— コネクタモデルと配線方法。
- [Claude Code を接続する](/ja/how-to/connect-claude-code/) —— 協調パスのエンドツーエンド。
- [モジュール III —— access map](/ja/reference/modules/iii-access-map/) —— エッジが何になるか。
- [正直さと限界](/ja/start/honesty-and-limits/) —— 製品全体の正直な契約。
