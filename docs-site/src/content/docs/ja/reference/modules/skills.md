---
title: "Skills — 不変カタログと固定割り当て"
description: >-
  32 モジュールのひとつ：不変のスキルカタログとその固定割り当て。スキルパックは
  digest 検証済みの不変リビジョンとしてインストールされ、ターゲットが使えるものは
  記録され監査されるピンであり、フォルダのライブ編集では決してない。
---

Skills（`modules/skills`、`olivares.skills`）は 32 モジュールのひとつです。
**不変のスキルカタログ**とその**固定割り当て**を担います。スキルパックは一度だけ
インストールされ、マニフェスト digest に対してエントリごとに検証され、不変の
リビジョンとして公開されます — その後にその場で編成されることはありません。
ターゲット（エージェント、グループ、ワークスペース）が使えるものは、パックの
リビジョンを固定する**割り当て**であり、各ピンは監査台帳に書き込まれます
（`skills.assignment.pin`）。


新規インストールでは **既定で有効** ですが、Skills は任意で、必須モジュールはありません。アップグレードでは保存されたモジュール選択を維持します。**Skills** (`/skills`) で組み込みパックを選び、必要に応じてワークスペースに割り当てるか、**Git から** を選択します。CLI も同じダイジェスト検証済みの不変リビジョンを使用します。

```sh
olivares skills ls --builtin
olivares skills install --builtin ecc-security
olivares skills install --git https://github.com/owner/skills --ref <commit> --subdir skills
olivares skills assign <pack-id> --revision <revision-id> --workspace <workspace-id>
olivares skills update <pack-id>                       # read the recorded source again and review the diff
olivares skills update <pack-id> --apply --expected-digest <digest>
olivares skills pin <pack-id> [--revision <older-revision-id>]
olivares skills enable tdd-workflow code-review --folder <folder-ref>
olivares skills selection --workspace <workspace-id> --folder <folder-ref>
```

## 提供するもの

- **カタログ。** `/v1/m/skills/packs` 配下のインストール済みパック。各不変リビジョンは
  そのマニフェスト digest を記録します。割り当て中、または記録された
  会話で使用中のパックの引退は拒否され、黙ってカスケード削除されません。
- **内蔵カタログ。** コミットとアーカイブ digest で固定された上流スキルパックの
  同梱セット（`builtin/PIN.json`）。アップロードされたアーカイブとまったく同様に
  インストール時に検証されます。インストール時に実行されるものはありません。
- **割り当て。** `GET/POST /v1/m/skills/assignments` と
  `PUT/DELETE /v1/m/skills/assignments/{id}` がリビジョンをターゲットに固定します
  （アクティブなワークスペースがある場合はそのスコープ）。同じ種類の 2 つのターゲットが同名のスキルに異なる内容を固定した場合はセッション開始前に拒否されます。より狭いターゲット（フォルダー、テンプレート、エージェント、セッション）は、より広いターゲットの同名スキルを置き換えるため、プロジェクトは汎用パックを編集せずに上書きできます。
- **Git インポート。** オペレータースコープのインポーターが、固定ポリシーの下で
  Git ソースからスキルパックを取得できます。インポートは別の不変リビジョンとして
  カタログに着地し、エンジンが読むライブチェックアウトにはなりません。

## サーフェスと権限

モジュールのルートはベータ名前空間（`/v1/m/skills/…`）にあり、
[モジュールルートリファレンス](/reference/api-beta/)に文書化され、コンソールの
Skills ビューに表示されます。読み取りには read 権限、パックやリビジョンの
インストールには write、引退には admin、割り当てには assign 権限が必要です。
セッションは記録された割り当てのみを通してスキルを解決します — ディスク上で
変わるフォルダが、すでに固定された起動の実行内容を変えることはありません。

:::caution[正直な限界]
- **26.10.1<!-- release-fixed --> リリース以降の新規。** 公式 26.10.1<!-- release-fixed --> バイナリにはこのモジュールは
  含まれません。次のリリースで同梱され、このページは現在の統合ラインに存在する
  モジュールを説明しています。
- **カタログは統治し、実行はしない。** インストール・固定・インポートはスキル
  コードを決して実行しません — 実行は固定されたリビジョンを消費するツールと
  セッションに残ります。
:::

## 関連

- [モジュールカタログ](/ja/reference/modules/overview/) — 32 モジュールとその中の
  このモジュールの位置。
- [MCP、スキルとケイパビリティ](/ja/reference/modules/v-capabilities/) — この
  カタログが供給するツールとケイパビリティの統治ビュー。
- [内部カタログとマーケットプレイス](/ja/reference/modules/xiv-catalog/) — 承認
  済みエージェント・MCP サーバー・スキルのキュレーションされたマーケットプレイス。
- [モジュールルート（ベータ）](/reference/api-beta/) — `/v1/m/skills/` の操作。
- [正直さと限界](/ja/start/honesty-and-limits/) — 不変性が暗黙でなく明言される理由。
