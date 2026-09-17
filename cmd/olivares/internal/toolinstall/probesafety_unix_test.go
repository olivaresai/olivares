// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package toolinstall

import (
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeInfo lets the ownership rule be exercised for uids this test cannot
// create files as (root, a foreign account).
type fakeInfo struct {
	name string
	mode os.FileMode
	uid  uint32
}

func (f fakeInfo) Name() string       { return f.name }
func (f fakeInfo) Size() int64        { return 1 }
func (f fakeInfo) Mode() os.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeInfo) Sys() any           { return &syscall.Stat_t{Uid: f.uid} }

func TestCheckProbeSafetyOwnershipAndModes(t *testing.T) {
	const me = 1000
	file := func(mode os.FileMode, uid uint32) fakeInfo { return fakeInfo{name: "claude", mode: mode, uid: uid} }
	dir := func(mode os.FileMode, uid uint32) fakeInfo {
		return fakeInfo{name: "bin", mode: os.ModeDir | mode, uid: uid}
	}
	cases := []struct {
		name string
		file fakeInfo
		dir  fakeInfo
		want string // "" = allowed, else a fragment of the refusal
	}{
		{"own file in own dir", file(0o755, me), dir(0o755, me), ""},
		{"root-owned system tool", file(0o755, 0), dir(0o755, 0), ""},
		{"own file in root-owned dir", file(0o755, me), dir(0o755, 0), ""},
		{"foreign uid file", file(0o755, 4242), dir(0o755, 0), "owned by uid 4242"},
		{"foreign uid dir", file(0o755, me), dir(0o755, 4242), "directory /usr/local/bin is owned by uid 4242"},
		{"group-writable file", file(0o775, me), dir(0o755, me), "group- or world-writable"},
		{"world-writable dir", file(0o755, 0), dir(0o777, 0), "containing directory"},
		{"sticky world-writable dir", file(0o755, me), dir(0o1777, 0), "containing directory"},
		{"not executable", file(0o644, me), dir(0o755, me), "not executable"},
		{"symlink", fakeInfo{name: "claude", mode: os.ModeSymlink | 0o777, uid: me}, dir(0o755, me), "not a regular file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkProbeSafety(tc.file, tc.dir, "/usr/local/bin", me)
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("want refusal containing %q, got %v", tc.want, err)
			}
		})
	}
	// Running as root trusts root and itself only: a user-owned file is refused.
	if err := checkProbeSafety(file(0o755, me), dir(0o755, 0), "/usr/local/bin", 0); err == nil {
		t.Fatal("root probing a user-owned file was allowed")
	}
}
