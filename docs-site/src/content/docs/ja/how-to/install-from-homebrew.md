---
title: Homebrew でインストール
description: >-
  Olivares AI の macOS Homebrew cask 座標、cask が Gatekeeper に対して行うこと、および
  26.10.1 の tap bump の公開状態。
draft: false
---

これは `INSTALL.md` が推奨として名付ける macOS 経路です。Homebrew cask 経由で
署名済み `olivares` バイナリをインストールし、Gatekeeper 隔離を解除します。
Linux パッケージ経路
（[パッケージからインストール](/how-to/install-from-packages/)）でも Docker
（[Docker でデプロイ](/how-to/docker-deployment/)）でもありません。

:::note[ベータ — 26.10 の cask は公開済み]
tap の `Casks/olivares.rb` は 2026-10-01 に 26.10 向けに更新されました。バージョン 26.10.1 と、
SHA-256 がリリースの署名済み `checksums.txt` と一致する 4 つのプラットフォーム向けアーカイブを示しています。プロデューサーは
`.goreleaser.yaml` `homebrew_casks:` で、tap の cask はリリースジョブが上げます。下のコマンドは
`INSTALL.md` が名付ける座標です（`brew install olivaresai/tap/olivares`）。
:::

## 1. cask をインストールする

```sh
brew install olivaresai/tap/olivares
```

Homebrew は各 cask ダウンロードを記録済み SHA-256 と照合します
（`INSTALL.md`）。cask は署名済みバイナリをインストールし、**Gatekeeper
隔離を解除します**。

Darwin バイナリは cosign で署名され（サプライチェーン信頼）、**まだ Apple
公証されていません**。手動アーカイブのダウンロードは隔離されます。cask が
それを処理するか、手動経路として `INSTALL.md` が示す
`xattr -d com.apple.quarantine olivares` で解除します。

## 2. 初回起動

```sh
olivares quickstart
```

安全な既定: TLS オン、loopback、既定資格情報なし。エンジンはコンソール URL と
ワンタイムセットアップトークンを印刷します。続けて
[最初の1時間](/how-to/first-hour/)。

一時的な合成 estate（loopback、平文）は見るためだけです:

```sh
olivares serve --seed-demo --insecure --data-dir "$(mktemp -d)"
```

`--seed-demo` は製品ツアーではありません。
[最初の1時間](/how-to/first-hour/) を参照。

## 関連

- [コントロールプレーンをセルフホストする](/how-to/self-hosting/) — 他のインストール形状。
- [リリースを検証する](/how-to/verify-a-release/) — cosign、SBOM、来歴。
- [パッケージからインストール](/how-to/install-from-packages/) — Linux `.deb` / `.rpm` / `.apk`。
