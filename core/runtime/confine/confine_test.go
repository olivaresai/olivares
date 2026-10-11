// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package confine

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestExplicitPolicyValidatesPathsAndSealedGrants(t *testing.T) {
	if err := (Policy{}).validate(); err != nil {
		t.Fatal(err)
	} // no implicit writable/default requirement
	for _, p := range []Policy{
		{ReadOnly: []string{"relative"}},
		{Devices: []Device{{Path: "relative"}}},
		{ReadWrite: []string{"/sealed/subdir"}, Sealed: []string{"/sealed"}},
		{Devices: []Device{{Path: "/dev/null", Write: true}}, Sealed: []string{"/dev"}},
	} {
		if err := p.validate(); err == nil {
			t.Errorf("accepted invalid policy %+v", p)
		}
	}
}

func TestWrapPreservesCallerCommand(t *testing.T) {
	if Probe().Mode != ModeLandlock {
		t.Skip(Probe().Reason)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), self, "caller-argument")
	cmd.Args[0] = "caller-argv-zero"
	cmd.Dir, cmd.Env = t.TempDir(), []string{"FIXTURE=value"}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	env, dir, cancel := cmd.Env, cmd.Dir, cmd.Cancel
	policy := Policy{ReadOnly: []string{filepath.Dir(self)}}
	if _, err := Wrap(cmd, policy); err != nil {
		t.Fatal(err)
	}
	if !IsWrapped(cmd) || cmd.Args[len(cmd.Args)-2] != "caller-argv-zero" || cmd.Args[len(cmd.Args)-1] != "caller-argument" {
		t.Fatalf("wrapper changed original argv: %q", cmd.Args)
	}
	if !reflect.DeepEqual(cmd.Env, env) || cmd.Dir != dir || cmd.Stdin != os.Stdin || cmd.Stdout != os.Stdout || cmd.Stderr != os.Stderr || reflect.ValueOf(cmd.Cancel).Pointer() != reflect.ValueOf(cancel).Pointer() {
		t.Fatal("wrapper changed caller environment, directory, streams or cancellation")
	}
}

func TestHelperRejectsIncompletePayload(t *testing.T) {
	for _, args := range [][]string{nil, {policyArg}, {policyArg, "{}", "--"}, {policyArg, "{", "--", "/bin/false"}} {
		if got := RunHelper(args); got != 126 {
			t.Errorf("RunHelper(%q)=%d", args, got)
		}
	}
}
