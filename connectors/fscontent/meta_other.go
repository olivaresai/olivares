// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package fscontent

import (
	"fmt"
	"os"
	"strconv"
)

// platformMeta on non-Linux platforms exposes only what os.FileInfo carries (size and
// mode) — no POSIX owner/group/ACL or xattr mapping is available, so no ACL is derived
// (the Document inherits the knowledge base's default ACL) and the classification
// default applies. Honest degradation, never invented metadata. The connector targets
// Linux self-hosted file servers (NFS/SMB mounts); this keeps it building everywhere.
func platformMeta(f *os.File, sc *sourceConfig) posixMeta {
	attrs := map[string]string{}
	if fi, err := f.Stat(); err == nil {
		attrs["size"] = strconv.FormatInt(fi.Size(), 10)
		attrs["mode"] = fmt.Sprintf("%04o", fi.Mode().Perm())
	}
	return posixMeta{classification: sc.classifyDefault, attrs: attrs}
}
