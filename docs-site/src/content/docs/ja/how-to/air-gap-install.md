---
title: エアギャップ環境へのインストール
description: >-
  署名済みのリリースバンドルをギャップ越しに運び、すべてのイメージと Helm チャートを
  完全にオフラインで検証し、ダイジェストでプライベートレジストリにミラーリングして
  インストールする —— 切断された側からのアウトバウンド呼び出しは一切なし。
---

Olivares AI は **セルフホストファーストかつエアギャップ対応**です。本ガイドでは、
**切断された側にネットワークがない**状態で、署名済みのリリースをエアギャップ越しに運びます。
公開鍵に対してすべてのイメージと Helm チャートをオフラインで検証し、**ダイジェストで**
プライベートレジストリにミラーリングして、インストールします。本製品は**起動時に必須の
アウトバウンド呼び出しを一切行わない**ため、ギャップの内側からインターネットに到達すること
はありません。ベンダーのエンドポイントに到達しうる唯一のコマンドは
`olivares upgrade` で、`--endpoint` または `--bundle` で自前のミラーに向けられます。

このフローは二面構成です。

1. **オンライン、一度だけ** —— メンテナーが自己完結型のバンドルをビルドします。
2. **ギャップの内側** —— あなたがそれをオフラインで検証し、レジストリにミラーリングします。

このページは、バンドルと同梱スクリプトの**使い方**を文書化したものであり、リリース
パイプラインを再構築するものではありません。

## 1. バンドルをビルドする（オンライン、一度だけ）

接続されたマシン上で、`scripts/airgap-bundle.sh` がすべてのイメージを**ダイジェストで
固定して**プルし、Helm チャートをパッケージ化して署名し、SBOM/OpenVEX/プロベナンスを
収集して、`VERIFY.md` を含む単一の tarball を出力します。

```bash
scripts/airgap-bundle.sh \
  --version v26.8.0 \
  --image docker.io/olivaresai/olivares:26.8.0-amd64 \
  --chart deploy/helm/olivares \
  --cosign-key cosign.key \
  [--collector-image <ref>] [--out dist/airgap] [--gpg-key <id>]
```

イメージは、その公式座標（`docker.io/olivaresai/olivares`）で Docker Hub からプルされます。
同じコンテンツは `ghcr.io/olivaresai/olivares` にもあり、ダイジェストで同一です。
そこからミラーリングしたい場合に利用できます。Docker Hub は**匿名**プルにレート制限を課しますが、
ghcr.io は公開イメージには課さないため、認証していないビルドホストでは有用です。

:::caution[SBOM/VEX/プロベナンスは生成ではなく供給される]
バンドラーは、SBOM、OpenVEX、プロベナンスを環境変数（`OLIVARES_SBOM_FILES`、
`OLIVARES_VEX_FILES`、`OLIVARES_PROV_FILES`）から**ベストエフォートで**バンドルに
コピーします。これらが設定されていない場合、バンドル内の `sbom/`、`vex/`、`prov/`
ディレクトリは空になります —— 切断されたサイトがアテステーションを受け取れるよう、
これらを設定してください。
:::

### バンドルに含まれるもの

```text
images/<name>/   cosign-saved image + its signatures/attestations (offline)
chart/<chart>.tgz   packaged Helm chart  (+ .tgz.sig cosign, + .prov if gpg)
sbom/  vex/  prov/   SBOM, OpenVEX and SLSA provenance for the release
cosign.pub          the public key to verify everything offline (key mode)
digests.txt         the pinned digest of every image (the manifest of record)
VERIFY.md           the exact offline verification + mirror walkthrough
```

バンドルには `airgap-mirror.sh` と `verify-release.sh` のコピーも含まれているため、
切断された側はネットワークから何も必要としません。

## 2. ギャップの内側で検証してミラーリングする

切断された側で必要なのは `cosign`、`crane`、`helm`、`tar` —— そして到達可能な
**プライベートレジストリ**だけです。インターネットは不要です。

### すべてのイメージをオフラインで検証する（透明性ログなし）

```bash
for d in images/*/; do
  cosign verify --local-image "$d" --insecure-ignore-tlog --key cosign.pub
done
```

`--insecure-ignore-tlog` は Sigstore のオンライン透明性ログをスキップします。信頼は
同梱された `cosign.pub` から得られます。（これはキーレスの `--offline` フラグとは*同じでは
ありません* —— キーモードでは、オフラインのトラストルートは公開鍵です。）

### Helm チャートをオフラインで検証する

```bash
cosign verify-blob --key cosign.pub --insecure-ignore-tlog \
  --signature chart/*.tgz.sig chart/*.tgz
# If a Helm-native .prov is present, additionally: helm verify chart/*.tgz
# (needs the signer's GPG public key in your keyring)
```

### ダイジェストでプライベートレジストリにミラーリングする

`scripts/airgap-mirror.sh` は各イメージをオフラインで検証し、レジストリに読み込み、
ミラーリングを越えてダイジェストが維持されたことを確認するために**ダイジェストで再固定**します
（`crane` と `cosign load` を使用し、`oras` は**使いません**）。

```bash
scripts/airgap-mirror.sh \
  --bundle olivares-airgap-v26.8.0.tar.gz \
  --registry registry.internal:5000 [--insecure]
```

### タグではなく、必ずダイジェストでインストールする

```bash
helm install olivares \
  oci://registry.internal:5000/charts/olivares \
  --version <chart-version> \
  --set image.repository=registry.internal:5000/olivares \
  --set image.digest=<digest-from-digests.txt>
```

可変のタグではなく、必ず `digests.txt` の**ダイジェスト**からインストールしてください。
ダイジェストは不変であり、あなたが検証したものそのものです。

## ギャップの内側からは何も外に出ない

> エンジンは**起動時に必須のアウトバウンド呼び出しを一切行いません**（デフォルトで
> ループバックにバインドします）。したがって、ギャップの内側からインターネットに到達
> することはありません。ベンダーのエンドポイントに到達しうる唯一のコマンドは
> `olivares upgrade` で、`--endpoint` または `--bundle` で自前のミラーに向けられます。

ライセンスは**オフラインで**検証され（Ed25519 署名、ライセンスサーバーなし）、上記の検証や
インストールの手順は、バンドルがギャップを越えた後はいずれもインターネットに触れません。
無効化すべきテレメトリホームのデフォルトは存在しません。

外部と通信するのは**オンライン側**であり、これは設計どおりです。バンドルのビルドでリリースを
ダウンロードし、商用環境ではサブスクリプションが、アドオンとその更新プログラムおよび修正
プログラムを取得するための資格情報になります。これが SUSE/Novell モデルです —— エアギャップ
環境は、同じ資格を持つローカルミラーから配信されます。
[セルフホスティング](/ja/how-to/self-hosting/) を参照してください。

:::note[コンテナとバイナリのリッスンデフォルト]
直接実行した場合、バイナリはデフォルトで**ループバック**にバインドします。リリースの
**コンテナイメージの**デフォルトコマンドは、Ingress/Service でフロントできるように
コンテナ内で `0.0.0.0` にバインドします —— これはコンテナ内のバインドであり、アウトバウンドの
呼び出しではありません。デプロイメントに応じて、リッスンアドレスを明示的に設定してください。
:::

## FIPS / STIG バリアント

ハードニングされたビルドバリアントが存在します（CMVP 検証済みの Go 暗号モジュールを
リンクする FIPS モードビルドと、STIG 指向のイメージ）。これらは **v1 後**のものであり、
独自の正直さの台帳を持ちます —— 特に、**FedRAMP/DoD ATO は主張されておらず**、検証されたものと
して表現してよいのは、具体的に検証されたモジュールバージョンのみです。これらは、認証された
提供物としてではなく、利用可能だが未だ v1 ではないものとして扱ってください。

## 関連項目

- [ダウンロードしたものを検証する](/how-to/verify-a-release/) —— 非エアギャップの
  検証チェーン（署名、SBOM、OpenVEX、SLSA）。
- [コントロールプレーンをセルフホストする](/how-to/self-hosting/) —— 単一バイナリ、Compose、
  Kubernetes の各経路と、それらのセキュアなデフォルト。
