// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package confine

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestMain serves HelperArg: Command re-executes this test binary as the helper.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == HelperArg {
		os.Exit(RunHelper(os.Args[2:]))
	}
	os.Exit(m.Run())
}

func TestPolicyRoundTrip(t *testing.T) {
	p := Policy{ReadWrite: []string{"/a", "/b"}, ReadOnly: []string{"/c"}, Protect: []string{"/d"}}
	args := append(p.encode(), "--", "/bin/sh", "-c", "--rw")
	got, argv, err := decode(args)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, p) || !reflect.DeepEqual(argv, []string{"/bin/sh", "-c", "--rw"}) {
		t.Fatalf("decode(encode) = %+v %v", got, argv)
	}
	for _, bad := range [][]string{nil, {"--rw"}, {"--rw", "/a"}, {"--x", "/a", "--", "p"}} {
		if _, _, err := decode(bad); err == nil {
			t.Errorf("decode(%q) accepted", bad)
		}
	}
}

func TestPolicyNeedsAbsolutePathsAndAWritablePath(t *testing.T) {
	for _, p := range []Policy{{}, {ReadWrite: []string{"rel"}}, {ReadWrite: []string{"/a"}, Protect: []string{"rel"}}} {
		if err := p.validate(); err == nil {
			t.Errorf("validate(%+v) accepted", p)
		}
	}
}

// fixture is an engine data directory with a sealing key and a session folder.
type fixture struct{ root, data, key, folder string }

func newFixture(t *testing.T) fixture {
	t.Helper()
	root := t.TempDir()
	f := fixture{root: root, data: filepath.Join(root, "data"), folder: filepath.Join(root, "folder")}
	f.key = filepath.Join(f.data, "secret-store.key")
	for _, d := range []string{f.data, f.folder} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(f.key, []byte("sealing-key-fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func requireLandlock(t *testing.T) {
	t.Helper()
	if s := Probe(); s.Mode != ModeLandlock {
		t.Skip("host cannot confine: " + s.Reason)
	}
}

// run starts sh -c script under p and returns its combined output.
func run(t *testing.T, p Policy, dir, script string) string {
	t.Helper()
	cmd, state, err := Command(context.Background(), p, "sh", "-c", script)
	if err != nil {
		t.Fatal(err)
	}
	if state.Mode != ModeLandlock {
		t.Fatalf("state = %v", state)
	}
	cmd.Dir = dir
	out, _ := cmd.CombinedOutput()
	return string(out)
}

func TestConfinedChildWritesItsFolderAndCannotReadTheEngineKey(t *testing.T) {
	requireLandlock(t)
	f := newFixture(t)
	out := run(t, Policy{ReadWrite: []string{f.folder}, Protect: []string{f.data}}, f.folder,
		`echo edit > notes.txt && cat notes.txt
cat `+f.key+` && echo KEY-READ || echo key-denied
echo x > `+filepath.Join(f.data, "planted")+` && echo DATA-WRITTEN || echo data-denied
ls /usr/bin > /dev/null && echo system-ok`)
	for _, want := range []string{"edit", "key-denied", "data-denied", "system-ok"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
	for _, leak := range []string{"KEY-READ", "DATA-WRITTEN", "sealing-key-fixture"} {
		if strings.Contains(out, leak) {
			t.Errorf("confined child reached the engine data: %q in\n%s", leak, out)
		}
	}
	if _, err := os.Stat(filepath.Join(f.data, "planted")); err == nil {
		t.Error("confined child wrote into the engine data directory")
	}
}

// A folder that contains the data directory (a home directory, say) is granted
// without it, and a symlink planted in the folder does not lead back into it.
func TestGrantContainingTheDataDirectoryIsCarved(t *testing.T) {
	requireLandlock(t)
	f := newFixture(t)
	if err := os.Symlink(f.key, filepath.Join(f.root, "key-link")); err != nil {
		t.Fatal(err)
	}
	out := run(t, Policy{ReadWrite: []string{f.root}, Protect: []string{f.data}}, f.root,
		`echo ok > folder/out && cat folder/out
cat data/secret-store.key && echo KEY-READ || echo key-denied
cat key-link && echo LINK-READ || echo link-denied`)
	for _, want := range []string{"ok", "key-denied", "link-denied"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sealing-key-fixture") {
		t.Errorf("confined child read the key:\n%s", out)
	}
}

// A path under the data directory that the policy names (a tool install, the
// run's hook settings) is readable; the rest of the data directory is not.
func TestNamedPathUnderTheDataDirectoryIsGranted(t *testing.T) {
	requireLandlock(t)
	f := newFixture(t)
	runDir := filepath.Join(f.data, "run", "r1")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "pep-settings.json"), []byte(`{"hooks":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out := run(t, Policy{ReadWrite: []string{f.folder}, ReadOnly: []string{runDir}, Protect: []string{f.data}}, f.folder,
		`cat `+filepath.Join(runDir, "pep-settings.json")+`
echo x > `+filepath.Join(runDir, "w")+` && echo RUN-WRITTEN || echo run-readonly
cat `+f.key+` && echo KEY-READ || echo key-denied`)
	for _, want := range []string{`{"hooks":{}}`, "run-readonly", "key-denied"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

func TestUnconfinedSameUserReadsTheKey(t *testing.T) {
	f := newFixture(t)
	if b, err := os.ReadFile(f.key); err != nil || string(b) != "sealing-key-fixture" {
		t.Fatalf("control: the engine user reads its own key: %q %v", b, err)
	}
}

// A sealed path is read-only for good: a writable path that is the same, lies
// inside it or contains it would reopen it (Landlock grants add up), so the
// policy refuses before anything starts. That includes the writable directories
// the helper grants by default. A writable path beside it is fine. Sealed paths
// survive the trip to the helper.
func TestPolicyRefusesAWritablePathThatReopensASealedOne(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy Policy
		ok     bool
	}{
		{"writable beside", Policy{ReadWrite: []string{"/w/tmp"}, ReadOnly: []string{"/w/project"}, Sealed: []string{"/w/project"}}, true},
		{"writable inside", Policy{ReadWrite: []string{"/w/project/tmp"}, ReadOnly: []string{"/w/project"}, Sealed: []string{"/w/project"}}, false},
		{"writable equal", Policy{ReadWrite: []string{"/w/project"}, Sealed: []string{"/w/project"}}, false},
		{"writable above", Policy{ReadWrite: []string{"/w"}, Sealed: []string{"/w/project"}}, false},
		{"nothing sealed", Policy{ReadWrite: []string{"/w"}, ReadOnly: []string{"/w/project"}}, true},
	} {
		if err := tc.policy.sealedConflict(nil); (err == nil) != tc.ok {
			t.Errorf("%s: sealedConflict = %v, want ok=%v", tc.name, err, tc.ok)
		}
	}
	defaults := []string{"/dev/shm"}
	if err := (Policy{ReadWrite: []string{"/w/tmp"}, Sealed: []string{"/dev/shm/project"}}).sealedConflict(defaults); err == nil {
		t.Error("a sealed path under a default writable directory was accepted")
	}

	p := Policy{ReadWrite: []string{"/a"}, ReadOnly: []string{"/c"}, Sealed: []string{"/c"}, Protect: []string{"/d"}}
	got, argv, err := decode(append(p.encode(), "--", "/bin/true"))
	if err != nil || !reflect.DeepEqual(got, p) || !reflect.DeepEqual(argv, []string{"/bin/true"}) {
		t.Fatalf("decode(encode) = %+v %v %v", got, argv, err)
	}
}
