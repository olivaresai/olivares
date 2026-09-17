// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sqlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// This file is the whole QF1 initialized-fixture module: a closed two-class helper
// for tests whose subject BEGINS with an already initialized empty store, plus the
// focused tests that qualify it.
//
// Why it exists: openSQLiteTest runs a complete first initialization per test —
// migrations, guard rollout, boot conformance, audit-mode resolution — against a
// fresh :memory: database. For a test that only ever observes behavior AFTER that
// point, the initialization is repeated setup, not subject. This helper pays for it
// once per class, keeps the resulting CLOSED database file as private immutable
// bytes, and gives every caller its own private copy reopened through the same
// public Open.
//
// What it deliberately is NOT:
//
//   - Not a callback cache. The class is a closed enum, not a caller-supplied Config,
//     DSN, registration function or registry fingerprint. There is no test-name
//     heuristic, no automatic substitution and no environment switch: a test opts in
//     by an explicit, reviewed source change.
//   - Not a replacement for openSQLiteTest. Anything whose subject includes initial
//     creation, migrations, schema or guard transitions, corruption, registration
//     changes, engine configuration, connection pragmas, audit auto-selection,
//     connection lifetime or Open failure keeps the fresh helper.
//   - Not a live pool shared between tests. Only immutable bytes are shared.
//   - Not validated against a sample. The empty-template check is derived from the
//     store's own closed registry, so it covers every registered tenant table.
//
// The memory-to-file move is a real semantic difference (WAL behavior, persistence,
// reconnection), not an assumed equivalence — which is why eligibility was censused
// per test and why the guard controls below are exercised on the initialized path
// rather than inferred from a successful write.

// initializedSQLiteFixtureClass selects one of the two supported template shapes.
// It is closed on purpose: the set of shapes is a reviewed list, not an argument.
type initializedSQLiteFixtureClass uint8

const (
	// initializedSQLiteCore is Open with no extension registration (register nil).
	initializedSQLiteCore initializedSQLiteFixtureClass = iota + 1
	// initializedSQLiteWidget is Open with the exact registerWidget callback.
	initializedSQLiteWidget
)

// initializedSQLiteFixtureFile is the fixed base name of both the builder's template
// database and every per-test copy. Callers never choose it.
const initializedSQLiteFixtureFile = "initialized.db"

// initializedSQLiteFixturePerm is the mode a per-test copy is created with. The
// engine narrows its own files at Open (sqliteFilePerm); a fixture must not hand it
// a wider file in the first place.
const initializedSQLiteFixturePerm fs.FileMode = 0o600

// initializedSQLiteTenantTables returns the COMPLETE set of tenant-scoped tables the
// store's own closed registry declares: every registered descriptor table plus
// audit_events and audit_heads. It is the same set the boot self-test uses
// (registry.tenantTables), so the fixture cannot drift from the product.
//
// QF1-R1: this used to be a hand-written six-name sample. That sample omitted 49 of
// the 55 registered core tenant tables, so a template carrying a row in any of them
// — providers, sessions, api_tokens, audit_heads and the rest — was published as
// "empty". Deriving the set is the correction; a second list is exactly the defect.
func initializedSQLiteTenantTables(ss *sqlStore) []string { return ss.reg.tenantTables() }

// initializedSQLiteTemplateCache holds the immutable bytes of one class template, or
// the error that construction produced. It is a private result cache: sync.OnceValues
// gives exactly one build per process and repeats the same outcome to every later
// caller. It captures no testing.T, no caller context, no per-test directory and no
// live Store, pool or transaction.
type initializedSQLiteTemplateCache struct {
	once func() ([]byte, error)
}

// newInitializedSQLiteTemplateCache is the private constructor. It takes the build
// function so the focused tests below can exercise success, failure and at-most-once
// construction on their OWN cache instead of resetting a package-level one. It is not
// a caller configuration API: the two real caches are package-private variables and
// openInitializedSQLiteTest reaches them only through the closed class enum.
func newInitializedSQLiteTemplateCache(build func() ([]byte, error)) *initializedSQLiteTemplateCache {
	return &initializedSQLiteTemplateCache{once: sync.OnceValues(build)}
}

// copyInto writes a complete private copy of the template to path, creating it
// exclusively at initializedSQLiteFixturePerm. It never hands the caller a reference
// to the cached slice.
func (c *initializedSQLiteTemplateCache) copyInto(path string) error {
	b, err := c.once()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, initializedSQLiteFixturePerm)
	if err != nil {
		return fmt.Errorf("sqlstore: initialized fixture: create %s: %w", path, err)
	}
	if _, werr := f.Write(b); werr != nil {
		return errors.Join(fmt.Errorf("sqlstore: initialized fixture: write %s: %w", path, werr), f.Close())
	}
	if cerr := f.Close(); cerr != nil {
		return fmt.Errorf("sqlstore: initialized fixture: close %s: %w", path, cerr)
	}
	return nil
}

// digest returns the SHA-256 of the cached bytes as a value. It exists so a test can
// prove a copy is complete without receiving a writable reference to the template.
func (c *initializedSQLiteTemplateCache) digest() (string, error) {
	b, err := c.once()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

var (
	initializedSQLiteCoreTemplate = newInitializedSQLiteTemplateCache(func() ([]byte, error) {
		return buildInitializedSQLiteTemplate(nil, false)
	})
	initializedSQLiteWidgetTemplate = newInitializedSQLiteTemplateCache(func() ([]byte, error) {
		return buildInitializedSQLiteTemplate(registerWidget, true)
	})
)

// initializedSQLiteFixtureSpec is everything a class means: the registration callback
// Open receives and the template cache that class is served from.
type initializedSQLiteFixtureSpec struct {
	name     string
	register func(store.ExtensionRegistry) error
	template *initializedSQLiteTemplateCache
}

// initializedSQLiteFixtureSpecOf resolves a class. An unknown class is an error, not a
// fallback: a fixture that silently rebuilt a fresh store for an unrecognized class
// would hide exactly the defect this enum exists to prevent.
func initializedSQLiteFixtureSpecOf(class initializedSQLiteFixtureClass) (initializedSQLiteFixtureSpec, error) {
	switch class {
	case initializedSQLiteCore:
		return initializedSQLiteFixtureSpec{
			name: "core", register: nil, template: initializedSQLiteCoreTemplate,
		}, nil
	case initializedSQLiteWidget:
		return initializedSQLiteFixtureSpec{
			name: "widget", register: registerWidget, template: initializedSQLiteWidgetTemplate,
		}, nil
	default:
		return initializedSQLiteFixtureSpec{}, fmt.Errorf(
			"sqlstore: unknown initialized SQLite fixture class %d", uint8(class))
	}
}

// openInitializedSQLiteTest returns a store opened from a private copy of the class
// template: a fresh pool, a fresh file in this test's own TempDir, and the full
// current Open path including boot, readiness and guard verification. The store is
// closed at test end, before the directory holding it is removed.
//
// This is the only entry point eligible tests use.
func openInitializedSQLiteTest(t *testing.T, class initializedSQLiteFixtureClass) store.Store {
	t.Helper()
	st, _ := openInitializedSQLiteTestCopy(t, class)
	return st
}

// openInitializedSQLiteTestCopy is openInitializedSQLiteTest plus the path of the copy,
// for the focused tests that must inspect the file itself.
func openInitializedSQLiteTestCopy(t *testing.T, class initializedSQLiteFixtureClass) (store.Store, string) {
	t.Helper()
	return openInitializedSQLiteTestIn(t, class, t.TempDir())
}

// openInitializedSQLiteTestIn opens a copy inside a directory the CALLER owns.
//
// It is the seam that makes cleanup order observable: the directory exists before the
// Close cleanup is registered, so testing's LIFO cleanup runs Close first and removes
// the directory second. A test that registers its own cleanup between the two sees the
// store already closed and the directory still present — which is the observation, not
// an inference from another test's lifetime.
func openInitializedSQLiteTestIn(t *testing.T, class initializedSQLiteFixtureClass, dir string) (store.Store, string) {
	t.Helper()
	spec, err := initializedSQLiteFixtureSpecOf(class)
	if err != nil {
		t.Fatalf("initialized sqlite fixture: %v", err)
	}
	path := filepath.Join(dir, initializedSQLiteFixtureFile)
	if err := spec.template.copyInto(path); err != nil {
		t.Fatalf("initialized sqlite %s fixture: %v", spec.name, err)
	}
	st, err := Open(context.Background(), store.Config{
		Engine: store.EngineSQLite,
		DSN:    path,
		Debug:  true,
	}, spec.register)
	if err != nil {
		t.Fatalf("open initialized sqlite %s fixture: %v", spec.name, err)
	}
	// Registered immediately, before the caller can run an assertion that terminates
	// the test, and after the directory exists so Close precedes its removal.
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("close initialized sqlite %s fixture: %v", spec.name, err)
		}
	})
	return st, path
}

// buildInitializedSQLiteTemplate performs the mandatory closed-file procedure once per
// class and returns the bytes of the CLOSED database.
//
// The sequence is fixed: own a private directory, Open normally, verify the schema and
// that no tenant row exists, finalize and close, require a regular main file with no
// surviving WAL/SHM sidecar, read the closed file, finish cleanup, and only then
// publish. Close success alone is not the proof — sqlite3_close_v2 may defer final
// closure while resources remain, so the absence of the sidecars is what says the
// engine really finished. No checkpoint or backup framework is added, nothing is
// erased to make a check pass, and no live database bytes are read.
//
// On any failure it returns NO bytes and the attributable errors joined, including a
// cleanup failure. A failure never falls back to a fresh store.
func buildInitializedSQLiteTemplate(register func(store.ExtensionRegistry) error, wantWidgetTable bool) ([]byte, error) {
	dir, err := os.MkdirTemp("", "sqlstore-initialized-template-*")
	if err != nil {
		return nil, fmt.Errorf("sqlstore: initialized template: create builder directory: %w", err)
	}
	path := filepath.Join(dir, initializedSQLiteFixtureFile)

	st, err := Open(context.Background(), store.Config{
		Engine: store.EngineSQLite,
		DSN:    path,
		Debug:  true,
	}, register)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("sqlstore: initialized template: open: %w", err),
			removeInitializedSQLiteTemplateDir(dir))
	}
	return finalizeInitializedSQLiteTemplate(st, dir, path, wantWidgetTable)
}

// finalizeInitializedSQLiteTemplate is the tail of the build path: validate the open
// store, close it and check the error, then publish the closed file. It is a private
// seam of this test file, not a fixture caller API and not a production fault hook —
// it exists so a control can drive the REAL validation, close and cleanup over a
// store it opened normally and then dirtied on purpose.
func finalizeInitializedSQLiteTemplate(st store.Store, dir, path string, wantWidgetTable bool) ([]byte, error) {
	if verr := verifyInitializedSQLiteTemplate(st, wantWidgetTable); verr != nil {
		return nil, errors.Join(verr, closeInitializedSQLiteTemplate(st), removeInitializedSQLiteTemplateDir(dir))
	}
	if cerr := closeInitializedSQLiteTemplate(st); cerr != nil {
		return nil, errors.Join(cerr, removeInitializedSQLiteTemplateDir(dir))
	}
	return publishInitializedSQLiteTemplate(dir, path)
}

// verifyInitializedSQLiteTemplate checks the registered schema and that every
// tenant-scoped table the store's registry declares is empty, BEFORE the store is
// closed. The set is derived, never sampled (QF1-R1).
//
// Table names come from the closed registry, never from a caller, and each one is
// still put through the package's existing validIdent rule before it reaches a
// statement: that rule is precisely what makes a name safe to place in an identifier
// position without quoting, and it fails closed if the registry ever declares a name
// outside the plain lower-case shape.
//
// Every query here is a single-value QueryRow: it completes and releases the row it
// owns, so the builder leaves no Rows, statement, transaction or checked-out
// connection behind for Close to trip over. Nothing is deferred past Store.Close.
func verifyInitializedSQLiteTemplate(st store.Store, wantWidgetTable bool) error {
	ss, ok := st.(*sqlStore)
	if !ok {
		return fmt.Errorf("sqlstore: initialized template: store is %T, want *sqlStore", st)
	}
	ctx := context.Background()
	count := func(query string, args ...any) (int, error) {
		var n int
		if err := ss.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
			return 0, fmt.Errorf("sqlstore: initialized template: %q: %w", query, err)
		}
		return n, nil
	}
	const tableExists = "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?"

	// The migration ledger proves this template is past first initialization; a
	// template that reported zero applied migrations would be a fresh store wearing
	// the fixture's name.
	applied, err := count("SELECT COUNT(*) FROM schema_migrations_core")
	if err != nil {
		return err
	}
	if applied == 0 {
		return errors.New("sqlstore: initialized template: schema_migrations_core is empty, so this store is not initialized")
	}

	tables := initializedSQLiteTenantTables(ss)
	if len(tables) == 0 {
		return errors.New("sqlstore: initialized template: the store registry declares no tenant tables")
	}

	// Class discrimination is checked twice: what the registry declares, and what the
	// database physically has. Only the second sees a widget table on a core template,
	// because a table the registry does not declare is never reached by the loop below.
	widgetName, err := validIdent("extension table", widgetDescriptor.Table)
	if err != nil {
		return fmt.Errorf("sqlstore: initialized template: %w", err)
	}
	var errs []error
	if registered := slices.Contains(tables, widgetName); registered != wantWidgetTable {
		errs = append(errs, fmt.Errorf(
			"sqlstore: initialized template: registry declares %q = %v, want %v for this class",
			widgetName, registered, wantWidgetTable))
	}
	physical, err := count(tableExists, widgetName)
	if err != nil {
		return err
	}
	if (physical == 1) != wantWidgetTable {
		errs = append(errs, fmt.Errorf(
			"sqlstore: initialized template: database has %q = %v, want %v for this class",
			widgetName, physical == 1, wantWidgetTable))
	}

	for _, table := range tables {
		name, err := validIdent("tenant table", table)
		if err != nil {
			errs = append(errs, fmt.Errorf("sqlstore: initialized template: %w", err))
			continue
		}
		present, err := count(tableExists, name)
		if err != nil {
			return err
		}
		if present != 1 {
			errs = append(errs, fmt.Errorf("sqlstore: initialized template: registered tenant table %q missing", name))
			continue
		}
		rows, err := count("SELECT COUNT(*) FROM " + name)
		if err != nil {
			return err
		}
		if rows != 0 {
			errs = append(errs, fmt.Errorf(
				"sqlstore: initialized template: registered tenant table %q holds %d rows, want an empty template", name, rows))
		}
	}
	return errors.Join(errs...)
}

func closeInitializedSQLiteTemplate(st store.Store) error {
	if err := st.Close(); err != nil {
		return fmt.Errorf("sqlstore: initialized template: close: %w", err)
	}
	return nil
}

// requireClosedSQLiteFileSet is the absence-after-close policy: a regular main
// database and no surviving -wal or -shm. An unexpected sidecar and a failed
// inspection are both errors; neither is erased or healed.
func requireClosedSQLiteFileSet(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("sqlstore: initialized template: inspect closed database: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("sqlstore: initialized template: %s is not a regular file (mode %v)", path, info.Mode())
	}
	var errs []error
	for _, suffix := range []string{"-wal", "-shm"} {
		side := path + suffix
		_, serr := os.Stat(side)
		switch {
		case serr == nil:
			errs = append(errs, fmt.Errorf(
				"sqlstore: initialized template: %s survived Store.Close, so the engine did not finish; refusing to erase it", side))
		case !errors.Is(serr, fs.ErrNotExist):
			errs = append(errs, fmt.Errorf("sqlstore: initialized template: inspect %s: %w", side, serr))
		}
	}
	return errors.Join(errs...)
}

// publishInitializedSQLiteTemplate validates the closed file set, reads the main
// database, completes removal of the builder-owned directory and only then returns
// the bytes. Bytes are never returned alongside a non-nil error — including when the
// read succeeded and only the cleanup failed.
func publishInitializedSQLiteTemplate(dir, path string) ([]byte, error) {
	if err := requireClosedSQLiteFileSet(path); err != nil {
		return nil, errors.Join(err, removeInitializedSQLiteTemplateDir(dir))
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("sqlstore: initialized template: open closed database: %w", err),
			removeInitializedSQLiteTemplateDir(dir))
	}
	b, rerr := io.ReadAll(f)
	cerr := f.Close()
	if rerr != nil || cerr != nil {
		return nil, errors.Join(
			fmt.Errorf("sqlstore: initialized template: read closed database: %w", errors.Join(rerr, cerr)),
			removeInitializedSQLiteTemplateDir(dir))
	}
	if err := removeInitializedSQLiteTemplateDir(dir); err != nil {
		return nil, err
	}
	return b, nil
}

func removeInitializedSQLiteTemplateDir(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("sqlstore: initialized template: remove builder directory %s: %w", dir, err)
	}
	return nil
}

// --- focused helper tests -----------------------------------------------------
//
// These qualify the helper itself. Each one is written so that removing the
// invariant it names makes it fail: an unknown class that silently resolved, a
// cache that published bytes after a failure, a copy that was not complete, two
// callers sharing a pool or a file, a template that carried tenant rows, a store
// whose Debug never reached its repositories, a ledger the database no longer
// protects, or a directory removed before its store was closed.

func fixtureFileSHA256(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func fixtureCount(t *testing.T, st store.Store, query string, args ...any) int {
	t.Helper()
	ss, ok := st.(*sqlStore)
	if !ok {
		t.Fatalf("store is %T, want *sqlStore", st)
	}
	var n int
	if err := ss.db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%q: %v", query, err)
	}
	return n
}

// TestInitializedSQLiteFixtureClassIsClosed proves an unrecognized class is refused
// before any store work happens, and that the two known classes carry exactly the
// registration the contract names: nil, or registerWidget itself.
func TestInitializedSQLiteFixtureClassIsClosed(t *testing.T) {
	for _, class := range []initializedSQLiteFixtureClass{0, 3, 99, 255} {
		spec, err := initializedSQLiteFixtureSpecOf(class)
		if err == nil {
			t.Fatalf("class %d resolved to %+v, want an error", class, spec)
		}
		if !strings.Contains(err.Error(), "unknown initialized SQLite fixture class") {
			t.Fatalf("class %d: err = %v, want an unknown-class error", class, err)
		}
		if spec.template != nil || spec.register != nil {
			t.Fatalf("class %d returned a usable spec %+v alongside its error", class, spec)
		}
	}

	core, err := initializedSQLiteFixtureSpecOf(initializedSQLiteCore)
	if err != nil {
		t.Fatalf("core class: %v", err)
	}
	if core.register != nil {
		t.Fatalf("core class registers %p, want nil", core.register)
	}
	if core.template != initializedSQLiteCoreTemplate {
		t.Fatal("core class is not served from the core template cache")
	}

	widget, err := initializedSQLiteFixtureSpecOf(initializedSQLiteWidget)
	if err != nil {
		t.Fatalf("widget class: %v", err)
	}
	if widget.register == nil {
		t.Fatal("widget class registers nil, want registerWidget")
	}
	if got, want := reflect.ValueOf(widget.register).Pointer(), reflect.ValueOf(registerWidget).Pointer(); got != want {
		t.Fatalf("widget class registers a different callback (%#x), want registerWidget (%#x)", got, want)
	}
	if widget.template != initializedSQLiteWidgetTemplate {
		t.Fatal("widget class is not served from the widget template cache")
	}
}

// TestInitializedSQLiteTemplateCachePublishesNothingAfterBuildFailure proves a failed
// construction publishes no file and no digest, repeats its error instead of
// rebuilding, and never falls back to a fresh store.
func TestInitializedSQLiteTemplateCachePublishesNothingAfterBuildFailure(t *testing.T) {
	wantErr := errors.New("refused on purpose")
	var builds atomic.Int64
	// The build returns bytes AND an error: a cache that looked only at the bytes
	// would happily publish them.
	cache := newInitializedSQLiteTemplateCache(func() ([]byte, error) {
		builds.Add(1)
		return []byte("bytes that must never be published"), wantErr
	})

	path := filepath.Join(t.TempDir(), initializedSQLiteFixtureFile)
	if err := cache.copyInto(path); !errors.Is(err, wantErr) {
		t.Fatalf("copyInto after a failed build: err = %v, want %v", err, wantErr)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a failed build left %s behind (stat err = %v)", path, err)
	}
	if got, err := cache.digest(); !errors.Is(err, wantErr) || got != "" {
		t.Fatalf("digest after a failed build = %q, %v; want \"\", %v", got, err, wantErr)
	}
	if err := cache.copyInto(path); !errors.Is(err, wantErr) {
		t.Fatalf("second copyInto: err = %v, want the cached %v", err, wantErr)
	}
	if n := builds.Load(); n != 1 {
		t.Fatalf("builds = %d after three requests, want exactly 1 (a failure must not be retried silently)", n)
	}
}

// TestInitializedSQLiteTemplateRefusesSurvivingSidecar proves the absence-after-close
// policy: a surviving -wal or -shm is an error, the sidecar is not erased to make the
// check pass, and no bytes are published even though the main file was readable.
func TestInitializedSQLiteTemplateRefusesSurvivingSidecar(t *testing.T) {
	for _, suffix := range []string{"-wal", "-shm"} {
		t.Run(suffix, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, initializedSQLiteFixtureFile)
			if err := os.WriteFile(path, []byte("main database bytes"), initializedSQLiteFixturePerm); err != nil {
				t.Fatal(err)
			}
			side := path + suffix
			if err := os.WriteFile(side, []byte("sidecar"), initializedSQLiteFixturePerm); err != nil {
				t.Fatal(err)
			}
			if err := requireClosedSQLiteFileSet(path); err == nil || !strings.Contains(err.Error(), side) {
				t.Fatalf("requireClosedSQLiteFileSet with %s present: err = %v, want it named", side, err)
			}
			if _, err := os.Stat(side); err != nil {
				t.Fatalf("%s was erased to make the check pass: %v", side, err)
			}
			b, err := publishInitializedSQLiteTemplate(dir, path)
			if err == nil {
				t.Fatal("publish succeeded with a surviving sidecar")
			}
			if b != nil {
				t.Fatalf("publish returned %d bytes alongside its error", len(b))
			}
			if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("the builder directory survived a failed publish (stat err = %v)", err)
			}
		})
	}

	// A missing or non-regular main database is refused too.
	dir := t.TempDir()
	if err := requireClosedSQLiteFileSet(filepath.Join(dir, "absent.db")); err == nil {
		t.Fatal("a missing main database was accepted")
	}
	if err := requireClosedSQLiteFileSet(dir); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("a directory was accepted as the main database: err = %v", err)
	}
}

// TestInitializedSQLiteTemplatePublishesNothingWhenCleanupFails is the case that is
// easiest to get wrong: everything succeeded except the removal of the builder's own
// directory, so the bytes are in hand. They must still not be published.
func TestInitializedSQLiteTemplatePublishesNothingWhenCleanupFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("this control needs an unwritable parent directory, which root ignores")
	}
	parent := t.TempDir()
	dir := filepath.Join(parent, "builder")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, initializedSQLiteFixtureFile)
	if err := os.WriteFile(path, []byte("a perfectly readable template"), initializedSQLiteFixturePerm); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })

	b, err := publishInitializedSQLiteTemplate(dir, path)
	if err == nil {
		t.Fatal("publish succeeded although its directory could not be removed")
	}
	if b != nil {
		t.Fatalf("publish returned %d bytes alongside a cleanup failure", len(b))
	}
	if !strings.Contains(err.Error(), "remove builder directory") {
		t.Fatalf("err = %v, want the cleanup failure attributed", err)
	}
}

// TestInitializedSQLiteTemplateCacheBuildsOnceUnderConcurrency proves concurrent
// requests share one construction and one result. The build blocks until every
// caller has announced itself, so the contention window is real and not a sleep.
func TestInitializedSQLiteTemplateCacheBuildsOnceUnderConcurrency(t *testing.T) {
	const callers = 8
	payload := []byte("immutable template payload")
	sum := sha256.Sum256(payload)
	want := hex.EncodeToString(sum[:])

	var builds atomic.Int64
	var announced sync.WaitGroup
	announced.Add(callers)
	cache := newInitializedSQLiteTemplateCache(func() ([]byte, error) {
		builds.Add(1)
		announced.Wait()
		return payload, nil
	})

	digests := make([]string, callers)
	errs := make([]error, callers)
	var done sync.WaitGroup
	done.Add(callers)
	for i := range callers {
		go func() {
			defer done.Done()
			announced.Done()
			digests[i], errs[i] = cache.digest()
		}()
	}
	done.Wait()

	if n := builds.Load(); n != 1 {
		t.Fatalf("builds = %d for %d concurrent callers, want exactly 1", n, callers)
	}
	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if digests[i] != want {
			t.Fatalf("caller %d digest = %s, want %s", i, digests[i], want)
		}
	}
}

// TestInitializedSQLiteFixtureCopiesAreCompleteAndPrivate proves a copy is the whole
// template, is created exclusively at 0600, and that the cache hands out no writable
// reference: callers get files, the bytes stay inside.
func TestInitializedSQLiteFixtureCopiesAreCompleteAndPrivate(t *testing.T) {
	want, err := initializedSQLiteCoreTemplate.digest()
	if err != nil {
		t.Fatalf("core template: %v", err)
	}
	first := filepath.Join(t.TempDir(), initializedSQLiteFixtureFile)
	second := filepath.Join(t.TempDir(), initializedSQLiteFixtureFile)
	if err := initializedSQLiteCoreTemplate.copyInto(first); err != nil {
		t.Fatalf("first copy: %v", err)
	}
	if err := initializedSQLiteCoreTemplate.copyInto(second); err != nil {
		t.Fatalf("second copy: %v", err)
	}
	if got := fixtureFileSHA256(t, first); got != want {
		t.Fatalf("first copy sha256 = %s, want the template's %s", got, want)
	}
	if got := fixtureFileSHA256(t, second); got != want {
		t.Fatalf("second copy sha256 = %s, want the template's %s", got, want)
	}
	for _, path := range []string{first, second} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != initializedSQLiteFixturePerm {
			t.Fatalf("%s mode = %v, want %v", path, got, initializedSQLiteFixturePerm)
		}
	}
	// A copy never overwrites: the destination is created exclusively.
	if err := initializedSQLiteCoreTemplate.copyInto(first); err == nil {
		t.Fatal("copyInto overwrote an existing file")
	}

	widget, err := initializedSQLiteWidgetTemplate.digest()
	if err != nil {
		t.Fatalf("widget template: %v", err)
	}
	if widget == want {
		t.Fatal("the core and widget templates are byte-identical, so the class selects nothing")
	}
}

// TestInitializedSQLiteTemplateIsBuiltOncePerClass proves the cached template really
// is cached, using an oracle with discriminating power: two INDEPENDENT builds of the
// same class are not byte-identical (they record their own instants), so the equality
// of two served copies is evidence of a single construction rather than of
// determinism.
func TestInitializedSQLiteTemplateIsBuiltOncePerClass(t *testing.T) {
	want, err := initializedSQLiteCoreTemplate.digest()
	if err != nil {
		t.Fatalf("core template: %v", err)
	}
	again, err := initializedSQLiteCoreTemplate.digest()
	if err != nil {
		t.Fatalf("core template, second read: %v", err)
	}
	if again != want {
		t.Fatalf("the core cache returned two different templates: %s then %s", want, again)
	}

	independent, err := buildInitializedSQLiteTemplate(nil, false)
	if err != nil {
		t.Fatalf("independent core build: %v", err)
	}
	sum := sha256.Sum256(independent)
	if got := hex.EncodeToString(sum[:]); got == want {
		t.Fatal("an independently built core template is byte-identical to the cached one, " +
			"so equality between served copies no longer proves a single construction")
	}
}

// TestInitializedSQLiteFixtureBothClassesStartEmpty proves each class opens an
// initialized but EMPTY store, on its own file and its own pool, with the widget
// table present in exactly one of them.
func TestInitializedSQLiteFixtureBothClassesStartEmpty(t *testing.T) {
	core, corePath := openInitializedSQLiteTestCopy(t, initializedSQLiteCore)
	widget, widgetPath := openInitializedSQLiteTestCopy(t, initializedSQLiteWidget)

	if corePath == widgetPath || filepath.Dir(corePath) == filepath.Dir(widgetPath) {
		t.Fatalf("both fixtures landed in the same place: %s and %s", corePath, widgetPath)
	}
	if core.(*sqlStore).db == widget.(*sqlStore).db {
		t.Fatal("both fixtures share one pool")
	}

	for _, c := range []struct {
		name string
		st   store.Store
		path string
	}{{"core", core, corePath}, {"widget", widget, widgetPath}} {
		t.Run(c.name, func(t *testing.T) {
			info, err := os.Stat(c.path)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != initializedSQLiteFixturePerm {
				t.Fatalf("%s mode = %v, want %v", c.path, got, initializedSQLiteFixturePerm)
			}
			var attached string
			if err := c.st.(*sqlStore).db.QueryRowContext(context.Background(),
				"SELECT file FROM pragma_database_list WHERE name='main'").Scan(&attached); err != nil {
				t.Fatal(err)
			}
			if resolved, err := filepath.EvalSymlinks(attached); err != nil {
				t.Fatal(err)
			} else if wantPath, err := filepath.EvalSymlinks(c.path); err != nil {
				t.Fatal(err)
			} else if resolved != wantPath {
				t.Fatalf("attached main = %s, want this fixture's own %s", resolved, wantPath)
			}
			if applied := fixtureCount(t, c.st, "SELECT COUNT(*) FROM schema_migrations_core"); applied == 0 {
				t.Fatal("no migrations recorded: this store is not initialized")
			}
			// The COMPLETE registered set, not a sample (QF1-R1).
			tables := initializedSQLiteTenantTables(c.st.(*sqlStore))
			if !slices.Contains(tables, auditTable) || !slices.Contains(tables, auditHeadsTable) {
				t.Fatalf("%s class: the derived tenant-table set is missing the audit tables: %v", c.name, tables)
			}
			for _, table := range tables {
				if _, err := validIdent("tenant table", table); err != nil {
					t.Fatalf("%s class: %v", c.name, err)
				}
				if n := fixtureCount(t, c.st,
					"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table); n != 1 {
					t.Fatalf("%s class: registered tenant table %s present = %d, want 1", c.name, table, n)
				}
				if n := fixtureCount(t, c.st, "SELECT COUNT(*) FROM "+table); n != 0 {
					t.Fatalf("%s class: table %s holds %d rows in a fresh fixture", c.name, table, n)
				}
			}
			// Class discrimination, read from that same derived set.
			wantWidget := c.name == "widget"
			if got := slices.Contains(tables, widgetDescriptor.Table); got != wantWidget {
				t.Fatalf("%s class: registry has %s = %v, want %v",
					c.name, widgetDescriptor.Table, got, wantWidget)
			}
			gotTable := fixtureCount(t, c.st,
				"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", widgetDescriptor.Table) == 1
			if gotTable != wantWidget {
				t.Fatalf("%s class: %s table present = %v, want %v", c.name, widgetDescriptor.Table, gotTable, wantWidget)
			}
		})
	}
}

// TestInitializedSQLiteFixtureCopiesDoNotLeak proves two copies of the same class are
// independent databases. For the widget class it goes past tenant scoping and asks
// copy B's extension TABLE directly for copy A's row: an ordinary scoped miss would
// also be produced by a correct tenant predicate over a shared table, and that is not
// what this must establish.
func TestInitializedSQLiteFixtureCopiesDoNotLeak(t *testing.T) {
	ctx := context.Background()

	t.Run("core", func(t *testing.T) {
		a, pathA := openInitializedSQLiteTestCopy(t, initializedSQLiteCore)
		b, pathB := openInitializedSQLiteTestCopy(t, initializedSQLiteCore)
		if pathA == pathB {
			t.Fatalf("both copies share the file %s", pathA)
		}
		tenantA := provisionTenant(t, a, "leak-a")
		agent := mustCreateAgent(t, a, tenantA, "only-in-a")

		if n := fixtureCount(t, b, "SELECT COUNT(*) FROM orgs WHERE id = ?", tenantA.String()); n != 0 {
			t.Fatalf("copy B holds %d rows for copy A's tenant", n)
		}
		if n := fixtureCount(t, b, "SELECT COUNT(*) FROM agents WHERE id = ?", agent.ID.String()); n != 0 {
			t.Fatalf("copy B holds %d rows for copy A's agent", n)
		}
		if n := fixtureCount(t, b, "SELECT COUNT(*) FROM agents"); n != 0 {
			t.Fatalf("copy B holds %d agent rows, want an untouched copy", n)
		}
		// Positive counterpart: the row really was written in copy A.
		if n := fixtureCount(t, a, "SELECT COUNT(*) FROM agents WHERE id = ?", agent.ID.String()); n != 1 {
			t.Fatalf("copy A holds %d rows for its own agent, want 1", n)
		}
	})

	t.Run("widget", func(t *testing.T) {
		a, _ := openInitializedSQLiteTestCopy(t, initializedSQLiteWidget)
		b, _ := openInitializedSQLiteTestCopy(t, initializedSQLiteWidget)
		tenantA := provisionTenant(t, a, "widget-leak-a")
		tenantB := provisionTenant(t, b, "widget-leak-b")

		var widgetID model.ID
		if err := a.Mutate(ctx, tenantA, func(sc store.Scope) error {
			repo, err := sc.Ext("rrw.widget")
			if err != nil {
				return err
			}
			rec, err := repo.Create(ctx, model.Record{"label": "only-in-a", "count": int64(1)})
			if err != nil {
				return err
			}
			widgetID = model.ID(rec.String("id"))
			return nil
		}); err != nil {
			t.Fatalf("create widget in copy A: %v", err)
		}
		if widgetID == "" {
			t.Fatal("copy A produced no widget id")
		}

		// The direct question: does copy B's own table hold that row at all?
		if n := fixtureCount(t, b,
			"SELECT COUNT(*) FROM "+widgetDescriptor.Table+" WHERE id = ?", widgetID.String()); n != 0 {
			t.Fatalf("copy B's %s table holds %d rows for copy A's widget", widgetDescriptor.Table, n)
		}
		if n := fixtureCount(t, b, "SELECT COUNT(*) FROM "+widgetDescriptor.Table); n != 0 {
			t.Fatalf("copy B's %s table holds %d rows, want an untouched copy", widgetDescriptor.Table, n)
		}
		if n := fixtureCount(t, b, "SELECT COUNT(*) FROM orgs WHERE id = ?", tenantA.String()); n != 0 {
			t.Fatalf("copy B holds %d rows for copy A's tenant", n)
		}
		// Ordinary tenant-scoped isolation, on copy B's own tenant.
		if err := b.View(ctx, tenantB, func(sc store.Scope) error {
			repo, err := sc.Ext("rrw.widget")
			if err != nil {
				return err
			}
			if _, err := repo.Get(ctx, widgetID); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("copy B Get(copy A widget): err = %v, want ErrNotFound", err)
			}
			return nil
		}); err != nil {
			t.Fatalf("copy B view: %v", err)
		}
		// Positive counterpart: copy A can still read its own row.
		if err := a.View(ctx, tenantA, func(sc store.Scope) error {
			repo, err := sc.Ext("rrw.widget")
			if err != nil {
				return err
			}
			rec, err := repo.Get(ctx, widgetID)
			if err != nil {
				return err
			}
			if got := rec.String("label"); got != "only-in-a" {
				t.Fatalf("copy A widget label = %q, want only-in-a", got)
			}
			return nil
		}); err != nil {
			t.Fatalf("copy A view: %v", err)
		}
	})
}

// TestInitializedSQLiteFixtureDebugGuardReachesRepositories proves Debug=true survives
// the template round trip all the way into a real generic repository, by requiring the
// exact panic its guard raises. A boolean field or an ErrReadOnly would not prove the
// guard ran.
func TestInitializedSQLiteFixtureDebugGuardReachesRepositories(t *testing.T) {
	const wantPrefix = "sqlstore: tenant-table statement without tenant predicate: "
	for _, c := range []struct {
		name  string
		class initializedSQLiteFixtureClass
	}{{"core", initializedSQLiteCore}, {"widget", initializedSQLiteWidget}} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			st := openInitializedSQLiteTest(t, c.class)
			tenant := provisionTenant(t, st, "guard-"+c.name)

			// Positive counterpart: correctly scoped work succeeds on this store, so
			// the panic below is the guard firing, not a broken fixture.
			agent := mustCreateAgent(t, st, tenant, "guard-positive")
			if n := fixtureCount(t, st, "SELECT COUNT(*) FROM agents WHERE id = ?", agent.ID.String()); n != 1 {
				t.Fatalf("scoped create left %d rows, want 1", n)
			}

			var recovered any
			if err := st.View(ctx, tenant, func(sc store.Scope) error {
				ts, ok := sc.(*tenantScope)
				if !ok {
					return fmt.Errorf("scope is %T, want *tenantScope", sc)
				}
				repo := ts.repo(agentDescriptor)
				func() {
					defer func() { recovered = recover() }()
					repo.guard("SELECT id FROM agents")
				}()
				return nil
			}); err != nil {
				t.Fatalf("view: %v", err)
			}
			msg, ok := recovered.(string)
			if !ok {
				t.Fatalf("the generic repository did not panic on an unscoped statement (recovered %#v); "+
					"Debug did not reach it through the initialized fixture", recovered)
			}
			if !strings.HasPrefix(msg, wantPrefix) {
				t.Fatalf("panic = %q, want the prefix %q", msg, wantPrefix)
			}
		})
	}
}

// TestInitializedSQLiteFixtureAuditLedgerStaysAppendOnly proves the DATABASE still
// refuses to rewrite the ledger on a store restored from template bytes. This is the
// trigger, not the repository: it is exercised through a real bound tenant
// transaction, and the original row must survive the attempt.
func TestInitializedSQLiteFixtureAuditLedgerStaysAppendOnly(t *testing.T) {
	for _, c := range []struct {
		name  string
		class initializedSQLiteFixtureClass
	}{{"core", initializedSQLiteCore}, {"widget", initializedSQLiteWidget}} {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			st := openInitializedSQLiteTest(t, c.class)
			tenant := provisionTenant(t, st, "appendonly-"+c.name)
			// A legitimate scoped write succeeds on this fixture, so a later refusal
			// is the ledger guard and not a store that accepts nothing.
			agent := mustCreateAgent(t, st, tenant, "writable-control")
			if n := fixtureCount(t, st, "SELECT COUNT(*) FROM agents WHERE id = ?", agent.ID.String()); n != 1 {
				t.Fatalf("scoped create left %d agent rows, want 1", n)
			}
			appendN(t, st, tenant, 1)

			before := fixtureCount(t, st, "SELECT COUNT(*) FROM audit_events WHERE tenant_id = ?", tenant.String())
			if before == 0 {
				t.Fatal("no audit events to protect")
			}

			ss := st.(*sqlStore)
			tx, err := ss.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := ss.dia.BindTenant(ctx, tx, tenant); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			_, updErr := tx.ExecContext(ctx, "UPDATE audit_events SET action='x' WHERE tenant_id=?", tenant.String())
			_, delErr := tx.ExecContext(ctx, "DELETE FROM audit_events WHERE tenant_id=?", tenant.String())
			if err := tx.Rollback(); err != nil {
				t.Fatalf("rollback: %v", err)
			}
			if updErr == nil || !strings.Contains(strings.ToLower(updErr.Error()), "append-only") {
				t.Fatalf("update audit: err = %v, want append-only", updErr)
			}
			if delErr == nil || !strings.Contains(strings.ToLower(delErr.Error()), "append-only") {
				t.Fatalf("delete audit: err = %v, want append-only", delErr)
			}

			if after := fixtureCount(t, st, "SELECT COUNT(*) FROM audit_events WHERE tenant_id = ?", tenant.String()); after != before {
				t.Fatalf("audit rows = %d after the refused statements, want the original %d", after, before)
			}
			if n := fixtureCount(t, st, "SELECT COUNT(*) FROM audit_events WHERE tenant_id = ? AND action = 'x'", tenant.String()); n != 0 {
				t.Fatalf("%d audit rows were rewritten", n)
			}
			// The ledger is append-ONLY, not frozen: the direction the product uses
			// still works afterwards. Without this, "UPDATE and DELETE failed" would
			// also describe a store that had stopped accepting the ledger entirely.
			appendN(t, st, tenant, 1)
			if after := fixtureCount(t, st, "SELECT COUNT(*) FROM audit_events WHERE tenant_id = ?", tenant.String()); after != before+1 {
				t.Fatalf("audit rows = %d after a legitimate append, want %d", after, before+1)
			}
		})
	}
}

// TestInitializedSQLiteFixtureConcurrentCallersGetDistinctStores proves concurrent
// callers of the real helper receive their own file and their own pool, and that all
// of them are served from a single template construction.
func TestInitializedSQLiteFixtureConcurrentCallersGetDistinctStores(t *testing.T) {
	const callers = 4
	want, err := initializedSQLiteCoreTemplate.digest()
	if err != nil {
		t.Fatalf("core template: %v", err)
	}

	type served struct {
		path   string
		pool   *sql.DB
		digest string
	}
	var mu sync.Mutex
	seen := make([]served, 0, callers)

	t.Run("callers", func(t *testing.T) {
		for i := range callers {
			t.Run(fmt.Sprintf("caller-%d", i), func(t *testing.T) {
				t.Parallel()
				// A copy taken before Open, so its digest is the served template and
				// not a database that has since been reopened.
				sample := filepath.Join(t.TempDir(), initializedSQLiteFixtureFile)
				if err := initializedSQLiteCoreTemplate.copyInto(sample); err != nil {
					t.Fatalf("sample copy: %v", err)
				}
				st, path := openInitializedSQLiteTestCopy(t, initializedSQLiteCore)
				tenant := provisionTenant(t, st, fmt.Sprintf("concurrent-%d", i))
				mustCreateAgent(t, st, tenant, "own")
				if n := fixtureCount(t, st, "SELECT COUNT(*) FROM agents"); n != 1 {
					t.Fatalf("caller %d sees %d agents, want only its own", i, n)
				}
				mu.Lock()
				seen = append(seen, served{path: path, pool: st.(*sqlStore).db, digest: fixtureFileSHA256(t, sample)})
				mu.Unlock()
			})
		}
	})

	if len(seen) != callers {
		t.Fatalf("%d callers reported, want %d", len(seen), callers)
	}
	paths := make(map[string]bool, callers)
	pools := make(map[*sql.DB]bool, callers)
	for _, s := range seen {
		if paths[s.path] {
			t.Fatalf("two callers shared the file %s", s.path)
		}
		if pools[s.pool] {
			t.Fatalf("two callers shared a pool (%p)", s.pool)
		}
		if s.digest != want {
			t.Fatalf("a concurrent caller was served %s, want the single construction %s", s.digest, want)
		}
		paths[s.path] = true
		pools[s.pool] = true
	}
}

// TestInitializedSQLiteFixtureClosesStoreBeforeRemovingItsDirectory observes the order
// directly. The cleanup registered here sits between the directory's own removal
// (registered first, by TempDir) and the fixture's Close (registered last, by the
// helper), so testing's LIFO order runs it exactly in between: the store must already
// be closed and its directory must still exist.
func TestInitializedSQLiteFixtureClosesStoreBeforeRemovingItsDirectory(t *testing.T) {
	var (
		observed    bool
		dirPresent  bool
		filePresent bool
		pingErr     error
		dir         string
		path        string
	)
	t.Run("fixture", func(t *testing.T) {
		dir = t.TempDir()
		var opened store.Store
		t.Cleanup(func() {
			observed = true
			_, err := os.Stat(dir)
			dirPresent = err == nil
			_, err = os.Stat(path)
			filePresent = err == nil
			if opened != nil {
				pingErr = opened.Ping(context.Background())
			}
		})
		st, p := openInitializedSQLiteTestIn(t, initializedSQLiteCore, dir)
		opened, path = st, p
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("fixture file missing while the test runs: %v", err)
		}
	})

	if !observed {
		t.Fatal("the ordering observation never ran")
	}
	if pingErr == nil {
		t.Fatal("the store was still usable when its directory was about to be removed: Close did not run first")
	}
	if !dirPresent || !filePresent {
		t.Fatalf("the fixture directory was already gone when its store closed (dir=%v file=%v)", dirPresent, filePresent)
	}
	if _, err := os.Stat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the fixture directory survived the test (stat err = %v)", err)
	}
}

// openInitializedSQLiteTemplateUnderTest opens a store exactly as the builder does —
// public Open, the fixed class configuration, a builder-owned private directory — and
// hands back the pieces the finalization seam takes. The store is deliberately NOT
// closed here: the seam owns that, which is the behavior under test.
func openInitializedSQLiteTemplateUnderTest(t *testing.T, register func(store.ExtensionRegistry) error) (store.Store, string, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "sqlstore-initialized-control-*")
	if err != nil {
		t.Fatal(err)
	}
	// Defensive only: the seam is expected to remove this directory itself, and the
	// test asserts that it did. This keeps a failed run from leaking one.
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, initializedSQLiteFixtureFile)
	st, err := Open(context.Background(), store.Config{
		Engine: store.EngineSQLite,
		DSN:    path,
		Debug:  true,
	}, register)
	if err != nil {
		t.Fatalf("open control store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, dir, path
}

// nonEmptyRegisteredTenantTables reports which of the store's registered tenant
// tables actually hold rows. It is derived from the same registry the validator uses,
// so the control can state what it dirtied without keeping a list of its own.
func nonEmptyRegisteredTenantTables(t *testing.T, st store.Store) []string {
	t.Helper()
	var out []string
	for _, table := range initializedSQLiteTenantTables(st.(*sqlStore)) {
		if _, err := validIdent("tenant table", table); err != nil {
			t.Fatal(err)
		}
		if n := fixtureCount(t, st, "SELECT COUNT(*) FROM "+table); n != 0 {
			out = append(out, table)
		}
	}
	slices.Sort(out)
	return out
}

// TestInitializedSQLiteTemplateRefusesARowInAnyRegisteredTenantTable is the QF1-R1
// control.
//
// It puts one REAL row, written through the product's own repository path, into
// `providers` — a registered tenant table that the superseded six-name sample did not
// name — and requires the actual build-tail path to refuse it. Because the row lands
// in exactly one relation and the control derives that fact from the registry rather
// than from a list of its own, a validator that checks only a subset cannot see it:
// running this test against the superseded validator is what produces the historical
// red recorded in the correction packet.
//
// The same case carries the three outcomes construction revision 2 requires of a
// validation failure: no bytes are published, the owned Store is closed, and the
// owned directory is removed.
func TestInitializedSQLiteTemplateRefusesARowInAnyRegisteredTenantTable(t *testing.T) {
	ctx := context.Background()

	// Positive counterpart: the identical seam over a clean store publishes bytes and
	// completes its cleanup, so the refusal below is caused by the row and not by the
	// seam itself.
	cleanStore, cleanDir, cleanPath := openInitializedSQLiteTemplateUnderTest(t, nil)
	published, err := finalizeInitializedSQLiteTemplate(cleanStore, cleanDir, cleanPath, false)
	if err != nil {
		t.Fatalf("a clean store failed the build tail: %v", err)
	}
	if len(published) == 0 {
		t.Fatal("a clean store published no bytes")
	}
	if _, serr := os.Stat(cleanDir); !errors.Is(serr, fs.ErrNotExist) {
		t.Fatalf("the clean run left its builder directory behind (stat err = %v)", serr)
	}

	st, dir, path := openInitializedSQLiteTemplateUnderTest(t, nil)
	tenant := model.TenantID(model.NewID())
	if err := st.Mutate(ctx, tenant, func(sc store.Scope) error {
		_, cerr := sc.Providers().Create(ctx, model.Provider{
			Name: "qf1-r1-omitted-relation", Kind: "openai", Status: model.StatusActive,
		})
		return cerr
	}); err != nil {
		t.Fatalf("seed a row in a registered tenant table: %v", err)
	}

	dirty := nonEmptyRegisteredTenantTables(t, st)
	if !slices.Equal(dirty, []string{providerDescriptor.Table}) {
		t.Fatalf("the control dirtied %v, want exactly [%s]: the case only discriminates "+
			"a complete check from a partial one while a single relation carries the row",
			dirty, providerDescriptor.Table)
	}

	bytes, err := finalizeInitializedSQLiteTemplate(st, dir, path, false)
	if err == nil {
		t.Fatalf("a template holding a real row in %s was published as an empty template", providerDescriptor.Table)
	}
	if bytes != nil {
		t.Fatalf("published %d bytes alongside a validation failure", len(bytes))
	}
	if !strings.Contains(err.Error(), providerDescriptor.Table) {
		t.Fatalf("err = %v, want the offending relation %s named", err, providerDescriptor.Table)
	}
	if perr := st.Ping(ctx); perr == nil {
		t.Fatal("the owned store was left open after a validation failure")
	}
	if _, serr := os.Stat(dir); !errors.Is(serr, fs.ErrNotExist) {
		t.Fatalf("the owned builder directory survived a validation failure (stat err = %v)", serr)
	}
}

// TestInitializedSQLiteTemplateRefusesAWidgetTableOnTheCoreClass proves the physical
// class guard, which the registry comparison alone cannot supply: the core registry
// never declares the widget table, so the derived-set loop never inspects it.
//
// A normally opened core store is given that table, then driven through the real
// finalization seam. The refusal must publish nothing and still close the owned Store
// and remove the owned directory.
func TestInitializedSQLiteTemplateRefusesAWidgetTableOnTheCoreClass(t *testing.T) {
	ctx := context.Background()
	st, dir, path := openInitializedSQLiteTemplateUnderTest(t, nil)

	name, err := validIdent("extension table", widgetDescriptor.Table)
	if err != nil {
		t.Fatal(err)
	}
	ss := st.(*sqlStore)
	if slices.Contains(initializedSQLiteTenantTables(ss), name) {
		t.Fatalf("the core registry declares %s, so this case would not isolate the physical guard", name)
	}
	if _, err := ss.db.ExecContext(ctx, "CREATE TABLE "+name+" (id TEXT PRIMARY KEY)"); err != nil {
		t.Fatalf("create the unexpected %s table: %v", name, err)
	}
	if n := fixtureCount(t, st,
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name); n != 1 {
		t.Fatalf("the unexpected %s table was not created (present = %d)", name, n)
	}

	published, err := finalizeInitializedSQLiteTemplate(st, dir, path, false)
	if err == nil {
		t.Fatalf("a core template carrying a physical %s table was published", name)
	}
	if published != nil {
		t.Fatalf("published %d bytes alongside the validation failure", len(published))
	}
	if !strings.Contains(err.Error(), name) {
		t.Fatalf("err = %v, want %s named", err, name)
	}
	if perr := st.Ping(ctx); perr == nil {
		t.Fatal("the owned store was left open after a validation failure")
	}
	if _, serr := os.Stat(dir); !errors.Is(serr, fs.ErrNotExist) {
		t.Fatalf("the owned builder directory survived a validation failure (stat err = %v)", serr)
	}
}
