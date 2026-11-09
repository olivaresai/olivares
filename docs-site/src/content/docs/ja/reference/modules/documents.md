---
title: ドキュメント
description: MarkItDown によるローカルの Markdown 変換。元のファイルを保持し、ネットワークにはアクセスしません。
---

PDF、DOCX、PPTX、XLSX、HTML、CSV、JSON、XML、EPUB、ZIP をセッションに添付するか、「マイファイル」にアップロードするか、セッションのフォルダー内のファイルを `@path` で指定します。空白を含む場合は `@"path with spaces.pdf"` を使います。Microsoft MarkItDown がローカルで変換し、エージェントには信頼できない文書内容として Markdown が渡されます。画像は引き続き画像として送信されます。

元のファイルは保持され、既存のファイルを置き換えずに隣に Markdown が作成されます。ドキュメントモジュールは新規インストールで有効です。「設定 > モジュール」で無効にできます。無効な場合や変換ツールがない場合、新しい変換は明確なエラーで拒否されます。

`olivares files convert report.pdf` は `report.pdf.md` のパスを出力します。`--json` は `{"path":"..."}` を返します。終了コードは成功が 0、変換失敗が 1、引数の誤りが 2 です。既存の Markdown は置き換えません。

変換ツールはネットワーク、プラグイン、プロバイダーの認証情報にアクセスできません。stdin で文書のバイト列だけを受け取り、Linux Landlock ABI 3 以降を必要とします。上限は入出力各 32 MiB、実時間 30 秒、CPU 時間 25 秒、仮想メモリー 768 MiB です。添付の上限は従来どおり 3.75 MiB です。サーバー ISO と Docker イメージにはハッシュで固定した依存関係が含まれ、変換中にパッケージをインストールしません。

## Scanned pages and OCR

OCR は任意で、既定では無効です。設定 > モジュールで Scanned documents (OCR) を有効にし、`OLIVARES_SOURCES_CONFIG` の `document_ocr` を設定してください。テキスト層のないページのみがローカルのエンドポイントに送信されます。無効時はテキスト層のない PDF がエラーになり、 `this page has no text layer; OCR is off (Settings > Modules)` と表示されます。有効時は画像も Markdown に変換されます。モデルの重みは事前に SHA-256 で固定して配備し、セッション開始時にはダウンロードしません。

[Configuration and endpoint privacy rules](/reference/modules/documents/#scanned-pages-and-ocr).
