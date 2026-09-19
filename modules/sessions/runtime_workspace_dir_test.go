// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// TestLaunchWithoutWorkspaceGetsItsOwnDirectory pins the golden-path finding of
// 2026-09-18: a governed session created WITHOUT a workspace ran in the engine's
// own working directory, because launchWorkspaceTarget returned an empty
// LaunchSpec.Dir and the native runner fell back to the process cwd.
//
// The assertion is deliberately NOT "Dir is non-empty": that would pass on any
// string. It is that the child is started in a directory of its OWN, under this
// node's root, named by the run, that the directory EXISTS and is private, that
// the row records it, and that it is not the engine's cwd.
func TestLaunchWithoutWorkspaceGetsItsOwnDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fr := &fakeRunner{initSID: "sess-own-dir"}
	m, _, tenant, _ := newRuntimeHarness(t,
		WithRunner(fr), WithCredentialSource(staticCred()), WithSessionWorkspaceRoot(root))
	ctx := context.Background()

	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun without workspace: %v", err)
	}
	want := filepath.Join(root, dto.RunRef)
	if got := fr.lastSpec().Dir; got != want {
		t.Fatalf("child working directory = %q, want its own %q", got, want)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if fr.lastSpec().Dir == cwd || fr.lastSpec().Dir == "" {
		t.Fatalf("the child inherited the engine's working directory (%q)", cwd)
	}
	info, err := os.Stat(want)
	if err != nil || !info.IsDir() {
		t.Fatalf("the session's own directory was not created: %v", err)
	}
	if perm := info.Mode().Perm(); perm != sessionWorkspaceDirMode {
		t.Fatalf("the session's own directory is %o, want %o", perm, sessionWorkspaceDirMode)
	}
	if dto.WorkspacePath != want {
		t.Fatalf("the record does not name the directory: %q, want %q", dto.WorkspacePath, want)
	}
	// And it is READ BACK from the row, not just returned by the create call.
	stored, err := m.getRun(ctx, tenant, dto.RunRef)
	if err != nil {
		t.Fatalf("getRun: %v", err)
	}
	if stored.WorkspacePath != want {
		t.Fatalf("stored workspace_path = %q, want %q", stored.WorkspacePath, want)
	}
}

// TestLaunchWithARegisteredWorkspaceStillUsesIt proves the fix did not take the
// operator's own directory away: a run that NAMES a workspace keeps using its
// canonical root, and the row records that root rather than a directory this
// plane would have created.
func TestLaunchWithARegisteredWorkspaceStillUsesIt(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fr := &fakeRunner{initSID: "sess-registered"}
	m, _, tenant, _ := newRuntimeHarness(t,
		WithRunner(fr), WithCredentialSource(staticCred()), WithSessionWorkspaceRoot(root))
	ctx := context.Background()

	wsRoot := t.TempDir()
	real, err := filepath.EvalSymlinks(wsRoot)
	if err != nil {
		t.Fatalf("evalsymlinks: %v", err)
	}
	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		WorkspaceRef: registerTestWorkspace(t, m, tenant, wsRoot),
		Actor:        "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun with workspace: %v", err)
	}
	if got := fr.lastSpec().Dir; got != real {
		t.Fatalf("child working directory = %q, want the registered workspace %q", got, real)
	}
	if dto.WorkspacePath != real {
		t.Fatalf("recorded workspace_path = %q, want %q", dto.WorkspacePath, real)
	}
	if _, err := os.Stat(filepath.Join(root, dto.RunRef)); !os.IsNotExist(err) {
		t.Fatalf("a run with a workspace must not get a directory of its own under the root")
	}
}

// TestLaunchWithoutASessionWorkspaceRootIsDenyClosed pins the posture: with
// nowhere to put a private directory, the launch is REFUSED rather than started
// in the engine's own tree. 503, like every other unwired dependency here.
func TestLaunchWithoutASessionWorkspaceRootIsDenyClosed(t *testing.T) {
	t.Parallel()

	fr := &fakeRunner{initSID: "sess-denied"}
	m, _, tenant, _ := newRuntimeHarness(t, WithRunner(fr), WithCredentialSource(staticCred()))
	// Un-wire what the harness wires. The option ignores an empty value on purpose
	// (a composition root must not be able to erase a root by passing ""), so the
	// unwired state is reached directly.
	m.rt.sessionWorkspaceRoot = ""
	ctx := context.Background()

	_, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err == nil {
		t.Fatal("a launch with no workspace and no session root was accepted")
	}
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusServiceUnavailable {
		t.Fatalf("want 503 with the wiring named, got %v", err)
	}
	if !strings.Contains(err.Error(), "engine's own working directory") {
		t.Fatalf("the refusal must name what it is refusing to do, got %q", err.Error())
	}
	if len(fr.specs) != 0 {
		t.Fatalf("a refused launch must not reach the runner (%d specs)", len(fr.specs))
	}
}

// TestReleaseRemovesTheSessionsOwnDirectory measures both halves of the retention
// rule in one place, because they are the same predicate seen from two sides: the
// directory this plane CREATED is purged on release, and the operator's own
// registered workspace is not touched by the same verb.
func TestReleaseRemovesTheSessionsOwnDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fr := &fakeRunner{initSID: "sess-release"}
	m, _, tenant, _ := newRuntimeHarness(t,
		WithRunner(fr), WithCredentialSource(staticCred()), WithSessionWorkspaceRoot(root))
	ctx := context.Background()

	own, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	ownDir := filepath.Join(root, own.RunRef)
	// Something the child wrote: release must take the contents with it, not stop
	// at an empty directory.
	if err := os.WriteFile(filepath.Join(ownDir, "notes.txt"), []byte("work"), 0o600); err != nil {
		t.Fatalf("write in the session directory: %v", err)
	}
	if _, err := m.stopRun(ctx, tenant, own.RunRef, "user:u1", "user"); err != nil {
		t.Fatalf("stopRun: %v", err)
	}
	cleaned, err := m.cleanupRun(ctx, tenant, own.RunRef, "user:u1", "user")
	if err != nil {
		t.Fatalf("cleanupRun: %v", err)
	}
	if cleaned.State != stateCleaned {
		t.Fatalf("state after release = %q", cleaned.State)
	}
	if _, err := os.Stat(ownDir); !os.IsNotExist(err) {
		t.Fatalf("the session's own directory survived its release: %v", err)
	}

	wsRoot := t.TempDir()
	keep := filepath.Join(wsRoot, "keep.txt")
	if err := os.WriteFile(keep, []byte("operator's own file"), 0o600); err != nil {
		t.Fatalf("write in the registered workspace: %v", err)
	}
	reg, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		WorkspaceRef: registerTestWorkspace(t, m, tenant, wsRoot),
		Actor:        "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun with workspace: %v", err)
	}
	if _, err := m.stopRun(ctx, tenant, reg.RunRef, "user:u1", "user"); err != nil {
		t.Fatalf("stopRun: %v", err)
	}
	if _, err := m.cleanupRun(ctx, tenant, reg.RunRef, "user:u1", "user"); err != nil {
		t.Fatalf("cleanupRun: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("releasing a session DELETED the operator's registered workspace: %v", err)
	}
}

// TestRunWorkspaceDirPathRefusesAReferenceThatEscapesTheRoot is the guard that
// keeps a future producer of run references from placing a session's directory
// outside the root — or from naming one the release purge would then refuse to
// remove. It is a unit test on the predicate because the ids this module mints
// today cannot exercise it.
func TestRunWorkspaceDirPathRefusesAReferenceThatEscapesTheRoot(t *testing.T) {
	t.Parallel()

	m := New(WithSessionWorkspaceRoot("/var/lib/olivares/sessions"))
	// The escape cases, and — since the purge now requires a directory's NAME to be
	// one this module mints — the shape cases too. Creation and removal have to
	// agree on that shape or a name accepted here would produce a directory the
	// release could never remove.
	for _, ref := range []string{
		"", ".", "..", "../elsewhere", "a/b", `a\b`, "/abs",
		"run-1", "etc", "acme-project", "01a0b490-311d-7000-8000",
	} {
		if _, err := m.runWorkspaceDirPath(ref); err == nil {
			t.Fatalf("run reference %q was accepted as a directory name", ref)
		}
	}
	dir, err := m.runWorkspaceDirPath("01a0b490-311d-7000-8000-000000000001")
	if err != nil {
		t.Fatalf("an ordinary reference was refused: %v", err)
	}
	if dir != "/var/lib/olivares/sessions/01a0b490-311d-7000-8000-000000000001" {
		t.Fatalf("unexpected directory %q", dir)
	}
}

// TestRemoveRunWorkspaceDirRefusesAnythingOutsideTheRoot pins the retention
// guard directly, since the column it reads also carries operator-owned paths.
func TestRemoveRunWorkspaceDirRefusesAnythingOutsideTheRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := t.TempDir()
	m := New(WithSessionWorkspaceRoot(root))

	if m.removeRunWorkspaceDir(outside) != purgeKept {
		t.Fatal("a directory outside the root was removed")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("the outside directory is gone: %v", err)
	}
	if m.removeRunWorkspaceDir(root) != purgeKept {
		t.Fatal("the root itself was removed")
	}
	// A real run reference, because the name is part of the predicate now: the
	// directory must be one this module would have minted, not merely a child.
	ref := string(model.NewID())
	nested := filepath.Join(root, ref, "deeper")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if m.removeRunWorkspaceDir(nested) != purgeKept {
		t.Fatal("a grandchild of the root was removed (only a direct child is a session directory)")
	}
	if m.removeRunWorkspaceDir(filepath.Join(root, ref)) != purgeRemoved {
		t.Fatal("a direct child of the root was not removed")
	}
}

// TestReleaseNeverRemovesARegisteredWorkspaceUnderTheRoot is the case the
// positional predicate cannot answer: an operator registers a workspace that
// HAPPENS to live directly under this node's session-workspace root.
//
// `workspace_path` then holds the operator's own canonical root, its parent IS
// the root, and every string test the purge can make on the path alone says
// "this looks exactly like a directory I created". It is not one, and the
// difference is not visible in the path — only in whether this module created it.
func TestReleaseNeverRemovesARegisteredWorkspaceUnderTheRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fr := &fakeRunner{initSID: "sess-registered-under-root"}
	m, st, tenant, _ := newRuntimeHarness(t,
		WithRunner(fr), WithCredentialSource(staticCred()), WithSessionWorkspaceRoot(root))
	ctx := context.Background()

	// The operator's own project, registered from directly under the root.
	project := filepath.Join(root, "acme-project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatalf("mkdir the operator's project: %v", err)
	}
	source := filepath.Join(project, "SOURCE.txt")
	if err := os.WriteFile(source, []byte("the operator's own work"), 0o600); err != nil {
		t.Fatalf("write the operator's file: %v", err)
	}

	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		WorkspaceRef: registerTestWorkspace(t, m, tenant, project),
		Actor:        "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun with a workspace under the root: %v", err)
	}
	if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
		t.Fatalf("stopRun: %v", err)
	}
	if _, err := m.cleanupRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
		t.Fatalf("cleanupRun: %v", err)
	}

	if _, err := os.Stat(project); err != nil {
		t.Fatalf("releasing a session REMOVED the operator's registered workspace %q: %v", project, err)
	}
	if b, err := os.ReadFile(source); err != nil || string(b) != "the operator's own work" {
		t.Fatalf("the operator's own file did not survive the release: %v", err)
	}
	// And the release SAYS so: a session that worked in a directory this plane did
	// not create has nothing of its own to purge, so the ledger must not claim one.
	events := listRunEvents(t, st, tenant, dto.RunRef)
	last := events[len(events)-1]
	if last.Event != "cleaned" {
		t.Fatalf("last ledger event = %q, want cleaned", last.Event)
	}
	if strings.Contains(last.Detail, "own workspace directory removed") {
		t.Fatalf("the release claimed it removed a directory of its own: %q", last.Detail)
	}
}

// TestASessionWorkspaceRootOfTheFilesystemRootIsRefusedAtWiring pins the
// configuration-time half of the retention guard.
//
// With the root set to "/", every direct child of "/" satisfies the positional
// predicate, so `/etc` reads as a session directory. Measured before the guard:
// `removeRunWorkspaceDir("/nonexistent-probe")` answered true. The refusal
// happens where the value is WIRED, because a root nobody can put a session
// directory under is a wiring mistake and not a per-request failure.
func TestASessionWorkspaceRootOfTheFilesystemRootIsRefusedAtWiring(t *testing.T) {
	t.Parallel()

	m := New(WithSessionWorkspaceRoot("/"))
	if m.sessionWorkspaceRootConfigured() {
		t.Fatal(`"/" was accepted as this node's session workspace root`)
	}
	_, err := m.runWorkspaceDirPath("01a0b490-311d-7000-8000-000000000001")
	if err == nil {
		t.Fatal(`a launch under a "/" root was accepted`)
	}
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusServiceUnavailable {
		t.Fatalf("want 503 naming the wiring, got %v", err)
	}
	// The sentence has to tell an operator what to do, not just that something is
	// wrong: this is a value in their configuration.
	for _, want := range []string{"filesystem root", "data directory"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not name the remedy (%q missing): %q", want, err.Error())
		}
	}
	// And the purge cannot be reached through it either.
	if m.removeRunWorkspaceDir("/nonexistent-session-workspace-root-probe") != purgeKept {
		t.Fatal(`a direct child of "/" was accepted for removal`)
	}
}

// TestASessionWorkspaceRootNotOwnedByTheEngineUserIsRefused uses a real
// directory this process did not create and does not own. /etc satisfies every
// other condition — absolute, existing, a directory, not a symlink, not the
// filesystem root — so it isolates the ownership rule on its own.
func TestASessionWorkspaceRootNotOwnedByTheEngineUserIsRefused(t *testing.T) {
	t.Parallel()

	if os.Getuid() == 0 {
		t.Skip("running as uid 0, which owns /etc: this control cannot distinguish the rule it measures")
	}
	m := New(WithSessionWorkspaceRoot("/etc"))
	if m.sessionWorkspaceRootConfigured() {
		t.Fatal("a root owned by another user was accepted as this node's session workspace root")
	}
	_, err := m.runWorkspaceDirPath("01a0b490-311d-7000-8000-000000000001")
	if err == nil || !strings.Contains(err.Error(), "not owned by the user this engine runs as") {
		t.Fatalf("want a refusal naming the owner, got %v", err)
	}
}

// TestASymlinkedSessionWorkspaceRootIsRefused: the purge's positional predicate
// compares STRINGS (filepath.Dir(dir) == root). A root whose last component is a
// symlink makes those strings describe a tree the join does not land in, so the
// comparison stops meaning what it is read to mean.
func TestASymlinkedSessionWorkspaceRootIsRefused(t *testing.T) {
	t.Parallel()

	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	m := New(WithSessionWorkspaceRoot(link))
	if m.sessionWorkspaceRootConfigured() {
		t.Fatal("a symlinked session workspace root was accepted")
	}
	_, err := m.runWorkspaceDirPath("01a0b490-311d-7000-8000-000000000001")
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("want a refusal naming the link, got %v", err)
	}
	// The real directory is a perfectly good root: it is the LINK that is refused,
	// so the remedy the sentence asks for actually works.
	if !New(WithSessionWorkspaceRoot(real)).sessionWorkspaceRootConfigured() {
		t.Fatal("the link's target was refused too, so the refusal names no remedy")
	}
}

// TestUseSessionWorkspaceRootReportsARefusalToItsCallers: the late-binding
// setter is the composition root's other door onto the same value, so it answers
// with the same rule — and it can say so directly, because it is a method and
// not an Option.
func TestUseSessionWorkspaceRootReportsARefusalToItsCallers(t *testing.T) {
	t.Parallel()

	m := New()
	if err := m.UseSessionWorkspaceRoot("/"); err == nil {
		t.Fatal(`UseSessionWorkspaceRoot("/") reported success`)
	}
	if err := m.UseSessionWorkspaceRoot("relative/path"); err == nil {
		t.Fatal("a relative root reported success")
	}
	good := t.TempDir()
	if err := m.UseSessionWorkspaceRoot(good); err != nil {
		t.Fatalf("a usable root was refused: %v", err)
	}
	if !m.sessionWorkspaceRootConfigured() {
		t.Fatal("a usable root did not configure the node")
	}
}

// TestThePurgeRefusesAChildThatIsNotShapedLikeARunReference is the purge-time
// half: "its parent is my root" is not enough, because an operator's own
// directory can satisfy it. A session directory is named by a run REFERENCE,
// which this module mints, so the name is checked against that shape.
func TestThePurgeRefusesAChildThatIsNotShapedLikeARunReference(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	m := New(WithSessionWorkspaceRoot(root))

	// Shaped like the operator's own tree, sitting directly under the root.
	for _, name := range []string{"etc", "acme-project", "run-1", "session-workspaces"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %q: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SOURCE.txt"), []byte("not ours"), 0o600); err != nil {
			t.Fatalf("write in %q: %v", name, err)
		}
		if m.removeRunWorkspaceDir(dir) != purgeKept {
			t.Fatalf("a directory named %q was accepted as a session's own", name)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("directory %q was removed anyway: %v", name, err)
		}
	}
	// And a name this module actually mints still is one.
	own := filepath.Join(root, string(model.NewID()))
	if err := os.MkdirAll(own, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if m.removeRunWorkspaceDir(own) != purgeRemoved {
		t.Fatal("a directory named by a real run reference was refused")
	}
	if _, err := os.Stat(own); !os.IsNotExist(err) {
		t.Fatalf("the session's own directory survived: %v", err)
	}
}

// TestThePurgeRefusesADirectoryTheRowDoesNotProveIsOurs isolates the OWNERSHIP
// condition, which is the headline rule: a release removes only a directory this
// module created for that run.
//
// ⛔ IT HAS TO ISOLATE IT, AND THAT IS THE POINT OF THE FIXTURE. The lifecycle
// test that was written for this rule uses a registered workspace named
// `acme-project`, so the purge-time NAME condition refuses it on its own and the
// test stays green with the ownership condition deleted — it measures the name
// rule now, not this one. Here the directory's name is one this module MINTS and
// its parent IS the root, so every path predicate available to the purge says
// "mine". The only thing that can keep it is the row.
func TestThePurgeRefusesADirectoryTheRowDoesNotProveIsOurs(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	m, _, tenant, _ := newRuntimeHarness(t, WithSessionWorkspaceRoot(root))
	ctx := context.Background()

	// Three rows over three identical directories: the fact absent (a row from
	// before the column), the fact explicitly false, and the fact present.
	plant := func(name string) string {
		t.Helper()
		dir := filepath.Join(root, string(model.NewID()))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "SOURCE.txt"), []byte(name), 0o600); err != nil {
			t.Fatalf("write in %s: %v", name, err)
		}
		return dir
	}
	absent, explicit := plant("absent"), plant("false")
	for _, tc := range []struct {
		what string
		rec  model.Record
		dir  string
	}{
		{"a row from before the ownership column", model.Record{colRunWorkspacePath: absent}, absent},
		{"a row that says the directory is not ours", model.Record{
			colRunWorkspacePath: explicit, colRunWorkspaceDirOwned: false}, explicit},
	} {
		if res := m.removeOwnRunWorkspaceDir(ctx, tenant, tc.rec); res.outcome == purgeRemoved {
			t.Fatalf("the purge removed a directory on %s", tc.what)
		}
		if _, err := os.Stat(filepath.Join(tc.dir, "SOURCE.txt")); err != nil {
			t.Fatalf("the bytes under %s did not survive: %v", tc.what, err)
		}
	}

	// And the CONTROL, so this test cannot pass by refusing everything: the same
	// shape with the ownership fact PRESENT is removed.
	own := plant("owned")
	if res := m.removeOwnRunWorkspaceDir(ctx, tenant, model.Record{
		colRunWorkspacePath: own, colRunWorkspaceDirOwned: true}); res.outcome != purgeRemoved {
		t.Fatal("a directory this module proved it created was not removed")
	}
	if _, err := os.Stat(own); !os.IsNotExist(err) {
		t.Fatalf("the session's own directory survived: %v", err)
	}
}

// TestReleaseKeepsADirectoryTheOperatorRegisteredAsAWorkspace is the case the
// ownership column alone answers WRONG. This module created `<root>/<run_ref>`
// for a run that named no workspace, so the row proves it is ours — and then the
// operator registered THAT path as a workspace and kept their work in it. The
// console shows it as the run's working directory, and nothing in the row says
// somebody else now depends on the bytes.
//
// A release must not remove it: the rule is "a directory this module created for
// this run AND that nobody has registered since". The second half is a lookup in
// the registry, canonicalised the way workspace.go canonicalises a root.
func TestReleaseKeepsADirectoryTheOperatorRegisteredAsAWorkspace(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fr := &fakeRunner{initSID: "sess-registered-later"}
	m, st, tenant, _ := newRuntimeHarness(t,
		WithRunner(fr), WithCredentialSource(staticCred()), WithSessionWorkspaceRoot(root))
	ctx := context.Background()

	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun without workspace: %v", err)
	}
	ownDir := filepath.Join(root, dto.RunRef)
	source := filepath.Join(ownDir, "SOURCE.txt")
	if err := os.WriteFile(source, []byte("the operator's own work"), 0o600); err != nil {
		t.Fatalf("write in the session directory: %v", err)
	}
	// The operator registers the very directory this module created.
	ref := registerTestWorkspace(t, m, tenant, ownDir)

	if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
		t.Fatalf("stopRun: %v", err)
	}
	if _, err := m.cleanupRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
		t.Fatalf("cleanupRun: %v", err)
	}

	if b, err := os.ReadFile(source); err != nil || string(b) != "the operator's own work" {
		t.Fatalf("releasing the session removed a REGISTERED workspace %q: %v", ownDir, err)
	}
	// And the ledger says WHY the bytes are still there, by name: an operator who
	// wonders where the directory went can find the registration that holds it.
	events := listRunEvents(t, st, tenant, dto.RunRef)
	last := events[len(events)-1]
	if last.Event != "cleaned" {
		t.Fatalf("last ledger event = %q, want cleaned", last.Event)
	}
	if strings.Contains(last.Detail, "own workspace directory removed") {
		t.Fatalf("the release claimed it removed a directory of its own: %q", last.Detail)
	}
	if !strings.Contains(last.Detail, ref) {
		t.Fatalf("the ledger detail does not name the workspace that holds the directory (%q): %q",
			ref, last.Detail)
	}
}

// TestASessionWorkspaceRootUnderASymlinkedParentIsCanonicalised covers the half
// of the symlink condition the LAST component leaves out. `os.Lstat(root)`
// resolves every intermediate component, so `<base>/link-parent/sub` passes it —
// and then the purge compares path STRINGS against a root that describes a tree
// the comparison does not land in. Re-pointing the parent link after wiring moves
// the tree the purge computes into.
//
// A registered workspace has canonicalised its root since
// (filepath.EvalSymlinks, workspace.go); the session root now does the same, and
// the purge compares CANONICAL paths on both sides — so the same directory
// written in either spelling is recognised as the same directory.
func TestASessionWorkspaceRootUnderASymlinkedParentIsCanonicalised(t *testing.T) {
	t.Parallel()

	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("evalsymlinks the base: %v", err)
	}
	real := filepath.Join(base, "real")
	sub := filepath.Join(real, "sub")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(base, "link-parent")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	m := New(WithSessionWorkspaceRoot(filepath.Join(link, "sub")))
	if !m.sessionWorkspaceRootConfigured() {
		t.Fatal("a root under a symlinked parent was refused; only the LAST component is a wiring mistake")
	}
	ref := string(model.NewID())
	dir, err := m.runWorkspaceDirPath(ref)
	if err != nil {
		t.Fatalf("runWorkspaceDirPath: %v", err)
	}
	if want := filepath.Join(sub, ref); dir != want {
		t.Fatalf("a session's directory is placed through the link: %q, want the canonical %q", dir, want)
	}

	// And at purge time the two spellings are the SAME directory: a row written
	// through the link is removed, and the bytes that go are the canonical ones.
	linkDir := filepath.Join(link, "sub", ref)
	if err := os.MkdirAll(linkDir, 0o700); err != nil {
		t.Fatalf("mkdir through the link: %v", err)
	}
	if err := os.WriteFile(filepath.Join(linkDir, "notes.txt"), []byte("work"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if m.removeRunWorkspaceDir(linkDir) != purgeRemoved {
		t.Fatalf("a directory named through the root's own symlinked parent was refused: %q", linkDir)
	}
	if _, err := os.Stat(filepath.Join(sub, ref)); !os.IsNotExist(err) {
		t.Fatalf("the canonical directory survived: %v", err)
	}

	// The last component is still a wiring mistake, not a spelling: it is refused
	// with the sentence that names its remedy.
	if New(WithSessionWorkspaceRoot(link)).sessionWorkspaceRootConfigured() {
		t.Fatal("a root whose LAST component is a link was accepted")
	}
}

// releaseDetail releases a stopped run and returns the ledger detail the release
// recorded, which is the only durable statement about what happened to the
// directory that run worked in.
func releaseDetail(t *testing.T, m *Module, st store.Store, tenant model.TenantID, runRef string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := m.stopRun(ctx, tenant, runRef, "user:u1", "user"); err != nil {
		t.Fatalf("stopRun %s: %v", runRef, err)
	}
	if _, err := m.cleanupRun(ctx, tenant, runRef, "user:u1", "user"); err != nil {
		t.Fatalf("cleanupRun %s: %v", runRef, err)
	}
	events := listRunEvents(t, st, tenant, runRef)
	last := events[len(events)-1]
	if last.Event != "cleaned" {
		t.Fatalf("last ledger event = %q, want cleaned", last.Event)
	}
	return last.Detail
}

// TestTheReleaseDoesNotClaimItRemovedWhatWasNotThere: the purge answered "true"
// for a path that was already gone — `os.RemoveAll` returns nil on a missing
// path, and a root removed after wiring is accepted by the validation's
// fs.ErrNotExist branch — so the ledger recorded "its own workspace directory
// removed" for a release that removed nothing. Three different events cannot
// share one sentence in a ledger that is supposed to be evidence.
func TestTheReleaseDoesNotClaimItRemovedWhatWasNotThere(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fr := &fakeRunner{initSID: "sess-nothing-there"}
	m, st, tenant, _ := newRuntimeHarness(t,
		WithRunner(fr), WithCredentialSource(staticCred()), WithSessionWorkspaceRoot(root))
	ctx := context.Background()

	launch := func(what string) string {
		t.Helper()
		dto, err := m.createRun(ctx, tenant, CreateRunParams{
			Transport: TransportStreamJSON, Isolation: IsolationNative,
			Actor: "user:u1", ActorKind: "user",
		})
		if err != nil {
			t.Fatalf("createRun %s: %v", what, err)
		}
		return dto.RunRef
	}
	gone, rootless := launch("gone"), launch("rootless")

	// (a) The directory itself is gone — an operator's own cleanup, a restore.
	if err := os.RemoveAll(filepath.Join(root, gone)); err != nil {
		t.Fatalf("remove the session directory: %v", err)
	}
	detail := releaseDetail(t, m, st, tenant, gone)
	if strings.Contains(detail, "its own workspace directory removed") {
		t.Fatalf("the release claimed it removed a directory that was not there: %q", detail)
	}
	if !strings.Contains(detail, "no directory of its own was there to remove") {
		t.Fatalf("the release does not say what happened: %q", detail)
	}

	// (b) The ROOT is gone, so nothing under it can be proven to be this node's.
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove the root: %v", err)
	}
	detail = releaseDetail(t, m, st, tenant, rootless)
	if strings.Contains(detail, "its own workspace directory removed") {
		t.Fatalf("the release claimed a removal under a root that is not there: %q", detail)
	}
	if !strings.Contains(detail, "could not prove") {
		t.Fatalf("the release does not say why nothing was removed: %q", detail)
	}
}

// TestTheReleaseNeverFollowsALinkPlantedWhereItsDirectoryWas is the
// time-of-check/time-of-use case. Swapping the session's directory for a link
// into an operator's tree does not remove the target — os.Remove unlinks the
// link — but the call used to answer "removed", so the ledger recorded a
// directory purge for an event in which the bytes are all still there and
// somebody else's tree was pointed at.
func TestTheReleaseNeverFollowsALinkPlantedWhereItsDirectoryWas(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fr := &fakeRunner{initSID: "sess-planted-link"}
	m, st, tenant, _ := newRuntimeHarness(t,
		WithRunner(fr), WithCredentialSource(staticCred()), WithSessionWorkspaceRoot(root))
	ctx := context.Background()

	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun: %v", err)
	}
	ownDir := filepath.Join(root, dto.RunRef)

	victim := t.TempDir()
	keep := filepath.Join(victim, "SOURCE.txt")
	if err := os.WriteFile(keep, []byte("somebody else's work"), 0o600); err != nil {
		t.Fatalf("write the victim's file: %v", err)
	}
	if err := os.RemoveAll(ownDir); err != nil {
		t.Fatalf("remove the session directory: %v", err)
	}
	if err := os.Symlink(victim, ownDir); err != nil {
		t.Fatalf("plant the link: %v", err)
	}

	detail := releaseDetail(t, m, st, tenant, dto.RunRef)

	if b, err := os.ReadFile(keep); err != nil || string(b) != "somebody else's work" {
		t.Fatalf("the release followed the planted link into %q: %v", victim, err)
	}
	if _, err := os.Lstat(ownDir); !os.IsNotExist(err) {
		t.Fatalf("the planted link is still under the root: %v", err)
	}
	if strings.Contains(detail, "its own workspace directory removed") {
		t.Fatalf("the release claimed it removed a directory when it unlinked a link: %q", detail)
	}
	if !strings.Contains(detail, "no directory of its own was there to remove") {
		t.Fatalf("the release does not say what happened: %q", detail)
	}
}

// TestTheFilesystemRootIsRefusedByItsOwnCondition measures the "/" refusal in the
// configuration where it is the ONLY thing standing between a release and every
// top-level directory of the host.
//
// ⛔ WHY IT NEEDS THE INJECTION. The shipped control for "/" passes with the
// filesystem-root condition deleted: "/" is owned by root, this process is not
// root, and the OWNERSHIP condition refuses it first — the RED line of that
// control says so in its own words. On an engine running as uid 0 the ownership
// condition admits every root and `:116` is the whole guard, and that was the
// configuration nothing measured. Here the ownership condition is satisfied on
// purpose, so the refusal that answers is the one under test.
func TestTheFilesystemRootIsRefusedByItsOwnCondition(t *testing.T) {
	t.Parallel()

	m := New()
	m.rt.dirOwner = func(os.FileInfo) (owned, known bool) { return true, true }

	err := m.UseSessionWorkspaceRoot("/")
	if err == nil {
		t.Fatal(`"/" was accepted as a session workspace root by an engine that owns it`)
	}
	var re *runErr
	if !errors.As(err, &re) || re.status != http.StatusServiceUnavailable {
		t.Fatalf("want 503 naming the wiring, got %v", err)
	}
	for _, want := range []string{"filesystem root", "data directory"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not name the remedy (%q missing): %q", want, err.Error())
		}
	}
	if m.sessionWorkspaceRootConfigured() {
		t.Fatal(`the node took "/" as its session workspace root`)
	}

	// And the purge is re-validated at the moment it would act, so a root that
	// reached the runtime by another door is refused there too — with the
	// ownership condition satisfied, this is the filesystem-root condition alone.
	m.rt.sessionWorkspaceRoot = "/"
	if got := m.removeRunWorkspaceDir("/" + string(model.NewID())); got != purgeKept {
		t.Fatalf(`a direct child of "/" was not kept: outcome %d`, got)
	}
}

// TestReleaseKeepsAWorkspaceRegisteredInsideTheSessionsOwnDirectory is the half
// of the retention rule that asking about the DIRECTORY ITSELF leaves out.
//
// A run that names no workspace is given `<root>/<run_ref>`, and an operator who
// works there does the ordinary thing: they make a project folder inside it and
// register THAT as a workspace — the console then shows the project by name and
// nothing says the bytes are on loan. The release removes the session's
// directory with `os.RemoveAll`, and the registered project goes with it as a
// child. The row is true, the registry was asked, and the answer was about the
// wrong path: the question has to be "is anything registered AT OR UNDER this
// directory", because that is what the removal reaches.
//
// The assertion is the operator's file and the ledger sentence, not an internal
// predicate: the bytes survive, and the release names the registration that kept
// them so an operator can find out why the directory is still there.
func TestReleaseKeepsAWorkspaceRegisteredInsideTheSessionsOwnDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fr := &fakeRunner{initSID: "sess-registered-inside"}
	m, st, tenant, _ := newRuntimeHarness(t,
		WithRunner(fr), WithCredentialSource(staticCred()), WithSessionWorkspaceRoot(root))
	ctx := context.Background()

	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun without workspace: %v", err)
	}
	ownDir := filepath.Join(root, dto.RunRef)
	project := filepath.Join(ownDir, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatalf("make the operator's project directory: %v", err)
	}
	source := filepath.Join(project, "SOURCE.txt")
	if err := os.WriteFile(source, []byte("the operator's own work"), 0o600); err != nil {
		t.Fatalf("write in the operator's project: %v", err)
	}
	// The operator registers the project INSIDE the directory this module created.
	ref := registerTestWorkspace(t, m, tenant, project)

	if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
		t.Fatalf("stopRun: %v", err)
	}
	if _, err := m.cleanupRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
		t.Fatalf("cleanupRun: %v", err)
	}

	if b, err := os.ReadFile(source); err != nil || string(b) != "the operator's own work" {
		t.Fatalf("releasing the session removed a workspace REGISTERED at %q, inside its own directory: %v",
			project, err)
	}
	events := listRunEvents(t, st, tenant, dto.RunRef)
	last := events[len(events)-1]
	if last.Event != "cleaned" {
		t.Fatalf("last ledger event = %q, want cleaned", last.Event)
	}
	if strings.Contains(last.Detail, "own workspace directory removed") {
		t.Fatalf("the release claimed it removed a directory of its own: %q", last.Detail)
	}
	if !strings.Contains(last.Detail, ref) {
		t.Fatalf("the ledger detail does not name the workspace that holds the directory (%q): %q",
			ref, last.Detail)
	}
}

// TestReleaseRemovesItsOwnDirectoryDespiteASiblingWhoseNameExtendsIt is the
// control on HOW "under this directory" is computed: by path COMPONENT, never by
// string prefix.
//
// `<root>/<run_ref>-backup` is a directory an operator can make in one keystroke
// of tab-completion, and a registration there is a SIBLING of the session's
// directory — nothing in it is reachable by removing `<root>/<run_ref>`. A bare
// `strings.HasPrefix(registered, dir)` says otherwise, and the cost is not a
// harmless extra refusal: the release would keep a directory it owns for ever
// and name a workspace that has nothing to do with it, so every future release
// on this node would leak in exactly the same way.
//
// It passes before the containment rule exists and dies the moment the prefix is
// compared without its separator, which is the point of writing it here.
func TestReleaseRemovesItsOwnDirectoryDespiteASiblingWhoseNameExtendsIt(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fr := &fakeRunner{initSID: "sess-sibling-prefix"}
	m, st, tenant, _ := newRuntimeHarness(t,
		WithRunner(fr), WithCredentialSource(staticCred()), WithSessionWorkspaceRoot(root))
	ctx := context.Background()

	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: "user",
	})
	if err != nil {
		t.Fatalf("createRun without workspace: %v", err)
	}
	ownDir := filepath.Join(root, dto.RunRef)
	sibling := ownDir + "-backup"
	if err := os.MkdirAll(sibling, 0o700); err != nil {
		t.Fatalf("make the sibling directory: %v", err)
	}
	kept := filepath.Join(sibling, "KEEP.txt")
	if err := os.WriteFile(kept, []byte("not in the session's directory"), 0o600); err != nil {
		t.Fatalf("write in the sibling: %v", err)
	}
	ref := registerTestWorkspace(t, m, tenant, sibling)

	if _, err := m.stopRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
		t.Fatalf("stopRun: %v", err)
	}
	if _, err := m.cleanupRun(ctx, tenant, dto.RunRef, "user:u1", "user"); err != nil {
		t.Fatalf("cleanupRun: %v", err)
	}

	if _, err := os.Stat(ownDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the session's own directory %q survived a release because a SIBLING shares its "+
			"name as a prefix: stat err = %v", ownDir, err)
	}
	if b, err := os.ReadFile(kept); err != nil || string(b) != "not in the session's directory" {
		t.Fatalf("the sibling workspace %q was touched by the release: %v", sibling, err)
	}
	events := listRunEvents(t, st, tenant, dto.RunRef)
	last := events[len(events)-1]
	if !strings.Contains(last.Detail, "own workspace directory removed") {
		t.Fatalf("the release did not report removing its own directory: %q", last.Detail)
	}
	if strings.Contains(last.Detail, ref) {
		t.Fatalf("the ledger named a workspace that is not under the session's directory (%q): %q",
			ref, last.Detail)
	}
}

// TestReleaseKeepsTheBytesWhenTheRegistryIsLargerThanARelease measures what the
// containment rule does at the edge of what it can read.
//
// "No workspace is registered under this directory" is a statement about the
// WHOLE registry, and this store offers no predicate that proves it: an ordered
// range over a path column is a prefix only under a byte-ordering collation, and
// LIKE reads `_` and `%` — both legal in a directory name — as wildcards. So the
// rows are walked, and a walk has an end this node may not reach. Past the bound
// the answer is a refusal that says so, never "nothing was found": a directory
// removed because the registration sat on the next page is gone the same way as
// one removed because nobody asked at all.
//
// The second half of the test is the control that keeps the first honest: the
// SAME two registrations, with the bound back at its default, and the release
// removes its directory. What refused was the bound, not the registrations.
func TestReleaseKeepsTheBytesWhenTheRegistryIsLargerThanARelease(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	fr := &fakeRunner{initSID: "sess-registry-bound"}
	m, st, tenant, _ := newRuntimeHarness(t,
		WithRunner(fr), WithCredentialSource(staticCred()), WithSessionWorkspaceRoot(root))
	ctx := context.Background()

	newRun := func(what string) (string, string) {
		t.Helper()
		dto, err := m.createRun(ctx, tenant, CreateRunParams{
			Transport: TransportStreamJSON, Isolation: IsolationNative,
			Actor: "user:u1", ActorKind: "user",
		})
		if err != nil {
			t.Fatalf("createRun (%s): %v", what, err)
		}
		dir := filepath.Join(root, dto.RunRef)
		if err := os.WriteFile(filepath.Join(dir, "WORK.txt"), []byte(what), 0o600); err != nil {
			t.Fatalf("write in the session directory (%s): %v", what, err)
		}
		return dto.RunRef, dir
	}
	release := func(ref, what string) runEventDTO {
		t.Helper()
		if _, err := m.stopRun(ctx, tenant, ref, "user:u1", "user"); err != nil {
			t.Fatalf("stopRun (%s): %v", what, err)
		}
		if _, err := m.cleanupRun(ctx, tenant, ref, "user:u1", "user"); err != nil {
			t.Fatalf("cleanupRun (%s): %v", what, err)
		}
		events := listRunEvents(t, st, tenant, ref)
		return events[len(events)-1]
	}

	refA, dirA := newRun("bounded")
	refB, dirB := newRun("unbounded")
	// Two registrations, neither of them anywhere near either directory, so what
	// decides is only how much of the registry the release managed to read.
	registerTestWorkspace(t, m, tenant, t.TempDir())
	registerTestWorkspace(t, m, tenant, t.TempDir())

	m.rt.workspaceRegistryScanRows = 1
	last := release(refA, "bounded")
	if _, err := os.Stat(filepath.Join(dirA, "WORK.txt")); err != nil {
		t.Fatalf("a release removed %q on an UNPROVEN absence: it read 1 of 2 registrations: %v", dirA, err)
	}
	if !strings.Contains(last.Detail, "more registered workspaces than a release reads") {
		t.Fatalf("the ledger does not say the registry was too large to rule anything out: %q", last.Detail)
	}

	m.rt.workspaceRegistryScanRows = 0
	last = release(refB, "unbounded")
	if _, err := os.Stat(dirB); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("with the whole registry read and nothing registered under %q, the release still kept it: "+
			"stat err = %v, ledger %q", dirB, err, last.Detail)
	}
	if !strings.Contains(last.Detail, "own workspace directory removed") {
		t.Fatalf("the release did not report removing its own directory: %q", last.Detail)
	}
}
