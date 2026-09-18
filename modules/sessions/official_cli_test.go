// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sessions

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions/cliruntime"
)

// TestOfficialCLI_ProcessCustodyIfPresent runs the process-custody half of the
// driver contract against a real vendor binary when it is on PATH. It does not
// send a model turn and does not use the operator's HOME: missing credentials
// are an allowed non-zero exit, not a fabricated success.
//
// Full attach/resume conformance stays on Fake and the local PTY peer. An
// authenticated official-account turn is a skip, said as such.
func TestOfficialCLI_ProcessCustodyIfPresent(t *testing.T) {
	cases := []struct {
		kind string
		bin  string
	}{
		{cliruntime.KindClaude, "claude"},
		{cliruntime.KindCodex, "codex"},
		{cliruntime.KindGrok, "grok"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			path, err := exec.LookPath(tc.bin)
			if err != nil {
				t.Skipf("%s not on PATH; fake and PTY-peer conformance still ran", tc.bin)
			}
			verCtx, verCancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer verCancel()
			out, verr := exec.CommandContext(verCtx, path, "--version").CombinedOutput()
			if verr != nil {
				t.Logf("%s --version: %v (%s)", tc.bin, verr, out)
			} else {
				t.Logf("%s --version: %s", tc.bin, bytesHead(out, 120))
			}

			d, err := NewOfficialLocal(tc.kind, path)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			sess, err := d.Launch(ctx, cliruntime.LaunchRequest{
				WorkDir:    t.TempDir(),
				UserHome:   t.TempDir(),
				ConfigHome: t.TempDir(),
			})
			if err != nil {
				t.Fatalf("launch official %s: %v", tc.kind, err)
			}
			if sess.PID() <= 0 {
				t.Fatalf("%s pid = %d", tc.kind, sess.PID())
			}
			res, err := sess.Stop(ctx)
			if err != nil {
				t.Fatalf("stop official %s: %v", tc.kind, err)
			}
			if !res.ProcessExited {
				t.Fatalf("official %s stop did not observe process exit", tc.kind)
			}
			t.Logf("official %s process custody: pid=%d exit=%d (no model turn sent)", tc.kind, sess.PID(), res.ExitCode)
		})
	}
}

func bytesHead(b []byte, n int) string {
	b = bytesTrim(b)
	if len(b) > n {
		return string(b[:n]) + "…"
	}
	return string(b)
}

func bytesTrim(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\n' || b[i] == '\r' || b[i] == '\t') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\n' || b[j-1] == '\r' || b[j-1] == '\t') {
		j--
	}
	return b[i:j]
}

func TestOfficialCLI_LookPathReport(t *testing.T) {
	for _, name := range []string{"claude", "codex", "grok"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Logf("LOOKPATH %s: absent (%v)", name, err)
			continue
		}
		t.Logf("LOOKPATH %s: present (%s)", name, os.Getenv("PATH"))
	}
}
