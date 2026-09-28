// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package storage

import (
	"strconv"
	"strings"
)

// deviceNode reports whether node is /dev/<name> with a plain kernel block device name: a
// lowercase letter, then lowercase letters, digits and hyphens, at most 32 in all. No link
// directory, no dot and no slash after /dev/ is accepted.
func deviceNode(node string) bool {
	name, ok := strings.CutPrefix(node, "/dev/")
	if !ok || name == "" || len(name) > 32 || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for i := 1; i < len(name); i++ {
		c := name[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// loopNode reports whether node is /dev/loop<N>.
func loopNode(node string) bool {
	n, ok := strings.CutPrefix(node, "/dev/loop")
	if !ok || n == "" || len(n) > 6 {
		return false
	}
	for i := 0; i < len(n); i++ {
		if n[i] < '0' || n[i] > '9' {
			return false
		}
	}
	return true
}

// sysfsDevIs reports whether a sysfs dev file ("<major>:<minor>\n") names number, a device
// number in the C library's encoding, as UDisks2 reports it.
func sysfsDevIs(content string, number uint64) bool {
	majorText, minorText, ok := strings.Cut(strings.TrimSuffix(content, "\n"), ":")
	if !ok {
		return false
	}
	major, err := strconv.ParseUint(majorText, 10, 32)
	if err != nil {
		return false
	}
	minor, err := strconv.ParseUint(minorText, 10, 32)
	if err != nil {
		return false
	}
	wantMajor := (number>>8)&0xfff | (number>>32)&^uint64(0xfff)
	wantMinor := number&0xff | (number>>12)&^uint64(0xff)
	return major == wantMajor && minor == wantMinor
}
