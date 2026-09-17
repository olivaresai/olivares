---
title: パッケージからインストール
description: >-
  硬化した Linux ホストで .deb、.rpm、.apk から Olivares AI をインストールする。信頼する前に
  リリースを検証し、同梱の systemd ユニット、またはデフォルトの Alpine ではフォアグラウンド
  で動かし、最初のソースを接続して、オンライン・固定版・完全エアギャップで
  アップグレードする。
draft: false
---

:::note[公開されているパッケージ名]
GitHub の v26.9.0 リリースは `amd64` と `arm64` 向けの `.deb`、`.rpm`、`.apk` を公開し、
あわせて `checksums.txt`、`checksums.txt.sig`、`checksums.txt.pem` を置く。以下のコマンドは
そのリリースのリテラルな `amd64` 名を使う。64 ビット ARM ホストでは `amd64` を `arm64` に
置き換える。このガイドでは、それらの検証済みリリース成果物からインストールする。ソースツリー
にあるリポジトリメタデータ生成器は、このガイドのインストール手順ではない。

**DIST-24-05 の認定。** CI が認定するのは検証済みの **シェルインストーラ** とそのサービス/doctor
契約であり、`dpkg`、`rpm`、`apk` ではない。dispatch/pull request のマトリクスが、公開済み
`v26.9.0` リリースに対して Debian stable、Ubuntu 24.04 LTS、Fedora、openSUSE Leap、Alpine の
コンテナ userland とホスト型 macOS 14 ランナーでそれを実行し、公開リリースに到達できないときは
dry-run をカバレッジに数えず「測定不能」として失敗する。このマトリクスはネイティブなパッケージ
マネージャによるインストールを認定しない。このソースツリーから構築したパッケージのネイティブな
ライフサイクル — インストール、起動、再起動、稼働中の OpenRC サービスのアップグレード、削除 —
は使い捨ての Alpine ゲストでローカルに実行した。それはこのツリーの証拠であり、署名済み・
ホスト済み・preproduction の認定ではない。それらは未了のままである。

**DIST-24-06 提案中のリポジトリ（稼働中のインストール面ではない）。** ソースツリーには決定的な
apt、rpm-md、APK リポジトリ生成器、署名付きインデックスの検証器、クリーンクライアントの認定、
レビュアーが承認するまで dispatch が不活性のままの段階的公開ワークフローがある。**稼働中の
パッケージリポジトリ URL は存在しない**。委譲された DNS 名も、本番のリポジトリ署名鍵も
プロビジョニングされていない。この提案のどれもパッケージマネージャのソースではない。引き続き
以下の検証済みリリース成果物を使うこと。
:::

これは、エンジンをコンテナではなくサービスとして動かしたい通常の Linux ホスト向けの経路である。
Debian/Ubuntu と RHEL/Fedora/SUSE のデフォルトは **systemd** である。Alpine のデフォルト init
は systemd ではなく **OpenRC** である。コンテナは
[Docker でデプロイする](/ja/how-to/docker-deployment/) を、出口経路のないホストは
[エアギャップ環境にインストールする](/ja/how-to/air-gap-install/) を参照。このページは
アップグレード手順で後者に戻る。

## 1. 信頼する前にリリースを検証する

セキュリティ製品ではビルドパイプラインが信頼モデルの一部なので、ここではダウンロードを
鵜呑みにするよう求めない。パッケージ、`checksums.txt`、署名を同じディレクトリに置き、
検証器を **そのディレクトリから** 実行する。

```bash
# keyless / Sigstore (default; reaches Rekor over the network)
./verify-release.sh
```

リリースはキーレスで署名され、cosign の公開鍵は公開されない。したがって、リリースから
ダウンロードしたパッケージにはキーレスのコマンドを使う。これには Sigstore の信頼ルート材料が
必要で、キャッシュ済みでなければ cosign が取得する。`--offline` は Rekor 照会を除くだけで、
検証がネットワーク不要になるわけではない。`--key` は自分が管理する秘密鍵で署名されたファイル専用で
あり、その公開鍵は検証対象のファイルとは別に入手しなければならない。

各ステップが何を検査するか、部分リリースでの振る舞い、コンテナ画像の検証方法は
[ダウンロードしたものを検証する](/ja/how-to/verify-a-release/) にある。

## 2. パッケージをインストールする

3 つの形式はバイナリ `/usr/bin/olivares`、環境ファイル `/etc/olivares/olivares.env`
（`config|noreplace`）、データディレクトリ `/var/lib/olivares`、ライセンス文書を運ぶ。
`.deb`/`.rpm` は硬化した **systemd** ユニットを同梱する。**このソースのパッケージ** の
`.apk` は実行可能な **OpenRC** ユニットを `/etc/init.d/olivares` に置き、フックがホストの
`systemctl` の有無から推測しないよう明示的な `package-init` スタンプを添える。

以前に公開された `.apk` は同じ systemd ユニットを同梱し、OpenRC ユニットは **同梱しない**。
その公開済み linux tarball はライセンス文書、README、`SECURITY.md` を運ぶが、
`scripts/install-service.sh` も `packaging/service/` も含まない。それらのアダプタファイルは、
次のリリースに向けたツリー内の署名済みアーカイブにある。

```bash
# Debian / Ubuntu
sudo dpkg -i olivares_26.9.0_linux_amd64.deb

# RHEL / Fedora / SUSE
sudo rpm -Uvh olivares_26.9.0_linux_amd64.rpm

# Alpine
sudo apk add --allow-untrusted olivares_26.9.0_linux_amd64.apk
```

インストールは **システムユーザーとグループ `olivares` を作成する**（シェルは
`/usr/sbin/nologin`、ホームは `/var/lib/olivares`）。`/var/lib/olivares` をそのユーザー所有の
モード `0750` で作成し、`/etc/olivares` を作成する。systemd パッケージは `systemctl` が
あるとき systemd を再読み込みする。OpenRC パッケージはサービスを有効化も起動もしない。
**何も起動しない** —
[パッケージがしないこと](#8-パッケージがしないこと) を参照。

### エンジンを起動

Debian/Ubuntu と RHEL/Fedora/SUSE では:

```bash
sudo systemctl enable --now olivares
```

**OpenRC**（このソースの `.apk`）:

```bash
sudo rc-service olivares start
# 任意。パッケージはこれをしない:
sudo rc-update add olivares default
```

初回起動トークンは `/var/log/olivares.log` にある（syslogd が動いていれば `logread` にも）。
`OLIVARES_EXTRA_ARGS` の追加フラグはグロブを無効にして空白で分割して付加される。入れ子の
引用は解釈されず、env ファイルはシェルとして評価されない。

**以前に公開された Alpine** には `systemctl` がなく、その `.apk` に OpenRC ユニットはない。
サービスユーザーとしてエンジンを起動する。初回起動トークンは stdout に印字される:

```bash
sudo -u olivares olivares serve --data-dir=/var/lib/olivares \
  --listen=127.0.0.1:8443 --grpc-listen=127.0.0.1:8444 --checkpoint-interval=1h
```

## 3. 硬化した systemd ユニット

同梱の systemd ユニットは、空の capability bounding set を持つ非特権ユーザー `olivares`
としてエンジンを動かす。ambient も bounding も capability を持たず、
`NoNewPrivileges=true` なので、起動したものが権限を得ることはない。その上に
`ProtectSystem=strict`（ファイルシステムは `ReadWritePaths=/var/lib/olivares` 以外読み取り専用）、
`ProtectHome`、`PrivateTmp`、`PrivateDevices`、4 つの `ProtectKernel*`/`ProtectClock`
指令、`RestrictNamespaces`、`RestrictSUIDSGID`、`RestrictRealtime`、`LockPersonality`、
`MemoryDenyWriteExecute`、`SystemCallArchitectures=native`、`@privileged` と
`@resources` をさらに落とす `@system-service` syscall フィルタ、`UMask=0027` を載せる。

デフォルトの Alpine では、**以前に公開された** `.apk` にこれらの systemd 指令は適用されない。
そのペイロードの systemd ユニットが動いていないからだ。このソースから構築した `.apk` は代わりに
OpenRC の下で動く。`olivares` アカウントを使い、初回起動トークンを `/var/log/olivares.log` に
書き、systemd のサンドボックス指令は実装しない。

**リスナーはデフォルトでループバックのみ** — HTTP（REST と埋め込みコンソール）は
`--listen=127.0.0.1:8443`、gRPC は `--grpc-listen=127.0.0.1:8444`。広げるときは
`/etc/olivares/olivares.env` の `OLIVARES_EXTRA_ARGS` で意図的に行い、手前に自分の
TLS 終端を置く。IPv6 ループバックは `--listen=[::1]:8443` を使う。

### 実行可能なスクラッチマウント

systemd ホストであらかじめ知っておくべき失敗である。症状が原因を名乗らないからだ。

プロセス外のファーストパーティコネクタは **バイナリに埋め込まれて** 出荷される。起動時、
エンジンは必要なものを私有スクラッチに展開し、サブプロセスとして実行する。`TMPDIR` が
未設定ならスクラッチは `<data-dir>/tmp` の下に作られる。データディレクトリが書けないときだけ
システム一時ディレクトリに落ちる。明示的な `TMPDIR` は常に勝つ。

そのため同梱の systemd サービスはデフォルトで `/tmp` ではなく `/var/lib/olivares/tmp`
を使う。実行可能なスクラッチを実際に載せるマウントを確認する:

```bash
check_scratch_mount() {
  target=${1:-/var/lib/olivares}
  opts=$(findmnt -no OPTIONS --target "$target") || {
    printf '%s\n' "cannot read mount options for $target (missing path or permission)" >&2
    return 1
  }
  [ -n "$opts" ] || {
    printf '%s\n' "mount options for $target are unknown" >&2
    return 1
  }
  case ",$opts," in
    *,noexec,*) printf '%s\n' 'noexec — set TMPDIR' ;;
    *) printf '%s\n' 'exec-capable — nothing to do' ;;
  esac
}
check_scratch_mount /var/lib/olivares
```

`noexec` と出たら、`ProtectSystem=strict` の下で書け、**かつ** 実行可能なマウント上にある
ディレクトリへ `TMPDIR` を向ける:

```bash
sudo install -d -o olivares -g olivares -m 0750 /run/olivares-exec-tmp
sudo systemctl edit olivares      # creates a drop-in; do not edit the shipped unit
```

```ini
[Service]
Environment=TMPDIR=/run/olivares-exec-tmp
ReadWritePaths=/run/olivares-exec-tmp
```

その後再起動する。exec が `EACCES` または `ENOEXEC` で拒否された場合、エンジンのエラーは
展開マウントと 2 つの再配置制御（`TMPDIR` と data-dir）を名指しする。noexec マウントを
欠落コネクタとしては報告しない。

`/usr/lib/systemd/system/olivares.service` を直接編集せず、`systemctl edit` を使う。
そのファイルはパッケージのものであり、アップグレードで置き換わる。

## 4. 初回起動: `olivares quickstart`

`quickstart` は親しみやすい既定値と案内バナー付きの `serve` である。既定の資格情報は
決して作らない。埋め込みコンソールへ案内し、**ワンタイムトークン** で最初の管理者を
作らせる。

同梱の systemd ユニットですでにサーブしているなら `quickstart` は実行しない —
**初回起動セットアップトークンはジャーナルに印字される**:

```bash
journalctl -u olivares | sed -n '/FIRST-BOOT SETUP/,/========================/p'
```

フォアグラウンドで `serve` を起動した場合（デフォルトの Alpine）、同じバナーは stdout に出る。

コンソールを `https://127.0.0.1:8443` で開く（初回起動時に自己署名証明書が生成される）。
そのトークンを提示して管理者を作成する。トークンは単回使用である。

ワークステーションでサービスを入れずに見るなら、`olivares quickstart` がフォアグラウンドで
同じことをする。既定からずらすなら `--listen`/`--grpc-listen`/`--data-dir` を使う。

## 5. 最初のソース: pgAudit

ソースはエンジンが観測を取り込む場所である。動詞はそれぞれのコストで分かれており、その順で
使う価値がある。`plan` は何が変わるかを述べて何も書かない。`validate` は設定がそれ自体で
整合していると述べ、**ネットワークには触れない**。`test` はソースを本当に開いて応答を証明し、
`set` が適用する。設定は秘密の **参照**（`store:<name>`）を運び、値は運ばない。

pgAudit は PostgreSQL の監査ログを読むので、必須フィールドは `log_path` だけである:

```bash
# coherent by itself? (writes nothing, opens no socket)
sudo -u olivares olivares sources validate --name pg-prod --kind pgaudit \
  --tenant <your-tenant-id> \
  --config log_path=/var/log/postgresql/postgresql.log \
  --data-dir /var/lib/olivares

# does it actually answer? (opens the source for real)
sudo -u olivares olivares sources test --name pg-prod --kind pgaudit \
  --tenant <your-tenant-id> \
  --config log_path=/var/log/postgresql/postgresql.log \
  --data-dir /var/lib/olivares

# apply it
sudo -u olivares olivares sources set --name pg-prod --kind pgaudit \
  --tenant <your-tenant-id> \
  --config log_path=/var/log/postgresql/postgresql.log \
  --actor "$(id -un)" --reason "onboard the production audit log" \
  --data-dir /var/lib/olivares
```

上のコマンドラインで詰め物ではない点が 3 つある。

- **`--tenant` は必須である。** ソースは観測が属するビジネステナントを名指ししなければならない。
  なければコマンドは監査データの所有者を推測せず拒否する。
- **`--actor` と `--reason` は `set` だけが要求する。** 特権的なオフライン操作は誰が何のために
  行ったかを記録する必要がある。`validate` はどちらも不要である。何も書かないからだ。
  この非対称こそが要点である。
- **`format` の既定は `csvlog`、`follow` の既定は `true`** なので、標準的な pgAudit
  配備ではどちらも不要である。

適用するとフィールドごとに何が変わったかが印字され、**稼働中** のエンジンが再起動なしで
取り込む方法が示される — `POST /v1/console/runtime/reload`、または `SIGHUP`。同梱の
systemd ユニットでは `sudo systemctl reload olivares` である。フォアグラウンドで
`serve` を起動した場合は、そのプロセスに `SIGHUP` を送る。

サービスユーザーはそのログファイルへの読み取りが必要である。多くのディストリビューションでは
`olivares` を `adm` または `postgres` グループに入れることを意味する。パッケージが代わりに
行うことではなく、あなたが意図して与える権限である。

## 6. アップグレード

`olivares upgrade` はバイナリをその場で置き換える。手でパッケージを入れ直すよりこれを選ぶ
理由は安全特性にある。**ダウンロードした候補の exec 探査が成功するまでバイナリを置き換えず**、
タイムスタンプ付きバックアップを残し、**入れ替え後の探査が失敗すればそのバックアップへ戻す**。

```bash
sudo olivares upgrade --check    # what would change, without changing anything
sudo olivares upgrade --yes      # do it
```

パッケージインストールで特に効くフラグが 3 つある。

- **`--endpoint`** — 既定ではなく、自分が制御する GitHub リポジトリから更新を取る。
  ミラーやフォークの逃げ道である。
- **`--bundle`** — ローカルのバンドルディレクトリまたは `.tar.gz` から **ネットワークなし**
  でインストールする。そのバンドルを作って運ぶ手順は
  [エアギャップ環境にインストールする](/ja/how-to/air-gap-install/) にある。
- **`--install-timer`** — 予定に従って更新を確認する **任意の systemd** タイマーとサービスを
  出力する。何も代わりに入れてくれない。
  [パッケージがしないこと](#8-パッケージがしないこと) を参照。systemd の生成器である。

ステージングと exec 探査は **インストールディレクトリ内、対象の隣 — `/tmp` ではない**
で行われるので、[上で](#実行可能なスクラッチマウント) 述べた `noexec` マウントは
アップグレードを壊さない。**インストール** ディレクトリ上の `noexec` マウントは別問題で、
インストール済みバージョンを測定不能にする。その場合、リリースチャネル、段階的ロールアウト、
ロールバックは
[アップグレードとロールバック](/ja/how-to/upgrade-and-rollback/) にある。

## 7. パスを推測せずにアンインストールまたは移行する

パッケージは `/var/lib/olivares/install-manifest.json` を書く。アンインストーラはサービスや
ファイルシステムに触れる前に、署名済み配布インデックスに対して完全なマニフェストを検証する。
想定外のパスは 2 を返す。まず検査し、保持方針を明示的に選ぶ:

```bash
sudo olivares uninstall --plan --data-dir /var/lib/olivares
sudo olivares uninstall --preserve --data-dir /var/lib/olivares
sudo olivares uninstall --purge --data-dir /var/lib/olivares --yes
```

Preserve はパッケージ削除の方針である。設定、データ、ログ、鍵とそのサービス識別を残す。
systemd パッケージでは削除フックが `--preserve` を実行する。このソースから構築した OpenRC の
`.apk` パッケージでは、フックはサービスが稼働中なら止め、一度も有効化されていなくても失敗せずに
デフォルト runlevel の項目を外し、完全なマニフェストを `--plan` で検証し、apk にパッケージ所有
ファイルの削除を任せる。以前に公開された Alpine パッケージは `--plan` での検証だけを行った。
そのペイロードには止める OpenRC ユニットがなかったからだ。消去が意図であるときだけ、パッケージを外す前に `--purge` を実行する。
確認が必要で、索引されたパスだけを削除する。

拠点の移動では、先に `olivares dr backup` を作り、先方をインストールしてそこで
`olivares dr restore` を実行する。現行バンドルは `hmac-sha256-kek-v1` でマニフェストと
KEK 下の各ペイロードを認証する。より新しいエンジンからのエクスポートは書き込み前に拒否される。
別に認証された pre-v26.9 バンドルは明示的に `--allow-legacy-unsigned` が必要である。
[バックアップと復元のガイド](/ja/how-to/backup-and-restore/) が KEK の保管とインポート後の
継続性証明を扱う。

## 8. パッケージがしないこと

セキュリティ製品がここで曖昧ならインストールに値しないので、はっきり書く。

- **リポジトリを追加しない。** `/etc/apt/sources.list.d`、`/etc/yum.repos.d`、
  `/etc/apk/repositories` には何も書かない。インストールしたのは 1 ファイルであり、
  入ったのもそのファイルだけである。アップグレードはあなたが起動する — 新しいパッケージか
  `olivares upgrade` で。
- **サービスを起動も有効化もしない。** systemd パッケージは `systemctl` があるとき systemd を
  再読み込みし、`systemctl enable --now` を印字する。OpenRC パッケージは
  `rc-service olivares start` を印字し、`rc-update add` はしない。起動はあなたの判断のままで
  ある。稼働中の OpenRC サービスのアップグレードはそれを止め、ファイルを置き換え、再び起動する。
  停止中のサービスは停止中のままである。
- **ライセンスの検証は誰にも電話しない。有料分のダウンロードはする。** オープンビルドの
  ライセンス検証はオフライン Ed25519 であり、遠隔キルスイッチはなく、ライセンス鍵がその
  ビルドを制限したり劣化させたりしない — AGPL ビルドがプラットフォーム全体である。
  **必須テレメトリも、デフォルトのコントロールプレーン外向き通信もない。境界を越えるのは、
  越えるよう設定したものだけである** — モデル API への呼び出し、配線した SIEM/webhook
  出力、プロビジョニングした外部埋め込みプロバイダ、コネクタがポーリングするソース
  （その `addr`、`base_url`、`endpoint`）を、設定した間隔で。
- **しかし `olivares upgrade` は実行したときに意図してネットワーク呼び出しをする** —
  それが更新確認の意味であり、`--check` は何か動く前に計画を見せる。約束の正直な形は:
  *ライセンスの検証は誰にも電話しない。有料分のダウンロードはする。* 更新経路も呼び出しを
  したくなければ `--bundle` を使う。
- **ネットワークへポートを開けない。** ユニットは、自分で広げるまでループバックにだけ
  バインドする。

## 関連

- [ダウンロードしたものを検証する](/ja/how-to/verify-a-release/) — 完全な検証経路
- [アップグレードとロールバック](/ja/how-to/upgrade-and-rollback/) — チャネル、段階的
  ロールアウト、ロールバック
- [デプロイを硬化する](/ja/how-to/security-hardening/) — ユニットが既にしていること以上
- [エアギャップ環境にインストールする](/ja/how-to/air-gap-install/)
- [Docker でデプロイする](/ja/how-to/docker-deployment/)
- [ソースを接続する](/ja/how-to/connect-a-source/)
- [バックアップと復元](/ja/how-to/backup-and-restore/)
