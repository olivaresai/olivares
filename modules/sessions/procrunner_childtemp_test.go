// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// A confined child's TMPDIR is a private s-* directory under the engine's child temporary root (<data>/tool-tmp in the
// composition), never the host's /tmp: on the Server image the tool's temporary type covers that folder only, and the
// engine's own scratch in /tmp (the egress proxy, gpg, DR) stays out of the tool's reach.
func TestProcRunnerConfinedChildTempDirIsUnderTheChildTempRoot(t *testing.T) {
	if s := confine.Probe(); s.Mode != confine.ModeLandlock {
		t.Skip("host cannot confine: " + s.Reason)
	}
	root := filepath.Join(t.TempDir(), "tool-tmp")
	confine.UseChildTempRoot(root)
	t.Cleanup(func() { confine.UseChildTempRoot("") })
	folder := t.TempDir()
	proc, err := NewProcRunner().Launch(context.Background(), LaunchSpec{
		Program: "/bin/sh", Args: []string{"-c", `echo "tmpdir=$TMPDIR"`}, Dir: folder, WaitDelay: 2 * time.Second,
		Confinement: &confine.Policy{ReadWrite: []string{folder}},
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	var out strings.Builder
	for f := range proc.Output() {
		out.Write(f.Data)
		out.WriteByte('\n')
	}
	if exit, _ := proc.Wait(); exit != 0 {
		t.Fatalf("exit %d:\n%s", exit, out.String())
	}
	tmp := ""
	for _, line := range strings.Split(out.String(), "\n") {
		if v, ok := strings.CutPrefix(line, "tmpdir="); ok {
			tmp = v
		}
	}
	if filepath.Dir(tmp) != root || !strings.HasPrefix(filepath.Base(tmp), "s-") {
		t.Fatalf("child TMPDIR = %q, want an s-* directory under %s", tmp, root)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("the child's TMPDIR %s survives the child: %v", tmp, err)
	}
}
