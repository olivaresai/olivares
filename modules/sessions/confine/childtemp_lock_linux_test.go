// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package confine

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const secondEngineRootEnv = "OLIVARES_TEST_SECOND_ENGINE_CHILD_TEMP_ROOT"

// A second engine started by hand on the same data folder runs its start-up sweep before it fails on the busy port.
// The first engine still serves, and its sessions' temporary folders are live: the second engine must remove none of
// them. Two processes, as two engines are.
func TestASecondEngineSweepsNothingOfTheEngineThatHoldsTheRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tool-tmp")
	UseChildTempRoot(root)
	t.Cleanup(func() { UseChildTempRoot("") })
	if err := SweepChildTemp(); err != nil { // the first engine starts: from here on it holds the root
		t.Fatal(err)
	}
	live, err := ChildTempDir() // a live session's TMPDIR
	if err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(live, "work")
	if err := os.WriteFile(work, []byte("in use"), 0o600); err != nil {
		t.Fatal(err)
	}

	second := exec.Command(os.Args[0], "-test.run=^TestHelperSecondEngineSweep$", "-test.count=1", "-test.v")
	second.Env = append(os.Environ(), secondEngineRootEnv+"="+root)
	out, err := second.CombinedOutput()
	if err != nil {
		t.Fatalf("the second engine's sweep: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "--- PASS: TestHelperSecondEngineSweep") {
		t.Fatalf("the second engine did not run its sweep:\n%s", out)
	}
	if _, err := os.Stat(work); err != nil {
		t.Fatalf("the second engine's sweep removed a live session's temporary folder: %v\n%s", err, out)
	}
}

// TestHelperSecondEngineSweep is the second engine of the test above; on its own it does nothing.
func TestHelperSecondEngineSweep(t *testing.T) {
	root := os.Getenv(secondEngineRootEnv)
	if root == "" {
		t.Skip("runs only as the second engine of TestASecondEngineSweepsNothingOfTheEngineThatHoldsTheRoot")
	}
	UseChildTempRoot(root)
	if err := SweepChildTemp(); !errors.Is(err, ErrChildTempHeld) {
		t.Fatalf("the second engine's sweep = %v, want ErrChildTempHeld", err)
	}
}

const (
	handOverRoleEnv = "OLIVARES_TEST_HAND_OVER_ROLE"
	handOverDirEnv  = "OLIVARES_TEST_HAND_OVER_DIR"
)

// The documented --reuse-port hand-over (docs/UPGRADE-AND-ROLLBACK.md): engine B starts beside A and serves, A exits,
// and a later engine C starts beside B. B must hold the root once A is gone, so C's start-up sweep removes none of B's
// live sessions' temporary folders. Three engine processes, driven by files in a scratch folder.
func TestAHandOverEngineHoldsTheRootOnceThePreviousOneExits(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "tool-tmp")
	engine := func(role string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHandOverEngine$", "-test.count=1", "-test.v")
		cmd.Env = append(os.Environ(), handOverRoleEnv+"="+role, handOverDirEnv+"="+dir, secondEngineRootEnv+"="+root)
		return cmd
	}
	waitFile := func(name string) string {
		t.Helper()
		if !appears(filepath.Join(dir, name), 20*time.Second) {
			t.Fatalf("%s never appeared", name)
		}
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(content)
	}
	a, b := engine("A"), engine("B")
	var aOut, bOut strings.Builder
	a.Stdout, a.Stderr, b.Stdout, b.Stderr = &aOut, &aOut, &bOut, &bOut
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(filepath.Join(dir, "A.stop"), nil, 0o600); _ = a.Wait() })
	waitFile("A.serving")
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(filepath.Join(dir, "B.stop"), nil, 0o600); _ = b.Wait() })
	work := waitFile("B.serving") // B's live session folder
	if err := os.WriteFile(filepath.Join(dir, "A.stop"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Wait(); err != nil {
		t.Fatalf("engine A: %v\n%s", err, aOut.String())
	}
	if !appears(filepath.Join(dir, "B.holds"), 20*time.Second) {
		_ = b.Wait() // B ends on its own deadline and says why
		t.Fatalf("engine B never held the root after engine A exited:\n%s", bOut.String())
	}
	c := engine("C")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("engine C: %v\n%s", err, out)
	}
	if _, err := os.Stat(work); err != nil {
		t.Fatalf("engine C's sweep removed engine B's live session folder: %v\nB:\n%s", err, bOut.String())
	}
}

// TestHelperHandOverEngine is engine A, B or C of the test above; on its own it does nothing.
func TestHelperHandOverEngine(t *testing.T) {
	role, dir, root := os.Getenv(handOverRoleEnv), os.Getenv(handOverDirEnv), os.Getenv(secondEngineRootEnv)
	if role == "" {
		t.Skip("runs only as an engine of TestAHandOverEngineHoldsTheRootOnceThePreviousOneExits")
	}
	UseChildTempRoot(root)
	mark := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	waitStop := func() {
		for {
			if _, err := os.Stat(filepath.Join(dir, role+".stop")); err == nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	switch role {
	case "A":
		if err := SweepChildTemp(); err != nil {
			t.Fatal(err)
		}
		mark("A.serving", "")
		waitStop()
	case "B":
		if err := SweepChildTemp(); !errors.Is(err, ErrChildTempHeld) {
			t.Fatalf("B's sweep beside A = %v, want ErrChildTempHeld", err)
		}
		live, err := ChildTempDir()
		if err != nil {
			t.Fatal(err)
		}
		work := filepath.Join(live, "work")
		if err := os.WriteFile(work, []byte("in use"), 0o600); err != nil {
			t.Fatal(err)
		}
		mark("B.serving", work)
		for deadline := time.Now().Add(15 * time.Second); !childTempHeld(root); time.Sleep(20 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal("B never held the root after A exited: a later engine's sweep would remove B's live folders")
			}
		}
		mark("B.holds", "")
		waitStop()
	case "C":
		if err := SweepChildTemp(); !errors.Is(err, ErrChildTempHeld) {
			t.Fatalf("C's sweep beside B = %v, want ErrChildTempHeld", err)
		}
	}
}

// childTempHeld reports whether this process holds root's lock.
func childTempHeld(root string) bool {
	held.Lock()
	defer held.Unlock()
	return held.locks[root] != nil
}

// appears waits up to d for path to exist.
func appears(path string, d time.Duration) bool {
	for deadline := time.Now().Add(d); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	return false
}
