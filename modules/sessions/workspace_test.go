// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

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

// TestWorkspace_ReadRefusesANonRegularFile holds the read and the archive of a FIFO: a
// blocking open would wait for a writer forever and hold the request, one per pipe.
// Each answers at once that the path is not a regular file.
func TestWorkspace_ReadRefusesANonRegularFile(t *testing.T) {
	t.Parallel()

	m, _, tenant, _ := newRuntimeHarness(t)
	ctx := context.Background()
	ws := mkWorkspace(t, m, tenant, CreateWorkspaceParams{})
	if err := syscall.Mkfifo(filepath.Join(ws.RootPath, "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() error{
		"read": func() error {
			_, err := m.readFile(ctx, tenant, ws.WorkspaceRef, "pipe", actorU, actorKindU)
			return err
		},
		"archive": func() error {
			_, _, err := m.archiveFiles(ctx, tenant, ws.WorkspaceRef, []string{"pipe"}, actorU, actorKindU)
			return err
		},
	} {
		done := make(chan error, 1)
		go func() { done <- call() }()
		select {
		case err := <-done:
			if !isStatus(err, 400) || !strings.Contains(err.Error(), "not a regular file") {
				t.Errorf("%s of a FIFO = %v, want 400 not a regular file", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s of a FIFO blocked", name)
		}
	}
}

// TestWorkspace_DLPLabelAndDeny is the DoD DLP case: a read of sensitive content is
// CLASSIFIED+LABELED in label mode (and allowed), and DENIED in deny mode.
func TestWorkspace_DLPLabelAndDeny(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	secret := "API_KEY=THIS_IS_SECRET"

	// Explicit label mode: the read returns the content + the sensitivity label.
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

// TestWorkspace_UploadCarriesDLPLabels: a write is labeled like a read. The answer and the
// audit row carry the classes (never the content), and a deny-mode workspace still takes the
// write, because refusing it would change the published PUT contract.
func TestWorkspace_UploadCarriesDLPLabels(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	for _, mode := range []string{dlpLabel, dlpDeny} {
		m, _, tenant, _ := newRuntimeHarness(t, WithClassifier(fakeClassifier{trigger: "SECRET"}))
		ws := mkWorkspace(t, m, tenant, CreateWorkspaceParams{DLPMode: mode})
		wr, err := m.writeFile(ctx, tenant, ws.WorkspaceRef, "c.env", []byte("API_KEY=THIS_IS_SECRET"), actorU, actorKindU)
		if err != nil {
			t.Fatalf("%s: write: %v", mode, err)
		}
		if len(wr.Sensitivity) != 1 || wr.Sensitivity[0].Class != "secret.credential" {
			t.Fatalf("%s: the write answer must carry the label, got %+v", mode, wr.Sensitivity)
		}
		writes := acctAuditsOf(t, m, tenant, "sessions.workspace.write")
		if len(writes) != 1 || !strings.Contains(fmt.Sprint(writes[0].meta["sensitivity"]), "secret.credential") || strings.Contains(fmt.Sprint(writes), "THIS_IS_SECRET") {
			t.Fatalf("%s: write audit = %v, want the class and never the content", mode, writes)
		}
		clean, err := m.writeFile(ctx, tenant, ws.WorkspaceRef, "ok.txt", []byte("hello"), actorU, actorKindU)
		if err != nil || len(clean.Sensitivity) != 0 {
			t.Fatalf("%s: a clean write must carry no label: %+v %v", mode, clean, err)
		}
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
	if _, err := createProfiledTestRun(t, mU, ctx, tenU, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative, WorkspaceRef: "nope",
		Actor: actorU, ActorKind: actorKindU,
	}); !isStatus(err, 400) {
		t.Fatalf("unregistered workspace_ref must be denied, got %v", err)
	}

	// Native: Dir = canonical root, no container mount.
	frN := &fakeRunner{initSID: "s"}
	mN, _, tenN, _ := newRuntimeHarness(t, WithRunner(frN), WithCredentialSource(staticCred()))
	wsN := mkWorkspace(t, mN, tenN, CreateWorkspaceParams{})
	if _, err := createProfiledTestRun(t, mN, ctx, tenN, CreateRunParams{
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
	if _, err := createProfiledTestRun(t, mC, ctx, tenC, CreateRunParams{
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

// A folder on a network share whose file server is down blocks in the kernel. The engine
// answers "not responding" within the deadline instead of hanging the request, one stuck
// lookup serves every request meanwhile, and the folder works again once the server does.
func TestWorkspace_RootNotRespondingIsRefusedAndRecovers(t *testing.T) {
	// Not parallel: it swaps the package's probe and deadline.
	m, _, tenant, _ := newRuntimeHarness(t)
	ctx := context.Background()
	ws := mkWorkspace(t, m, tenant, CreateWorkspaceParams{})

	realProbe, realTimeout := probeRoot, rootProbeTimeout
	t.Cleanup(func() { probeRoot, rootProbeTimeout = realProbe, realTimeout })
	hold, calls := make(chan struct{}), atomic.Int32{}
	probeRoot = func(root string) (string, os.FileInfo, error) {
		calls.Add(1)
		<-hold
		return realProbe(root)
	}
	rootProbeTimeout = 50 * time.Millisecond

	for i := 0; i < 3; i++ {
		start := time.Now()
		_, err := m.resolveLaunchWorkspace(ctx, tenant, ws.WorkspaceRef)
		var re *runErr
		if !errors.As(err, &re) || re.status != 424 || !strings.Contains(re.Error(), "is not responding") {
			t.Fatalf("call %d: want 424 not responding, got %v", i, err)
		}
		if time.Since(start) > 5*time.Second {
			t.Fatalf("call %d took %v", i, time.Since(start))
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("a stuck lookup is shared: %d lookups started, want 1", n)
	}

	close(hold)
	rootProbeTimeout = realTimeout
	if _, err := m.resolveLaunchWorkspace(ctx, tenant, ws.WorkspaceRef); err != nil {
		t.Fatalf("server back: %v", err)
	}
}

func TestDocumentMentionsPreserveOrdinaryInput(t *testing.T) {
	m, _, tenant, _ := newRuntimeHarness(t)
	folder := t.TempDir()
	for _, name := range []string{"package.json", "report.pdf"} {
		if err := os.WriteFile(filepath.Join(folder, name), []byte("original"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	m.UseDocumentConverter(func(context.Context, string, []byte) ([]byte, error) { return nil, errors.New("converter unavailable") })
	rec := model.Record{colRunWorkspacePath: folder}
	for _, text := range []string{"Create @schema.json for the API", "see @package.json", "see @missing.pdf", "see @report.pdf"} {
		got, err := m.expandDocumentMentions(context.Background(), tenant, rec, text)
		if err != nil || got != text {
			t.Errorf("%q: got %q, %v; want original input", text, got, err)
		}
	}
}

func TestDocumentMentionPinsCompanionDirectory(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("document mention conversion requires Linux confinement")
	}
	m, _, tenant, _ := newRuntimeHarness(t)
	folder := t.TempDir()
	docs, sealed := filepath.Join(folder, "docs"), filepath.Join(folder, "sealed")
	for _, dir := range []string{docs, sealed} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(docs, "a.pdf"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	m.UseDocumentConverter(func(context.Context, string, []byte) ([]byte, error) {
		if err := os.Rename(docs, docs+"-old"); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("sealed", docs); err != nil {
			t.Fatal(err)
		}
		return []byte("converted table cell"), nil
	})
	got, err := m.expandDocumentMentions(context.Background(), tenant, model.Record{colRunWorkspacePath: folder}, "read @docs/a.pdf")
	if err != nil || !strings.Contains(got, "converted table cell") {
		t.Fatalf("conversion: %q, %v", got, err)
	}
	entries, err := os.ReadDir(sealed)
	if err != nil || len(entries) != 0 {
		t.Fatalf("companion redirected into sealed directory: %v, %v", entries, err)
	}
	entries, err = os.ReadDir(docs + "-old")
	if err != nil || len(entries) != 2 {
		t.Fatalf("companion absent from pinned directory: %v, %v", entries, err)
	}
}

// An uploaded document whose Markdown copy does not fit the folder's size limit is refused
// before anything is written: before, the document landed and only its copy was refused, so
// the person was told the upload failed while the document was already there.
func TestDocumentUploadIsRefusedWholeWhenItsCopyDoesNotFit(t *testing.T) {
	m, _, tenant, _ := newRuntimeHarness(t)
	folder := mkWorkspace(t, m, tenant, CreateWorkspaceParams{MaxReadBytes: 64})
	m.UseDocumentConverter(func(context.Context, string, []byte) ([]byte, error) {
		return bytes.Repeat([]byte("a long table cell "), 10), nil
	})
	_, err := m.writeDocumentFile(context.Background(), tenant, folder.WorkspaceRef, "report.pdf", []byte("%PDF a small document"), actorU, actorKindU)
	var re *runErr
	if !errors.As(err, &re) || re.status != 400 {
		t.Fatalf("upload with a copy over the limit = %v, want 400", err)
	}
	entries, err := os.ReadDir(folder.RootPath)
	if err != nil || len(entries) != 0 {
		t.Fatalf("the refused upload left %v (%v), want nothing written", entries, err)
	}
	m.UseDocumentConverter(func(context.Context, string, []byte) ([]byte, error) { return []byte("cell"), nil })
	if _, err := m.writeDocumentFile(context.Background(), tenant, folder.WorkspaceRef, "report.pdf", []byte("%PDF a small document"), actorU, actorKindU); err != nil {
		t.Fatalf("the retry whose copy fits: %v", err)
	}
}
