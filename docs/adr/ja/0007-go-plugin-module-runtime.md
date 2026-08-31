> 機械翻訳です。正式な情報源は英語版です。

# ADR-0007: go-plugin（gRPC）によるアウトオブプロセスの module/connector ランタイム

- **Status:** accepted
- **Date:** 2026-06
- **Deciders:** Olivares AI
- **References:** stack design (module runtime); license-boundary design

## 背景と課題

プラットフォームは、ファーストパーティおよびサードパーティの connector と module が、
それらの依存ツリーをエンジンに引き込むことなく、また permissive な connector エコシステムを
エンジンの copyleft ライセンスで汚染することなく、プラットフォームを拡張できるようにしなければならない。

## 意思決定の要因

- connector の依存関係をエンジンのビルド／SBOM から隔離する。
- プロセス境界をまたぐ安定した、バージョン管理された契約。
- Apache-2.0 の connector 境界をクリーンに保つ（connector が AGPL エンジンを決してリンクしない）。

## 検討した選択肢

- アウトオブプロセスの module/connector 向けの **gRPC 上の `hashicorp/go-plugin`**、
  加えてインプロセスでコンパイルされるコア module。
- **インプロセスのプラグインのみ**（Go の `plugin` パッケージまたはコンパイル組み込み）。

## 決定の結果

選択した選択肢: アウトオブプロセスの connector/module には **`hashicorp/go-plugin`（gRPC）**
を用い、ファーストパーティの connector は埋め込んだうえで隔離されたサブプロセスとして起動し、コア
module はコンパイル組み込みとする。connector SDK は Go インターフェースに加え、バージョン管理された
gRPC/protobuf 契約である。

### 帰結

- **良い点:** connector の依存関係はエンジンのバイナリ／SBOM に入らない。Apache/AGPL の境界は
  クリーンに保たれ、CI で強制される。サードパーティは connector を独立して出荷できる。
- **悪い点／トレードオフ:** バージョン管理すべき gRPC 契約と、アウトオブプロセスコンポーネントへの
  IPC ホップ（IPC 跳躍）が発生する。
- **中立:** 単一バイナリは依然としてファーストパーティの connector を埋め込む（サブプロセスで
  隔離）ため、1つのアーティファクトのままである。

## 代替案を却下した理由

- **インプロセスのみ** — すべての connector の依存関係をエンジンに引き込み、ライセンス境界を
  機械的に強制することを不可能にする。
