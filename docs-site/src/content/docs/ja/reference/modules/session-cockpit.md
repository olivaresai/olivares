---
title: "セッションコックピット（可用性）"
description: >-
  session-cockpit API 名前空間の可用性ディスクリプタ。Community はこの名前空間を
  ハンドラなし・対話型コックピットなしで登録する。ライブセッションと AgentOps で
  欠如を確認する方法と、利用できるオープンなケイパビリティ。
---

Community バイナリは `session-cockpit` API 名前空間の可用性ディスクリプタを
登録する。この名前空間には現在 **ハンドラがゼロ** であり、**対話型コックピットは
ない**。`/v1/m/session-cockpit` 配下のリクエストは **欠如による 404** を受ける。
このディスクリプタはカタログの 30 個の製品モジュールには含まれない。

## 現在の可用性

| サーフェス | Community（この成果物） |
|---|---|
| API 名前空間 | `session-cockpit`（`/v1/m/session-cockpit`） |
| ディスクリプタ | `olivares.session-cockpit` `0.1.0` — タイトル `Session cockpit (availability)` |
| 登録済みルート / ハンドラ | なし |
| 対話型コックピット | なし |
| 宣言された権限 | `session-cockpit:availability:read`（宣言のみ、ルート未割当） |
| ライフサイクル | 空（`Init` / `Start` / `Stop` は何もしない） |

この名前空間の 404 は Community の期待どおりの応答である。コントロールプレーンの
インストール失敗を意味しない。

## 欠如の診断

出荷済みのセッションサーフェスが動いていることを確認する。

1. `sessions` 名前空間のライブセッションモジュールルート —
   [ライブ運用とセッション](/ja/reference/modules/ii-sessions/)。
2. コンソールの **Sessions**（`/sessions`）、**Claude Code**（`/agentops`）、
   **Work**（`/work`）— [コンソールリファレンス](/ja/reference/console/)。
3. 次節の公式 CLI ライフサイクル。

これらが応答し、`/v1/m/session-cockpit` が 404 なら、ディスクリプタはこの成果物と
一致している。

## オープンなケイパビリティ

公式 CLI のインストール、起動、観測、管理はオープンな Community 製品に残る。

- [ライブ運用とセッション](/ja/reference/modules/ii-sessions/) — ライブの
  エージェントセッション、タイムライン、プロバイダープロファイル、`live_ref`。
- [プロバイダーセッションを運用する](/ja/how-to/operate-provider-sessions/) —
  固定した公式バイナリの下で Claude、Codex、Grok を起動する。
- [Olivares で Claude Code を実行する](/ja/how-to/run-claude-code-with-olivares/) —
  公式 `claude` セッションの AgentOps 共存デプロイ。
- Connector と PEP-hook ガイド（観測/統治であり、セッション起動ではない）:
  [Claude Code](/ja/how-to/integrations/claude-code/)、
  [Codex](/ja/how-to/integrations/codex/)、
  [Grok Build](/ja/how-to/integrations/grok/)。
- [特権セッションの記録](/ja/reference/modules/recording/)
- [アイデンティティ、権限、ガバナンス](/ja/reference/modules/vi-governance/)

## 関連

- [モジュールカタログ](/ja/reference/modules/overview/)
- [誠実さと限界](/ja/start/honesty-and-limits/)
