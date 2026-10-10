// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package sessions

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/modules/sessions/confine"
	"github.com/olivaresai/olivares/modules/sessions/egress"
)

const procProbeEngineEnv = "OLIVARES_TEST_PROC_ENGINE"

// A session child the engine confines cannot read the engine's /proc entries
// that need ptrace access (environ holds the engine's keys and tokens): Landlock
// lets a confined process reach only processes in its own domain, with no
// exception for root or CAP_SYS_PTRACE. An unconfined child of the same engine
// reads them, so the denial is the confinement's.
func TestProcRunnerConfinedChildCannotReadTheEngineProcEntries(t *testing.T) {
	if engine := os.Getenv(procProbeEngineEnv); engine != "" {
		procProbe(engine)
		return
	}
	if s := confine.Probe(); s.Mode != confine.ModeLandlock {
		t.Fatal("this security reproducer requires Landlock: " + s.Reason)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	engine := strconv.Itoa(os.Getpid())
	policy := func() *confine.Policy { return &confine.Policy{ReadWrite: []string{folder}} }
	for _, tc := range []struct {
		name     string
		spec     LaunchSpec
		confined bool
	}{
		{"unconfined", LaunchSpec{}, false},
		{"landlock", LaunchSpec{Confinement: policy(), ConfinementRequired: true}, true},
		{"landlock-network", LaunchSpec{Confinement: policy(), ConfinementRequired: true,
			NetworkPolicy: &egress.Policy{Providers: []string{"http://127.0.0.1:11434"}}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := tc.spec
			spec.Program, spec.Dir, spec.WaitDelay = self, folder, 2*time.Second
			spec.Args = []string{"-test.run=^TestProcRunnerConfinedChildCannotReadTheEngineProcEntries$"}
			spec.Env = []EnvVar{{Name: procProbeEngineEnv, Value: engine}}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			proc, err := NewProcRunner().Launch(ctx, spec)
			if err != nil {
				t.Fatalf("launch: %v", err)
			}
			if state, ok := confinementOf(proc); !ok || (state.Mode == confine.ModeLandlock) != tc.confined {
				t.Fatalf("confinement = %+v, %v", state, ok)
			}
			var out strings.Builder
			for f := range proc.Output() {
				out.Write(f.Data)
				out.WriteByte('\n')
			}
			if code, err := proc.Wait(); err != nil || code != 0 {
				t.Fatalf("child exited %d: %v\n%s", code, err, out.String())
			}
			got := out.String()
			want := []string{fmt.Sprintf("uid=%d", os.Getuid()), "self=read", "selfroot=read", "stat=read"}
			for _, entry := range []string{"environ", "maps", "fd", "root"} {
				if tc.confined {
					want = append(want, entry+"=denied")
				} else {
					want = append(want, entry+"=read")
				}
			}
			for _, w := range want {
				if !strings.Contains(got, w+"\n") {
					t.Errorf("want %s:\n%s", w, got)
				}
			}
		})
	}
}

// procProbe reports whether the session child can open each entry, never its
// content. Its own environ and /usr and the engine's stat need no ptrace access:
// they show the probe ran, sees the engine and may list /usr, so a denied entry
// is the ptrace check's.
func procProbe(engine string) {
	dir := "/proc/" + engine + "/"
	read := func(path string) error { _, err := os.ReadFile(path); return err }
	fmt.Printf("uid=%d\n", os.Getuid())
	for _, p := range []struct {
		name string
		open func() error
	}{
		{"self", func() error { return read("/proc/self/environ") }},
		{"selfroot", func() error { _, err := os.ReadDir("/proc/self/root/usr"); return err }},
		{"stat", func() error { return read(dir + "stat") }},
		{"environ", func() error { return read(dir + "environ") }},
		{"maps", func() error { return read(dir + "maps") }},
		{"fd", func() error { _, err := os.Readlink(dir + "fd/0"); return err }},
		{"root", func() error { _, err := os.ReadDir(dir + "root/usr"); return err }},
	} {
		switch err := p.open(); {
		case err == nil:
			fmt.Println(p.name + "=read")
		case errors.Is(err, fs.ErrPermission):
			fmt.Println(p.name + "=denied")
		default:
			fmt.Printf("%s=error: %v\n", p.name, err)
		}
	}
}
