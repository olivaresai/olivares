// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package dr

import "golang.org/x/sys/unix"

// aclRecordPrefix is how GNU tar and bsdtar carry an extended attribute in a PAX
// record, so a tree opens with the stock tools too.
const aclRecordPrefix = "SCHILY.xattr."

// openNoFollow makes a read refuse a file that became a symlink after the walk.
const openNoFollow = unix.O_NOFOLLOW

// aclAttributes are the POSIX ACLs of a file or directory.
var aclAttributes = []string{"system.posix_acl_access", "system.posix_acl_default"}

// readACLs returns the PAX records for the ACLs on path, or nil when it has none
// or the filesystem has no ACLs.
func readACLs(path string) map[string]string {
	var records map[string]string
	for _, attr := range aclAttributes {
		size, err := unix.Getxattr(path, attr, nil)
		if err != nil || size <= 0 {
			continue
		}
		buf := make([]byte, size)
		n, err := unix.Getxattr(path, attr, buf)
		if err != nil {
			continue
		}
		if records == nil {
			records = map[string]string{}
		}
		records[aclRecordPrefix+attr] = string(buf[:n])
	}
	return records
}

// writeACL sets one ACL attribute named by a PAX record, and refuses any other
// attribute: a bundle restores ACLs, never arbitrary extended attributes.
func writeACL(path, attr string, value []byte) error {
	if attr != aclAttributes[0] && attr != aclAttributes[1] {
		return unix.EINVAL
	}
	return unix.Setxattr(path, attr, value, 0)
}
