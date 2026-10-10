// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package secure

import (
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

// EnsureAs writes the token as the account given, through the staging descriptor, so a
// root caller never publishes a file the engine cannot read (#514). A group this process
// is not in makes the ownership change observable without root: the kernel refuses it,
// and nothing may be left at the path.
func TestSetupTokenEnsureAsGivesTheFileToTheAccount(t *testing.T) {
	dir := t.TempDir()
	st := NewSetupToken(filepath.Join(dir, "setup.token"))
	tok, created, err := st.EnsureAs(os.Getuid(), os.Getgid())
	if err != nil || !created || !st.Verify(tok) {
		t.Fatalf("EnsureAs(own account) = (created=%v, err=%v); verifies=%v", created, err, st.Verify(tok))
	}
	if err := st.Consume(); err != nil {
		t.Fatal(err)
	}
	groups, _ := os.Getgroups()
	foreign := -1
	for gid := 0; gid < 1<<16 && foreign < 0; gid++ {
		if gid != os.Getgid() && !slices.Contains(groups, gid) {
			foreign = gid
		}
	}
	_, _, err = st.EnsureAs(os.Getuid(), foreign)
	if os.Geteuid() == 0 {
		// Root may give the file to any group: it must then carry that group.
		if err != nil {
			t.Fatalf("EnsureAs as root: %v", err)
		}
		info, statErr := os.Stat(filepath.Join(dir, "setup.token"))
		if statErr != nil || int(info.Sys().(*syscall.Stat_t).Gid) != foreign {
			t.Fatalf("setup.token does not belong to group %d (stat err %v)", foreign, statErr)
		}
		return
	}
	if err == nil {
		t.Fatalf("EnsureAs gave the token to group %d, which this process is not in", foreign)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("a refused EnsureAs left %d entries behind (first %q)", len(entries), entries[0].Name())
	}
}
