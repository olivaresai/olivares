// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build unix

package opgate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE MEMORY FORMS THAT MUST KEEP WORKING.
//
// A resolver that refused these would break the console's scratch verification, the
// DR drill and every in-memory test in the tree, so they come first: the refusals
// below only mean something if the ordinary forms still resolve.
func TestSupportedMemoryDSNsResolveToNoFile(t *testing.T) {
	for _, dsn := range []string{
		"",
		":memory:",
		"file::memory:",
		"file::memory:?cache=shared",
		"file:/anywhere/named.db?mode=memory",
		"file:/anywhere/named.db?mode=memory&cache=shared",
		":memory:?_pragma=busy_timeout(5000)",
	} {
		t.Run(dsn, func(t *testing.T) {
			target, err := ResolveSQLiteTarget(dsn)
			if err != nil {
				t.Fatalf("a supported memory DSN was refused: %v", err)
			}
			if !target.Memory() {
				t.Fatalf("%q did not classify as memory (canonical %q)", dsn, target.CanonicalPath())
			}
			if !target.Anchor().Zero() || target.CanonicalPath() != "" {
				t.Fatalf("a memory destination produced a file anchor: %v", target.Anchor())
			}
			if target.DriverDSN() == "" {
				t.Fatal("a memory destination produced no driver DSN")
			}
		})
	}
}

// ⛔ THE SUBSTRING TEST THIS REPLACES, MEASURED FROM THE OTHER SIDE.
//
// The store classified memory with strings.Contains(dsn, "mode=memory"). In the
// PLAIN name grammar the query never reaches SQLite at all, so this DSN is an
// ordinary file — and the old test declared it in-memory, produced no anchor, and
// left a real on-disk database unfenced and un-chmodded.
func TestAMisleadingMemorySubstringIsStillAFileDestination(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		filepath.Join(dir, "real.db") + "?mode=memory",
		filepath.Join(dir, "real.db") + "?_pragma=busy_timeout(1)&note=mode=memory",
		filepath.Join(dir, "mode=memory"),
	} {
		t.Run(name, func(t *testing.T) {
			target, err := ResolveSQLiteTarget(name)
			if err != nil {
				t.Fatalf("an ordinary file DSN was refused: %v", err)
			}
			if target.Memory() {
				t.Fatalf("%q was classified as an in-memory database, so nothing fences the file it actually opens", name)
			}
			if target.Anchor().Zero() {
				t.Fatalf("%q produced no anchor", name)
			}
		})
	}
}

// A PLAIN name is LITERAL. No percent-decoding, because the pinned driver hands it
// to SQLite without the URI flag taking effect, and a resolver that decoded here
// would fence a file the driver never opens.
func TestAPlainNameIsNotPercentDecoded(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "a%20b.db")
	target, err := ResolveSQLiteTarget(plain)
	if err != nil {
		t.Fatalf("a plain name containing %% was refused: %v", err)
	}
	if target.CanonicalPath() != plain {
		t.Fatalf("a plain name was decoded: %q became %q", plain, target.CanonicalPath())
	}
	if target.DriverDSN() != plain {
		t.Fatalf("the driver would be given %q for the plain name %q", target.DriverDSN(), plain)
	}
	// And a plain name containing a '#' is a filename character too: only the URI
	// grammar has fragments.
	hash := filepath.Join(dir, "a#b.db")
	target, err = ResolveSQLiteTarget(hash)
	if err != nil {
		t.Fatalf("a plain name containing '#' was refused: %v", err)
	}
	if target.CanonicalPath() != hash {
		t.Fatalf("a plain name was cut at '#': %q became %q", hash, target.CanonicalPath())
	}
}

// THE URI PATH IS DECODED EXACTLY ONCE, and the characters that would otherwise end
// the path are filename characters when they arrive escaped.
func TestURIPathDecoding(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		uri  string
		want string
	}{
		{"space", "file:" + dir + "/real%20db.sqlite", filepath.Join(dir, "real db.sqlite")},
		{"question-mark", "file:" + dir + "/who%3fknows.db", filepath.Join(dir, "who?knows.db")},
		{"hash", "file:" + dir + "/sharp%23.db", filepath.Join(dir, "sharp#.db")},
		{"escaped-percent", "file:" + dir + "/a%2520b.db", filepath.Join(dir, "a%20b.db")},
		{"literal-plus", "file:" + dir + "/a+b.db", filepath.Join(dir, "a+b.db")},
		{"localhost-authority", "file://localhost" + dir + "/host.db", filepath.Join(dir, "host.db")},
		{"empty-authority", "file://" + dir + "/empty.db", filepath.Join(dir, "empty.db")},
		{"query-value-with-specials", "file:" + dir + "/plain.db?_pragma=busy_timeout(1)", filepath.Join(dir, "plain.db")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			target, err := ResolveSQLiteTarget(c.uri)
			if err != nil {
				t.Fatalf("%s: %v", c.uri, err)
			}
			if target.CanonicalPath() != c.want {
				t.Fatalf("%s resolved to %q, want %q", c.uri, target.CanonicalPath(), c.want)
			}
			if target.Anchor().Canonical() != c.want {
				t.Fatalf("the anchor fences %q while the destination is %q", target.Anchor().Canonical(), c.want)
			}
		})
	}
}

// The driver DSN this resolver emits must name the SAME file back. This is the
// round trip that keeps the fenced target and the opened target identical, and it is
// checked against the decoder rather than against the resolver's own output.
func TestTheCanonicalDriverDSNNamesTheSameFile(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"real db.sqlite", "who?knows.db", "sharp#.db", "a%20b.db", "a+b.db", "ordinary.db"} {
		t.Run(name, func(t *testing.T) {
			target, err := ResolveSQLiteTarget("file:" + dir + "/" + escapeURIPath(name))
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			again, err := ResolveSQLiteTarget(target.DriverDSN())
			if err != nil {
				t.Fatalf("the canonical driver DSN %q does not resolve: %v", target.DriverDSN(), err)
			}
			if again.CanonicalPath() != target.CanonicalPath() {
				t.Fatalf("the driver DSN names %q while the fence holds %q", again.CanonicalPath(), target.CanonicalPath())
			}
		})
	}
}

// THE REFUSALS. Each of these used to be an empty anchor, and an empty anchor is an
// unfenced destination with no diagnosis.
func TestUnprovableDestinationsAreTypedRefusals(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "present.db"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "nowhere.db"), filepath.Join(dir, "dangling.db")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "adir"), 0o700); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"missing-parent":        filepath.Join(dir, "no-such-dir", "x.db"),
		"dangling-final-link":   filepath.Join(dir, "dangling.db"),
		"directory-target":      filepath.Join(dir, "adir"),
		"whitespace-dsn":        "   ",
		"reserved-colon-name":   ":something:",
		"unsupported-authority": "file://elsewhere.example" + dir + "/x.db",
		"custom-vfs":            "file:" + dir + "/x.db?vfs=hostile",
		"plain-custom-vfs":      filepath.Join(dir, "x.db") + "?vfs=hostile",
		"nul-escape":            "file:" + dir + "/x%00y.db",
		"malformed-escape":      "file:" + dir + "/x%zzy.db",
		"trailing-percent":      "file:" + dir + "/x%",
		"unknown-uri-param":     "file:" + dir + "/x.db?retarget=elsewhere",
		"unsupported-mode":      "file:" + dir + "/x.db?mode=ram",
		"conflicting-mode":      "file:" + dir + "/x.db?mode=ro&mode=memory",
		"nolock":                "file:" + dir + "/x.db?nolock=1",
		"immutable":             "file:" + dir + "/x.db?immutable=1",
		"fragment":              "file:" + dir + "/x.db#frag",
		"anonymous-temp":        "file:",
		// A DSN that BEGINS with '?' has no query in the driver's grammar, so the whole
		// string is a filename — and a plain path containing '?' cannot be handed back
		// to the driver unambiguously, because the driver would cut it there.
		"leading-question-mark": "?foo.db",
	}
	for name, dsn := range cases {
		t.Run(name, func(t *testing.T) {
			target, err := ResolveSQLiteTarget(dsn)
			if err == nil {
				t.Fatalf("%q was accepted: memory=%t canonical=%q", dsn, target.Memory(), target.CanonicalPath())
			}
			if !errors.Is(err, ErrUnsupportedDSN) {
				t.Fatalf("%q was refused with the wrong classification: %v", dsn, err)
			}
		})
	}
}

// A hard-linked destination is refused, because a control keyed by pathname cannot
// enumerate an inode's other names. The refusal is the honest answer; fencing one
// name and calling it coverage is not.
func TestAMultiplyLinkedDestinationIsRefused(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "one.db")
	if err := os.WriteFile(first, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// One name: accepted.
	if _, err := ResolveSQLiteTarget(first); err != nil {
		t.Fatalf("a single-named destination was refused: %v", err)
	}
	if err := os.Link(first, filepath.Join(dir, "two.db")); err != nil {
		t.Skipf("this filesystem does not support hard links: %v", err)
	}
	_, err := ResolveSQLiteTarget(first)
	if !errors.Is(err, ErrHardLinked) {
		t.Fatalf("a destination with two names was accepted (err=%v)", err)
	}
	if !strings.Contains(err.Error(), "2 names") {
		t.Fatalf("the refusal does not report the link count: %v", err)
	}
}

// A RELATIVE DSN is frozen against the working directory read once, and a final
// SYMLINK resolves to what it points at. Both of these produced a different anchor
// from the file the driver opened.
func TestAliasesFreezeToOneIdentity(t *testing.T) {
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(real, "real db.sqlite")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(real, "alias.sqlite")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	canonical, err := ResolveSQLiteTarget(target)
	if err != nil {
		t.Fatal(err)
	}
	for name, dsn := range map[string]string{
		"final-symlink":  link,
		"encoded-uri":    "file:" + real + "/real%20db.sqlite",
		"dot-segments":   filepath.Join(real, "..", filepath.Base(real), "real db.sqlite"),
		"trailing-slash": target + "/",
	} {
		t.Run(name, func(t *testing.T) {
			alias, err := ResolveSQLiteTarget(dsn)
			if err != nil {
				t.Fatalf("%s: %v", dsn, err)
			}
			if alias.Anchor().LockPath() != canonical.Anchor().LockPath() {
				t.Fatalf("the %s alias fences %s while the canonical name fences %s",
					name, alias.Anchor().LockPath(), canonical.Anchor().LockPath())
			}
		})
	}

	// And a relative spelling of the same file lands there too.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if err := os.Chdir(real); err != nil {
		t.Fatal(err)
	}
	relative, err := ResolveSQLiteTarget("real db.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if relative.Anchor().LockPath() != canonical.Anchor().LockPath() {
		t.Fatalf("a relative DSN fences %s while the absolute one fences %s",
			relative.Anchor().LockPath(), canonical.Anchor().LockPath())
	}
	if !filepath.IsAbs(relative.DriverDSN()) {
		t.Fatalf("the canonical driver DSN for a relative name is still relative: %q", relative.DriverDSN())
	}
}

// A destination that does not exist yet is fenced at the pathname under its RESOLVED
// parent — which is what lets the fence be held before anything creates the file.
func TestANonexistentDestinationFencesItsCanonicalPathname(t *testing.T) {
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(real, "actual"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(real, "actual"), filepath.Join(real, "linked")); err != nil {
		t.Fatal(err)
	}
	viaLink, err := ResolveSQLiteTarget(filepath.Join(real, "linked", "new.db"))
	if err != nil {
		t.Fatalf("a not-yet-created destination under a symlinked parent was refused: %v", err)
	}
	direct, err := ResolveSQLiteTarget(filepath.Join(real, "actual", "new.db"))
	if err != nil {
		t.Fatal(err)
	}
	if viaLink.Anchor().LockPath() != direct.Anchor().LockPath() {
		t.Fatalf("a symlinked parent produced a different fence: %s vs %s",
			viaLink.Anchor().LockPath(), direct.Anchor().LockPath())
	}
	if _, err := os.Stat(direct.CanonicalPath()); !os.IsNotExist(err) {
		t.Fatalf("resolving a nonexistent destination CREATED it: %v", err)
	}
}

// A SCRATCH destination is an ordinary, separately anchored file. "Scratch" is a
// word about intent, not an exemption from destination identity.
func TestAScratchDestinationIsAnOrdinaryFencedFile(t *testing.T) {
	scratch := filepath.Join(t.TempDir(), "scratch-verify.db")
	target, err := ResolveSQLiteTarget(scratch)
	if err != nil {
		t.Fatalf("a scratch destination was refused: %v", err)
	}
	if target.Memory() || target.Anchor().Zero() {
		t.Fatal("a scratch destination resolved to no anchor")
	}
}

// ⛔ F3A-IR-1: A SYMLINK FOLLOWED BY `..` IS RESOLVED IN FILESYSTEM ORDER.
//
// The independent review measured this one through the public store constructor:
// with `a/link` a symlink to `b/child`, the DSN `a/link/../dot.db` names `b/dot.db`
// to the pinned driver, and this resolver answered `a/dot.db`. Open then CREATED
// `a/dot.db` and could not see the marker table in the database the caller had
// named — a changed destination with real file effects, not an unequal string.
//
// The oracle here is THE KERNEL, never the resolver's own arithmetic: every case
// creates the file through the literal spelling with os.OpenFile and requires
// os.SameFile between what the kernel made and what the resolver froze. A resolver
// compared against itself agrees with any bug it has.
func TestASymlinkFollowedByADotSegmentResolvesInFilesystemOrder(t *testing.T) {
	// Cases cover both halves of FreezeDestinationPath — a target that already
	// exists and one that does not yet — in both grammars, and in both spellings.
	for _, existing := range []bool{true, false} {
		for _, grammar := range []string{"plain", "uri"} {
			for _, spelling := range []string{"absolute", "relative"} {
				name := fmt.Sprintf("%s/%s/existing=%t", grammar, spelling, existing)
				t.Run(name, func(t *testing.T) {
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
					// The kernel resolves `a/link` to `b/child` and applies `..` to
					// THAT, so this spelling names b/dot.db. filepath.Join would erase
					// the pair as text before any of that happened, which is why the
					// spelling is built by concatenation.
					lexicalAlternate := filepath.Join(a, "dot.db")
					filesystemTarget := filepath.Join(dir, "b", "dot.db")

					path := a + "/link/../dot.db"
					if spelling == "relative" {
						cwd, err := os.Getwd()
						if err != nil {
							t.Fatal(err)
						}
						t.Cleanup(func() { _ = os.Chdir(cwd) })
						if err := os.Chdir(dir); err != nil {
							t.Fatal(err)
						}
						path = "a/link/../dot.db"
					}
					dsn := path
					if grammar == "uri" {
						dsn = "file:" + escapeURIPath(path)
					}

					if existing {
						// Create it THROUGH THE SPELLING: whatever the kernel makes is
						// the destination this DSN names.
						f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
						if err != nil {
							t.Fatal(err)
						}
						if _, err := f.WriteString("original b"); err != nil {
							t.Fatal(err)
						}
						if err := f.Close(); err != nil {
							t.Fatal(err)
						}
					}

					target, err := ResolveSQLiteTarget(dsn)
					if err != nil {
						t.Fatalf("the valid DSN %q was refused: %v", dsn, err)
					}
					if !existing {
						// Resolution creates nothing. The fence has to be takeable
						// before the driver makes the file.
						if _, err := os.Stat(filesystemTarget); !os.IsNotExist(err) {
							t.Fatalf("resolving created the destination: %v", err)
						}
						f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
						if err != nil {
							t.Fatalf("the kernel refused the spelling this resolver accepted: %v", err)
						}
						_ = f.Close()
					}

					made, err := os.Stat(path)
					if err != nil {
						t.Fatal(err)
					}
					frozen, err := os.Stat(target.CanonicalPath())
					if err != nil {
						t.Fatalf("the resolver froze %q, which does not exist: %v", target.CanonicalPath(), err)
					}
					if !os.SameFile(made, frozen) {
						t.Fatalf("the resolver froze %q, which is not the inode the kernel reaches through %q", target.CanonicalPath(), dsn)
					}
					if target.CanonicalPath() != filesystemTarget {
						t.Fatalf("the resolver froze %q, want the filesystem-order destination %q", target.CanonicalPath(), filesystemTarget)
					}
					if _, err := os.Stat(lexicalAlternate); !os.IsNotExist(err) {
						t.Fatalf("the lexical alternate %q exists: the dot segment was erased before the symlink was followed (%v)", lexicalAlternate, err)
					}
					// The anchor and the driver DSN follow the frozen identity, so the
					// fence and the open cannot land on different files.
					if target.Anchor().Canonical() != filesystemTarget {
						t.Fatalf("the anchor fences %q while the destination is %q", target.Anchor().Canonical(), filesystemTarget)
					}
					again, err := ResolveSQLiteTarget(target.DriverDSN())
					if err != nil {
						t.Fatalf("the canonical driver DSN %q does not re-resolve: %v", target.DriverDSN(), err)
					}
					if again.CanonicalPath() != target.CanonicalPath() {
						t.Fatalf("the canonical driver DSN names %q, not the frozen %q", again.CanonicalPath(), target.CanonicalPath())
					}
				})
			}
		}
	}
}

// The trailing-separator handling is the DRIVER's, not a convenience, so it is
// pinned to the driver's measured behavior: `<file>/`, `file:<file>/` and
// `<file>//` all open `<file>` and report it as main in PRAGMA database_list.
// (The measurement itself is in the store package, where the driver may be
// imported; this leaf stays stdlib-only.) A final `.` or `..` is a directory and
// stays a refusal.
func TestTrailingSeparatorsFollowTheDriverAndDotDirectoriesDoNot(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "olivares.db")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dsn := range []string{target + "/", target + "//", "file:" + target + "/"} {
		resolved, err := ResolveSQLiteTarget(dsn)
		if err != nil {
			t.Fatalf("%q was refused, though the driver opens it: %v", dsn, err)
		}
		if resolved.CanonicalPath() != target {
			t.Fatalf("%q resolved to %q, want %q", dsn, resolved.CanonicalPath(), target)
		}
	}
	for _, dsn := range []string{dir + "/.", dir + "/..", "/"} {
		if _, err := ResolveSQLiteTarget(dsn); !errors.Is(err, ErrUnsupportedDSN) {
			t.Fatalf("%q names a directory and was not refused: %v", dsn, err)
		}
	}
}

// ⛔ F3A-IR-4: A REPEATABLE DRIVER OPTION IS NOT A CONFLICTING IDENTITY PARAMETER,
// AND A PRESENT-BUT-EMPTY QUERY IS NOT AN ABSENT ONE.
//
// Both halves were measured against the pinned driver by the independent review:
// it applies EVERY `_pragma` to the same database, and it cuts the filename at the
// first '?' whether or not anything follows it. This resolver refused both, which
// is a compatibility regression rather than a fence: a refused DSN publishes
// nothing, but it also stops an installation that was valid yesterday.
//
// The negatives beside them are the point of the case: what makes a repetition
// dangerous is that the parameter selects a FILE, and those are still refused.
func TestSupportedDriverOptionsAndEmptyQueriesAreAccepted(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "options.db")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	accepted := map[string]string{
		"uri-repeated-pragma":     "file:" + target + "?_pragma=application_id(723)&_pragma=user_version(419)",
		"plain-repeated-pragma":   target + "?_pragma=application_id(723)&_pragma=user_version(419)",
		"uri-repeated-identical":  "file:" + target + "?_pragma=busy_timeout(5000)&_pragma=busy_timeout(5000)",
		"plain-empty-query":       target + "?",
		"uri-empty-query":         "file:" + target + "?",
		"uri-timezone":            "file:" + target + "?_timezone=UTC",
		"uri-inttotime":           "file:" + target + "?_inttotime=1",
		"uri-texttotime":          "file:" + target + "?_texttotime=1",
		"uri-txlock-with-pragmas": "file:" + target + "?_txlock=immediate&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)",
		"uri-mode-repeated-same":  "file:" + target + "?mode=rw&mode=rw",
	}
	for name, dsn := range accepted {
		t.Run(name, func(t *testing.T) {
			resolved, err := ResolveSQLiteTarget(dsn)
			if err != nil {
				t.Fatalf("the driver accepts %q; this resolver refused it: %v", dsn, err)
			}
			if resolved.CanonicalPath() != target {
				t.Fatalf("%q resolved to %q, want %q", dsn, resolved.CanonicalPath(), target)
			}
			if resolved.Memory() {
				t.Fatalf("%q is a file destination and was classified as memory", dsn)
			}
		})
	}
	// Every option the caller wrote survives into the canonical DSN: dropping one
	// would open the intended file with a configuration nobody asked for.
	repeated, err := ResolveSQLiteTarget(accepted["uri-repeated-pragma"])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"_pragma=application_id(723)", "_pragma=user_version(419)"} {
		if !strings.Contains(repeated.DriverDSN(), want) {
			t.Fatalf("the canonical DSN %q dropped %q", repeated.DriverDSN(), want)
		}
	}

	refused := map[string]string{
		"conflicting-mode":      "file:" + target + "?mode=ro&mode=rw",
		"conflicting-cache":     "file:" + target + "?cache=shared&cache=private",
		"conflicting-vfs":       "file:" + target + "?vfs=unix&vfs=unix-none",
		"unknown-parameter":     "file:" + target + "?retarget=elsewhere",
		"nolock":                "file:" + target + "?nolock=1",
		"immutable":             "file:" + target + "?immutable=1",
		"unsupported-mode":      "file:" + target + "?mode=sideways",
		"plain-vfs":             target + "?vfs=unix-none",
		"conflicting-immutable": "file:" + target + "?immutable=0&immutable=1",
	}
	for name, dsn := range refused {
		t.Run("refused/"+name, func(t *testing.T) {
			if _, err := ResolveSQLiteTarget(dsn); !errors.Is(err, ErrUnsupportedDSN) {
				t.Fatalf("%q names an ambiguous or unhonorable identity and was not refused: %v", dsn, err)
			}
		})
	}
}
