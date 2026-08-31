// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package fscontent is the Olivares AI knowledge DATA connector that ingests files
// from a local directory or a mounted file server (NFS/SMB) as governed knowledge
// Documents (contentsource.Source). It is the filesystem counterpart to the SaaS /
// warehouse / database content sources: a self-hosted operator points it at a
// directory tree and the files flow through the SAME governed pipeline (redact →
// classify → chunk → embed → index → MCP serving) as every other source. It is a
// content SOURCE, distinct from the filelog SINK (which forwards logs OUT).
//
// READ SECURITY BY CONSTRUCTION (non-negotiable, docs/SECURITY-HARDENING.md):
//   - The whole tree is accessed through an os.Root confined to the configured root:
//     a symlink pointing OUTSIDE the root, an absolute path, or a ".." escape is
//     REFUSED by the standard library, so the connector can never read a file the
//     operator did not point it at. Symlinks are not followed at all during the walk.
//   - Every file body is bounded (a per-file cap), only text/document types are read
//     (binaries are skipped and counted), content is NEVER executed, and the walk is
//     bounded by a file-count and total-byte budget so it cannot tumble an NFS mount.
//
// Adversarial tests prove the symlink-escape / traversal refusal; the full wire-proof
// (a fixture tree behind a binding, served over MCP) is the CI integration job.
//
// SOURCE AUTHORIZATION, MAPPED HONESTLY: each file's POSIX owner/group and — when
// present — its POSIX.1e access ACL (the system.posix_acl_access xattr, i.e. what
// getfacl shows) are mapped to Document.ACL as principal references
// (user:<name>/group:<name>, resolved through the host's name service, which may
// include LDAP). uid/gid that do not resolve fall back to a numeric ref. What the
// filesystem does not express (e.g. SMB security descriptors a mount does not expose)
// is DECLARED, never invented — the docs carry the local/NFS/SMB mapping matrix.
// Classification and additional external labels are read from xattrs when present.
//
// It imports only the SDK, the contentsource contract, the connector-internal content
// helpers, and golang.org/x/sys/unix (POSIX metadata / xattrs) — never the engine —
// so the Apache license boundary stays clean. The module (VIII), not the connector,
// redacts the body before persisting.
package fscontent
