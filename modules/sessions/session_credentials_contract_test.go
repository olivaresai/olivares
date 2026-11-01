// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/license"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The core checks a broker's result by what it names, not by its shape: a file it grants a
// confined child is a regular file of its own, a breach is declined and never retried, and
// nothing a broker names reaches the log.

// credentialHarnessConfined is credentialHarness with the engine's data directory protected
// from confined children, as a composed engine protects it.
func credentialHarnessConfined(t *testing.T, dataDir string) (*Module, model.TenantID, *fakeRunner, *fakeCredentialBroker) {
	t.Helper()
	return credentialHarnessData(t, []string{dataDir}, dataDir, dataDir)
}

// credentialHarnessData protects protect, tells the module its data directory is moduleDataDir,
// and makes the broker build its paths from brokerDataDir, as the Business broker builds them
// from the unresolved data directory the engine was given.
func credentialHarnessData(t *testing.T, protect []string, moduleDataDir, brokerDataDir string) (*Module, model.TenantID, *fakeRunner, *fakeCredentialBroker) {
	t.Helper()
	fr := &fakeRunner{}
	gate := &spyGate{inner: LaunchDecision{Allowed: true}}
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()), WithLaunchGate(gate), WithConfinement(protect, false), WithCredentialDataDir(moduleDataDir))
	stopModuleAtCleanup(t, m)
	broker := newFakeCredentialBroker(t)
	broker.st = st
	broker.dataDir = brokerDataDir
	m.CredentialBroker = broker
	return m, tenant, fr, broker
}

// captureWarnings points the module's log at a buffer and returns it.
func captureWarnings(m *Module) *bytes.Buffer {
	var buf bytes.Buffer
	m.log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return &buf
}

// assertReleasedOnce waits for the broker's helpers to be released and checks that no second
// release follows: a helper released twice would be revoked twice.
func assertReleasedOnce(t *testing.T, broker *fakeCredentialBroker) {
	t.Helper()
	broker.awaitRelease(t)
	select {
	case <-broker.released:
		t.Fatal("the helpers were released twice")
	case <-time.After(100 * time.Millisecond):
	}
}

// writeCredentialFile makes path a regular file of its own, its directories included.
func writeCredentialFile(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("Host prod\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// damageStoredCredentialNames overwrites a run row's stored credential list, as a damaged row would be.
func damageStoredCredentialNames(t *testing.T, m *Module, tenant model.TenantID, runRef, raw string) {
	t.Helper()
	ctx := context.Background()
	if err := m.runtimeData(ctx).Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := findRunRec(ctx, repo, runRef)
		if err != nil {
			return err
		}
		rec[colRunCredentials] = raw
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatalf("damage the stored credential list: %v", err)
	}
}

// A file the broker grants a confined child is a regular file of its own: not a directory (the
// sandbox would grant its whole tree), not a link, and not inside a protected folder unless it
// is in this run's own directory there.
func TestCredentials_ABrokerGrantsOnlyARegularFileOfItsOwnRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, tc := range map[string]struct {
		setup func(t *testing.T, dataDir string) string
		own   bool // the broker's own file in this run's directory: granted
	}{
		"its own file in this run's directory": {own: true},
		"a file in the protected folder": {setup: func(t *testing.T, dataDir string) string {
			return writeCredentialFile(t, filepath.Join(dataDir, "vault.db"))
		}},
		"a file in another run's directory": {setup: func(t *testing.T, dataDir string) string {
			return writeCredentialFile(t, filepath.Join(dataDir, "run", "run-someone-else", "ssh_config"))
		}},
		"a directory in the protected folder": {setup: func(t *testing.T, dataDir string) string {
			dir := filepath.Join(dataDir, "keys")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			return dir
		}},
		"a directory outside the protected folder": {setup: func(t *testing.T, _ string) string { return t.TempDir() }},
		"a link to a file": {setup: func(t *testing.T, _ string) string {
			target := writeCredentialFile(t, filepath.Join(t.TempDir(), "id_ed25519"))
			link := filepath.Join(t.TempDir(), "id")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			return link
		}},
		"a hard link to a file of the protected folder": {setup: func(t *testing.T, dataDir string) string {
			target := writeCredentialFile(t, filepath.Join(dataDir, "vault.db"))
			link := filepath.Join(t.TempDir(), "ssh_config")
			if err := os.Link(target, link); err != nil {
				t.Fatal(err)
			}
			return link
		}},
		"a file of the protected folder reached through a symlinked folder": {setup: func(t *testing.T, dataDir string) string {
			writeCredentialFile(t, filepath.Join(dataDir, "vault.db"))
			link := filepath.Join(t.TempDir(), "keys")
			if err := os.Symlink(dataDir, link); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(link, "vault.db")
		}},
		"a path through a regular file": {setup: func(t *testing.T, _ string) string {
			return filepath.Join(writeCredentialFile(t, filepath.Join(t.TempDir(), "id")), "ssh_config")
		}},
		"a path with a NUL byte": {setup: func(t *testing.T, _ string) string {
			return filepath.Join(t.TempDir(), "x\x00y")
		}},
		"a path through a symlink loop": {setup: func(t *testing.T, _ string) string {
			loop := filepath.Join(t.TempDir(), "loop")
			if err := os.Symlink(loop, loop); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(loop, "ssh_config")
		}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dataDir := t.TempDir()
			m, tenant, fr, broker := credentialHarnessConfined(t, dataDir)
			if tc.own {
				broker.runFile = true
			} else {
				broker.read = []string{tc.setup(t, dataDir)}
			}
			_, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true))
			if tc.own {
				if err != nil {
					t.Fatalf("its own file: create = %v, want the launch to start", err)
				}
				if got := launchCount(fr); got != 1 {
					t.Fatalf("its own file: the runner was launched %d times, want 1", got)
				}
				return
			}
			if statusOf(err) != http.StatusUnprocessableEntity {
				t.Fatalf("create = %v (status %d), want 422", err, statusOf(err))
			}
			broker.awaitRelease(t)
			if got := launchCount(fr); got != 0 {
				t.Fatalf("a refused grant reached the runner %d times", got)
			}
		})
	}
}

// The broker builds its run directory from the data directory the engine was given, which the
// engine never resolves, so the same file must be granted however that directory is reached, and
// the rest of the data directory must stay protected.
func TestCredentials_ABrokersRunFileIsGrantedWhereverTheDataDirectoryLives(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	type layout func(t *testing.T) (protect []string, moduleDataDir, brokerDataDir string)
	viaLink := layout(func(t *testing.T) ([]string, string, string) {
		link := filepath.Join(t.TempDir(), "data")
		if err := os.Symlink(t.TempDir(), link); err != nil {
			t.Fatal(err)
		}
		return []string{link}, link, link
	})
	insideProtected := layout(func(t *testing.T) ([]string, string, string) {
		outer := t.TempDir()
		dataDir := filepath.Join(outer, "data")
		if err := os.MkdirAll(dataDir, 0o700); err != nil {
			t.Fatal(err)
		}
		return []string{outer, dataDir}, dataDir, dataDir
	})
	for name, tc := range map[string]struct {
		layout layout
		vault  bool // the broker names a file of the data directory outside this run's directory
		linked bool // this run's directory is a link into the data directory's other files
		folder bool // the broker names a file reached through a link to a folder outside every protected folder
	}{
		"a file reached through a link to a folder outside the protected folders": {folder: true, layout: func(t *testing.T) ([]string, string, string) {
			dataDir := t.TempDir()
			return []string{dataDir}, dataDir, dataDir
		}},
		"a run directory that links into the protected folder through a linked data directory": {layout: viaLink, linked: true},
		"a data directory reached through a link":                                              {layout: viaLink},
		"a data directory reached through a link keeps its other files protected":              {layout: viaLink, vault: true},
		"a data directory inside another protected folder":                                     {layout: insideProtected},
		"a data directory inside another protected folder keeps its other files protected":     {layout: insideProtected, vault: true},
		"a data directory given with surrounding spaces": {layout: func(t *testing.T) ([]string, string, string) {
			dataDir := t.TempDir()
			return []string{dataDir}, " " + dataDir + " ", dataDir
		}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			protect, moduleDataDir, brokerDataDir := tc.layout(t)
			m, tenant, fr, broker := credentialHarnessData(t, protect, moduleDataDir, brokerDataDir)
			if tc.folder {
				link := filepath.Join(t.TempDir(), "keys")
				if err := os.Symlink(filepath.Dir(writeCredentialFile(t, filepath.Join(t.TempDir(), "ssh_config"))), link); err != nil {
					t.Fatal(err)
				}
				broker.read = []string{filepath.Join(link, "ssh_config")}
			} else if tc.linked {
				keys := filepath.Join(brokerDataDir, "keys")
				if err := os.MkdirAll(keys, 0o700); err != nil {
					t.Fatal(err)
				}
				broker.runDirTo, broker.runFile = keys, true
			} else if tc.vault {
				broker.read = []string{writeCredentialFile(t, filepath.Join(brokerDataDir, "vault.db"))}
			} else {
				broker.runFile = true
			}
			_, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true))
			if tc.vault || tc.linked {
				if statusOf(err) != http.StatusUnprocessableEntity {
					t.Fatalf("a file outside this run's directory: create = %v (status %d), want 422", err, statusOf(err))
				}
				broker.awaitRelease(t)
				if got := launchCount(fr); got != 0 {
					t.Fatalf("a refused grant reached the runner %d times", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("its own file: create = %v, want the launch to start", err)
			}
			if got := launchCount(fr); got != 1 {
				t.Fatalf("its own file: the runner was launched %d times, want 1", got)
			}
			assertReadGrantIsResolved(t, fr, "ssh_config")
		})
	}
}

// assertReadGrantIsResolved checks that the child was granted the file that was checked, where it
// really is: a grant that still ran through a link could be pointed elsewhere before the launch.
func assertReadGrantIsResolved(t *testing.T, fr *fakeRunner, name string) {
	t.Helper()
	fr.mu.Lock()
	defer fr.mu.Unlock()
	spec := fr.specs[0]
	if spec.Confinement == nil {
		t.Fatal("the launch was not confined, so no read grant can be checked")
	}
	found := false
	for _, granted := range spec.Confinement.ReadOnly {
		if filepath.Base(granted) != name {
			continue
		}
		found = true
		if real, err := filepath.EvalSymlinks(granted); err != nil || real != granted {
			t.Fatalf("the granted file %q runs through a link (resolves to %q, err %v)", granted, real, err)
		}
	}
	if !found {
		t.Fatalf("no read grant named %q in %v", name, spec.Confinement.ReadOnly)
	}
}

// A success that names helpers but no way to revoke them leaves helpers no one can close.
func TestCredentials_ASuccessWithoutAReleaseIsDeclined(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, broker := credentialHarness(t)
	broker.noRelease = true
	if _, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true)); statusOf(err) != http.StatusUnprocessableEntity {
		t.Fatalf("a success with no release: create = %v (status %d), want 422", err, statusOf(err))
	}
	if got := launchCount(fr); got != 0 {
		t.Fatalf("a success with no release reached the runner %d times", got)
	}
}

// A broker result that breaks the contract is declined, not retried: a queued resume retries an
// outage (503) on every poll, and the same bad result would come back each time. An outage is
// still retried.
func TestCredentials_ABrokerContractBreachIsDeclinedNotRetried(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, breach := range map[string]func(*fakeCredentialBroker){
		"no helper location":        func(b *fakeCredentialBroker) { b.noEnv = true },
		"no release":                func(b *fakeCredentialBroker) { b.noRelease = true },
		"a variable it may not set": func(b *fakeCredentialBroker) { b.env = []EnvVar{{Name: "PATH", Value: "/x"}} },
		"a relative file to read":   func(b *fakeCredentialBroker) { b.read = []string{"relative/ssh_config"} },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			m, tenant, _, broker := credentialHarness(t)
			breach(broker)
			_, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true))
			if statusOf(err) != http.StatusUnprocessableEntity {
				t.Fatalf("create = %v (status %d), want 422", err, statusOf(err))
			}
			if retryableApprovalWait(err) {
				t.Fatalf("a queued resume would retry this breach: %v", err)
			}
			// A success with no release has no release to count.
			if name != "no release" {
				assertReleasedOnce(t, broker)
			}
		})
	}
	t.Run("an outage", func(t *testing.T) {
		t.Parallel()
		m, tenant, _, broker := credentialHarness(t)
		broker.err = errors.New("vault down")
		_, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true))
		if statusOf(err) != http.StatusServiceUnavailable {
			t.Fatalf("an outage: create = %v (status %d), want 503", err, statusOf(err))
		}
		if !retryableApprovalWait(err) {
			t.Fatal("a queued resume would not retry an outage")
		}
	})
}

// A file under a folder the engine's user cannot search is the broker's own fault, like a path
// that does not resolve: declined 422, never an outage a queued resume would retry.
func TestCredentials_AFileTheEngineCannotSearchIsADeclineNotAnOutage(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	own := writeCredentialFile(t, filepath.Join(dir, "ssh_config"))
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if _, err := os.Lstat(own); err == nil {
		t.Skipf("NOT exercised: euid %d can still stat through a 0000 directory", os.Geteuid())
	} else if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("the probe was denied for another cause: %v", err)
	}
	m, tenant, fr, broker := credentialHarness(t)
	logs := captureWarnings(m)
	broker.read = []string{own}
	_, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true))
	if statusOf(err) != http.StatusUnprocessableEntity {
		t.Fatalf("create = %v (status %d), want 422", err, statusOf(err))
	}
	if retryableApprovalWait(err) {
		t.Fatalf("a queued resume would retry this breach: %v", err)
	}
	if out := logs.String(); !strings.Contains(out, "cannot stat") {
		t.Fatalf("the log does not name the stat class of the breach: %s", out)
	}
	assertReleasedOnce(t, broker)
	if got := launchCount(fr); got != 0 {
		t.Fatalf("a refused grant reached the runner %d times", got)
	}
}

// brokerPathErr names every path fault that is the broker's own, whatever user runs the suite:
// the end-to-end denial above needs mode bits to bind, this table does not, and it also pins the
// class each fault is logged as, so the log cannot degrade to another class under any user.
func TestBrokerPathErrNamesTheBrokersOwnFaults(t *testing.T) {
	t.Parallel()
	for name, row := range map[string]struct {
		errno syscall.Errno
		class string
	}{
		"a missing path":                  {syscall.ENOENT, "a file to read that is not a regular file of its own"},
		"a path through a file":           {syscall.ENOTDIR, "a file to read that is not a regular file of its own"},
		"a path through a link loop":      {syscall.ELOOP, "a file to read that is not a regular file of its own"},
		"a path too long":                 {syscall.ENAMETOOLONG, "a file to read that is not a regular file of its own"},
		"a path the engine cannot search": {syscall.EACCES, "a file to read the engine's own user cannot stat"},
		"a path the engine may not touch": {syscall.EPERM, "a file to read the engine's own user cannot stat"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := &fs.PathError{Op: "lstat", Path: "/srv/olivares/run/run-x/ssh_config", Err: row.errno}
			if !brokerPathErr(err) {
				t.Fatalf("%v is not named as the broker's own fault", row.errno)
			}
			if got := credentialStatErr(err).Error(); got != row.class {
				t.Fatalf("%v is classed as %q, want %q", row.errno, got, row.class)
			}
		})
	}
	storage := &fs.PathError{Op: "lstat", Path: "/srv/x", Err: syscall.EIO}
	if brokerPathErr(storage) {
		t.Fatal("a fault of the storage is not the broker's own fault")
	}
	if got := credentialStatErr(storage); got != errReadUnchecked {
		t.Fatalf("a fault of the storage is an outage, not %v", got)
	}
}

// A typed-nil refusal is a contract break with no reason: declined 422, never an outage a
// queued resume would retry, and never a panic in the redaction that reads its reason.
func TestCredentials_ATypedNilRefusalIsADeclineNotAnOutageOrAPanic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, broker := credentialHarness(t)
	logs := captureWarnings(m)
	broker.refuse = (*CredentialRefusal)(nil)
	_, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true))
	if statusOf(err) != http.StatusUnprocessableEntity {
		t.Fatalf("create = %v (status %d), want 422", err, statusOf(err))
	}
	if retryableApprovalWait(err) {
		t.Fatalf("a queued resume would retry this breach: %v", err)
	}
	if out := logs.String(); !strings.Contains(out, "breaks its contract") || !strings.Contains(out, "no reason") {
		t.Fatalf("the log does not name the class of a refusal with no reason: %s", out)
	}
	assertReleasedOnce(t, broker)
	if got := launchCount(fr); got != 0 {
		t.Fatalf("a typed-nil refusal reached the runner %d times", got)
	}
}

// A Business broker whose licence does not cover managed credentials refuses with the add-on
// refusal: the launch answers 403 addon_requires_license, not an outage a queued resume would
// retry, and the child never starts.
func TestCredentials_AnUnlicensedBrokerIsTheAddonRefusal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, broker := credentialHarness(t)
	broker.err = license.AddonRequired("session-credentials", "broker-credentials")
	_, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true))
	var coded *codedRunErr
	if statusOf(err) != http.StatusForbidden || !errors.As(err, &coded) || coded.code != "addon_requires_license" {
		t.Fatalf("create = %v (status %d), want 403 addon_requires_license", err, statusOf(err))
	}
	if retryableApprovalWait(err) {
		t.Fatalf("a queued resume would retry a licence refusal: %v", err)
	}
	if got := launchCount(fr); got != 0 {
		t.Fatalf("an unlicensed broker reached the runner %d times", got)
	}
}

// A refusal that comes back with the helpers it started releases them exactly once.
func TestCredentials_ARefusalThatComesBackWithHelpersReleasesThemOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, broker := credentialHarness(t)
	broker.refuse = &CredentialRefusal{Reason: "no credential named prod-db"}
	if _, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true)); statusOf(err) != http.StatusUnprocessableEntity {
		t.Fatalf("a refusal: create = %v (status %d), want 422", err, statusOf(err))
	}
	assertReleasedOnce(t, broker)
	if got := launchCount(fr); got != 0 {
		t.Fatalf("a refusal reached the runner %d times", got)
	}
}

// Every file a broker grants is checked: a good first element does not admit a bad second one.
func TestCredentials_AGrantedReadListIsDeclinedWhenAnyElementIsBad(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, broker := credentialHarness(t)
	good := writeCredentialFile(t, filepath.Join(t.TempDir(), "ssh_config"))
	broker.read = []string{good, "relative/known_hosts"}
	if _, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true)); statusOf(err) != http.StatusUnprocessableEntity {
		t.Fatalf("a bad second element: create = %v (status %d), want 422", err, statusOf(err))
	}
	broker.awaitRelease(t)
	if got := launchCount(fr); got != 0 {
		t.Fatalf("a read list with a bad element reached the runner %d times", got)
	}
}

// A stored credential list damaged between launches is a declined resume, logged with the class
// of its cause and the run it belongs to. It never relaunches.
func TestCredentials_ADamagedStoredListIsADeclinedResumeLoggedWithItsCause(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, fr, broker := credentialHarness(t)
	logs := captureWarnings(m)
	dto, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	broker.awaitRelease(t)
	damageStoredCredentialNames(t, m, tenant, dto.RunRef, "{not json")
	_, err = m.resumeRunAs(ctx, tenant, dto.RunRef, actorU, actorKindU, "", true)
	if statusOf(err) != http.StatusConflict {
		t.Fatalf("resume = %v (status %d), want 409", err, statusOf(err))
	}
	if retryableApprovalWait(err) {
		t.Fatalf("a queued resume would retry a damaged stored list: %v", err)
	}
	if got := launchCount(fr); got != 1 {
		t.Fatalf("a damaged list relaunched the run: %d launches, want 1", got)
	}
	if out := logs.String(); !strings.Contains(out, "not a JSON list") || !strings.Contains(out, dto.RunRef) {
		t.Fatalf("the log does not name the cause and the run: %s", out)
	}
}

// The list a session shows names none when its stored list is damaged, and the log says why and
// for which run.
func TestCredentials_ADamagedStoredListReadsAsNoneAndIsLogged(t *testing.T) {
	t.Parallel()
	m, _, _, _ := credentialHarness(t)
	logs := captureWarnings(m)
	rec := model.Record{colRunRef: "run-damaged", colRunCredentials: "[]"}
	if got := m.storedCredentialNames(rec); len(got) != 0 {
		t.Fatalf("a damaged list read as %v, want none", got)
	}
	if out := logs.String(); !strings.Contains(out, "an empty list") || !strings.Contains(out, "run-damaged") {
		t.Fatalf("the log does not name the cause and the run: %s", out)
	}
}

// A breach is logged by its class; the value the broker named is never in the log.
func TestCredentials_AContractBreachIsLoggedWithoutTheValueItNamed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, _, broker := credentialHarness(t)
	logs := captureWarnings(m)
	broker.read = []string{"relative/CANARY-breach-7731/ssh_config"}
	if _, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true)); statusOf(err) != http.StatusUnprocessableEntity {
		t.Fatalf("create = %v (status %d), want 422", err, statusOf(err))
	}
	broker.awaitRelease(t)
	out := logs.String()
	if strings.Contains(out, "CANARY-breach-7731") {
		t.Fatalf("the log carries the value the broker named: %s", out)
	}
	if !strings.Contains(out, "breaks its contract") {
		t.Fatalf("the log does not name the class of the breach: %s", out)
	}
}

// A variable the broker may not set is logged by its class; the name the broker gave is never in the log.
func TestCredentials_AVariableItMayNotSetIsLoggedWithoutItsName(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, _, broker := credentialHarness(t)
	logs := captureWarnings(m)
	broker.env = []EnvVar{{Name: "CANARY-name-5520", Value: "/tmp/x"}}
	if _, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true)); statusOf(err) != http.StatusUnprocessableEntity {
		t.Fatalf("create = %v (status %d), want 422", err, statusOf(err))
	}
	assertReleasedOnce(t, broker)
	out := logs.String()
	if strings.Contains(out, "CANARY-name-5520") {
		t.Fatalf("the log carries the variable name the broker named: %s", out)
	}
	if !strings.Contains(out, "breaks its contract") {
		t.Fatalf("the log does not name the class of the breach: %s", out)
	}
}

// The run directory the exception covers is one path segment under the protected folder: a run
// ref that could name another directory (empty, dot, a separator) grants nothing.
func TestInBrokerRunDirIsOnlyThisRunsDirectory(t *testing.T) {
	t.Parallel()
	protected := t.TempDir()
	own := filepath.Join(protected, "run", "r1", "ssh_config")
	for name, tc := range map[string]struct {
		path, runRef string
		want         bool
	}{
		"this run's own file":                {own, "r1", true},
		"a sibling run that shares a prefix": {filepath.Join(protected, "run", "r10", "ssh_config"), "r1", false},
		"another run":                        {own, "r2", false},
		"an empty run ref":                   {own, "", false},
		"a dot run ref":                      {own, ".", false},
		"a parent run ref":                   {own, "..", false},
		"a run ref with a separator":         {own, "r1/../r2", false},
	} {
		if got := inBrokerRunDir(tc.path, protected, tc.runRef); got != tc.want {
			t.Errorf("%s: inBrokerRunDir = %v, want %v", name, got, tc.want)
		}
	}
}

// The run directory is exempt from the protected folders only where it sits in the engine's data
// directory. A run directory under any other protected folder is that folder's own, and stays
// protected: the broker's file there is a breach.
func TestCredentials_ARunDirectoryUnderAnotherProtectedFolderIsRefused(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDir, other := t.TempDir(), t.TempDir()
	fr := &fakeRunner{}
	gate := &spyGate{inner: LaunchDecision{Allowed: true}}
	m, st, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()), WithLaunchGate(gate), WithConfinement([]string{dataDir, other}, false))
	stopModuleAtCleanup(t, m)
	broker := newFakeCredentialBroker(t)
	broker.st = st
	broker.dataDir = other
	broker.runFile = true
	m.CredentialBroker = broker
	_, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true))
	if statusOf(err) != http.StatusUnprocessableEntity {
		t.Fatalf("a run directory under another protected folder: create = %v (status %d), want 422", err, statusOf(err))
	}
	broker.awaitRelease(t)
	if got := launchCount(fr); got != 0 {
		t.Fatalf("a refused grant reached the runner %d times", got)
	}
}

// A breach that names a file inside the protected folder is logged without that path.
func TestCredentials_AProtectedFolderBreachIsLoggedWithoutItsPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDir := t.TempDir()
	m, tenant, _, broker := credentialHarnessConfined(t, dataDir)
	logs := captureWarnings(m)
	broker.read = []string{writeCredentialFile(t, filepath.Join(dataDir, "CANARY-vault-4412.db"))}
	if _, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true)); statusOf(err) != http.StatusUnprocessableEntity {
		t.Fatalf("create = %v (status %d), want 422", err, statusOf(err))
	}
	broker.awaitRelease(t)
	if out := logs.String(); strings.Contains(out, "CANARY-vault-4412") || !strings.Contains(out, "breaks its contract") {
		t.Fatalf("the log does not show a breach without its path: %s", out)
	}
}

// A refusal's reason is the broker's text: the response and the log carry only its first line,
// as they do for every other cause, so a second line the broker names stays out of both.
func TestCredentials_ARefusalReasonIsRedactedInTheResponseAndTheLog(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, _, broker := credentialHarness(t)
	logs := captureWarnings(m)
	broker.refuse = &CredentialRefusal{Reason: "no credential named prod-db\nCANARY-refusal-4418"}
	_, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true))
	if statusOf(err) != http.StatusUnprocessableEntity {
		t.Fatalf("a refusal: create = %v (status %d), want 422", err, statusOf(err))
	}
	broker.awaitRelease(t)
	if strings.Contains(err.Error(), "CANARY-refusal-4418") {
		t.Fatalf("the response carries the broker's second line: %v", err)
	}
	if out := logs.String(); strings.Contains(out, "CANARY-refusal-4418") {
		t.Fatalf("the log carries the broker's second line: %s", out)
	}
}

// A release that panics is contained: the launch still ends with its own refusal, and the log
// names the class of the panic, never the value the broker put in it.
func TestCredentials_AReleaseThatPanicsIsContainedAndLoggedWithoutItsValue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m, tenant, _, broker := credentialHarness(t)
	logs := captureWarnings(m)
	broker.refuse, broker.releasePanics = &CredentialRefusal{Reason: "no credential named prod-db"}, true
	if _, err := createProfiledTestRun(t, m, ctx, tenant, credentialParams(true)); statusOf(err) != http.StatusUnprocessableEntity {
		t.Fatalf("a refusal whose release panics: create = %v (status %d), want 422", err, statusOf(err))
	}
	broker.awaitRelease(t)
	if out := logs.String(); strings.Contains(out, "CANARY-panic-9913") || !strings.Contains(out, "a credential broker's release panicked") {
		t.Fatalf("the log does not show a contained release panic without its value: %s", out)
	}
}
