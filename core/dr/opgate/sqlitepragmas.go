// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package opgate

import (
	"net/url"
	"strings"
)

// SQLiteTempStoreMemory keeps a statement's temporary b-trees and journals in memory: SQLite otherwise writes them to
// the system's temporary directory, which a confined engine may not write (the Olivares Server's SELinux policy), and
// every statement that needs one then fails. Every connection the store and the DR snapshot open sets it;
// the engine's two remaining opens (the restore destination read and the payload count) need no temporary file
// (#3352 review, advisory 3).
// shortcut: temporary space is RAM, upgrade to an engine-owned SQLite temporary directory when a measured statement
// needs more than 256 MiB of it.
const SQLiteTempStoreMemory = "temp_store(2)"

// SQLiteDSNWithPragmas appends pragmas to a DSN as the pinned driver's `_pragma`
// query parameters. They ride the DSN rather than a post-open Exec because the
// driver applies them inside its own per-connection setup, so EVERY physical
// connection is configured — including a replacement dialed after database/sql
// discards one it considers broken. A post-open Exec configures only whichever
// connection happened to serve that single call. Appended pragmas merge with any
// already in the DSN, and the driver applies `_pragma` values in sorted order, so
// a pragma carried by the DSN can still override an appended one.
func SQLiteDSNWithPragmas(dsn string, pragmas ...string) string {
	var params strings.Builder
	sep := "?"
	if strings.ContainsRune(dsn, '?') {
		sep = "&"
	}
	for _, pragma := range pragmas {
		params.WriteString(sep)
		params.WriteString("_pragma=")
		params.WriteString(url.QueryEscape(pragma))
		sep = "&"
	}
	return dsn + params.String()
}
