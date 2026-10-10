// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !unix

package main

// fileOwner reports no owner where files carry no numeric uid. The release builds are
// linux and darwin only (.goreleaser.yaml), both unix; here first-boot mints a
// replacement setup token as itself, as it always has.
func fileOwner(string) (uid, gid int, known bool, err error) { return 0, 0, false, nil }
