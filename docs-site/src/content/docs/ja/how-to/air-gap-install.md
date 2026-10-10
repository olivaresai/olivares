---
title: エアギャップ環境へのインストール
description: >-
  署名済みのリリースバンドルをギャップ越しに運び、すべてのイメージと Helm チャートを
  完全にオフラインで検証し、ダイジェストでプライベートレジストリにミラーリングして
  インストールする —— 切断された側からのアウトバウンド呼び出しは一切なし。
---

This deployment capability is distributed with Enterprise. See [editions](https://olivares.ai/pricing).

## 1. バンドルをビルドする（オンライン、一度だけ）

### バンドルに含まれるもの

## 2. ギャップの内側で検証してミラーリングする

### すべてのイメージをオフラインで検証する（透明性ログなし）

### Helm チャートをオフラインで検証する

# If a Helm-native .prov is present, additionally: helm verify chart/*.tgz

# (needs the signer's GPG public key in your keyring)

### ダイジェストでプライベートレジストリにミラーリングする

### タグではなく、必ずダイジェストでインストールする

## ギャップの内側からは何も外に出ない

## FIPS / STIG バリアント

## 関連項目
