// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/dr/opgate"
	"github.com/olivaresai/olivares/core/store"
)

// aliasFixture builds a real database with a marker row, and returns its canonical
// path plus the aliases that address the very same file.
//
// The marker is what makes the identity assertions mean something: without it a
// second, EMPTY database opened at a different path would satisfy "Open succeeded"
// just as well as the real target, and the finding would be unfalsifiable.
func aliasFixture(t *testing.T) (canonical string, aliases map[string]string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// A parent symlink AND a final symlink, so both kinds of alias are covered.
	actual := filepath.Join(dir, "actual")
	if err := os.Mkdir(actual, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(actual, filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	canonical = filepath.Join(actual, "real db.sqlite")

	st, err := openSQLiteDestination(t, canonical)
	if err != nil {
		t.Fatalf("create the destination: %v", err)
	}
	if _, err := st.(*sqlStore).db.Exec("CREATE TABLE alias_marker (token TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.(*sqlStore).db.Exec("INSERT INTO alias_marker VALUES ('owned-target')"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(actual, "alias.sqlite")
	if err := os.Symlink(canonical, link); err != nil {
		t.Fatal(err)
	}
	// A symlink whose target has a DIFFERENT PARENT than the link itself, so that
	// `hop/..` means one directory to the kernel and another to a lexical reader.
	//
	// ⛔ THE `dot-segments` ROW BELOW DOES NOT MEASURE THAT, and the independent
	// review said so: filepath.Join cleans, so `Join(actual, "..", "actual", …)` is
	// already `actual/…` before the resolver ever sees it. It is kept because a dot
	// segment that survives no symlink is still a spelling that must land on the
	// same identity — but the causal row is the one built by concatenation below.
	if err := os.Mkdir(filepath.Join(actual, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(actual, "nested"), filepath.Join(dir, "hop")); err != nil {
		t.Fatal(err)
	}
	return canonical, map[string]string{
		"canonical":      canonical,
		"final-symlink":  link,
		"parent-symlink": filepath.Join(dir, "linked", "real db.sqlite"),
		"encoded-uri":    "file:" + strings.ReplaceAll(canonical, " ", "%20"),
		"dot-segments":   filepath.Join(actual, "..", "actual", "real db.sqlite"),
		// dir/hop -> dir/actual/nested, so `..` applied to the RESOLVED link is
		// dir/actual and this names the canonical file. A resolver that cleaned the
		// text first would answer dir/real db.sqlite, which is a different file —
		// one nobody holds, and one that does not carry the marker row.
		"dot-segment-through-symlink": dir + "/hop/../real db.sqlite",
	}
}

// ⛔ F3-IR-2, MEASURED AGAINST A REAL SECOND PROCESS.
//
// The independent review created a real database, had a SEPARATE PROCESS hold its
// canonical anchor exclusively, confirmed the same-name Open refused, and then
// watched Open SUCCEED through a final symlink and through
// `file:.../real%20db.sqlite`. Both aliases opened the very same inode: the URI case
// was corroborated by reading the existing marker row, by PRAGMA database_list and
// by os.SameFile.
//
// This is that case, retargeted to this correction and widened to the alias classes
// the ratified resolution contract names.
func TestSQLiteAliasesCannotEscapeTheExternalFence(t *testing.T) {
	canonical, aliases := aliasFixture(t)
	stop := holdDestinationInAnotherProcess(t, canonical)
	defer stop()

	for name, dsn := range aliases {
		t.Run(name, func(t *testing.T) {
			st, err := openSQLiteDestination(t, dsn)
			if st != nil {
				_ = st.Close()
			}
			if err == nil {
				t.Fatalf("the %s alias PUBLISHED a store for a database a separate process holds exclusively (dsn %q)", name, dsn)
			}
			if !errors.Is(err, ErrRestorePublicationBusy) {
				t.Fatalf("the %s alias was refused with the wrong classification: %v", name, err)
			}
		})
	}

	// NO NEW BYTES. A refusal that had already created the WAL and SHM sidecars would
	// have touched the very file the holder is protecting.
	for _, sidecar := range []string{canonical + "-wal", canonical + "-shm"} {
		if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
			t.Fatalf("the refused aliases left %s behind: %v", sidecar, err)
		}
	}

	// THE POSITIVE, WITHOUT WHICH EVERY REFUSAL ABOVE WOULD BE SATISFIED BY A FENCE
	// THAT REFUSED EVERYTHING: once the holder lets go, each alias opens and each one
	// finds the SAME marker-bearing database.
	stop()
	for name, dsn := range aliases {
		t.Run("unlocked/"+name, func(t *testing.T) {
			st, err := openSQLiteDestination(t, dsn)
			if err != nil {
				t.Fatalf("after the holder released, the %s alias was still refused: %v", name, err)
			}
			assertOpenedTheMarkedDatabase(t, st, canonical)
			_ = st.Close()
		})
	}
}

// assertOpenedTheMarkedDatabase proves the store really is on the fixture's file:
// the marker row it wrote, the driver's own idea of its main database path, and the
// inode behind that path.
//
// The oracle is the DRIVER and the FILESYSTEM, never equality with the resolver's
// own output — a resolver compared against itself would agree with any bug it has.
func assertOpenedTheMarkedDatabase(t *testing.T, st store.Store, canonical string) {
	t.Helper()
	db := st.(*sqlStore).db
	var token string
	if err := db.QueryRow("SELECT token FROM alias_marker").Scan(&token); err != nil || token != "owned-target" {
		t.Fatalf("this store is not the marked destination: token=%q err=%v", token, err)
	}
	var seq int
	var name, actual string
	if err := db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &actual); err != nil {
		t.Fatal(err)
	}
	want, err := os.Stat(canonical)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.Stat(actual)
	if err != nil {
		t.Fatalf("the driver reports main database %q, which cannot be inspected: %v", actual, err)
	}
	if !os.SameFile(want, got) {
		t.Fatalf("the driver opened %q, which is not the same inode as %q", actual, canonical)
	}
}

// ⛔ F3-IR-9: A BUSY DESTINATION MUST NOT BE CREATED BY THE REFUSAL ITSELF.
//
// The independent review started a separate exclusive holder on a NOT-YET-CREATED
// target, confirmed the target was absent, and called ordinary Open. Open correctly
// returned busy — and had already created a 4096-byte SQLite file, because openSQLite
// forces its first connection before the fence was reached.
func TestSQLiteBusyMustNotCreateTheDestination(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "not-yet.db")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the fixture's target already exists: %v", err)
	}
	stop := holdDestinationInAnotherProcess(t, path)
	defer stop()

	st, err := openSQLiteDestination(t, path)
	if st != nil {
		_ = st.Close()
	}
	if err == nil {
		t.Fatal("Open PUBLISHED a store for a destination another process holds exclusively")
	}
	if !errors.Is(err, ErrRestorePublicationBusy) {
		t.Fatalf("wrong classification: %v", err)
	}
	// THE ASSERTION THAT FAILED BEFORE: not merely that Open refused, but that it
	// refused with NOTHING CREATED.
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		info, serr := os.Stat(p)
		if serr == nil {
			t.Fatalf("the refused Open created %s (%d bytes) before reaching its fence", p, info.Size())
		}
		if !os.IsNotExist(serr) {
			t.Fatal(serr)
		}
	}

	// And the control for it: once nobody holds the destination, the same Open creates
	// and publishes it normally.
	stop()
	st, err = openSQLiteDestination(t, path)
	if err != nil {
		t.Fatalf("after the holder released, an ordinary Open of the same absent target was refused: %v", err)
	}
	_ = st.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the accepted Open did not create the destination: %v", err)
	}
}

// The file-mode restriction has to reach the CANONICAL database, not a lookalike
// path spelled the way the caller happened to type it.
//
// The old restrictSQLiteFiles cut `file:` off and stopped at the first `?` without
// decoding, so for `file:.../real%20db.sqlite` it would have chmodded a file
// literally named `real%20db.sqlite` — which does not exist — and left the real
// database at whatever the umask gave it.
func TestTheModeRestrictionReachesTheCanonicalDatabase(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	canonical := filepath.Join(dir, "real db.sqlite")
	uri := "file:" + strings.ReplaceAll(canonical, " ", "%20")

	st, err := openSQLiteDestination(t, uri)
	if err != nil {
		t.Fatalf("open through the encoded URI: %v", err)
	}
	assertNamedDatabase(t, st, canonical)
	_ = st.Close()

	info, err := os.Stat(canonical)
	if err != nil {
		t.Fatalf("the encoded URI did not create the decoded file: %v", err)
	}
	if info.Mode().Perm() != sqliteFilePerm {
		t.Fatalf("the canonical database is %#o, not %#o: the restriction went somewhere else", info.Mode().Perm(), sqliteFilePerm)
	}
	if _, err := os.Stat(filepath.Join(dir, "real%20db.sqlite")); !os.IsNotExist(err) {
		t.Fatalf("a lookalike %%20 filename exists, so the URI was not decoded: %v", err)
	}

	// A pre-existing lax mode is narrowed on the file the driver really opens.
	if err := os.Chmod(canonical, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err = openSQLiteDestination(t, uri)
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	info, err = os.Stat(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != sqliteFilePerm {
		t.Fatalf("a lax mode on the canonical database survived Open: %#o", info.Mode().Perm())
	}
}

func assertNamedDatabase(t *testing.T, st store.Store, canonical string) {
	t.Helper()
	var seq int
	var name, actual string
	if err := st.(*sqlStore).db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &actual); err != nil {
		t.Fatal(err)
	}
	want, err := os.Stat(canonical)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.Stat(actual)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(want, got) {
		t.Fatalf("the driver opened %q rather than %q", actual, canonical)
	}
}

// ⛔ THE DRIVER-BEHAVIOR CONTROL BEHIND THE MEMORY CLASSIFICATION.
//
// The store used to classify `mode=memory` by substring. This case measures the
// PINNED DRIVER rather than trusting the reading of its source: in the plain-name
// grammar the query never reaches SQLite, so `<path>?mode=memory` is an ordinary
// file — a row written through it survives a close and a reopen of the plain path.
// If the driver ever changed that, this fails and the classification is re-derived.
func TestThePinnedDriverTreatsPlainModeMemoryAsAFile(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "looks-like-memory.db")
	db, err := sql.Open("sqlite", path+"?mode=memory")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('durable')"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the pinned driver created no file for %q, so the plain-name grammar has changed: %v", path+"?mode=memory", err)
	}
	again, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close() //nolint:errcheck // teardown
	var v string
	if err := again.QueryRow("SELECT v FROM t").Scan(&v); err != nil || v != "durable" {
		t.Fatalf("the row did not survive: v=%q err=%v", v, err)
	}

	// And the resolver agrees with the driver, which is the whole point.
	target, err := opgate.ResolveSQLiteTarget(path + "?mode=memory")
	if err != nil {
		t.Fatal(err)
	}
	if target.Memory() {
		t.Fatal("the resolver classified a plain-name mode=memory DSN as in-memory while the driver wrote a durable file")
	}
}

// An unprovable destination is UNKNOWN coordination and a refusal. It used to be an
// empty anchor, which meant an unfenced Open with no diagnosis at all.
func TestAnUnprovableSQLiteDestinationRefusesRatherThanOpeningUnfenced(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-dir", "x.db")
	st, err := Open(context.Background(), store.Config{Engine: store.EngineSQLite, DSN: missing}, nil)
	if st != nil {
		_ = st.Close()
	}
	if err == nil {
		t.Fatal("a destination whose parent directory does not exist was opened without a fence")
	}
	if !errors.Is(err, ErrRestoreCoordinationUnknown) {
		t.Fatalf("wrong classification: %v", err)
	}
}

// THE CANONICAL DRIVER DSN, ROUND-TRIPPED THROUGH THE REAL DRIVER.
//
// The resolver rebuilds the DSN from the frozen path — a `file:` URI escaping only
// the three characters that would otherwise end the URI path, or the plain path
// itself when the caller used the plain grammar. That rebuild is the hinge of the
// whole correction, because it is what makes the FENCED target and the OPENED target
// the same file. So it is measured against the pinned driver on exactly the names
// where an escaping mistake would be invisible: a space, a question mark, a hash, a
// literal percent sign and a plus.
func TestTheCanonicalDriverDSNOpensTheIntendedFileForAwkwardNames(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"real db.sqlite", "who?knows.db", "sharp#.db", "a%20b.db", "a+b.db"} {
		t.Run(name, func(t *testing.T) {
			canonical := filepath.Join(dir, name)
			// A name containing '?' is NOT expressible in the plain grammar (the case
			// below measures why), so every name here is addressed through the URI form.
			uri := "file:" + dir + "/" + uriEscapeForTest(name)

			st, oerr := openSQLiteDestination(t, uri)
			if oerr != nil {
				t.Fatalf("open %q: %v", uri, oerr)
			}
			if _, eerr := st.(*sqlStore).db.Exec("CREATE TABLE marker (v TEXT)"); eerr != nil {
				t.Fatal(eerr)
			}
			assertNamedDatabase(t, st, canonical)
			_ = st.Close()

			// The file that appeared really carries that name, byte for byte.
			entries, rerr := os.ReadDir(dir)
			if rerr != nil {
				t.Fatal(rerr)
			}
			found := false
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
				if e.Name() == name {
					found = true
				}
			}
			if !found {
				t.Fatalf("no file named %q exists; the directory holds %v", name, names)
			}

			// And the resolver's own canonical DSN opens that very database again.
			target, terr := opgate.ResolveSQLiteTarget(uri)
			if terr != nil {
				t.Fatal(terr)
			}
			again, oerr := openSQLiteDestination(t, target.DriverDSN())
			if oerr != nil {
				t.Fatalf("the canonical driver DSN %q was refused: %v", target.DriverDSN(), oerr)
			}
			var rows int
			if qerr := again.(*sqlStore).db.QueryRow("SELECT count(*) FROM marker").Scan(&rows); qerr != nil {
				t.Fatalf("the canonical driver DSN opened a different database: %v", qerr)
			}
			assertNamedDatabase(t, again, canonical)
			_ = again.Close()
		})
	}
}

// uriEscapeForTest builds the URI spelling of a filename independently of the
// resolver's own escaper, so the round trip above is not the resolver checking its
// own arithmetic.
func uriEscapeForTest(name string) string {
	var out strings.Builder
	for i := 0; i < len(name); i++ {
		switch c := name[i]; c {
		case '%':
			out.WriteString("%25")
		case '?':
			out.WriteString("%3f")
		case '#':
			out.WriteString("%23")
		case ' ':
			out.WriteString("%20")
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}

// ⛔ A PLAIN DSN CANNOT NAME A FILE CONTAINING '?', AND THE RESOLVER AGREES WITH THE
// DRIVER ABOUT WHAT IT DOES INSTEAD.
//
// This case exists because the first version of the round trip above assumed it
// could, and was wrong. In the plain grammar the pinned driver cuts the name at the
// first '?' and treats the remainder as a query, so `/dir/who?knows.db` opens
// `/dir/who`. That is a trap for an operator, but it is the DRIVER's behaviour — and
// what this control pins is that the fence resolves to the same file the driver
// opens, measured against the driver rather than against a reading of its source.
// A "fix" here that resolved the whole string would fence a file nobody opens, which
// is the very defect this correction removes.
func TestAPlainDSNContainingAQuestionMarkResolvesWhereTheDriverOpens(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(dir, "who?knows.db")

	// What the DRIVER does with it, measured.
	raw, err := sql.Open("sqlite", plain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("CREATE TABLE t (v TEXT)"); err != nil {
		t.Fatal(err)
	}
	var driverPath string
	var seq int
	var alias string
	if err := raw.QueryRow("PRAGMA database_list").Scan(&seq, &alias, &driverPath); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	// What the RESOLVER says. They must be the same file.
	target, err := opgate.ResolveSQLiteTarget(plain)
	if err != nil {
		t.Fatalf("the resolver refused a DSN the driver accepts: %v", err)
	}
	want, err := os.Stat(driverPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.Stat(target.CanonicalPath())
	if err != nil {
		t.Fatalf("the resolver froze %q, which does not exist while the driver opened %q: %v",
			target.CanonicalPath(), driverPath, err)
	}
	if !os.SameFile(want, got) {
		t.Fatalf("the fence would hold %q while the driver opens %q", target.CanonicalPath(), driverPath)
	}
	if _, serr := os.Stat(plain); !os.IsNotExist(serr) {
		t.Fatalf("a file literally named %q exists, so the plain grammar has changed: %v", plain, serr)
	}
	// And the URI spelling is the way to actually name that file.
	viaURI, err := opgate.ResolveSQLiteTarget("file:" + dir + "/who%3fknows.db")
	if err != nil {
		t.Fatal(err)
	}
	if viaURI.CanonicalPath() != plain {
		t.Fatalf("the URI form resolved to %q, want %q", viaURI.CanonicalPath(), plain)
	}
}

// ⛔ F3A-IR-1, THROUGH THE REAL PUBLIC CONSTRUCTOR: A SYMLINK FOLLOWED BY `..`
// STILL NAMES THE DRIVER'S DATABASE.
//
// This is the independent review's P1, made permanent. With `a/link` a symlink to
// `b/child`, the DSN `a/link/../dot.db` names `b/dot.db` — the kernel resolves the
// link first and applies `..` to what it found. The resolver cleaned the spelling
// as text before any of that, answered `a/dot.db`, and the consequences were
// measured rather than argued: Open returned a Store, CREATED `a/dot.db`, and could
// not read the marker table in the database the caller had named.
//
// The oracle is the pinned driver on the same spelling, never the resolver's own
// output.
func TestThePublicOpenKeepsTheDriversSymlinkDotSegmentDestination(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := filepath.Join(dir, "a")
	child := filepath.Join(dir, "b", "child")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(a, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(child, filepath.Join(a, "link")); err != nil {
		t.Fatal(err)
	}
	// Built by concatenation: filepath.Join would erase the dot segment and the case
	// with it.
	spelling := a + "/link/../dot.db"
	lexicalAlternate := filepath.Join(a, "dot.db")
	filesystemTarget := filepath.Join(dir, "b", "dot.db")

	// THE DRIVER DECIDES WHAT THIS DSN MEANS. It creates the database, marks it, and
	// reports where it is.
	raw, err := sql.Open("sqlite", spelling)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Ping(); err != nil {
		t.Fatal(err)
	}
	var seq int
	var name, driverMain string
	if err := raw.QueryRow("PRAGMA database_list").Scan(&seq, &name, &driverMain); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("CREATE TABLE f3air_original (marker TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("INSERT INTO f3air_original VALUES ('original b')"); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	if driverMain != filesystemTarget {
		t.Fatalf("the pinned driver opened %q for %q; this case's premise is that it opens %q", driverMain, spelling, filesystemTarget)
	}

	// AND THE PRODUCT MUST OPEN THE SAME DATABASE.
	st, err := openSQLiteDestination(t, spelling)
	if err != nil {
		t.Fatalf("the public Open refused a DSN the pinned driver opens: %v", err)
	}
	db := st.(*sqlStore).db
	var marker string
	if err := db.QueryRow("SELECT marker FROM f3air_original").Scan(&marker); err != nil || marker != "original b" {
		t.Fatalf("the public Open is not on the database the driver named: marker=%q err=%v", marker, err)
	}
	var storeMain string
	if err := db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &storeMain); err != nil {
		t.Fatal(err)
	}
	if storeMain != driverMain {
		t.Fatalf("the public Open is on %q while the driver's own answer for that DSN is %q", storeMain, driverMain)
	}
	if _, err := os.Stat(lexicalAlternate); !os.IsNotExist(err) {
		t.Fatalf("the public Open created the lexical alternate %q: the dot segment was erased before the symlink was followed (%v)", lexicalAlternate, err)
	}
	// The FENCE landed on the same file as the open. A control beside the wrong
	// database is not a control.
	if _, err := os.Stat(filesystemTarget + ".dr-control.lock"); err != nil {
		t.Fatalf("no coordination lock beside the database that was actually opened: %v", err)
	}
	if _, err := os.Stat(lexicalAlternate + ".dr-control.lock"); !os.IsNotExist(err) {
		t.Fatalf("a coordination lock was created beside the file nobody opened: %v", err)
	}
	_ = st.Close()
}

// ⛔ F3A-IR-4, AGAINST THE PINNED DRIVER: REPEATED `_pragma` VALUES AND A
// PRESENT-BUT-EMPTY QUERY ARE VALID DSNs.
//
// The driver collects every `_pragma` and executes all of them against the SAME
// database, and it cuts the filename at the first '?' whether or not anything
// follows it. The resolver read two `_pragma` values as a conflicting identity
// parameter and read `<path>?` as a filename ending in '?', so both were refused —
// a store that opened yesterday and does not open today, for a destination that was
// never ambiguous.
//
// Each case measures the DRIVER first and then requires the product to agree.
func TestSupportedDriverOptionsAndEmptyQueriesOpenTheSameDatabase(t *testing.T) {
	cases := map[string]func(path string) string{
		"plain-repeated-pragma": func(p string) string { return p + "?_pragma=application_id(723)&_pragma=user_version(419)" },
		"uri-repeated-pragma":   func(p string) string { return "file:" + p + "?_pragma=application_id(723)&_pragma=user_version(419)" },
		"plain-empty-query":     func(p string) string { return p + "?" },
		"uri-empty-query":       func(p string) string { return "file:" + p + "?" },
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "options.db")
			dsn := build(path)

			raw, err := sql.Open("sqlite", dsn)
			if err != nil {
				t.Fatal(err)
			}
			if err := raw.Ping(); err != nil {
				t.Fatalf("this case's premise is that the pinned driver accepts %q: %v", dsn, err)
			}
			var seq int
			var dbName, driverMain string
			if err := raw.QueryRow("PRAGMA database_list").Scan(&seq, &dbName, &driverMain); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(name, "repeated-pragma") {
				var appID, userVersion int
				if err := raw.QueryRow("PRAGMA application_id").Scan(&appID); err != nil {
					t.Fatal(err)
				}
				if err := raw.QueryRow("PRAGMA user_version").Scan(&userVersion); err != nil {
					t.Fatal(err)
				}
				if appID != 723 || userVersion != 419 {
					t.Fatalf("the driver applied application_id=%d user_version=%d: both repetitions were expected on one database", appID, userVersion)
				}
			}
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}
			if driverMain != path {
				t.Fatalf("the driver opened %q for %q, not %q", driverMain, dsn, path)
			}

			pool, err := openSQLite(dsn)
			if err != nil {
				t.Fatalf("the product refused a DSN the pinned driver opens: %v", err)
			}
			defer pool.Close() //nolint:errcheck // the assertions below carry the diagnosis
			var storeMain string
			if err := pool.QueryRow("PRAGMA database_list").Scan(&seq, &dbName, &storeMain); err != nil {
				t.Fatal(err)
			}
			if storeMain != driverMain {
				t.Fatalf("the product opened %q while the driver opens %q for the same DSN", storeMain, driverMain)
			}
		})
	}
}
