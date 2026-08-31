---
title: "Govern your file server"
description: "Connect a directory tree (local, NFS, or SMB) as a read-only, governed knowledge source: files become documents, POSIX ownership and ACLs map to document ACLs, and reads are confined to the root by construction."
---

The `filesystem` content connector (`olivares.fs-content`) turns a directory tree —
a local path, an NFS export, or an SMB mount — into **governed knowledge documents**
that flow through the same pipeline as every other content source (redact → classify →
chunk → embed → index → serve over MCP), with document ACLs mapped from POSIX ownership
and classification from xattrs. It is a content **source**, distinct from the `filelog`
log **sink** (which forwards logs *out*).

For a self-hosted operator, the file server is often the oldest and largest document
store, so this is one of the highest-value connectors in the catalog.

## Read security by construction

The connector reads **confined to the configured root**, guaranteed by the Go standard
library's `os.Root`:

- A **symlink pointing outside the root**, an **absolute path**, or a **`..` traversal**
  is **refused** — the connector physically cannot read a file you did not point it at.
- Symlinks are **not followed** during the walk (they are counted, never resolved).
- Every file body is **size-capped** (larger files are truncated and marked), only
  **text/document types** are read (binaries are skipped and counted), content is
  **never executed**, and the walk is bounded by **file-count and total-byte budgets**
  so it cannot exhaust a large or slow (NFS) mount.

Adversarial tests prove the symlink-escape and traversal refusal.

## Point it at a tree

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

Each file becomes a Document: the body is the file content, the DocID is its
root-relative path, and provenance attributes carry `owner`, `group`, `mode`, `size`,
`world_readable` and `path`.

## How ownership and ACLs map — the honest matrix

The connector maps **only what the filesystem expresses**, and declares what it cannot:

| Filesystem | owner / group / mode | POSIX.1e ACL (`getfacl`) | Windows / NFSv4 ACL |
|---|---|---|---|
| **Local** (ext4/xfs/btrfs) | Mapped: owner → `user:<name>`, group (if group-readable) → `group:<name>` | Mapped: each named user/group entry with the read bit → a principal ref | n/a |
| **NFS** | Mapped, **if uid/gid map consistently** (idmapd / the same directory both sides) | Mapped when the mount exposes `system.posix_acl_access` | **NFSv4-native ACLs are NOT parsed** (declared limit) |
| **SMB / CIFS** | Mapped from the **mount's** `uid=/gid=/file_mode=` — i.e. mount options, **not** the real Windows owner | Usually absent | **Windows security descriptors are NOT parsed** (`system.cifs_acl` is a binary SD; declared limit) |

Principal names resolve through the host's name service (which may include **LDAP**,
so `uid`→username matches your directory). A uid/gid that does not resolve falls back to
its **numeric** id. A file with **no derivable ACL** inherits the knowledge base's default
ACL, which retrieval still enforces. The connector **never invents** an ACL a file does
not carry.

### Classification

- A default `classification` applies to every file.
- A per-file **xattr** (`user.classification` by default) overrides it.
- The **external-labels xattr** (`user.olivares.labels`, comma-separated) adds sensitivity
  labels that feed the retrieval DLP, enforced deny-closed alongside the
  classification.

## Honest limits

- **Text/document files only.** Binaries are skipped and counted. Rich formats that need
  extraction (PDF/DOCX) are **not** ingested by this connector (a declared follow-up, not
  a silent skip).
- A body is **capped at 1 MiB**; larger files are truncated and marked `truncated`.
- **SMB**: the connector sees your mount's synthetic POSIX view, not the real Windows ACL.
- The connector **reads**; it never writes to the tree (there is no write path, by design).

## Wire-proof

The security guarantees are covered by adversarial tests here (symlink-escape, traversal,
size cap, binary skip, POSIX owner/group/ACL mapping, xattr classification). The full
wire-proof — a fixture tree behind a folder binding, served over MCP so a Claude Code
session sees only what its binding + the file ACL permit, with a denied subtree proven —
is the CI integration job that composes the engine.
