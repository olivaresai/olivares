---
title: "ファイルサーバーをガバナンスする"
description: "ディレクトリツリー（ローカル、NFS、SMB）を読み取り専用のガバナンス対象ナレッジソースとして接続します。ファイルは文書になり、POSIX の所有者と ACL は文書 ACL にマッピングされ、読み取りは構造上 root 内に限定されます。"
---

`filesystem` コンテンツコネクタ（`olivares.fs-content`）は、ローカルパス、NFS
export、SMB mount などのディレクトリツリーを**ガバナンス対象のナレッジ文書**へ
変換します。それらは他のすべてのコンテンツソースと同じ pipeline（秘匿化 →
classify → chunk → embed → index → MCP で提供）を通り、文書 ACL は POSIX の所有者
情報から、分類は xattr からマッピングされます。これはコンテンツの**source**で
あり、ログを*外部へ*転送するログ **sink** の `filelog` とは別物です。

セルフホスト環境の運用者にとって、ファイルサーバーは最も古く、最も大きい文書
ストアであることが多いため、これはカタログ内でも特に価値の高いコネクタの 1 つ
です。

## 構造によって保証される読み取りセキュリティ

コネクタの読み取りは、Go 標準ライブラリの `os.Root` によって、**設定した root
内に限定**されます。

- **root の外を指す symlink**、**絶対パス**、または **`..` による traversal** は
  **拒否**されます。コネクタは、指定されていないファイルを物理的に読み取れません。
- walk 中に symlink を**たどりません**（数には含めますが、決して解決しません）。
- 各ファイル本文には**サイズ上限**があり（大きなファイルは切り詰めてマーク）、
  読み取るのは**テキスト/文書形式だけ**です（binary は skip して数を記録）。
  コンテンツを**実行することは決してなく**、walk は**ファイル数と総 byte 数の
  budget**にも制約されるため、大規模または低速な NFS mount を枯渇させません。

敵対的テストにより、symlink escape と traversal が拒否されることを証明しています。

## ディレクトリツリーを指定する

```jsonc
// OLIVARES_SOURCES_CONFIG — document sources live under "documents"
{
  "documents": [
    {
      "name": "file-server",
      "kind": "filesystem",
      "config": {
        "root": "/mnt/fileserver/shared",   // local path or an NFS/SMB mount
        "include": "*.md,*.txt,docs/*",       // globs (path or basename); empty = all text
        "exclude": "**/archive/*,*.tmp",
        "max_file_bytes": "1048576",          // per-file cap (hard-capped at 1 MiB)
        "max_files": "100000",                // walk budget
        "max_total_bytes": "1073741824",      // read budget
        "text_only": "true",                  // skip binaries (counted)
        "map_posix_acl": "true",              // owner/group + POSIX.1e ACL → Document ACL
        "classification": "internal",         // default label (an xattr overrides per file)
        "classification_xattr": "user.classification",
        "labels_xattr": "user.olivares.labels"
      }
    }
  ]
}
```

各ファイルが 1 つの Document になります。本文はファイルの内容、DocID は root からの
相対パスで、来歴属性には `owner`、`group`、`mode`、`size`、`world_readable`、
`path` が含まれます。

## 所有者と ACL のマッピング — 正確な対応表

コネクタがマッピングするのは**ファイルシステムが表現している情報だけ**であり、
マッピングできないものは明示します。

| ファイルシステム | owner / group / mode | POSIX.1e ACL（`getfacl`） | Windows / NFSv4 ACL |
|---|---|---|---|
| **ローカル**（ext4/xfs/btrfs） | マッピング: owner → `user:<name>`、group（group-readable の場合）→ `group:<name>` | マッピング: read bit を持つ名前付き user/group の各 entry → principal ref | 該当なし |
| **NFS** | **uid/gid が一貫してマッピングされている場合**にマッピング（idmapd / 両側で同じ directory） | mount が `system.posix_acl_access` を公開する場合にマッピング | **NFSv4 native ACL は解析しません**（明示された制限） |
| **SMB / CIFS** | **mount の** `uid=/gid=/file_mode=`、つまり mount option からマッピング。実際の Windows owner からでは**ありません** | 通常は存在しない | **Windows security descriptor は解析しません**（`system.cifs_acl` は binary SD。明示された制限） |

principal 名は host の name service を通じて解決します。ここには **LDAP** を含める
こともできるため、`uid`→username を組織の directory に一致させられます。解決
できない uid/gid には、その**数値** ID を使用します。導出可能な ACL が**ない**
ファイルはナレッジベースのデフォルト ACL を継承し、retrieval でも引き続き適用
されます。コネクタが、ファイルにない ACL を**捏造することは決してありません**。

### 分類

- デフォルトの `classification` がすべてのファイルに適用されます。
- ファイルごとの **xattr**（デフォルトは `user.classification`）がそれを上書き
  します。
- **external-labels xattr**（`user.olivares.labels`、comma 区切り）は sensitivity
  label を追加します。これは retrieval DLP に渡され、classification と並んで
  デニークローズドで適用されます。

## 明示されている制限

- **テキスト/文書ファイルだけ**を扱います。binary は skip して数を記録します。
  抽出が必要な rich format（PDF/DOCX）は、このコネクタでは**ingest しません**
  （黙って skip するのではなく、今後の対応として明示しています）。
- 本文は **1 MiB が上限**です。大きなファイルは切り詰められ、`truncated` と
  マークされます。
- **SMB**: コネクタに見えるのは mount の合成 POSIX view であり、実際の Windows
  ACL ではありません。
- コネクタは**読み取りだけ**を行い、ツリーに書き込むことはありません（設計上、
  書き込み経路が存在しません）。

## 実配線での証明

セキュリティ保証は、symlink escape、traversal、サイズ上限、binary skip、POSIX の
owner/group/ACL マッピング、xattr classification を対象とする敵対的テストでカバー
されています。完全な実配線での証明は、folder binding の背後に fixture tree を
置いて MCP で提供し、Claude Code session からは binding と file ACL の両方が許可
したものだけが見え、拒否対象の subtree が実際に拒否されることを証明する、engine
を構成した CI integration job です。
