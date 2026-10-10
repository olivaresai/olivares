// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package confine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFilesystemFailureReachesReadinessPipe(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"legacy", []string{"--rw", "relative", "--", "/bin/true"}, "not an absolute path"},
		{"shared", []string{"--policy-v1", `{"Program":"/bin/true","Policy":{"ReadWrite":["relative"]}}`, "--", "/bin/true"}, "not an absolute path"},
		{"unexpected argument", []string{"secret-fixture", "--", "/bin/true"}, "unexpected confinement argument"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			code := RunHelperWithReady(tc.args, writer)
			_ = writer.Close()
			body, err := io.ReadAll(reader)
			_ = reader.Close()
			if code != 126 || err != nil || !strings.Contains(string(body), tc.want) || strings.Contains(string(body), "secret-fixture") {
				t.Fatalf("confinement failure reason=%q, code=%d, err=%v", body, code, err)
			}
		})
	}
}

// TestMain serves HelperArg: Command re-executes this test binary as the helper.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "__ready_failure" {
		os.Exit(RunHelperWithReady(os.Args[2:], os.NewFile(3, "ready")))
	}
	if len(os.Args) > 1 && os.Args[1] == "__owned_policy_probe" {
		_, err := os.ReadFile(os.Args[2])
		switch {
		case err == nil:
			fmt.Println("read-ok")
		case errors.Is(err, os.ErrPermission):
			fmt.Println("read-denied")
		default:
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) > 1 && os.Args[1] == HelperArg {
		os.Exit(RunHelper(os.Args[2:]))
	}
	os.Exit(m.Run())
}

func TestFailedExecKeepsReasonAfterReadyMarker(t *testing.T) {
	requireLandlock(t)
	dir := t.TempDir()
	program := filepath.Join(dir, "missing-interpreter")
	if err := os.WriteFile(program, []byte("#!/olivares-fixture-no-such-interpreter\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"Program": program, "Policy": map[string]any{"ReadWrite": []string{dir}}})
	if err != nil {
		t.Fatal(err)
	}
	for i, args := range [][]string{
		{"--rw", dir, "--", program},
		{"--policy-v1", string(payload), "--", program},
	} {
		t.Run([]string{"legacy", "shared"}[i], func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			cmd := exec.CommandContext(ctx, os.Args[0], append([]string{"__ready_failure"}, args...)...)
			cmd.ExtraFiles = []*os.File{writer}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			_ = writer.Close()
			if err := reader.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(reader)
			_ = reader.Close()
			waitErr := cmd.Wait()
			cancel()
			var exited *exec.ExitError
			if err != nil || !errors.As(waitErr, &exited) || exited.ExitCode() != 126 || !strings.HasPrefix(string(body), "ready\n") || !strings.Contains(string(body), "no such file or directory") {
				t.Fatalf("failed exec reason=%q, read=%v, wait=%v", body, err, waitErr)
			}
		})
	}
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

// A tool installed by npm under a prefix outside the system directories
// (npm --prefix, nvm, ~/.npm-global) is a launcher in one package that runs a
// binary from a sibling package of the same node_modules tree: Codex's
// node_modules/.bin/codex -> @openai/codex/bin/codex.js spawns
// @openai/codex-linux-x64/vendor/.../codex. The whole tree is the install and
// can be read and run; the prefix around it and a protected path inside it stay
// closed.
func TestNpmLauncherRunsItsSiblingPackageUnderANonSystemPrefix(t *testing.T) {
	requireLandlock(t)
	f := newFixture(t)
	prefix := filepath.Join(f.root, "tools")
	modules := filepath.Join(prefix, "node_modules")
	native := filepath.Join(modules, "@openai", "codex-linux-x64", "vendor", "bin", "codex")
	launcher := filepath.Join(modules, "@openai", "codex", "bin", "codex.js")
	for path, body := range map[string]string{
		native: "#!/bin/sh\necho native-ran\n",
		launcher: "#!/bin/sh\ncat " + filepath.Join(prefix, "outside.txt") + " && echo PREFIX-READ || echo prefix-denied\n" +
			"cat " + filepath.Join(modules, ".protected", "key") + " && echo PROTECTED-READ || echo protected-denied\nexec " + native + "\n",
		filepath.Join(modules, ".protected", "key"): "protected-fixture",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(modules, ".bin", "codex")
	if err := os.MkdirAll(filepath.Dir(bin), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../@openai/codex/bin/codex.js", bin); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(prefix, "outside.txt")
	if err := os.WriteFile(outside, []byte("prefix-file"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd, _, err := Command(context.Background(), Policy{ReadWrite: []string{f.folder}, Protect: []string{f.data, filepath.Join(modules, ".protected")}}, bin)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Dir = f.folder
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "native-ran") {
		t.Fatalf("the npm launcher did not run its sibling package: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "prefix-denied") || !strings.Contains(string(out), "protected-denied") {
		t.Fatalf("the npm launcher reached its prefix outside node_modules or a protected path inside it:\n%s", out)
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

// Consumers keep the session package's published type identities, including
// serializers that name a Go type by its package path. Policy stays four-field.
func TestSessionPublishedTypesRemainInTheirPackage(t *testing.T) {
	for _, value := range []any{Policy{}, State{}, ModeNone} {
		if got := reflect.TypeOf(value).PkgPath(); got != "github.com/olivaresai/olivares/modules/sessions/confine" {
			t.Errorf("published %T moved to %s", value, got)
		}
	}
	if fields := reflect.TypeOf(Policy{}).NumField(); fields != 4 {
		t.Errorf("published session Policy has %d fields, want four", fields)
	}
}
