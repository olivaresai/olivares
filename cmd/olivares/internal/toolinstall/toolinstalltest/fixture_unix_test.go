// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package toolinstalltest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

type tempDirFixture struct {
	*testing.T
	dir string
}

func (t tempDirFixture) TempDir() string { return t.dir }

func TestExecCapableDirEstablishesPrivatePermissions(t *testing.T) {
	for _, mode := range []os.FileMode{0o700, 0o755, 0o775, 0o777} {
		t.Run(fmt.Sprintf("%04o", mode), func(t *testing.T) {
			dir := ExecCapableDir(t)
			if err := os.Chmod(dir, mode); err != nil {
				t.Fatal(err)
			}
			// Model TempDir's mode under different umasks without changing the
			// process-wide umask or relying on the host's setting.
			got := ExecCapableDir(tempDirFixture{T: t, dir: dir})
			info, err := os.Stat(got)
			if err != nil {
				t.Fatal(err)
			}
			if got != dir || info.Mode().Perm() != 0o700 {
				t.Fatalf("fixture directory = %q (%04o), want %q (0700)", got, info.Mode().Perm(), dir)
			}
			program := filepath.Join(got, "version")
			if err := os.WriteFile(program, Executable("2.1.261", ""), 0o755); err != nil {
				t.Fatal(err)
			}
			output, err := exec.Command(program).Output()
			if err != nil || string(output) != "2.1.261 (Claude Code)\n" {
				t.Fatalf("fixture execution = %q, %v", output, err)
			}
		})
	}
}
