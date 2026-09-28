// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package portal

import (
	"os"
	"syscall"
	"testing"
)

// ownedBy describes a file as FileInfo does, except that its owner is uid.
type ownedBy struct {
	os.FileInfo
	uid uint32
}

func (f ownedBy) Sys() any {
	stat := *f.FileInfo.Sys().(*syscall.Stat_t)
	stat.Uid = f.uid
	return &stat
}

// withoutOwner describes a file as FileInfo does, but records no owner.
type withoutOwner struct{ os.FileInfo }

func (withoutOwner) Sys() any { return nil }

func TestPortalCustody_OwnerRuleRefusesAnotherUserWithoutRoot(t *testing.T) {
	info, err := os.Lstat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		info os.FileInfo
		want bool
	}{
		{"root", ownedBy{info, 0}, true},
		{"this process's user", ownedBy{info, uint32(os.Getuid())}, true},
		{"another user", ownedBy{info, anotherUser()}, false},
		{"no owner recorded", withoutOwner{info}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ownedByRootOrSelf(tc.info); got != tc.want {
				t.Fatalf("ownedByRootOrSelf = %t, want %t", got, tc.want)
			}
		})
	}
}
