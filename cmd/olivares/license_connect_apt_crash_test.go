// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package main

// license_connect_apt_crash_test.go kills apt-refresh with SIGKILL at its two durable boundaries — after
// the answer checks, and after the handoff's publication but before the pending operation is cleared — in
// a CHILD process through the real command tree, then runs the next cycle in this process. The service is
// the apt stub of license_connect_apt_test.go.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

const (
	envAptFaultChild   = "OLIVARES_APT_FAULT_CHILD"   // the boundary the child is killed at
	envAptFaultArgv    = "OLIVARES_APT_FAULT_ARGV"    // the JSON argv
	envAptFaultHandoff = "OLIVARES_APT_FAULT_HANDOFF" // the handoff directory standing in for the fixed one
)

// TestConnectAptFaultChild is not a test: it is the helper process the crash test re-executes.
func TestConnectAptFaultChild(t *testing.T) {
	boundary := os.Getenv(envAptFaultChild)
	if boundary == "" {
		t.Skip("helper process for the apt-refresh crash test")
	}
	var argv []string
	if err := json.Unmarshal([]byte(os.Getenv(envAptFaultArgv)), &argv); err != nil {
		os.Exit(2)
	}
	aptHandoffDir = os.Getenv(envAptFaultHandoff)
	connectStepHook = func(b string) {
		if b == boundary {
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			select {}
		}
	}
	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(argv)
	_, err := root.ExecuteC()
	fmt.Printf("stdout: %s\nstderr: %s\nerr: %v\n", out.String(), errb.String(), err)
	os.Exit(connectChildNotReached)
}

// runAptFaultChild runs apt-refresh for cycle in a child process that is killed at boundary.
func runAptFaultChild(t *testing.T, c *aptCLI, boundary, cycle string) {
	t.Helper()
	enc, err := json.Marshal([]string{"license", "connect", "apt-refresh", "--cycle", cycle, "--data-dir", c.dir})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestConnectAptFaultChild$", "-test.count=1")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(),
		"OLIVARES_CLI_TRAMPOLINE=", // the child must be a test process, not the CLI
		"OLIVARES_CLI_CONFIG="+filepath.Join(cmd.Dir, "config.yaml"),
		"OLIVARES_SERVER_URL=", "OLIVARES_TOKEN=", "OLIVARES_TENANT=",
		"OLIVARES_LICENSE=", "OLIVARES_LICENSE_PATH=",
		envAptFaultChild+"="+boundary,
		envAptFaultArgv+"="+string(enc),
		envAptFaultHandoff+"="+c.handoff,
	)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("the child did not run: %v", err)
		}
	}
	c.outputs = append(c.outputs, out.String())
	t.Logf("child %s -> %v\n%s", boundary, cmd.ProcessState, out.String())
	ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("the child was not stopped at %s: %v", boundary, cmd.ProcessState)
	}
}

// TestConnectAptRefreshCrashLeavesTheOperationPending: for issued, the order is the answer checks, the
// handoff's publication, then clearing the pending operation (r2 §3.1.4.2). A crash after the checks
// publishes nothing; a crash between publication and clearing leaves an issued handoff AND the operation
// pending. Either way the next cycle repeats the same operation and receives the stored result: the same
// credential, no second mint.
func TestConnectAptRefreshCrashLeavesTheOperationPending(t *testing.T) {
	for _, tc := range []struct {
		boundary  string
		published bool
	}{
		{"apt-answer-checked", false},
		{"apt-handoff-published", true},
	} {
		t.Run(tc.boundary, func(t *testing.T) {
			c := newAptCLI(t)
			first := aptCycle(1)
			runAptFaultChild(t, c, tc.boundary, first)

			p := c.state().Pending
			if p == nil || p.Intent != "apt-refresh" {
				t.Fatalf("a crash at %s must leave the operation pending: %+v", tc.boundary, p)
			}
			_, err := os.Lstat(filepath.Join(c.handoff, first+".json"))
			if published := err == nil; published != tc.published {
				t.Fatalf("a crash at %s: handoff published %v, want %v", tc.boundary, published, tc.published)
			}
			if tc.published {
				if m := c.assertOutcome(first, "issued", nil); m["credential"] != c.apt.mintedCredential(0) {
					t.Fatal("the published handoff does not carry the minted credential")
				}
			}
			for _, name := range c.handoffFiles() {
				if strings.HasSuffix(name, ".tmp") {
					t.Fatalf("a crash at %s left %s", tc.boundary, name)
				}
			}

			second := aptCycle(2)
			if code := c.aptRefresh(second); code != exitcode.OK {
				t.Fatalf("the next cycle: exit %d (%s)", code, c.lastError)
			}
			m := c.assertOutcome(second, "issued", nil)
			if sends, minted := c.apt.counts(); sends != 2 || minted != 1 || m["credential"] != c.apt.mintedCredential(0) {
				t.Fatalf("%d requests and %d credentials; the repeat must receive the stored credential", sends, minted)
			}
			if q := c.state().Pending; q != nil {
				t.Fatalf("the issued repeat did not clear the operation: %+v", q)
			}
			c.assertCredentialOnlyInHandoff()
		})
	}
}
