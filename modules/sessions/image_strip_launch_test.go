// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"strings"
	"testing"
	"time"
)

// A launch that asks for StripImageData gets its stdout without the image bytes on
// both runners, and one that does not gets it as the child wrote it.
func TestLaunchStripImageDataOnPipesAndTerminal(t *testing.T) {
	const script = `printf '{"url":"data:image/png;base64,QUJDREVGR0g="}\n'; printf 'done\n'`
	for _, runner := range []struct {
		name string
		new  func() Runner
	}{{"pipes", NewProcRunner}, {"terminal", NewPTYRunner}} {
		for _, strip := range []bool{true, false} {
			t.Run(runner.name+map[bool]string{true: "/stripped", false: "/as-written"}[strip], func(t *testing.T) {
				proc, err := runner.new().Launch(t.Context(), LaunchSpec{
					Program: "/bin/sh", Args: []string{"-c", script}, Dir: t.TempDir(),
					Isolation: IsolationNative, StripImageData: strip,
				})
				if err != nil {
					t.Fatal(err)
				}
				var out []string
				timeout := time.After(10 * time.Second)
			read:
				for {
					select {
					case frame, ok := <-proc.Output():
						if !ok {
							break read
						}
						if frame.Stream == streamStdout {
							out = append(out, strings.TrimSpace(string(frame.Data)))
						}
					case <-timeout:
						t.Fatalf("no end of output; got %q", out)
					}
				}
				want := `{"url":"data:image/png;base64,QUJDREVGR0g="}`
				if strip {
					want = `{"url":"data:image/png;base64,"}`
				}
				if len(out) != 2 || out[0] != want || out[1] != "done" {
					t.Fatalf("stdout = %q; want %q then done", out, want)
				}
			})
		}
	}
}
