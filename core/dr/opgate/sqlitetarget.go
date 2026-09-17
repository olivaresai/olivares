// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package opgate

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// THE ONE PLACE A SQLITE DSN BECOMES A DESTINATION.
//
// Before this file there were two string-strippers — one in cmd/olivares and one in
// the store — and each of them cut a DSN at `file:` and the first `?` and called the
// remainder a path. That is not the grammar the pinned driver implements, and the
// gap was measured: an existing final symlink and a `file:` URI with `%20` in it
// produced sidecar and lock paths DIFFERENT from the database the driver actually
// opened, so a second process holding the canonical destination exclusively did not
// fence either alias. The alias opened the very same inode, marker row and all.
//
// So the fence and the driver must be told the destination by the SAME authority.
// ResolveSQLiteTarget is that authority: it produces the canonical driver DSN, the
// frozen absolute file identity and the anchor, together, from one parse. A caller
// that passes the resolver's DSN to the driver and the resolver's anchor to the lock
// cannot fence one file and open another.
//
// # The grammar this implements, and where it comes from
//
// modernc.org/sqlite v1.54.0 `conn.go:56-128` (the pinned driver) is the reference,
// not the SQLite documentation in general:
//
//   - The query is split at the FIRST '?' whose index is >= 1. A DSN that begins
//     with '?' therefore has no query at all: the whole string is a filename.
//   - If the DSN does NOT begin with `file:`, the query is REMOVED from the name
//     handed to sqlite3_open_v2. It is still parsed by the driver for `_pragma` and
//     `vfs`, so those keep working — but SQLite never sees `mode=` or `cache=`, and
//     the filename is used LITERALLY. No percent-decoding happens to a plain name.
//   - If the DSN DOES begin with `file:`, the whole string is handed to SQLite with
//     SQLITE_OPEN_URI, and SQLite parses it: optional `//authority`, a path that
//     ends at a raw '?' or '#', `%HH` decoded exactly once along the way, then the
//     query parameters.
//
// The two consequences that broke the old strippers are that `%3f` in a URI is a
// filename character rather than a query separator, and that `mode=memory` in a
// PLAIN DSN reaches nothing at all — the previous `strings.Contains(dsn,
// "mode=memory")` test classified `/var/lib/olivares/db?mode=memory` as an in-memory
// store and left a real file on disk unfenced.
//
// # What it refuses, and why refusing is the safe direction
//
// Every form whose unique destination cannot be established is a typed refusal
// BEFORE any I/O: an unsupported URI authority, a custom VFS, a NUL, a malformed
// escape, a conflicting identity parameter, a dangling final symlink, a missing
// parent directory, a target that is not a regular file, and an existing target with
// more than one name. There is deliberately no "unrecognized, therefore unfenced"
// fallback, because that fallback IS the defect: it turns every spelling this file
// does not understand into an unlocked destination.

// Errors this resolver returns. They are sentinels because a caller has to be able
// to tell "this DSN names no single file" from "the file system said no".
var (
	// ErrUnsupportedDSN reports a DSN whose destination this build cannot prove.
	// It is never downgraded to "no anchor".
	ErrUnsupportedDSN = errors.New("opgate: this SQLite DSN does not resolve to one provable destination")
	// ErrHardLinked reports an existing SQLite destination reachable under more than
	// one name. A control keyed by pathname cannot enumerate an inode's other names,
	// so it cannot honestly claim to fence them.
	ErrHardLinked = errors.New("opgate: the SQLite destination has more than one name (hard link), and a control keyed by pathname cannot fence the other names")
)

// SQLiteTarget is one resolved SQLite destination: what the driver will be told,
// what file that is, and what fences it.
//
// Its fields are unexported and there is no constructor other than
// ResolveSQLiteTarget, so a caller cannot assemble a target whose driver DSN and
// anchor disagree — which is exactly the state the measured defect was in.
type SQLiteTarget struct {
	input     string
	memory    bool
	canonical string
	driverDSN string
	anchor    Anchor
}

// Memory reports a destination with no file: `:memory:`, `file::memory:` or a
// supported `mode=memory` URI. It has no anchor and nothing to fence, and that is
// not a degraded answer — there is no custody and no file to lose.
func (t SQLiteTarget) Memory() bool { return t.memory }

// CanonicalPath is the frozen, absolute, symlink-resolved file this destination is.
// Empty for a memory target. It is what the file-mode restriction and the WAL/SHM
// sidecar handling must use: restricting a lookalike spelling restricts nothing.
func (t SQLiteTarget) CanonicalPath() string { return t.canonical }

// DriverDSN is the DSN to hand to the pinned driver. For a file target it names the
// canonical path in the same grammar family the caller used, so the driver opens the
// file this target froze rather than the alias the caller spelled.
func (t SQLiteTarget) DriverDSN() string { return t.driverDSN }

// Anchor is the control that fences this destination. Zero for a memory target.
func (t SQLiteTarget) Anchor() Anchor { return t.anchor }

// Input is the DSN as given, kept for diagnostics ONLY. Nothing resolves it again:
// re-parsing the caller's spelling after freezing the destination is how a fenced
// path and an opened path drift apart.
func (t SQLiteTarget) Input() string { return t.input }

func (t SQLiteTarget) String() string {
	if t.memory {
		return "sqlite memory destination"
	}
	return "sqlite " + t.canonical
}

// uriQueryParams are the query keys this build accepts in a `file:` URI, and what
// each one is allowed to be.
//
// The list is CLOSED, and it is closed in the direction that costs a refusal rather
// than a silent unfenced destination: a key nobody here recognizes may be one that
// retargets the database (SQLite grows URI parameters), so it is refused with its
// own name in the message instead of being carried through.
var (
	// identity parameters change WHAT is opened, so their values are validated.
	uriModeValues  = map[string]bool{"ro": true, "rw": true, "rwc": true, "memory": true}
	uriCacheValues = map[string]bool{"shared": true, "private": true}
	uriPsowValues  = map[string]bool{"0": true, "1": true, "true": true, "false": true}
	// driver options are non-identity: they configure the connection, not the file.
	// The list is the pinned driver's own (`sqlite.go` applyQueryParams/applyDQSConfig
	// and `conn.go` getErrorRcMode): every key it reads and this build does not, is a
	// supported option refused for no reason.
	uriDriverOptions = map[string]bool{
		"_pragma":              true,
		"_txlock":              true,
		"_time_format":         true,
		"_time_integer_format": true,
		"_timezone":            true,
		"_inttotime":           true,
		"_texttotime":          true,
		"_dqs":                 true,
		"_error_rc":            true,
	}
	// uriIdentityParams are the keys that change WHICH database is opened, or how it
	// is locked. They are the only ones whose REPETITION is a conflict: see
	// uriDeclaresMemory.
	uriIdentityParams = map[string]bool{
		"mode":      true,
		"cache":     true,
		"psow":      true,
		"vfs":       true,
		"nolock":    true,
		"immutable": true,
	}
)

// ResolveSQLiteTarget parses a SQLite DSN into its one destination.
//
// It performs NO connection, NO pragma and NO creation: it stats and resolves, and
// everything it refuses it refuses before the caller has touched the database. That
// ordering is the point — the fence this target feeds has to be held before the
// driver's first connection, because the driver CREATES the file it is pointed at.
func ResolveSQLiteTarget(dsn string) (SQLiteTarget, error) {
	if dsn == "" {
		// The store's own default. An empty DSN has always meant the in-memory
		// database, and naming it explicitly is what lets everything below assume a
		// non-empty string.
		return SQLiteTarget{input: dsn, memory: true, driverDSN: ":memory:"}, nil
	}
	if strings.TrimSpace(dsn) == "" {
		return SQLiteTarget{}, fmt.Errorf("%w: the DSN is only whitespace, which names neither a file nor the in-memory database", ErrUnsupportedDSN)
	}
	if strings.ContainsRune(dsn, 0) {
		return SQLiteTarget{}, fmt.Errorf("%w: the DSN contains a NUL byte", ErrUnsupportedDSN)
	}

	// The driver's own split, reproduced exactly. `pos >= 1` and not `pos >= 0`:
	// a DSN that BEGINS with '?' has no query in this grammar.
	query := splitDSNQuery(dsn)
	// getVFSName parses the raw query for BOTH grammars — a plain name with
	// `?vfs=x` selects a custom VFS just as a URI does — so the check is here,
	// before the two branches, rather than inside the URI one.
	if query.raw != "" {
		values, err := url.ParseQuery(query.raw)
		if err != nil {
			return SQLiteTarget{}, fmt.Errorf("%w: the driver cannot parse the DSN query %q: %v", ErrUnsupportedDSN, query.raw, err)
		}
		if vfs := values["vfs"]; len(vfs) > 0 {
			return SQLiteTarget{}, fmt.Errorf(
				"%w: the DSN selects the custom VFS %q, and a destination reached through a VFS this build does not implement cannot be resolved to a file to fence",
				ErrUnsupportedDSN, vfs[0])
		}
	}

	if strings.HasPrefix(dsn, "file:") {
		return resolveSQLiteURI(dsn, query)
	}
	return resolveSQLitePlainName(dsn, query)
}

// dsnQuery is the driver's split of a DSN into a name and a query, kept as TWO
// facts: whether a query delimiter is present at all, and what follows it.
//
// ⛔ THEY ARE NOT THE SAME FACT, and collapsing them was a measured refusal of a
// valid DSN. The pinned driver cuts the filename at the first '?' whose index is
// >= 1 WHETHER OR NOT ANYTHING FOLLOWS IT (`conn.go:62-74`: the cut is guarded by
// `pos >= 1`, never by the length of the query), so `/var/lib/olivares/db?` names
// `/var/lib/olivares/db`. Deciding by `rawQuery != ""` instead made the trailing
// '?' a filename byte, which named a file that does not exist — and this resolver
// then refused the whole DSN because a plain name may not contain '?'.
type dsnQuery struct {
	present bool
	raw     string
	// at is the index of the delimiter in the DSN. It is meaningful only when
	// present is true, which is why the name is cut with it rather than by
	// searching the string a second time somewhere else.
	at int
}

func splitDSNQuery(dsn string) dsnQuery {
	pos := strings.IndexRune(dsn, '?')
	if pos < 1 {
		return dsnQuery{}
	}
	return dsnQuery{present: true, raw: dsn[pos+1:], at: pos}
}

// suffix renders the query back onto a canonical DSN.
//
// An EMPTY query is not re-emitted: it carries no driver option, both spellings
// name the same file to the same driver, and the canonical DSN is composed with
// further `_pragma` parameters downstream — where a trailing '?' would have to be
// joined as `?&`. The presence of the delimiter is honored where it means
// something, which is the cut above, not here.
func (q dsnQuery) suffix() string {
	if q.raw == "" {
		return ""
	}
	return "?" + q.raw
}

// resolveSQLitePlainName handles the grammar the driver applies to a name that does
// NOT begin with `file:`: the query is cut off, and what remains is a LITERAL
// filename — no percent-decoding, no URI parameters, and `mode=memory` in the query
// reaches SQLite not at all.
func resolveSQLitePlainName(dsn string, query dsnQuery) (SQLiteTarget, error) {
	name := dsn
	if query.present {
		// The cut is decided by the DELIMITER, not by the length of what follows
		// it. See dsnQuery: `<path>?` names `<path>` in this grammar.
		name = dsn[:query.at]
	}
	if name == ":memory:" {
		return SQLiteTarget{input: dsn, memory: true, driverDSN: dsn}, nil
	}
	if name == "" {
		return SQLiteTarget{}, fmt.Errorf("%w: the DSN carries only a query and names no destination", ErrUnsupportedDSN)
	}
	if strings.HasPrefix(name, ":") {
		// SQLite reserves names beginning with ':' and documents that they may be
		// interpreted specially. Only `:memory:` has a defined meaning here, so any
		// other one is ambiguous rather than a file, and an ambiguous destination is
		// refused rather than fenced at a path nobody may open.
		return SQLiteTarget{}, fmt.Errorf(
			"%w: %q begins with ':' but is not the in-memory database, and SQLite reserves such names; write it as ./%s if it is a file",
			ErrUnsupportedDSN, name, strings.TrimPrefix(name, ":"))
	}
	canonical, anchor, err := resolveDestinationFile(name)
	if err != nil {
		return SQLiteTarget{}, err
	}
	// The canonical path goes back into the SAME grammar the caller used. It cannot
	// be re-spelled as a `file:` URI: that would make SQLite start interpreting a
	// query it has never seen for this DSN, so an inert `?mode=ro` would suddenly
	// open the destination read-only.
	if strings.ContainsRune(canonical, '?') {
		return SQLiteTarget{}, fmt.Errorf(
			"%w: the resolved destination %q contains '?', which the driver's plain-name grammar reads as the start of a query; name it as a file: URI with %%3f instead",
			ErrUnsupportedDSN, canonical)
	}
	return SQLiteTarget{input: dsn, canonical: canonical, driverDSN: canonical + query.suffix(), anchor: anchor}, nil
}

// resolveSQLiteURI handles a `file:` DSN, which the driver hands to SQLite whole
// with SQLITE_OPEN_URI set.
//
// The path scan below is sqlite3ParseUri's: it stops at a RAW '?' or '#', and a
// `%HH` is decoded into a plain byte that stops nothing. That is why `%3f` names a
// file with a question mark in it and `%23` one with a hash, and why a query VALUE
// containing either character never turns the destination into something else.
func resolveSQLiteURI(dsn string, query dsnQuery) (SQLiteTarget, error) {
	rest := strings.TrimPrefix(dsn, "file:")

	if strings.HasPrefix(rest, "//") {
		authority := rest[2:]
		if i := strings.IndexByte(authority, '/'); i >= 0 {
			rest = authority[i:]
			authority = authority[:i]
		} else {
			rest = ""
		}
		// SQLite accepts exactly two authorities and rejects everything else with
		// "invalid uri authority". A remote-looking authority is refused HERE so the
		// diagnosis names the destination rather than arriving as an open failure.
		if authority != "" && authority != "localhost" {
			return SQLiteTarget{}, fmt.Errorf(
				"%w: the URI authority %q is not a local one; SQLite accepts only an empty authority (file:///path) or file://localhost/path",
				ErrUnsupportedDSN, authority)
		}
	}

	// A fragment is refused rather than interpreted. SQLite ends the path at a raw
	// '#' while the pinned driver's own query split does not know about '#' at all,
	// so with a fragment present the two disagree about where the query begins —
	// and a destination whose two readers disagree is not a provable destination.
	if i := strings.IndexByte(rest, '#'); i >= 0 {
		return SQLiteTarget{}, fmt.Errorf(
			"%w: the URI carries a '#' fragment, which SQLite cuts the destination at and the driver's query split does not see; write %%23 for a literal hash",
			ErrUnsupportedDSN)
	}

	rawPath := rest
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		rawPath = rest[:i]
	}
	path, err := decodeURIPath(rawPath)
	if err != nil {
		return SQLiteTarget{}, err
	}

	memory, err := uriDeclaresMemory(query.raw)
	if err != nil {
		return SQLiteTarget{}, err
	}
	if path == ":memory:" {
		// `file::memory:` — the documented URI spelling of the in-memory database.
		memory = true
	}
	if memory {
		return SQLiteTarget{input: dsn, memory: true, driverDSN: dsn}, nil
	}
	if path == "" {
		// `file:` and `file://localhost` name SQLite's anonymous TEMPORARY database:
		// a file the library creates, names nowhere and deletes on close. It is
		// neither a memory target nor a destination with an identity to fence, so it
		// is refused rather than silently opened unfenced.
		return SQLiteTarget{}, fmt.Errorf(
			"%w: the URI names no path, which opens an anonymous temporary database; name :memory: for an in-memory store or a file for a durable one",
			ErrUnsupportedDSN)
	}
	canonical, anchor, cerr := resolveDestinationFile(path)
	if cerr != nil {
		return SQLiteTarget{}, cerr
	}
	driverDSN := "file:" + escapeURIPath(canonical) + query.suffix()
	return SQLiteTarget{input: dsn, canonical: canonical, driverDSN: driverDSN, anchor: anchor}, nil
}

// decodeURIPath applies sqlite3ParseUri's percent-decoding to a URI path: exactly
// once, over `%HH` only, leaving every other byte — including '+' — alone.
//
// '+' matters because Go's url.QueryUnescape would turn it into a space and SQLite
// does not: a destination named `a+b` decoded as `a b` is a different file.
func decodeURIPath(raw string) (string, error) {
	var out strings.Builder
	out.Grow(len(raw))
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c != '%' {
			out.WriteByte(c)
			continue
		}
		if i+2 >= len(raw) || !isHexDigit(raw[i+1]) || !isHexDigit(raw[i+2]) {
			// SQLite would keep the stray '%' literal. This build refuses instead:
			// the two readings ("a percent sign" and "a typo in an escape") name
			// different files, and guessing between them is how the fenced path and
			// the opened path stop being the same file.
			return "", fmt.Errorf(
				"%w: the URI path contains '%%' at offset %d without two hexadecimal digits after it; write %%25 for a literal percent sign",
				ErrUnsupportedDSN, i)
		}
		octet := hexValue(raw[i+1])<<4 | hexValue(raw[i+2])
		if octet == 0 {
			// SQLite's non-error handling of %00 DISCARDS the rest of the path, so
			// two visibly different DSNs would name the same file. Refuse it.
			return "", fmt.Errorf("%w: the URI path contains %%00, which truncates the destination SQLite opens", ErrUnsupportedDSN)
		}
		out.WriteByte(byte(octet))
		i += 2
	}
	return out.String(), nil
}

func isHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

func hexValue(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	default:
		return int(c-'A') + 10
	}
}

// escapeURIPath renders a canonical absolute path back into a URI path that SQLite
// decodes to exactly those bytes. Only the three characters that would otherwise
// change the parse are escaped, so an operator reading a log still recognizes the
// path they typed.
func escapeURIPath(path string) string {
	var out strings.Builder
	out.Grow(len(path) + 8)
	for i := 0; i < len(path); i++ {
		switch c := path[i]; c {
		case '%':
			out.WriteString("%25")
		case '?':
			out.WriteString("%3f")
		case '#':
			out.WriteString("%23")
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}

// uriDeclaresMemory reads the EFFECTIVE mode out of the supported URI grammar.
//
// It is a parse and not a substring test. `strings.Contains(dsn, "mode=memory")` —
// which is what the store did — is true of `/var/lib/olivares/db?_pragma=x&note=mode=memory`
// and of a file literally named `mode=memory`, and in both cases it declared a real
// on-disk database to be in-memory and left it unfenced.
//
// ⛔ REPETITION IS A CONFLICT ONLY FOR AN IDENTITY PARAMETER, and applying the rule
// to every key was a measured refusal of a valid DSN. The pinned driver collects
// `_pragma` into a LIST and executes every one of them
// (`sqlite.go:213-237`), so `?_pragma=application_id(723)&_pragma=user_version(419)`
// applies BOTH to the SAME database — verified against the driver. Reading two
// `_pragma` values as "the destination depends on which one SQLite happens to read"
// confused a repeatable connection option with a parameter that selects a file:
// nothing in the driver option set names a destination, and the identity of the
// database is decided entirely by the path and the keys in uriIdentityParams.
func uriDeclaresMemory(rawQuery string) (bool, error) {
	if rawQuery == "" {
		return false, nil
	}
	memory := false
	seen := map[string]string{}
	for _, pair := range strings.Split(rawQuery, "&") {
		if pair == "" {
			continue
		}
		key, value, _ := strings.Cut(pair, "=")
		// SQLite decodes %HH in keys and values too; a key that needs decoding is not
		// one of the identity parameters this build understands, so decoding failures
		// here are reported the same way as in the path.
		decodedKey, err := decodeURIPath(key)
		if err != nil {
			return false, fmt.Errorf("%w: in the URI query parameter %q", err, key)
		}
		decodedValue, err := decodeURIPath(value)
		if err != nil {
			return false, fmt.Errorf("%w: in the value of URI query parameter %q", err, decodedKey)
		}
		if uriIdentityParams[decodedKey] {
			if prev, dup := seen[decodedKey]; dup && prev != decodedValue {
				return false, fmt.Errorf(
					"%w: the URI gives the identity parameter %q two different values (%q and %q), so the destination it names depends on which one SQLite happens to read",
					ErrUnsupportedDSN, decodedKey, prev, decodedValue)
			}
			seen[decodedKey] = decodedValue
		}

		switch decodedKey {
		case "mode":
			if !uriModeValues[decodedValue] {
				return false, fmt.Errorf("%w: mode=%q is not a supported SQLite open mode", ErrUnsupportedDSN, decodedValue)
			}
			if decodedValue == "memory" {
				memory = true
			}
		case "cache":
			if !uriCacheValues[decodedValue] {
				return false, fmt.Errorf("%w: cache=%q is not a supported SQLite cache mode", ErrUnsupportedDSN, decodedValue)
			}
		case "psow":
			if !uriPsowValues[decodedValue] {
				return false, fmt.Errorf("%w: psow=%q is not a supported value", ErrUnsupportedDSN, decodedValue)
			}
		case "nolock", "immutable":
			// Both change what locking SQLite performs on the destination, and
			// `immutable` additionally promises the file will not change while it is
			// open. Neither is a mode this coordination contract can honor, and
			// accepting them silently would fence a file whose own writer had been
			// told nobody else would touch it.
			return false, fmt.Errorf(
				"%w: %s=%s alters SQLite's locking of the destination, which this restore coordination cannot honor",
				ErrUnsupportedDSN, decodedKey, decodedValue)
		case "vfs":
			// Already refused above; repeated so this switch is exhaustive over the
			// identity parameters rather than relying on a distant check.
			return false, fmt.Errorf("%w: the URI selects a custom VFS", ErrUnsupportedDSN)
		default:
			if uriDriverOptions[decodedKey] {
				continue
			}
			return false, fmt.Errorf(
				"%w: the URI query parameter %q is not one this build recognizes, and an unrecognized parameter may retarget the destination",
				ErrUnsupportedDSN, decodedKey)
		}
	}
	return memory, nil
}

// resolveDestinationFile freezes a file destination and builds its anchor.
//
// Everything the caller passed after this point is diagnostics: the frozen path is
// the destination, and it is never derived from the spelling a second time.
func resolveDestinationFile(path string) (canonical string, anchor Anchor, err error) {
	canonical, err = FreezeDestinationPath(path)
	if err != nil {
		return "", Anchor{}, err
	}
	return canonical, anchorAtResolvedPath(canonical), nil
}

// FreezeDestinationPath resolves one file path to the absolute, symlink-resolved
// pathname that IS the destination, or refuses.
//
// It is exported because the console's promotion path fences the file it is about
// to replace by PATH rather than by DSN, and it must land on the same identity this
// resolver produces or the two would fence different files again.
//
// The final component is resolved as well as the parents. That is the whole of
// F3-IR-2: `AnchorForStoreFile` used to resolve only the directory and keep the
// basename, so a symlink named `alias.sqlite` pointing at `real db.sqlite` produced
// an anchor for `alias.sqlite` while the driver opened `real db.sqlite`.
//
// ⛔ AND IT RESOLVES IN FILESYSTEM ORDER, NOT LEXICALLY. This function used to run
// `filepath.Clean` over the path first, and that single call CHANGED THE
// DESTINATION of a valid DSN — measured through the public store constructor, not
// argued from the source:
//
// With `A/link` a symbolic link to `B/child`, the spelling `A/link/../dot.db` is
// resolved by the kernel — and therefore by the pinned driver — one component at a
// time: `A/link` becomes `B/child`, `..` is applied to THAT, giving `B`, and the
// destination is `B/dot.db`. `filepath.Clean` erases the pair `link/..` as text
// BEFORE any symlink is followed and answers `A/dot.db`. The consequence was not an
// unequal string: the store opened `A/dot.db`, CREATED it, and could not see the
// marker table in the database the caller had named. Fencing `A` consistently would
// not have helped — a fence on the wrong file is not a fence.
//
// So the parent is handed to filepath.EvalSymlinks, which walks components in the
// order the kernel does and applies `..` to what it has already resolved, and the
// final component is attached to that answer afterwards. Nothing lexical happens to
// the caller's spelling anywhere on this path, including the join that makes a
// relative DSN absolute.
func FreezeDestinationPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("%w: the destination path is empty", ErrUnsupportedDSN)
	}
	if strings.ContainsRune(path, 0) {
		return "", fmt.Errorf("%w: the destination path contains a NUL byte", ErrUnsupportedDSN)
	}
	abs, err := absoluteSpelling(path)
	if err != nil {
		return "", fmt.Errorf("%w: the destination %q is relative and %v", ErrUnsupportedDSN, path, err)
	}
	parent, base, err := splitFinalComponent(abs)
	if err != nil {
		return "", err
	}

	// The PARENT is resolved in filesystem order, which is where a symlink followed
	// by `..` is decided. It must exist: a destination whose directory is not there
	// is not a destination this build can fence, whether the file itself exists or
	// not.
	resolvedParent, perr := filepath.EvalSymlinks(parent)
	if perr != nil {
		return "", fmt.Errorf(
			"%w: the directory %q of %q could not be resolved, so there is no provable destination to fence: %v",
			ErrUnsupportedDSN, parent, abs, perr)
	}
	pinfo, serr := os.Stat(resolvedParent)
	if serr != nil {
		return "", fmt.Errorf("%w: the directory %q could not be inspected: %v", ErrUnsupportedDSN, resolvedParent, serr)
	}
	if !pinfo.IsDir() {
		return "", fmt.Errorf("%w: %q is not a directory, so %q cannot be a database in it", ErrUnsupportedDSN, resolvedParent, base)
	}

	// base is one component with no separator in it, so this join adds a separator
	// and nothing else — there is no dot segment left for Clean to act on.
	frozen := filepath.Join(resolvedParent, base)
	_, lerr := os.Lstat(frozen)
	switch {
	case lerr == nil:
		// It exists: resolve the final component too, and apply the checks a
		// pathname-keyed control needs in order to be honest about what it fences.
		return freezeExistingPath(frozen)
	case errors.Is(lerr, os.ErrNotExist):
		// The ordinary first boot, and the destination a restore creates. The
		// canonical pathname under the resolved parent is what gets fenced BEFORE
		// anything creates the database.
		return frozen, nil
	default:
		return "", fmt.Errorf("%w: %q could not be inspected: %v", ErrUnsupportedDSN, frozen, lerr)
	}
}

// absoluteSpelling makes a path absolute WITHOUT normalizing it.
//
// filepath.Join is deliberately not used: it cleans, and cleaning is what erased
// the symlink/`..` pair above. The working directory is read ONCE, here, so a
// relative destination cannot resolve differently in two places; everything after
// this point works on the frozen answer.
func absoluteSpelling(path string) (string, error) {
	if filepath.IsAbs(path) {
		return path, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("the working directory could not be read: %v", err)
	}
	if !filepath.IsAbs(cwd) {
		// Getwd is documented to return an absolute path. If it ever did not, an
		// anchor built from it would be a registry key another directory could
		// collide with, so this refuses instead of guessing.
		return "", fmt.Errorf("the working directory %q is not absolute, so no absolute destination can be built from it", cwd)
	}
	if len(cwd) > 0 && os.IsPathSeparator(cwd[len(cwd)-1]) {
		return cwd + path, nil
	}
	return cwd + string(filepath.Separator) + path, nil
}

// splitFinalComponent cuts an absolute spelling at its LAST separator.
//
// The ONLY thing it normalizes is a run of trailing separators, and that is the
// driver's own behavior rather than a convenience: measured against the pinned
// driver, `<file>/`, `file:<file>/` and `<file>//` all open `<file>` and report it
// as `main` in PRAGMA database_list, because SQLite strips them before it opens.
// Removing an EMPTY final component cannot change which directory a `..` later
// applies to, which is what makes this safe where filepath.Clean was not.
//
// A spelling whose final component is `.` or `..` — or which is nothing but the
// root — names a directory, not a database file, and is refused here rather than
// quietly turned into some other file's name.
func splitFinalComponent(abs string) (parent, base string, err error) {
	end := len(abs)
	for end > 0 && os.IsPathSeparator(abs[end-1]) {
		end--
	}
	if end <= len(filepath.VolumeName(abs)) {
		return "", "", fmt.Errorf("%w: %q names the filesystem root rather than a database file", ErrUnsupportedDSN, abs)
	}
	trimmed := abs[:end]

	i := end - 1
	for i >= 0 && !os.IsPathSeparator(trimmed[i]) {
		i--
	}
	if i < 0 {
		// Unreachable for an absolute path; refused rather than assumed.
		return "", "", fmt.Errorf("%w: %q has no directory component", ErrUnsupportedDSN, abs)
	}
	base = trimmed[i+1:]
	parent = trimmed[:i]
	if vol := filepath.VolumeName(abs); len(parent) <= len(vol) {
		// The parent IS the root: keep its separator, because "" and "C:" are not
		// paths.
		parent = trimmed[:i+1]
	}
	switch base {
	case "", ".", "..":
		return "", "", fmt.Errorf("%w: %q names a directory rather than a database file", ErrUnsupportedDSN, abs)
	}
	return parent, base, nil
}

// freezeExistingPath resolves a destination that is on disk right now: every
// symlink including the final one, then the checks a pathname-keyed control needs
// in order to be honest about what it fences.
func freezeExistingPath(path string) (string, error) {
	info, lerr := os.Lstat(path)
	if lerr != nil {
		return "", fmt.Errorf("%w: %q could not be inspected: %v", ErrUnsupportedDSN, path, lerr)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%w: %q is a symbolic link that does not resolve to an existing file: %v", ErrUnsupportedDSN, path, err)
		}
		return "", fmt.Errorf("%w: %q could not be resolved: %v", ErrUnsupportedDSN, path, err)
	}
	target, serr := os.Stat(resolved)
	if serr != nil {
		return "", fmt.Errorf("%w: %q resolved to %q, which could not be inspected: %v", ErrUnsupportedDSN, path, resolved, serr)
	}
	if !target.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %q resolves to %q, which is not a regular file (%s)", ErrUnsupportedDSN, path, resolved, target.Mode().Type())
	}
	if links, known := hardLinkCount(target); known && links > 1 {
		// A sidecar keyed by pathname cannot enumerate an inode's other names, so a
		// second name for this database would be a completely separate lock. The
		// honest answer is to refuse the destination rather than fence one of its
		// names and describe that as coverage.
		return "", fmt.Errorf(
			"%w: %q has %d names; give the restore-coordinated database a single name",
			ErrHardLinked, resolved, links)
	}
	return resolved, nil
}
