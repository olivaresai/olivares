// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package egress

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions/confine"
)

// A session with a bound provider takes the network supervisor's stage; every
// other one takes the filesystem helper's. Both must leave Git's hooks and
// configuration read-only and everything Git itself writes writable.
func TestGitHooksAndConfigAreReadOnlyOnBothLaunchStages(t *testing.T) {
	script := `
w() { if sh -c "$2" 2>/dev/null; then echo "$1=writable"; else echo "$1=blocked"; fi; }
w hook 'echo x > .git/hooks/pre-commit'
w config 'echo x >> .git/config'
w newfile 'echo y > newfile'
w lock 'echo z > .git/index.lock && mv .git/index.lock .git/index'
w move 'mv .git .git.moved'
echo "caps=$(awk '/^Cap(Eff|Prm|Amb|Bnd):/ {print $2}' /proc/self/status | sort -u | tr '\n' ' ')"
`
	for _, stage := range []string{"filesystem helper", "network supervisor"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			for path, content := range map[string]string{".git/hooks/pre-commit.sample": "#!/bin/sh\n", ".git/config": "[core]\n", ".git/HEAD": "ref: refs/heads/main\n"} {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd, state, err := confine.Command(ctx, confine.Policy{ReadWrite: []string{dir}}, "/bin/sh", "-c", script)
			if err != nil {
				t.Fatal(err)
			}
			if state.Mode != confine.ModeLandlock {
				t.Skip("this host cannot confine with Landlock: " + state.Reason)
			}
			mounts := confine.Mounts{
				ReadOnly: []string{filepath.Join(dir, ".git/hooks"), filepath.Join(dir, ".git/config")},
				Pin:      []string{filepath.Join(dir, ".git")},
			}
			if err := confine.WrapMounts(cmd, mounts); errors.Is(err, confine.ErrMountsUnavailable) {
				t.Skip("this host does not allow user and mount namespaces: " + err.Error())
			} else if err != nil {
				t.Fatal(err)
			}
			cmd.Dir, cmd.Env = dir, []string{"PATH=/usr/bin:/bin"}
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			var ready func() error
			if stage == "network supervisor" {
				wait, release, _, err := Wrap(ctx, cmd, Policy{Offline: true})
				if err != nil {
					t.Fatal(err)
				}
				defer release()
				ready = wait
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if ready != nil {
				if err := ready(); err != nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					t.Fatalf("%v: %s", err, output.String())
				}
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("session: %v: %s", err, output.String())
			}
			got := map[string]string{}
			for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
				name, value, _ := strings.Cut(line, "=")
				got[name] = strings.TrimSpace(value)
			}
			want := map[string]string{"hook": "blocked", "config": "blocked", "newfile": "writable", "lock": "writable", "move": "blocked",
				"caps": "0000000000000000"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s: got %v, want %v", stage, got, want)
			}
		})
	}
}
