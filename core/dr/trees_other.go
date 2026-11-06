// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !linux

package dr

import "errors"

const aclRecordPrefix = "SCHILY.xattr."

// openNoFollow is zero off Linux: the flag is not portable.
const openNoFollow = 0

// readACLs finds no ACLs off Linux.
func readACLs(string) map[string]string { return nil }

// writeACL cannot set an ACL off Linux; the caller counts it as skipped.
func writeACL(string, string, []byte) error {
	return errors.New("dr: POSIX ACLs are restored on Linux only")
}
