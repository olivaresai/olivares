// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux

package main

// license_connect_transition_test.go interrupts the key transitions — rotate-key, recover and
// reactivate — at every durable boundary of commitKeyTransition. The completing command runs in a
// CHILD process through the real command tree, which is killed with SIGKILL or whose regular-file
// writes start failing with EFBIG (RLIMIT_FSIZE) at a named boundary; the same command is then run
// again in this process, as a restart. The service is the protocol-unit stub of
// license_connect_test.go, with the same caveat: it is not the license Worker and not D1.

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/license/connectv1"
)

const (
	// envConnectFaultChild is <boundary>:<crash|efbig|none>; envConnectFaultArgv the JSON argv.
	envConnectFaultChild = "OLIVARES_CONNECT_FAULT_CHILD"
	envConnectFaultArgv  = "OLIVARES_CONNECT_FAULT_ARGV"

	// Child exit statuses. Any other status, or a crash that is not SIGKILL, is a broken harness,
	// not an answer of the client.
	connectChildWriteFailed = 23 // efbig: the command failed with EFBIG after the boundary
	connectChildNotReached  = 3  // the boundary was never reached
	connectChildUnexpected  = 4
)

// TestConnectFaultChild is not a test: it is the helper process the transition tests re-execute.
func TestConnectFaultChild(t *testing.T) {
	spec := os.Getenv(envConnectFaultChild)
	if spec == "" {
		t.Skip("helper process for the connect key transition tests")
	}
	boundary, mode, _ := strings.Cut(spec, ":")
	var argv []string
	if err := json.Unmarshal([]byte(os.Getenv(envConnectFaultArgv)), &argv); err != nil {
		os.Exit(2)
	}
	reached := false
	connectStepHook = func(b string) {
		if b != boundary {
			return
		}
		reached = true
		switch mode {
		case "crash":
			_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
			select {}
		case "efbig":
			// From here every write to a regular file fails with EFBIG instead of raising SIGXFSZ.
			signal.Ignore(syscall.SIGXFSZ)
			if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 0, Max: 0}); err != nil {
				fmt.Println("setrlimit:", err)
				os.Exit(2)
			}
		}
	}
	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(argv)
	_, err := root.ExecuteC()
	fmt.Printf("stdout: %s\nstderr: %s\nerr: %v\n", out.String(), errb.String(), err)
	switch {
	case mode == "none" && err == nil:
		os.Exit(0)
	case mode != "none" && !reached:
		os.Exit(connectChildNotReached)
	case mode == "efbig" && errors.Is(err, syscall.EFBIG):
		os.Exit(connectChildWriteFailed)
	}
	os.Exit(connectChildUnexpected)
}

// runConnectFaultChild runs argv in a child process that stops at boundary as mode says.
func runConnectFaultChild(t *testing.T, boundary, mode string, argv []string) (*os.ProcessState, string) {
	t.Helper()
	enc, err := json.Marshal(argv)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestConnectFaultChild$", "-test.count=1")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(),
		"OLIVARES_CLI_TRAMPOLINE=", // the child must be a test process, not the CLI
		"OLIVARES_CLI_CONFIG="+filepath.Join(cmd.Dir, "config.yaml"),
		"OLIVARES_SERVER_URL=", "OLIVARES_TOKEN=", "OLIVARES_TENANT=",
		"OLIVARES_LICENSE=", "OLIVARES_LICENSE_PATH=",
		envConnectFaultChild+"="+boundary+":"+mode,
		envConnectFaultArgv+"="+string(enc),
	)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("the child did not run: %v", err)
		}
	}
	t.Logf("child %s:%s -> %v\n%s", boundary, mode, cmd.ProcessState, out.String())
	return cmd.ProcessState, out.String()
}

// identityKIDAt returns the KID of the identity file at path, derived from its seed.
func identityKIDAt(t *testing.T, path string) (string, bool) {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	var w connectIdentityWire
	if err := json.Unmarshal(data, &w); err != nil {
		t.Fatal(err)
	}
	seed, err := base64.RawURLEncoding.DecodeString(w.Seed)
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Fatalf("%s holds no valid seed", path)
	}
	kid, _ := connectv1.KID(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
	if kid != w.KID {
		t.Fatalf("%s names a key its seed does not produce", path)
	}
	return kid, true
}

// keyTransition is a data directory one command away from completing intent.
type keyTransition struct {
	intent  string
	stub    *connectStub
	c       *connectCLI
	dep     string
	args    []string // the completing command, for connectCLI.run
	oldKID  string   // the bound key before the transition
	oldKept bool     // whether identity.key holds the old key before promotion
}

func (k keyTransition) argv() []string {
	return append(append([]string{"license", "connect"}, k.args...), "--data-dir", k.c.dir)
}

// arrangeKeyTransition binds a data directory and, for recover and reactivate, obtains the owner's
// approval, so the next run of args sends the completing step.
func arrangeKeyTransition(t *testing.T, intent string) keyTransition {
	t.Helper()
	stub := newConnectStub(t)
	c := newConnectCLI(t, stub)
	k := keyTransition{intent: intent, stub: stub, c: c, dep: c.bind(), oldKept: true}
	k.oldKID = c.state().Binding.PopKID
	switch intent {
	case "rotate":
		k.args = []string{"rotate-key"}
	case "recover":
		if err := os.Remove(filepath.Join(c.dir, connectDirName, connectIdentityFileName)); err != nil {
			t.Fatal(err)
		}
		k.oldKept = false
		k.args = []string{"recover", "--deployment", k.dep}
	case "reactivate":
		if code, rep := c.run("deactivate", "--yes"); code != exitcode.OK || rep["status"] != "deactivated" {
			t.Fatalf("deactivate = %d %v", code, rep)
		}
		k.args = []string{"reactivate", "--deployment", k.dep}
	default:
		t.Fatalf("unknown intent %q", intent)
	}
	if intent != "rotate" {
		// The NEW request presents the purchase credential. k.args, the completing command, does not:
		// every run from here repeats the recorded request or completion without reading evidence.
		code, rep := c.run(append(append([]string(nil), k.args...), "--evidence", c.purchaseEvidence())...)
		if code != exitcode.OK || rep["status"] != "approval_pending" {
			t.Fatalf("%s request = %d %v", intent, code, rep)
		}
		stub.approve(requestIDOf(t, rep))
	}
	return k
}

// TestConnectKeyTransitionSurvivesCrashAndWriteFailureAtEveryBoundary: for rotate-key, recover and
// reactivate, a child is killed or its file writes fail at each boundary. Before the completion is
// recorded, the restart repeats the SAME step (bytes, Idempotency-Key, original epoch, proofs) and
// the service serves its stored result; after, the restart completes locally and sends nothing. In
// every case the service commits one transition, the proposed key ends as the only identity, and
// that key refreshes. The fault-free row is the normal flow through the same child harness.
func TestConnectKeyTransitionSurvivesCrashAndWriteFailureAtEveryBoundary(t *testing.T) {
	faults := []struct {
		boundary, mode string
		recorded       bool // the verified completion is durable when the child stops
		installed      bool // the returned credential is installed when the child stops
	}{
		{"", "none", true, true},
		{"credential-verified", "crash", false, false},
		{"credential-verified", "efbig", false, false}, // installing the credential fails
		{"credential-installed", "crash", false, true},
		{"credential-installed", "efbig", false, true}, // recording the completion fails
		{"transition-recorded", "crash", true, true},
		{"transition-promoted", "crash", true, true},
		{"transition-promoted", "efbig", true, true}, // recording the new binding fails
	}
	for _, intent := range []string{"rotate", "recover", "reactivate"} {
		for _, f := range faults {
			name := intent + "/normal"
			if f.mode != "none" {
				name = intent + "/" + f.mode + "@" + f.boundary
			}
			t.Run(name, func(t *testing.T) {
				k := arrangeKeyTransition(t, intent)
				c, stub := k.c, k.stub
				dir := filepath.Join(c.dir, connectDirName)
				idPath, nextPath := filepath.Join(dir, connectIdentityFileName), filepath.Join(dir, connectNextKeyFileName)
				statePath := filepath.Join(dir, connectStateFileName)
				licBefore := c.license()

				ps, out := runConnectFaultChild(t, f.boundary, f.mode, k.argv())
				c.outputs = append(c.outputs, out)
				switch f.mode {
				case "none":
					if !ps.Success() {
						t.Fatalf("the fault-free child failed: %v", ps)
					}
				case "crash":
					ws, _ := ps.Sys().(syscall.WaitStatus)
					if !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
						t.Fatalf("the child was not killed at %s: %v", f.boundary, ps)
					}
				case "efbig":
					if ps.ExitCode() != connectChildWriteFailed {
						t.Fatalf("the child did not fail with EFBIG after %s: %v", f.boundary, ps)
					}
				}
				stub.mu.Lock()
				boundKID, boundEpoch, transitions := stub.deps[k.dep].kid, stub.deps[k.dep].epoch, stub.transitions[k.dep]
				stub.mu.Unlock()
				if transitions != 1 || boundEpoch != 2 || boundKID == k.oldKID {
					t.Fatalf("the service committed %d transitions to epoch %d, want one to epoch 2 with a new key", transitions, boundEpoch)
				}

				if f.mode != "none" {
					mid := c.state()
					step := mid.Pending
					if step == nil || step.Intent != intent || step.BindingEpoch != 1 {
						t.Fatalf("the pending step must survive the interruption: %+v", step)
					}
					if mid.Binding == nil || mid.Binding.BindingEpoch != 1 || mid.Binding.PopKID != k.oldKID {
						t.Fatalf("the binding changed before the transition completed: %+v", mid.Binding)
					}
					if (mid.Completion != nil) != f.recorded {
						t.Fatalf("completion recorded = %v, want %v", mid.Completion != nil, f.recorded)
					}
					if f.recorded && (mid.Completion.Intent != intent || mid.Completion.DeploymentID != k.dep ||
						mid.Completion.PopKID != boundKID || mid.Completion.BindingEpoch != 2) {
						t.Fatalf("the recorded completion is not the committed transition: %+v", mid.Completion)
					}
					if installed := !bytes.Equal(c.license(), licBefore); installed != f.installed {
						t.Fatalf("credential installed = %v, want %v", installed, f.installed)
					}
					// Key custody at the interruption: both roles until the promotion, then only the
					// proposed key, never neither.
					curKID, curOK := identityKIDAt(t, idPath)
					nextKID, nextOK := identityKIDAt(t, nextPath)
					if f.boundary == "transition-promoted" {
						if nextOK || !curOK || curKID != boundKID {
							t.Fatalf("after promotion: current %q (%v), next %q (%v)", curKID, curOK, nextKID, nextOK)
						}
					} else {
						if !nextOK || nextKID != boundKID || curOK != k.oldKept || (curOK && curKID != k.oldKID) {
							t.Fatalf("before promotion: current %q (%v), next %q (%v)", curKID, curOK, nextKID, nextOK)
						}
					}

					if f.recorded {
						// Only the local completion remains: other commands name it, abandon refuses to
						// discard it, status shows it, and none of them changes a byte.
						stateBefore := c.file(statePath)
						code, rep := c.run("status")
						if comp, _ := rep["completion"].(map[string]any); code != exitcode.OK || rep["status"] != "pending" || comp["binding_epoch"] != float64(2) {
							t.Fatalf("status = %d %v", code, rep)
						}
						if code, _ := c.run("abandon", "--yes"); code != exitcode.Conflict {
							t.Fatalf("abandon of a recorded completion exit %d, want Conflict", code)
						}
						if code, _ := c.run("refresh"); code != exitcode.Conflict {
							t.Fatalf("refresh during a recorded completion exit %d, want Conflict", code)
						}
						if !bytes.Equal(c.file(statePath), stateBefore) {
							t.Fatal("a refused command changed the recorded state")
						}
					}

					stub.mu.Lock()
					sentBefore := len(stub.seen)
					stub.mu.Unlock()
					code, rep := c.run(k.args...)
					if code != exitcode.OK || rep["status"] != connectTransitionDone[intent] {
						t.Fatalf("restart = %d %v", code, rep)
					}
					stub.mu.Lock()
					sent := append([]stubSeen(nil), stub.seen[sentBefore:]...)
					var epochs []int64
					for _, ch := range stub.challenges {
						if ch.idem == step.IdempotencyKey {
							epochs = append(epochs, ch.epoch)
						}
					}
					transitions = stub.transitions[k.dep]
					stub.mu.Unlock()
					if transitions != 1 {
						t.Fatalf("the restart committed %d transitions, want the original one", transitions)
					}
					if f.recorded {
						if len(sent) != 0 {
							t.Fatalf("a recorded completion must finish without contacting the service; %d requests were sent", len(sent))
						}
					} else {
						var repeats []stubSeen
						for _, s := range sent {
							if s.path == step.Path {
								repeats = append(repeats, s)
							}
						}
						if len(repeats) != 1 {
							t.Fatalf("the restart sent the step %d times, want one repeat", len(repeats))
						}
						r := repeats[0]
						if r.header.Get(connectv1.HeaderIdempotencyKey) != step.IdempotencyKey || connectv1.BodyDigest(r.body) != step.BodySHA256 {
							t.Fatal("the restart did not repeat the same operation")
						}
						if hasNew := r.header.Get(connectv1.HeaderNewKeyProof) != ""; hasNew != (intent == "rotate") {
							t.Fatalf("new-key proof present = %v on the %s repeat", hasNew, intent)
						}
						if len(epochs) != 2 {
							t.Fatalf("%d challenges for the step, want the original and the repeat", len(epochs))
						}
						for _, e := range epochs {
							if e != 1 {
								t.Fatalf("a challenge of the step used epoch %d; the repeat keeps the original epoch 1", e)
							}
						}
					}
				}

				final := c.state()
				if final.Pending != nil || final.Completion != nil || final.Request != nil || final.Binding == nil ||
					final.Binding.DeploymentID != k.dep || final.Binding.PopKID != boundKID || final.Binding.BindingEpoch != 2 || final.Binding.Status != "active" {
					t.Fatalf("final state: %+v binding %+v", final, final.Binding)
				}
				if kid, ok := identityKIDAt(t, idPath); !ok || kid != boundKID {
					t.Fatalf("identity.key holds %q (%v), want the key the service bound", kid, ok)
				}
				if _, err := os.Lstat(nextPath); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("the proposed identity file remains after completion: %v", err)
				}
				for p, want := range map[string]os.FileMode{dir: 0o700, idPath: 0o600, statePath: 0o600} {
					if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != want {
						t.Fatalf("%s mode: %v %v, want %04o", p, fi, err, want)
					}
				}
				kr, _ := licenseKeyringForDataDir(c.dir)
				if v, err := kr.Verify(strings.TrimSpace(string(c.license())), time.Now()); err != nil ||
					v.Credential.Deployment != k.dep || v.Status(time.Now()) != license.StatusValid {
					t.Fatalf("the installed credential: %v", err)
				}
				if code, rep := c.run("refresh"); code != exitcode.OK || rep["status"] != "refreshed" {
					t.Fatalf("refresh with the promoted key = %d %v", code, rep)
				}
				c.assertNoSecretsLeaked()
			})
		}
	}
}

// TestConnectRecordedCompletionRefusesAnotherKey: the local completion promotes only the key the
// recorded completion names. A proposed identity file replaced by another key is refused and
// nothing changes; with the right file back, the same command completes.
func TestConnectRecordedCompletionRefusesAnotherKey(t *testing.T) {
	k := arrangeKeyTransition(t, "rotate")
	c := k.c
	dir := filepath.Join(c.dir, connectDirName)
	nextPath := filepath.Join(dir, connectNextKeyFileName)
	if ps, _ := runConnectFaultChild(t, "transition-recorded", "crash", k.argv()); ps.Success() {
		t.Fatal("the child was not interrupted")
	}
	proposed := c.file(nextPath)
	pub, priv := keyFromSeedForTest(0x33)
	kid, _ := connectv1.KID(pub)
	other, err := json.Marshal(connectIdentityWire{Schema: connectIdentitySchema, KID: kid, PublicKey: connectv1.EncodePublicKey(pub),
		Seed: base64.RawURLEncoding.EncodeToString(priv.Seed())})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nextPath, other, 0o600); err != nil {
		t.Fatal(err)
	}
	current, state := c.file(filepath.Join(dir, connectIdentityFileName)), c.file(filepath.Join(dir, connectStateFileName))
	if code, _ := c.run(k.args...); code == exitcode.OK {
		t.Fatal("a proposed identity that is not the recorded key was promoted")
	}
	if !bytes.Equal(c.file(filepath.Join(dir, connectIdentityFileName)), current) || !bytes.Equal(c.file(nextPath), other) ||
		!bytes.Equal(c.file(filepath.Join(dir, connectStateFileName)), state) {
		t.Fatal("a refused completion changed an identity or the state")
	}
	if err := os.WriteFile(nextPath, proposed, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, rep := c.run(k.args...); code != exitcode.OK || rep["status"] != "rotated" {
		t.Fatalf("completion with the recorded key = %d %v", code, rep)
	}
}
