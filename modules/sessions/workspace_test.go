// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// fakeClassifier flags any content containing trigger as a credential hit, so DLP
// posture (label vs deny) is exercised deterministically without importing the real
// catalog (no inter-module dependency).
type fakeClassifier struct{ trigger string }

func (f fakeClassifier) Classify(text string) ([]SensitivityHit, error) {
	if f.trigger != "" && strings.Contains(text, f.trigger) {
		return []SensitivityHit{{Class: "secret.credential", Rule: "test-rule", Count: 1, Severity: "high"}}, nil
	}
	return nil, nil
}

const actorU, actorKindU = "user:u1", "user"

func mkWorkspace(t *testing.T, m *Module, tenant model.TenantID, p CreateWorkspaceParams) workspaceDTO {
	t.Helper()
	if p.RootPath == "" {
		p.RootPath = t.TempDir()
	}
	p.Actor, p.ActorKind = actorU, actorKindU
	dto, err := m.createWorkspace(context.Background(), tenant, p)
	if err != nil {
		t.Fatalf("createWorkspace: %v", err)
	}
	return dto
}

// TestWorkspace_FileLifecycle drives the full governed file API round-trip:
// register → mkdir → write → read → stat → list → move → delete → gone.
func TestWorkspace_FileLifecycle(t *testing.T) {
	t.Parallel()

	m, _, tenant, _ := newRuntimeHarness(t)
	ctx := context.Background()
	ws := mkWorkspace(t, m, tenant, CreateWorkspaceParams{})

	if _, err := m.mkdir(ctx, tenant, ws.WorkspaceRef, "src", actorU, actorKindU); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	wr, err := m.writeFile(ctx, tenant, ws.WorkspaceRef, "src/main.go", []byte("package main\n"), actorU, actorKindU)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !wr.Created || wr.SHA256 == "" {
		t.Fatalf("write response: %+v", wr)
	}
	rd, err := m.readFile(ctx, tenant, ws.WorkspaceRef, "src/main.go", actorU, actorKindU)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if rd.Encoding != "utf-8" || rd.Content != "package main\n" {
		t.Fatalf("read response: %+v", rd)
	}
	st, err := m.statFile(ctx, tenant, ws.WorkspaceRef, "src/main.go", actorU, actorKindU)
	if err != nil || st.Type != ftFile || st.Path != "src/main.go" {
		t.Fatalf("stat: %+v err=%v", st, err)
	}
	ls, err := m.listFiles(ctx, tenant, ws.WorkspaceRef, "src", 0, "", actorU, actorKindU)
	if err != nil || len(ls.Entries) != 1 || ls.Entries[0].Name != "main.go" {
		t.Fatalf("list: %+v err=%v", ls, err)
	}
	if err := m.moveFile(ctx, tenant, ws.WorkspaceRef, "src/main.go", "src/app.go", actorU, actorKindU); err != nil {
		t.Fatalf("move: %v", err)
	}
	if _, err := m.readFile(ctx, tenant, ws.WorkspaceRef, "src/main.go", actorU, actorKindU); err == nil {
		t.Fatal("read of a moved-away path must 404")
	}
	if err := m.deleteFile(ctx, tenant, ws.WorkspaceRef, "src/app.go", false, actorU, actorKindU); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := m.statFile(ctx, tenant, ws.WorkspaceRef, "src/app.go", actorU, actorKindU); err == nil {
		t.Fatal("stat of a deleted path must 404")
	}
}

// TestWorkspace_TraversalRejectedViaAPI confirms the file methods refuse an escaping
// path (the jail is wired into every op, not just the pure function).
func TestWorkspace_TraversalRejectedViaAPI(t *testing.T) {
	t.Parallel()

	m, _, tenant, _ := newRuntimeHarness(t)
	ctx := context.Background()
	ws := mkWorkspace(t, m, tenant, CreateWorkspaceParams{})
	for _, bad := range []string{"../../etc/passwd", "/etc/passwd"} {
		if _, err := m.readFile(ctx, tenant, ws.WorkspaceRef, bad, actorU, actorKindU); !isStatus(err, 403) {
			t.Errorf("read %q = %v, want 403", bad, err)
		}
		if _, err := m.writeFile(ctx, tenant, ws.WorkspaceRef, bad, []byte("x"), actorU, actorKindU); !isStatus(err, 403) {
			t.Errorf("write %q = %v, want 403", bad, err)
		}
	}
}

// TestWorkspace_ReadOnlyBlocksWrite confirms a ro workspace refuses mutations.
func TestWorkspace_ReadOnlyBlocksWrite(t *testing.T) {
	t.Parallel()

	m, _, tenant, _ := newRuntimeHarness(t)
	ctx := context.Background()
	ws := mkWorkspace(t, m, tenant, CreateWorkspaceParams{MountMode: mountRO})
	if _, err := m.writeFile(ctx, tenant, ws.WorkspaceRef, "x.txt", []byte("x"), actorU, actorKindU); !isStatus(err, 403) {
		t.Fatalf("write to ro workspace = %v, want 403", err)
	}
}

// TestWorkspace_DeleteRootRejected confirms the workspace root itself is never
// deletable through the API.
func TestWorkspace_DeleteRootRejected(t *testing.T) {
	t.Parallel()

	m, _, tenant, _ := newRuntimeHarness(t)
	ctx := context.Background()
	ws := mkWorkspace(t, m, tenant, CreateWorkspaceParams{})
	if err := m.deleteFile(ctx, tenant, ws.WorkspaceRef, ".", false, actorU, actorKindU); !isStatus(err, 400) {
		t.Fatalf("delete root = %v, want 400", err)
	}
}

// TestWorkspace_ReadSizeLimitTruncates confirms the per-workspace cap clips a large
// read (and reports truncation) rather than reading unbounded.
func TestWorkspace_ReadSizeLimitTruncates(t *testing.T) {
	t.Parallel()

	m, _, tenant, _ := newRuntimeHarness(t)
	ctx := context.Background()
	ws := mkWorkspace(t, m, tenant, CreateWorkspaceParams{MaxReadBytes: 8})
	// Write a 100-byte file directly on disk (bypassing the write cap) to read back.
	if err := os.WriteFile(filepath.Join(ws.RootPath, "big.txt"), []byte(strings.Repeat("a", 100)), 0o644); err != nil {
		t.Fatal(err)
	}
	rd, err := m.readFile(ctx, tenant, ws.WorkspaceRef, "big.txt", actorU, actorKindU)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !rd.Truncated || len(rd.Content) != 8 {
		t.Fatalf("read should clip at the cap: truncated=%v len=%d", rd.Truncated, len(rd.Content))
	}
}

// TestWorkspace_DLPLabelAndDeny is the DoD DLP case: a read of sensitive content is
// CLASSIFIED+LABELED in label mode (and allowed), and DENIED in deny mode.
func TestWorkspace_DLPLabelAndDeny(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	secret := "API_KEY=THIS_IS_SECRET"

	// label mode (default): the read returns the content + the sensitivity label.
	mLabel, _, tenL, _ := newRuntimeHarness(t, WithClassifier(fakeClassifier{trigger: "SECRET"}))
	wsL := mkWorkspace(t, mLabel, tenL, CreateWorkspaceParams{DLPMode: dlpLabel})
	if _, err := mLabel.writeFile(ctx, tenL, wsL.WorkspaceRef, "c.env", []byte(secret), actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	rd, err := mLabel.readFile(ctx, tenL, wsL.WorkspaceRef, "c.env", actorU, actorKindU)
	if err != nil {
		t.Fatalf("label read: %v", err)
	}
	if len(rd.Sensitivity) == 0 || rd.Sensitivity[0].Class != "secret.credential" {
		t.Fatalf("label mode must surface the sensitivity hit, got %+v", rd.Sensitivity)
	}
	if rd.Content != secret {
		t.Fatalf("label mode must still return the content")
	}

	// deny mode: the same read is refused (deny-closed) with the class, no content.
	mDeny, _, tenD, _ := newRuntimeHarness(t, WithClassifier(fakeClassifier{trigger: "SECRET"}))
	wsD := mkWorkspace(t, mDeny, tenD, CreateWorkspaceParams{DLPMode: dlpDeny})
	if _, err := mDeny.writeFile(ctx, tenD, wsD.WorkspaceRef, "c.env", []byte(secret), actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	if _, err := mDeny.readFile(ctx, tenD, wsD.WorkspaceRef, "c.env", actorU, actorKindU); !isStatus(err, 403) {
		t.Fatalf("deny mode must refuse a sensitive read, got %v", err)
	}
	// A CLEAN file in deny mode reads through.
	if _, err := mDeny.writeFile(ctx, tenD, wsD.WorkspaceRef, "clean.txt", []byte("hello"), actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	if _, err := mDeny.readFile(ctx, tenD, wsD.WorkspaceRef, "clean.txt", actorU, actorKindU); err != nil {
		t.Fatalf("deny mode must allow a clean read, got %v", err)
	}
}

// TestWorkspace_DenyModeFailsClosedWithoutClassifier confirms deny-mode is fail-closed
// when no classifier is wired (it cannot prove content safe).
func TestWorkspace_DenyModeFailsClosedWithoutClassifier(t *testing.T) {
	t.Parallel()

	m, _, tenant, _ := newRuntimeHarness(t) // no WithClassifier
	ctx := context.Background()
	ws := mkWorkspace(t, m, tenant, CreateWorkspaceParams{DLPMode: dlpDeny})
	if _, err := m.writeFile(ctx, tenant, ws.WorkspaceRef, "f.txt", []byte("x"), actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	if _, err := m.readFile(ctx, tenant, ws.WorkspaceRef, "f.txt", actorU, actorKindU); !isStatus(err, 403) {
		t.Fatalf("deny mode without a classifier must fail closed, got %v", err)
	}
}

// TestWorkspace_LaunchResolution confirms the ref→path formalization: an
// unregistered ref is denied; a native launch resolves Dir to the canonical root; a
// container launch carries the resolved bind mount.
func TestWorkspace_LaunchResolution(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Unregistered workspace_ref → launch denied (deny-closed).
	mU, _, tenU, _ := newRuntimeHarness(t, WithRunner(&fakeRunner{}), WithCredentialSource(staticCred()))
	if _, err := mU.createRun(ctx, tenU, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, WorkspaceRef: "nope",
		Actor: actorU, ActorKind: actorKindU,
	}); !isStatus(err, 400) {
		t.Fatalf("unregistered workspace_ref must be denied, got %v", err)
	}

	// Native: Dir = canonical root, no container mount.
	frN := &fakeRunner{initSID: "s"}
	mN, _, tenN, _ := newRuntimeHarness(t, WithRunner(frN), WithCredentialSource(staticCred()))
	wsN := mkWorkspace(t, mN, tenN, CreateWorkspaceParams{})
	if _, err := mN.createRun(ctx, tenN, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, WorkspaceRef: wsN.WorkspaceRef,
		Actor: actorU, ActorKind: actorKindU,
	}); err != nil {
		t.Fatalf("native createRun: %v", err)
	}
	if got := frN.lastSpec(); got.Dir != wsN.RootPath || got.Workspace != nil {
		t.Fatalf("native spec: Dir=%q (want %q) Workspace=%+v (want nil)", got.Dir, wsN.RootPath, got.Workspace)
	}

	// Container: Dir = container target, Workspace bind carries the host root.
	frC := &fakeRunner{initSID: "s"}
	mC, _, tenC, _ := newRuntimeHarness(t, WithRunner(frC), WithCredentialSource(staticCred()))
	wsC := mkWorkspace(t, mC, tenC, CreateWorkspaceParams{MountMode: mountRO, ContainerTarget: "/ws"})
	if _, err := mC.createRun(ctx, tenC, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationContainer, WorkspaceRef: wsC.WorkspaceRef,
		Actor: actorU, ActorKind: actorKindU,
	}); err != nil {
		t.Fatalf("container createRun: %v", err)
	}
	got := frC.lastSpec()
	if got.Dir != "/ws" || got.Workspace == nil {
		t.Fatalf("container spec: Dir=%q Workspace=%+v", got.Dir, got.Workspace)
	}
	if got.Workspace.HostPath != wsC.RootPath || got.Workspace.ContainerTarget != "/ws" || !got.Workspace.ReadOnly {
		t.Fatalf("container mount wrong: %+v", got.Workspace)
	}
}

// TestWorkspace_MutationAudited confirms a write is sealed in the tamper-evident audit
// chain with the operation and the content hash (never the bytes).
func TestWorkspace_MutationAudited(t *testing.T) {
	t.Parallel()

	m, st, tenant, _ := newRuntimeHarness(t)
	ctx := context.Background()
	ws := mkWorkspace(t, m, tenant, CreateWorkspaceParams{})
	wr, err := m.writeFile(ctx, tenant, ws.WorkspaceRef, "f.txt", []byte("data"), actorU, actorKindU)
	if err != nil {
		t.Fatal(err)
	}
	// The ledger Walk discards Meta (the PayloadHash is the authoritative anchor), so
	// verify the sealed event's PayloadHash equals the canonical hash of the mutation —
	// proving the write is anchored to its operation + content hash (never the bytes).
	want := workspacePayloadHash(wsMutationInput{op: "write", workspaceRef: ws.WorkspaceRef, path: "f.txt", contentHash: wr.SHA256})
	found := false
	err = st.View(ctx, tenant, func(sc store.Scope) error {
		return sc.Audit().Walk(ctx, 0, func(ev model.AuditEvent) error {
			if ev.Action == "sessions.workspace.write" && bytes.Equal(ev.PayloadHash, want[:]) {
				found = true
			}
			return nil
		})
	})
	if err != nil {
		t.Fatalf("walk audit: %v", err)
	}
	if !found {
		t.Fatal("the write must be sealed in the audit chain anchored by its content hash")
	}
	// And the chain remains intact.
	if rerr := st.View(ctx, tenant, func(sc store.Scope) error {
		rep, e := sc.Audit().Verify(ctx, 0)
		if e == nil && !rep.OK {
			t.Fatalf("audit chain broken: %s", rep.Reason)
		}
		return e
	}); rerr != nil {
		t.Fatalf("verify: %v", rerr)
	}
}

// TestWorkspace_DeregisterDoesNotTouchFiles confirms deleting the registration leaves
// the host files intact (only the registry row is removed).
func TestWorkspace_DeregisterDoesNotTouchFiles(t *testing.T) {
	t.Parallel()

	m, _, tenant, _ := newRuntimeHarness(t)
	ctx := context.Background()
	ws := mkWorkspace(t, m, tenant, CreateWorkspaceParams{})
	if _, err := m.writeFile(ctx, tenant, ws.WorkspaceRef, "keep.txt", []byte("x"), actorU, actorKindU); err != nil {
		t.Fatal(err)
	}
	if err := m.deleteWorkspace(ctx, tenant, ws.WorkspaceRef, actorU, actorKindU); err != nil {
		t.Fatalf("deregister: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws.RootPath, "keep.txt")); err != nil {
		t.Fatalf("deregister must NOT delete host files, but keep.txt is gone: %v", err)
	}
	if _, err := m.getWorkspace(ctx, tenant, ws.WorkspaceRef); !isStatus(err, 404) {
		t.Fatalf("the registration must be gone, got %v", err)
	}
}

// TestWorkspace_AllowSubpathConfines confirms the file API is confined to the declared
// subpaths, and that an escaping subpath is refused at registration.
func TestWorkspace_AllowSubpathConfines(t *testing.T) {
	t.Parallel()

	m, _, tenant, _ := newRuntimeHarness(t)
	ctx := context.Background()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "a.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret.env"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := mkWorkspace(t, m, tenant, CreateWorkspaceParams{RootPath: root, AllowSubpaths: []string{"src"}})

	if _, err := m.readFile(ctx, tenant, ws.WorkspaceRef, "src/a.go", actorU, actorKindU); err != nil {
		t.Fatalf("read inside allowed subpath: %v", err)
	}
	if _, err := m.readFile(ctx, tenant, ws.WorkspaceRef, "secret.env", actorU, actorKindU); !isStatus(err, 403) {
		t.Fatalf("read outside the allowed subpath must be 403, got %v", err)
	}

	// An escaping allow_subpaths entry is refused at registration.
	if _, err := m.createWorkspace(ctx, tenant, CreateWorkspaceParams{
		RootPath: t.TempDir(), AllowSubpaths: []string{"../.."}, Actor: actorU, ActorKind: actorKindU,
	}); !isStatus(err, 400) {
		t.Fatalf("escaping allow_subpaths must be rejected at registration, got %v", err)
	}
}

// isStatus reports whether err is a runErr with the given HTTP status.
func isStatus(err error, status int) bool {
	var re *runErr
	return errors.As(err, &re) && re.status == status
}
