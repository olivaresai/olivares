// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// run_launch_target_read_test.go — the launch-target read port: which provider
// profile, which driver and which execution environment one run was launched
// under, presented to a consumer that holds no runtime.
//
// ⛔ EVERY IDENTIFIER OF THE PORT IS REACHED BY NAME, NEVER BY REFERENCE. This
// file is written before the port exists and must COMPILE against the tree that
// does not have it, so a missing port has to arrive as a failed assertion and
// not as a build error. The module's method is bound through reflection; the
// interface's arity is read out of the package's own source, because reflect
// cannot look up an interface type by name and naming it here would be the
// compile-time reference this file may not make.
const (
	runLaunchTargetMethodName = "ReadRunLaunchTarget"
	runLaunchTargetPortName   = "RunLaunchTargetReader"
	runLaunchTargetTypeName   = "RunLaunchTargetSnapshot"
)

// launchTargetSnapshotFields is the whole presented snapshot, in order. The two
// HOME paths (runtime_schema.go:152-153) and the authorized auth source (:160)
// are deliberately absent: the schema states the homes are read only on the
// authorized configuration read, and neither is a selector a launch is named by.
var launchTargetSnapshotFields = []string{
	"RunRef", "ProviderProfileID", "ProviderDriver", "ProviderEnvironmentRef",
}

// launchTargetPort binds the port's single method on the module and checks its
// signature before anything calls it — a reflected Call on a wrong signature
// panics, and a panic is not a legible red.
func launchTargetPort(t *testing.T, m *Module) reflect.Value {
	t.Helper()
	fn := reflect.ValueOf(m).MethodByName(runLaunchTargetMethodName)
	if !fn.IsValid() {
		t.Fatalf("the module has no %s method: the launch-target port does not exist", runLaunchTargetMethodName)
	}
	ft := fn.Type()
	want := []reflect.Type{
		reflect.TypeOf((*context.Context)(nil)).Elem(),
		reflect.TypeOf(model.TenantID("")),
		reflect.TypeOf(model.ID("")),
		reflect.TypeOf(""),
	}
	if ft.NumIn() != len(want) || ft.NumOut() != 2 {
		t.Fatalf("%s takes %d/returns %d, want %d arguments and (snapshot, error)",
			runLaunchTargetMethodName, ft.NumIn(), ft.NumOut(), len(want))
	}
	for i, w := range want {
		if ft.In(i) != w {
			t.Fatalf("%s argument %d is %s, want %s — the sibling's argument order and meaning",
				runLaunchTargetMethodName, i, ft.In(i), w)
		}
	}
	if ft.Out(1) != reflect.TypeOf((*error)(nil)).Elem() {
		t.Fatalf("%s second result is %s, want error", runLaunchTargetMethodName, ft.Out(1))
	}
	return fn
}

// readLaunchTarget calls the port and returns the snapshot value and the error.
func readLaunchTarget(t *testing.T, m *Module, tenant model.TenantID, workspace model.ID, runRef string) (reflect.Value, error) {
	t.Helper()
	out := launchTargetPort(t, m).Call([]reflect.Value{
		reflect.ValueOf(context.Background()),
		reflect.ValueOf(tenant),
		reflect.ValueOf(workspace),
		reflect.ValueOf(runRef),
	})
	err, _ := out[1].Interface().(error)
	return out[0], err
}

// launchTargetField reads one presented field of the snapshot by name.
func launchTargetField(t *testing.T, snap reflect.Value, name string) string {
	t.Helper()
	f := snap.FieldByName(name)
	if !f.IsValid() {
		t.Fatalf("the snapshot has no %s field", name)
	}
	if f.Kind() != reflect.String {
		t.Fatalf("snapshot field %s is a %s, want a string", name, f.Kind())
	}
	return f.String()
}

// assertLaunchTargetSnapshotShape holds the PUBLIC type to what it may carry: a
// presentation consumer receives values it can render, never a handle it could
// act through. A func, a channel, an interface or a pointer is such a handle,
// and a name that says home, credential or workspace is a fact this port has no
// business presenting.
func assertLaunchTargetSnapshotShape(t *testing.T, snap reflect.Type) {
	t.Helper()
	if snap.Kind() != reflect.Struct {
		t.Fatalf("%s returns a %s, want a struct snapshot", runLaunchTargetMethodName, snap.Kind())
	}
	if snap.Name() != runLaunchTargetTypeName || !strings.HasSuffix(snap.PkgPath(), "/modules/sessions") {
		t.Errorf("snapshot type = %q in %q, want %s in this package", snap.Name(), snap.PkgPath(), runLaunchTargetTypeName)
	}
	if snap.NumField() != len(launchTargetSnapshotFields) {
		t.Errorf("%s has %d fields, want exactly %v", snap.Name(), snap.NumField(), launchTargetSnapshotFields)
	}
	for _, name := range launchTargetSnapshotFields {
		f, ok := snap.FieldByName(name)
		if !ok {
			t.Errorf("%s has no %s field", snap.Name(), name)
			continue
		}
		if f.Type.Kind() != reflect.String {
			t.Errorf("%s.%s is a %s, want a string", snap.Name(), name, f.Type.Kind())
		}
	}
	banned := []string{"Home", "Auth", "Credential", "Secret", "Token", "Workspace", "Lineage", "PID", "Process", "Runtime"}
	for i := 0; i < snap.NumField(); i++ {
		f := snap.Field(i)
		switch f.Type.Kind() {
		case reflect.Func, reflect.Chan, reflect.Interface, reflect.Pointer, reflect.UnsafePointer:
			t.Errorf("%s.%s is a %s: a presented snapshot carries values, never a handle to the run",
				snap.Name(), f.Name, f.Type.Kind())
		}
		for _, word := range banned {
			if strings.Contains(f.Name, word) {
				t.Errorf("%s.%s names %q: that fact is not this port's to present", snap.Name(), f.Name, word)
			}
		}
	}
}

// launchTargetPackageFiles parses every non-test source file of this package,
// WITH its comments, so an assertion can read a DECLARATION — an interface's
// arity, an exported doc comment — rather than a value. Neither is reachable
// through reflect, and this file may not name at compile time the port it drives.
func launchTargetPackageFiles(t *testing.T) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, file)
	}
	return files
}

// assertLaunchTargetRefusalIsDocumented reads the EXPORTED doc comment of the
// port's method. The one refusal a real row can reach — a run that NAMES a
// provider profile and cannot say which driver it was launched under — is stated
// on an unexported decoder no consumer ever opens, so a consumer learns it from
// this comment or from nowhere.
func assertLaunchTargetRefusalIsDocumented(t *testing.T) {
	t.Helper()
	doc, found := "", false
	for _, file := range launchTargetPackageFiles(t) {
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Name.Name != runLaunchTargetMethodName {
				continue
			}
			found = true
			if fn.Doc != nil {
				doc = fn.Doc.Text()
			}
		}
	}
	if !found {
		t.Fatalf("package sessions declares no %s method to document", runLaunchTargetMethodName)
	}
	for _, word := range []string{"profile", "driver", "ErrRunAuthorityUnavailable"} {
		if !strings.Contains(strings.ToLower(doc), strings.ToLower(word)) {
			t.Errorf("the doc comment of %s never says %q: the only refusal a real row can reach is a profile named with no driver, and a consumer reads that comment, not the decoder",
				runLaunchTargetMethodName, word)
		}
	}
}

// assertLaunchTargetPortIsOneMethod reads the port's DECLARATION out of the
// package's own source. Arity and embedding are properties of the declaration
// rather than of any value, reflect offers no way to reach an interface type by
// name, and this file may not name the type it is written to drive.
func assertLaunchTargetPortIsOneMethod(t *testing.T) {
	t.Helper()
	var decl *ast.InterfaceType
	asserted := false
	for _, file := range launchTargetPackageFiles(t) {
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.TypeSpec:
				if iface, ok := node.Type.(*ast.InterfaceType); ok && node.Name.Name == runLaunchTargetPortName {
					decl = iface
				}
			case *ast.ValueSpec:
				// The `var _ Port = (*Module)(nil)` that makes the module's
				// obligation a compile error rather than a runtime surprise.
				id, ok := node.Type.(*ast.Ident)
				if ok && id.Name == runLaunchTargetPortName && len(node.Names) == 1 && node.Names[0].Name == "_" {
					asserted = true
				}
			}
			return true
		})
	}
	if decl == nil {
		t.Fatalf("package sessions declares no %s interface", runLaunchTargetPortName)
	}
	if !asserted {
		t.Errorf("no `var _ %s = (*Module)(nil)`: the module's obligation is unpinned", runLaunchTargetPortName)
	}
	methods, embedded := 0, 0
	if decl.Methods != nil {
		for _, field := range decl.Methods.List {
			if len(field.Names) == 0 {
				embedded++
				continue
			}
			methods += len(field.Names)
			for _, n := range field.Names {
				if n.Name != runLaunchTargetMethodName {
					t.Errorf("%s declares %s; the port names exactly %s",
						runLaunchTargetPortName, n.Name, runLaunchTargetMethodName)
				}
			}
		}
	}
	if embedded != 0 {
		t.Errorf("%s embeds %d interface(s): a narrow reader never inherits a wider one",
			runLaunchTargetPortName, embedded)
	}
	if methods != 1 {
		t.Errorf("%s declares %d methods, want exactly 1", runLaunchTargetPortName, methods)
	}
}

// launchTargetWorkspaces creates two active workspaces in the tenant.
func launchTargetWorkspaces(t *testing.T, m *Module, tenant model.TenantID) (model.ID, model.ID) {
	t.Helper()
	ctx := context.Background()
	var alpha, bravo model.ID
	if err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		a, err := sc.Workspaces().Create(ctx, model.Workspace{Name: "Alpha", Slug: "lt-alpha", Status: model.StatusActive})
		if err != nil {
			return err
		}
		alpha = a.ID
		b, err := sc.Workspaces().Create(ctx, model.Workspace{Name: "Bravo", Slug: "lt-bravo", Status: model.StatusActive})
		bravo = b.ID
		return err
	}); err != nil {
		t.Fatalf("workspaces: %v", err)
	}
	return alpha, bravo
}

// seedLaunchTargetRun creates one run row owned by workspace and stamps its
// provider snapshot through setProfileSnapshot (runtime_profile.go:236) — the
// module's only writer of those five columns, applied in the same order the
// launch applies it (runtime.go:2420-2440). A nil snap is the run launched under
// no profile. tweak, when given, then writes a row production cannot write.
func seedLaunchTargetRun(t *testing.T, m *Module, tenant model.TenantID, workspace model.ID, runRef string, snap *ProviderHomeSnapshot, tweak func(model.Record)) {
	t.Helper()
	ctx := context.Background()
	if err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec := model.Record{
			colRunRef: runRef, colTransport: "stream-json", colPermissionMode: "default",
			colIsolation: "native", colState: stateRunning, colLastEventSeq: int64(0),
			colRunAuthzWorkspaceID: workspace.String(),
		}
		setProfileSnapshot(rec, snap)
		if tweak != nil {
			tweak(rec)
		}
		_, err = repo.Create(ctx, rec)
		return err
	}); err != nil {
		t.Fatalf("seed run %s: %v", runRef, err)
	}
}

// clearLaunchTargetLineage makes a run look like every row created before the
// lineage column existed: lawful in every other respect, owned by nobody.
func clearLaunchTargetLineage(t *testing.T, m *Module, tenant model.TenantID, runRef string) {
	t.Helper()
	ctx := context.Background()
	if err := m.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rec, err := findRunRec(ctx, repo, runRef)
		if err != nil {
			return err
		}
		rec[colRunAuthzWorkspaceID] = nil
		_, err = repo.Update(ctx, rec)
		return err
	}); err != nil {
		t.Fatalf("clear the lineage of %s: %v", runRef, err)
	}
}

// launchTargetRunRows reads every run row of the tenant, keyed by its reference,
// so two readings can be compared without depending on a page's order.
func launchTargetRunRows(t *testing.T, m *Module, tenant model.TenantID) map[string]model.Record {
	t.Helper()
	ctx := context.Background()
	out := map[string]model.Record{}
	if err := m.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(runKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(ctx, model.Query{Limit: 100})
		if err != nil {
			return err
		}
		for _, row := range rows {
			out[row.String(colRunRef)] = row
		}
		return nil
	}); err != nil {
		t.Fatalf("read the run rows: %v", err)
	}
	return out
}

// confinedLaunchTargetData is the handle a request-scoped caller already holds:
// every transaction hands the module a scope ALREADY confined to one workspace.
// It reproduces core/api's confineIfMarked (core/api/modules.go:247-259), which
// is what produces that shape in production, so the reader is asked the question
// in the only form a confined caller can ask it.
type confinedLaunchTargetData struct {
	inner api.ModuleData
	ws    model.ID
}

func (d confinedLaunchTargetData) View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.inner.View(ctx, tenant, d.confine(ctx, fn))
}

func (d confinedLaunchTargetData) Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error {
	return d.inner.Mutate(ctx, tenant, d.confine(ctx, fn))
}

func (d confinedLaunchTargetData) confine(ctx context.Context, fn func(store.Scope) error) func(store.Scope) error {
	return func(raw store.Scope) error {
		sc, err := store.ConfineWorkspace(ctx, raw, d.ws)
		if err != nil {
			return err
		}
		return fn(sc)
	}
}

// TestRunLaunchTargetReaderNamesProviderAndEnvironmentWithoutTheRuntime is the
// whole contract of the launch-target port: what a lawfully visible run says
// about the target it was launched at, what every other kind of request is told
// instead, and that saying it changes nothing and hands over nothing.
func TestRunLaunchTargetReaderNamesProviderAndEnvironmentWithoutTheRuntime(t *testing.T) {
	m, st, tenant, _ := newSess(t)
	alpha, bravo := launchTargetWorkspaces(t, m, tenant)

	launched := &ProviderHomeSnapshot{
		ProfileID: "profile-alpha", Driver: "claude", EnvironmentRef: testEnvRef,
		ConfigHome: "/seeded/config", UserHome: "/seeded/home",
		AuthSource: "subscription", ProviderRecordRef: "record-alpha",
	}
	const (
		profiled  = "run-launch-target-profiled"
		legacy    = "run-launch-target-no-profile"
		halfStamp = "run-launch-target-half-stamp"
		unowned   = "run-launch-target-unowned"
		elsewhere = "run-launch-target-bravo"
	)
	seedLaunchTargetRun(t, m, tenant, alpha, profiled, launched, nil)
	seedLaunchTargetRun(t, m, tenant, alpha, legacy, nil, nil)
	// ⛔ A ROW PRODUCTION CANNOT WRITE, and that is the point. setProfileSnapshot
	// writes the five profile columns as ONE stamp (runtime_profile.go:236-260)
	// and normalizeDriverKey refuses an empty driver (provider_profile.go:280-284),
	// so a run that NAMES a profile and cannot say which driver it was launched
	// under is incoherent rather than legacy, and is refused rather than presented.
	seedLaunchTargetRun(t, m, tenant, alpha, halfStamp, nil, func(rec model.Record) {
		rec[colRunProfileID] = "profile-without-a-driver"
	})
	seedLaunchTargetRun(t, m, tenant, alpha, unowned, launched, nil)
	seedLaunchTargetRun(t, m, tenant, bravo, elsewhere, launched, nil)
	clearLaunchTargetLineage(t, m, tenant, unowned)

	before := launchTargetRunRows(t, m, tenant)

	t.Run("the port is one method over a snapshot of values", func(t *testing.T) {
		assertLaunchTargetPortIsOneMethod(t)
		assertLaunchTargetRefusalIsDocumented(t)
		assertLaunchTargetSnapshotShape(t, launchTargetPort(t, m).Type().Out(0))
	})

	t.Run("a lawfully visible run names its profile, driver and environment", func(t *testing.T) {
		snap, err := readLaunchTarget(t, m, tenant, alpha, profiled)
		if err != nil {
			t.Fatalf("lawful read: %v", err)
		}
		for field, want := range map[string]string{
			"RunRef":                 profiled,
			"ProviderProfileID":      launched.ProfileID,
			"ProviderDriver":         launched.Driver,
			"ProviderEnvironmentRef": launched.EnvironmentRef,
		} {
			if got := launchTargetField(t, snap, field); got != want {
				t.Errorf("%s = %q, want %q", field, got, want)
			}
		}
	})

	t.Run("a run launched under no profile is presented empty, not refused", func(t *testing.T) {
		// A legacy run is a REAL state, not a fault, and the WRITER settles it: the
		// sole writer of the five profile columns clears them TOGETHER for a launch
		// that carries no profile (runtime_profile.go:237-243), and the schema
		// declares all five nullable, so an all-empty stamp is a persisted state
		// under EVERY posture. The two readers that tolerate it —
		// resolveLaunchProfileInto:178-183 admitting an unprofiled launch, and
		// revalidateStoredProfile:881-885 continuing one — do so only where profiled
		// launches are OFF; with them on, both refuse, and rows written before that
		// posture keep their empty stamp regardless. So the decision rests on the
		// writer, not on either reader. Refusing here would report a row that is
		// perfectly available as unavailable.
		snap, err := readLaunchTarget(t, m, tenant, alpha, legacy)
		if err != nil {
			t.Fatalf("run with no profile: %v", err)
		}
		if got := launchTargetField(t, snap, "RunRef"); got != legacy {
			t.Errorf("RunRef = %q, want %q", got, legacy)
		}
		for _, field := range []string{"ProviderProfileID", "ProviderDriver", "ProviderEnvironmentRef"} {
			if got := launchTargetField(t, snap, field); got != "" {
				t.Errorf("%s = %q, want empty: this run was launched under no profile", field, got)
			}
		}
	})

	t.Run("an incoherent stamp is unavailable, never concealed", func(t *testing.T) {
		_, err := readLaunchTarget(t, m, tenant, alpha, halfStamp)
		if !errors.Is(err, ErrRunAuthorityUnavailable) {
			t.Fatalf("half-written stamp = %v, want ErrRunAuthorityUnavailable", err)
		}
		if errors.Is(err, store.ErrNotFound) {
			t.Error("a visible row answered as absent: concealment is for rows the caller may not see")
		}
	})

	t.Run("eight inputs, each ErrNotFound under errors.Is", func(t *testing.T) {
		// ⛔ WHAT THIS PROVES, AND WHAT IT DOES NOT. Each of the eight is
		// store.ErrNotFound under errors.Is, which is the whole of what a caller
		// branches on. It is NOT one identical error value, and the name no longer
		// says it is: the three argument guards return the bare sentinel
		// (identity_read.go:225-227), while a workspace that is not this tenant's
		// comes back wrapped by the confinement it failed ("confinement workspace
		// <id>: …", core/store/workspace_scope.go:82-87). The sibling ReadRunLaunch
		// differs in exactly those two ways, so this port conceals as it does. What
		// the longer text names is the workspace the CALLER supplied — never a run,
		// a lineage, or whether any row is there — and the last assertion holds it
		// to that. Both halves are asserted, not merely claimed.
		other := ensureTenant(t, st, "launch-target-other")
		for name, args := range map[string]struct {
			tenant    model.TenantID
			workspace model.ID
			runRef    string
			// bare says the argument guard answers before any store is opened, so
			// the sentinel arrives with nothing wrapped around it at all.
			bare bool
		}{
			"absent run":                      {tenant, alpha, model.NewID().String(), false},
			"a run of another workspace":      {tenant, alpha, elsewhere, false},
			"this run from another workspace": {tenant, bravo, profiled, false},
			"another tenant entirely":         {other, alpha, profiled, false},
			"a run with no lineage":           {tenant, alpha, unowned, false},
			"zero tenant":                     {"", alpha, profiled, true},
			"zero workspace":                  {tenant, "", profiled, true},
			"empty reference":                 {tenant, alpha, "", true},
		} {
			_, err := readLaunchTarget(t, m, args.tenant, args.workspace, args.runRef)
			if err == nil {
				t.Errorf("%s answered a snapshot, want ErrNotFound", name)
				continue
			}
			if !errors.Is(err, store.ErrNotFound) {
				t.Errorf("%s = %v, want ErrNotFound — telling these apart is an existence probe", name, err)
			}
			if errors.Is(err, ErrRunAuthorityUnavailable) {
				t.Errorf("%s answered unavailable, which says the row is there", name)
			}
			if args.bare && err != store.ErrNotFound {
				t.Errorf("%s = %v, want the bare sentinel the argument guard returns", name, err)
			}
			// An empty reference is skipped because strings.Contains is trivially
			// true for the empty string, not because that answer is exempt.
			if args.runRef != "" && strings.Contains(err.Error(), args.runRef) {
				t.Errorf("%s names the run it would not confirm: %v", name, err)
			}
		}
		// ⛔ AND THE CONCEALED ROW WAS NOT REPAIRED ON ITS WAY OUT. A reader that
		// quietly filled the column would turn a concealed row into a visible one
		// as a side effect of being looked at.
		row, ok := launchTargetRunRows(t, m, tenant)[unowned]
		if !ok || !row.IsNull(colRunAuthzWorkspaceID) {
			t.Error("reading a concealed run wrote a lineage onto it")
		}
	})

	t.Run("a request confined elsewhere is answered as foreign", func(t *testing.T) {
		m.UseData(confinedLaunchTargetData{inner: api.NewModuleData(st), ws: bravo})
		defer m.UseData(api.NewModuleData(st))
		if _, err := readLaunchTarget(t, m, tenant, alpha, profiled); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("confined to another workspace = %v, want ErrNotFound", err)
		}
		// The same boundary asking about its OWN workspace still reads, so the
		// answer above is confinement and not a blanket refusal.
		if _, err := readLaunchTarget(t, m, tenant, bravo, elsewhere); err != nil {
			t.Errorf("confined to its own workspace = %v, want the snapshot", err)
		}
	})

	if after := launchTargetRunRows(t, m, tenant); !reflect.DeepEqual(before, after) {
		t.Error("the reads changed a persisted run row: this port only ever looks")
	}
}

// TestRunLaunchTargetReaderReadsARunTheLaunchPathWrote asks the port about a run
// NO test hand-built. The launch writes the row, resolves its lineage and stamps
// the five profile columns itself, exactly as it does in production; the port is
// then asked the question a presentation consumer would ask, and its answer is
// held against the run DTO the same launch returned.
//
// It uses the package's untagged profiled harness (a fake runner, no child
// process) rather than the fixture the sibling reader's test drives: that one
// lives behind a `//go:build unix` file, and reaching it would put every case in
// this file behind the same constraint for a port that has no platform in it.
func TestRunLaunchTargetReaderReadsARunTheLaunchPathWrote(t *testing.T) {
	m, st, tenant, profile, _ := profiledHarness(t,
		WithRunner(&fakeRunner{initSID: "sess-launch-target"}), WithCredentialSource(staticCred()))
	ctx := context.Background()

	dto, err := m.createRun(ctx, tenant, CreateRunParams{
		Transport: TransportStreamJSON, Isolation: IsolationNative,
		Actor: "user:u1", ActorKind: model.ActorUser,
		ProviderProfileRef: profile.Ref,
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}

	// The launch resolves the run's lineage from the identity it is claimed under,
	// and an identity carrying no workspace of its own resolves to the tenant
	// default, stored as an EXPLICIT id (managed_stop_lineage.go:70-80,:86-97).
	// That is the workspace a route would have authorized, so it is the one the
	// reader is confined to here.
	var workspace model.ID
	if err := st.View(ctx, tenant, func(sc store.Scope) error {
		def, err := sc.DefaultWorkspace(ctx)
		workspace = def.ID
		return err
	}); err != nil {
		t.Fatalf("default workspace: %v", err)
	}

	snap, err := readLaunchTarget(t, m, tenant, workspace, dto.RunRef)
	if err != nil {
		t.Fatalf("read a launched run: %v", err)
	}
	for field, want := range map[string]string{
		"RunRef":                 dto.RunRef,
		"ProviderProfileID":      dto.ProviderProfileRef,
		"ProviderDriver":         dto.ProviderDriver,
		"ProviderEnvironmentRef": dto.ProviderEnvironmentRef,
	} {
		if got := launchTargetField(t, snap, field); got != want {
			t.Errorf("%s = %q, want %q — the launch's own value", field, got, want)
		}
	}
	// And the launch did record a target, so the agreement above is not two
	// readings of the same emptiness.
	if dto.ProviderProfileRef != profile.Ref || dto.ProviderDriver != "claude" ||
		dto.ProviderEnvironmentRef != testEnvRef {
		t.Fatalf("the launch itself recorded no target: %+v", dto)
	}
}
