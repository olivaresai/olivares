// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// postgres_administrative_authority_test.go — the bank for RA2-A1.
//
// The nine canonical vectors below are ASTRA ROOT'S, copied verbatim from
// assessments/product/retention-bound-authority/RA2-A1-CANONICAL-VECTORS.json
// (sha256 657246b8311823d479c5cce380db713f36c8872a972cd3774752a062e0479132). They
// were produced by an independent Python framing before this implementation existed
// and were independently recomputed by the construction's reviewer. NOTHING here
// derives an expected canonical byte string or ID from the code under test: a bank
// that asks the implementation what the answer is only proves the implementation
// agrees with itself.
//
// The raw JSON documents, by contrast, are writer-authored inputs — the vector file
// carries parsed documents, not bytes. That is sound because the expected results
// they are checked against remain root's.

// The literals whose exact bytes matter are gathered here, so the rest of the bank
// stays plain ASCII and a reviewer can see every risky byte in one place.
const (
	// Exact characters and bytes, spelled with Go escapes.
	// U+00E9, the composed spelling: two UTF-8 bytes.
	adminDeclNFCName = "\u00e9"
	// U+0065 U+0301, the decomposed spelling of the same letter: three bytes.
	adminDeclNFDName = "e\u0301"
	// A supplementary-plane code point, four UTF-8 bytes.
	adminDeclEmojiName = "dba_\U0001F600"
	adminDeclEmojiRune = "\U0001F600"
	// U+FEFF is not JSON whitespace and is not an alternative syntax.
	adminDeclBOM = "\ufeff"
	// U+00A0 is a space to a reader and not to RFC 8259.
	adminDeclNBSP        = "\u00a0"
	adminDeclNUL         = "\x00"
	adminDeclControlSOH  = "\x01"
	adminDeclVerticalTab = "\v"
	adminDeclFormFeed    = "\f"
	adminDeclLineFeed    = "\n"
	// All four bytes RFC 8259 accepts as whitespace, in one run.
	adminDeclSpaced      = " \t\r\n"
	adminDeclInvalidUTF8 = "\xff"
	// The lead byte of a two-byte sequence with its continuation missing.
	adminDeclCutUTF8 = "\xc3"
	// A surrogate code point encoded as if it were a scalar value.
	adminDeclSurrogateUTF8 = "\xed\xa0\xbd"

	// JSON escape sequences, carried verbatim into a document as text.
	adminDeclJSONNFC = `\u00e9`
	// JSON hexadecimal is case-insensitive.
	adminDeclJSONNFCUpper = `\u00E9`
	adminDeclJSONNFD      = `e\u0301`
	// A valid surrogate pair.
	adminDeclJSONEmoji      = `\ud83d\ude00`
	adminDeclJSONEmojiUpper = `\uD83D\uDE00`
	// The high half on its own.
	adminDeclJSONHigh = `\ud83d`
	// The low half on its own.
	adminDeclJSONLow      = `\ude00`
	adminDeclJSONSpace    = `\u0020`
	adminDeclJSONCapitalA = `\u0041`
	adminDeclJSONNULName  = `estate\u0000dba`
	// The schema member, first letter escaped.
	adminDeclJSONSchemaKey = `\u0073chema`
	adminDeclJSONRolesKey  = `\u0072oles`
	adminDeclJSONNameKey   = `\u006eame`
	adminDeclJSONPinKey    = `\u0065xpected_oid`
	adminDeclJSONEstate    = `\u0065state_dba`
	adminDeclJSONEstateA   = `estate_db\u0061`
	// Fewer than four hexadecimal digits.
	adminDeclJSONShortEsc = `\u12`
	adminDeclJSONBadHex   = `\u00zz`
	// Not a JSON escape at all.
	adminDeclJSONUnknownEsc = `\x41`
)

// adminDeclSchemaLiteral is spelled out rather than taken from the production
// constant, so renaming that constant cannot silently move the test inputs with it.
// TestPostgresAdministrativeAuthoritySchemaConstant pins the two together.
const adminDeclSchemaLiteral = "olivares.postgres.administrative-authority.v1"

// adminDeclEmptyID is root's ID for a declaration with no roles. Every spelling of
// "no additional administrators" has to reach exactly this value.
const adminDeclEmptyID = "sha256:faf44269d6aede77103160d7ff94a72a78f20bd1f36a07e41add2b507196214e"

// adminDeclUnpinnedID and adminDeclPinnedID are root's one-role vectors, reused by
// the distinction and escape banks.
const (
	adminDeclUnpinnedID      = "sha256:e32fd3970c8ecf6ba3a9bddba6ccb8beaf4c2abf6febe75f18436396e4d0fcbe"
	adminDeclPinnedID        = "sha256:7e8c8139e86407ba35d1727c69d7e3f0b1172db737ff1fbbd4c9421ba1c6f2bd"
	adminDeclSortedPairID    = "sha256:b57defd209fe187f852918ca4ce43c4b93c581e2c48fcacc4aa3f92bdb12ee5e"
	adminDeclComposedID      = "sha256:4dd4ab40b6d23ffb02737b383b48eebc1eb863993019d2f969a933b2ac218fc7"
	adminDeclDecomposedID    = "sha256:94968d5ddc0ac84470219738e130f73ab26f89b4f2bca1baee19500ef2afb26d"
	adminDeclSupplementaryID = "sha256:07fca4d2c86357dcdeb077395a45e8493ecd223a5a9724b0e508eb5d4582df26"
	adminDeclSpaceNameID     = "sha256:c3cdc5703fb556d0aa7017da8371ae812d08d294bdcb1d7c90ccd9b4ccb4d370"
	adminDeclCaseID          = "sha256:874befc2f295258adb23f2132a0b98efe34c56bce842a865e41aa2d7d4784c1a"
)

// adminDeclDocument wraps a roles-array body in the accepted document shape.
func adminDeclDocument(rolesBody string) string {
	return adminDeclDocumentWithSchema(adminDeclSchemaLiteral, rolesBody)
}

func adminDeclDocumentWithSchema(schema, rolesBody string) string {
	return `{"schema":"` + schema + `","roles":[` + rolesBody + `]}`
}

// adminDeclNamed builds a one-role body carrying the exact name bytes given.
func adminDeclNamed(name string) string {
	return `{"name":"` + name + `"}`
}

// adminDeclPin returns a pointer to a fresh copy of value, so a table row can hand
// the constructor a pin without sharing storage with any other row.
func adminDeclPin(value uint32) *uint32 {
	return &value
}

// adminDeclRolesEqual compares roles by name and pin VALUE. Pointer equality is the
// wrong oracle here: two copies of one declaration hold different pointers on
// purpose, so == on the struct would report every correct copy as unequal.
func adminDeclRolesEqual(a, b []PostgresAdministrativeRole) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name {
			return false
		}
		switch {
		case a[i].ExpectedOID == nil && b[i].ExpectedOID == nil:
		case a[i].ExpectedOID == nil || b[i].ExpectedOID == nil:
			return false
		case *a[i].ExpectedOID != *b[i].ExpectedOID:
			return false
		}
	}
	return true
}

func adminDeclRolesText(roles []PostgresAdministrativeRole) string {
	if roles == nil {
		return "nil"
	}
	parts := make([]string, 0, len(roles))
	for _, role := range roles {
		pin := "pin=absent"
		if role.ExpectedOID != nil {
			pin = "pin=" + strconv.FormatUint(uint64(*role.ExpectedOID), 10)
		}
		parts = append(parts, "{"+strconv.Quote(role.Name)+" "+pin+"}")
	}
	return "[" + strings.Join(parts, " ") + "]"
}

func mustNewAdminDecl(t *testing.T, roles []PostgresAdministrativeRole) PostgresAdministrativeAuthority {
	t.Helper()
	authority, err := NewPostgresAdministrativeAuthority(roles)
	if err != nil {
		t.Fatalf("NewPostgresAdministrativeAuthority(%s) = error %v, want a value", adminDeclRolesText(roles), err)
	}
	return authority
}

func mustParseAdminDecl(t *testing.T, document string) PostgresAdministrativeAuthority {
	t.Helper()
	authority, err := ParsePostgresAdministrativeAuthority([]byte(document))
	if err != nil {
		t.Fatalf("ParsePostgresAdministrativeAuthority(%q) = error %v, want a value", document, err)
	}
	return authority
}

func adminDeclErrorFromNew(roles []PostgresAdministrativeRole) error {
	_, err := NewPostgresAdministrativeAuthority(roles)
	return err
}

func adminDeclErrorFromParse(document string) error {
	_, err := ParsePostgresAdministrativeAuthority([]byte(document))
	return err
}

// adminDeclRootVector is one of root's nine vectors plus the writer-authored
// document that must resolve to it.
type adminDeclRootVector struct {
	name string
	// roles is the constructor input in the ROOT DOCUMENT's order, which for
	// sorted-pair is not canonical order: the constructor must sort it.
	roles        []PostgresAdministrativeRole
	document     string
	canonicalHex string
	id           string
}

// adminDeclRootVectors returns fresh values on every call: rows carry pointers and
// several banks deliberately write through the ones they were handed.
func adminDeclRootVectors() []adminDeclRootVector {
	return []adminDeclRootVector{
		{
			name:         "empty",
			roles:        nil,
			document:     adminDeclDocument(``),
			canonicalHex: "6f6c6976617265732e706f7374677265732e61646d696e6973747261746976652d617574686f726974792e7631000000000000000000",
			id:           adminDeclEmptyID,
		},
		{
			name:         "one-unpinned",
			roles:        []PostgresAdministrativeRole{{Name: "estate_dba"}},
			document:     adminDeclDocument(`{"name":"estate_dba"}`),
			canonicalHex: "6f6c6976617265732e706f7374677265732e61646d696e6973747261746976652d617574686f726974792e7631000000000000000001000000000000000a6573746174655f64626100",
			id:           adminDeclUnpinnedID,
		},
		{
			name:         "one-pinned",
			roles:        []PostgresAdministrativeRole{{Name: "estate_dba", ExpectedOID: adminDeclPin(16394)}},
			document:     adminDeclDocument(`{"name":"estate_dba","expected_oid":16394}`),
			canonicalHex: "6f6c6976617265732e706f7374677265732e61646d696e6973747261746976652d617574686f726974792e7631000000000000000001000000000000000a6573746174655f646261010000400a",
			id:           adminDeclPinnedID,
		},
		{
			name: "sorted-pair",
			roles: []PostgresAdministrativeRole{
				{Name: "recovery_dba", ExpectedOID: adminDeclPin(4294967295)},
				{Name: "estate_dba"},
			},
			document:     adminDeclDocument(`{"name":"recovery_dba","expected_oid":4294967295},{"name":"estate_dba"}`),
			canonicalHex: "6f6c6976617265732e706f7374677265732e61646d696e6973747261746976652d617574686f726974792e7631000000000000000002000000000000000a6573746174655f64626100000000000000000c7265636f766572795f64626101ffffffff",
			id:           adminDeclSortedPairID,
		},
		{
			name:         "unicode-composed",
			roles:        []PostgresAdministrativeRole{{Name: adminDeclNFCName}},
			document:     adminDeclDocument(adminDeclNamed(adminDeclNFCName)),
			canonicalHex: "6f6c6976617265732e706f7374677265732e61646d696e6973747261746976652d617574686f726974792e76310000000000000000010000000000000002c3a900",
			id:           adminDeclComposedID,
		},
		{
			name:         "unicode-decomposed",
			roles:        []PostgresAdministrativeRole{{Name: adminDeclNFDName}},
			document:     adminDeclDocument(adminDeclNamed(adminDeclNFDName)),
			canonicalHex: "6f6c6976617265732e706f7374677265732e61646d696e6973747261746976652d617574686f726974792e7631000000000000000001000000000000000365cc8100",
			id:           adminDeclDecomposedID,
		},
		{
			name:         "supplementary",
			roles:        []PostgresAdministrativeRole{{Name: adminDeclEmojiName, ExpectedOID: adminDeclPin(1)}},
			document:     adminDeclDocument(`{"name":"` + adminDeclEmojiName + `","expected_oid":1}`),
			canonicalHex: "6f6c6976617265732e706f7374677265732e61646d696e6973747261746976652d617574686f726974792e763100000000000000000100000000000000086462615ff09f98800100000001",
			id:           adminDeclSupplementaryID,
		},
		{
			name:         "space-name",
			roles:        []PostgresAdministrativeRole{{Name: " "}},
			document:     adminDeclDocument(`{"name":" "}`),
			canonicalHex: "6f6c6976617265732e706f7374677265732e61646d696e6973747261746976652d617574686f726974792e763100000000000000000100000000000000012000",
			id:           adminDeclSpaceNameID,
		},
		{
			name:         "case",
			roles:        []PostgresAdministrativeRole{{Name: "Estate_DBA"}},
			document:     adminDeclDocument(`{"name":"Estate_DBA"}`),
			canonicalHex: "6f6c6976617265732e706f7374677265732e61646d696e6973747261746976652d617574686f726974792e7631000000000000000001000000000000000a4573746174655f44424100",
			id:           adminDeclCaseID,
		},
	}
}

func TestPostgresAdministrativeAuthoritySchemaConstant(t *testing.T) {
	if administrativeAuthoritySchema != adminDeclSchemaLiteral {
		t.Fatalf("administrativeAuthoritySchema = %q, want %q: the domain is part of the canonical bytes, so changing it is a versioned identity change and not a refactor",
			administrativeAuthoritySchema, adminDeclSchemaLiteral)
	}
}

// TestPostgresAdministrativeAuthorityRootVectors is the primary oracle: both entry
// points must reproduce root's canonical bytes and root's ID for all nine vectors.
func TestPostgresAdministrativeAuthorityRootVectors(t *testing.T) {
	vectors := adminDeclRootVectors()
	if len(vectors) != 9 {
		t.Fatalf("root vector table holds %d rows, want the 9 ratified vectors", len(vectors))
	}
	seen := make(map[string]string, len(vectors))
	for _, vector := range vectors {
		t.Run(vector.name, func(t *testing.T) {
			constructed := mustNewAdminDecl(t, vector.roles)
			if got := hex.EncodeToString(constructed.canonicalBytes()); got != vector.canonicalHex {
				t.Errorf("constructor canonical bytes = %s, want root's %s", got, vector.canonicalHex)
			}
			if got := constructed.ID(); got != vector.id {
				t.Errorf("constructor ID = %s, want root's %s", got, vector.id)
			}
			parsed := mustParseAdminDecl(t, vector.document)
			if got := hex.EncodeToString(parsed.canonicalBytes()); got != vector.canonicalHex {
				t.Errorf("JSON canonical bytes = %s, want root's %s", got, vector.canonicalHex)
			}
			if got := parsed.ID(); got != vector.id {
				t.Errorf("JSON ID = %s, want root's %s (document %q)", got, vector.id, vector.document)
			}
			if !adminDeclRolesEqual(parsed.Roles(), constructed.Roles()) {
				t.Errorf("JSON roles %s and constructor roles %s disagree",
					adminDeclRolesText(parsed.Roles()), adminDeclRolesText(constructed.Roles()))
			}
		})
		if previous, duplicate := seen[vector.id]; duplicate {
			t.Errorf("vectors %q and %q share ID %s; root's nine vectors are pairwise distinct", previous, vector.name, vector.id)
		}
		seen[vector.id] = vector.name
	}
}

// TestPostgresAdministrativeAuthorityIDShape checks the published spelling of the
// identity: the sha256: domain plus 64 lowercase hexadecimal digits.
func TestPostgresAdministrativeAuthorityIDShape(t *testing.T) {
	for _, vector := range adminDeclRootVectors() {
		id := mustNewAdminDecl(t, vector.roles).ID()
		digits, found := strings.CutPrefix(id, "sha256:")
		if !found {
			t.Fatalf("%s: ID = %q, want a sha256: prefix", vector.name, id)
		}
		if len(digits) != 64 {
			t.Errorf("%s: ID carries %d hexadecimal digits, want 64", vector.name, len(digits))
		}
		if digits != strings.ToLower(digits) {
			t.Errorf("%s: ID digits are not lowercase: %q", vector.name, digits)
		}
		if _, err := hex.DecodeString(digits); err != nil {
			t.Errorf("%s: ID digits do not decode as hexadecimal: %v", vector.name, err)
		}
	}
}

// TestPostgresAdministrativeAuthorityEmptySemantics pins every spelling of "no
// additional administrators" onto root's one nonempty identity, and pins Roles to
// nil for all of them.
func TestPostgresAdministrativeAuthorityEmptySemantics(t *testing.T) {
	empties := map[string]PostgresAdministrativeAuthority{
		"zero value":              {},
		"nil slice":               mustNewAdminDecl(t, nil),
		"empty slice":             mustNewAdminDecl(t, []PostgresAdministrativeRole{}),
		"empty JSON roles array":  mustParseAdminDecl(t, adminDeclDocument(``)),
		"empty array, spaced out": mustParseAdminDecl(t, `{ "schema" : "`+adminDeclSchemaLiteral+`" , "roles" : [ ] }`),
	}
	for name, authority := range empties {
		if got := authority.ID(); got != adminDeclEmptyID {
			t.Errorf("%s: ID = %s, want root's empty ID %s", name, got, adminDeclEmptyID)
		}
		if roles := authority.Roles(); roles != nil {
			t.Errorf("%s: Roles() = %s, want nil", name, adminDeclRolesText(roles))
		}
	}
}

// TestPostgresAdministrativeAuthorityOrderAndWhitespaceInvariance proves that role
// order, member order and RFC 8259 whitespace do not reach the canonical bytes,
// checked against root's sorted-pair ID rather than against another run of the code.
func TestPostgresAdministrativeAuthorityOrderAndWhitespaceInvariance(t *testing.T) {
	documents := map[string]string{
		"document order, recovery first": adminDeclDocument(`{"name":"recovery_dba","expected_oid":4294967295},{"name":"estate_dba"}`),
		"document order, estate first":   adminDeclDocument(`{"name":"estate_dba"},{"name":"recovery_dba","expected_oid":4294967295}`),
		"roles member before schema":     `{"roles":[{"name":"estate_dba"},{"name":"recovery_dba","expected_oid":4294967295}],"schema":"` + adminDeclSchemaLiteral + `"}`,
		"role members reordered":         adminDeclDocument(`{"expected_oid":4294967295,"name":"recovery_dba"},{"name":"estate_dba"}`),
		"all four RFC 8259 whitespaces": adminDeclSpaced + "{" + adminDeclSpaced + `"schema"` + adminDeclSpaced + ":" + adminDeclSpaced +
			`"` + adminDeclSchemaLiteral + `"` + adminDeclSpaced + "," + adminDeclSpaced + `"roles"` + adminDeclSpaced + ":" + adminDeclSpaced +
			"[" + adminDeclSpaced + "{" + adminDeclSpaced + `"name"` + adminDeclSpaced + ":" + adminDeclSpaced + `"estate_dba"` + adminDeclSpaced +
			"}" + adminDeclSpaced + "," + adminDeclSpaced + "{" + adminDeclSpaced + `"name"` + adminDeclSpaced + ":" + adminDeclSpaced +
			`"recovery_dba"` + adminDeclSpaced + "," + adminDeclSpaced + `"expected_oid"` + adminDeclSpaced + ":" + adminDeclSpaced +
			"4294967295" + adminDeclSpaced + "}" + adminDeclSpaced + "]" + adminDeclSpaced + "}" + adminDeclSpaced,
	}
	for name, document := range documents {
		if got := mustParseAdminDecl(t, document).ID(); got != adminDeclSortedPairID {
			t.Errorf("%s: ID = %s, want root's sorted-pair ID %s", name, got, adminDeclSortedPairID)
		}
	}

	constructorOrders := map[string][]PostgresAdministrativeRole{
		"canonical order": {
			{Name: "estate_dba"},
			{Name: "recovery_dba", ExpectedOID: adminDeclPin(4294967295)},
		},
		"reversed order": {
			{Name: "recovery_dba", ExpectedOID: adminDeclPin(4294967295)},
			{Name: "estate_dba"},
		},
	}
	for name, roles := range constructorOrders {
		if got := mustNewAdminDecl(t, roles).ID(); got != adminDeclSortedPairID {
			t.Errorf("%s: ID = %s, want root's sorted-pair ID %s", name, got, adminDeclSortedPairID)
		}
	}
}

// TestPostgresAdministrativeAuthorityCanonicalOrderIsBytewise checks that the order
// is unsigned UTF-8 bytes and not a case-insensitive or locale-aware collation: the
// underscore sorts between an uppercase and a lowercase ASCII letter, and a
// multi-byte name sorts after both.
func TestPostgresAdministrativeAuthorityCanonicalOrderIsBytewise(t *testing.T) {
	authority := mustNewAdminDecl(t, []PostgresAdministrativeRole{
		{Name: "a_dba"},
		{Name: adminDeclNFCName + "_dba"},
		{Name: "Z_dba"},
		{Name: "_dba"},
	})
	want := []string{"Z_dba", "_dba", "a_dba", adminDeclNFCName + "_dba"}
	got := make([]string, 0, len(want))
	for _, role := range authority.Roles() {
		got = append(got, role.Name)
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Roles() order = %q, want bytewise UTF-8 order %q", got, want)
	}
}

// TestPostgresAdministrativeAuthorityDistinctions checks the distinctions the
// identity must keep, anchored on root's own IDs.
func TestPostgresAdministrativeAuthorityDistinctions(t *testing.T) {
	unpinned := mustNewAdminDecl(t, []PostgresAdministrativeRole{{Name: "estate_dba"}}).ID()
	pinned := mustNewAdminDecl(t, []PostgresAdministrativeRole{{Name: "estate_dba", ExpectedOID: adminDeclPin(16394)}}).ID()
	otherPin := mustNewAdminDecl(t, []PostgresAdministrativeRole{{Name: "estate_dba", ExpectedOID: adminDeclPin(16395)}}).ID()
	uppercase := mustNewAdminDecl(t, []PostgresAdministrativeRole{{Name: "Estate_DBA"}}).ID()
	composed := mustNewAdminDecl(t, []PostgresAdministrativeRole{{Name: adminDeclNFCName}}).ID()
	decomposed := mustNewAdminDecl(t, []PostgresAdministrativeRole{{Name: adminDeclNFDName}}).ID()
	spaced := mustNewAdminDecl(t, []PostgresAdministrativeRole{{Name: " estate_dba"}}).ID()

	if unpinned != adminDeclUnpinnedID {
		t.Fatalf("unpinned ID = %s, want root's one-unpinned vector %s", unpinned, adminDeclUnpinnedID)
	}
	if pinned != adminDeclPinnedID {
		t.Fatalf("pinned ID = %s, want root's one-pinned vector %s", pinned, adminDeclPinnedID)
	}
	if composed != adminDeclComposedID || decomposed != adminDeclDecomposedID {
		t.Fatalf("Unicode IDs = %s and %s, want root's %s and %s", composed, decomposed, adminDeclComposedID, adminDeclDecomposedID)
	}
	if uppercase != adminDeclCaseID {
		t.Fatalf("uppercase ID = %s, want root's case vector %s", uppercase, adminDeclCaseID)
	}
	if unpinned == pinned {
		t.Error("an absent pin and a present pin share an ID; the presence byte is not reaching the digest")
	}
	if pinned == otherPin {
		t.Error("two different pin values share an ID; the pin value is not reaching the digest")
	}
	if unpinned == uppercase {
		t.Error("case is being folded; names are exact byte strings")
	}
	if composed == decomposed {
		t.Error("NFC and NFD spellings share an ID; names must not be normalized")
	}
	if unpinned == spaced {
		t.Error("leading whitespace is being trimmed; names are exact byte strings")
	}
}

// TestPostgresAdministrativeAuthorityFramingIsUnambiguous is the control for the
// length prefixes: without them, one two-role list and one one-role list could
// serialize to the same bytes.
func TestPostgresAdministrativeAuthorityFramingIsUnambiguous(t *testing.T) {
	joined := mustNewAdminDecl(t, []PostgresAdministrativeRole{{Name: "ab"}}).ID()
	split := mustNewAdminDecl(t, []PostgresAdministrativeRole{{Name: "a"}, {Name: "b"}}).ID()
	if joined == split {
		t.Error(`["ab"] and ["a","b"] share an ID; the name length prefix is not framing the names`)
	}
	pinned := mustNewAdminDecl(t, []PostgresAdministrativeRole{{Name: "a", ExpectedOID: adminDeclPin(1)}}).ID()
	if pinned == split || pinned == joined {
		t.Error("a pinned single role collides with a two-role declaration; the framing is ambiguous")
	}
	domainNamed := mustNewAdminDecl(t, []PostgresAdministrativeRole{{Name: adminDeclSchemaLiteral}}).ID()
	if domainNamed == adminDeclEmptyID {
		t.Error("a role named after the domain collides with the empty declaration")
	}
}

// TestPostgresAdministrativeAuthorityEscapedSpellingsConverge proves that an escaped
// spelling is the same declaration as its literal one, for role names and member
// names alike, and that a valid surrogate pair decodes to its exact UTF-8.
func TestPostgresAdministrativeAuthorityEscapedSpellingsConverge(t *testing.T) {
	cases := []struct {
		name     string
		document string
		want     string
	}{
		{"escaped composed name", adminDeclDocument(adminDeclNamed(adminDeclJSONNFC)), adminDeclComposedID},
		{"escaped composed name, uppercase hexadecimal", adminDeclDocument(adminDeclNamed(adminDeclJSONNFCUpper)), adminDeclComposedID},
		{"escaped decomposed name", adminDeclDocument(adminDeclNamed(adminDeclJSONNFD)), adminDeclDecomposedID},
		{"valid surrogate pair", adminDeclDocument(`{"name":"dba_` + adminDeclJSONEmoji + `","expected_oid":1}`), adminDeclSupplementaryID},
		{"valid surrogate pair, uppercase hexadecimal", adminDeclDocument(`{"name":"dba_` + adminDeclJSONEmojiUpper + `","expected_oid":1}`), adminDeclSupplementaryID},
		{"escaped space name", adminDeclDocument(adminDeclNamed(adminDeclJSONSpace)), adminDeclSpaceNameID},
		{"escaped ASCII name", adminDeclDocument(adminDeclNamed(adminDeclJSONEstate)), adminDeclUnpinnedID},
		{"escaped schema member name", `{"` + adminDeclJSONSchemaKey + `":"` + adminDeclSchemaLiteral + `","roles":[]}`, adminDeclEmptyID},
		{"escaped roles member name", `{"schema":"` + adminDeclSchemaLiteral + `","` + adminDeclJSONRolesKey + `":[]}`, adminDeclEmptyID},
		{"escaped name member name", adminDeclDocument(`{"` + adminDeclJSONNameKey + `":"estate_dba"}`), adminDeclUnpinnedID},
		{"escaped expected_oid member name", adminDeclDocument(`{"name":"estate_dba","` + adminDeclJSONPinKey + `":16394}`), adminDeclPinnedID},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := mustParseAdminDecl(t, testCase.document).ID(); got != testCase.want {
				t.Errorf("ID = %s, want %s", got, testCase.want)
			}
		})
	}
}

// TestPostgresAdministrativeAuthorityStringDecoding checks the decoded bytes
// directly, which is the oracle a digest comparison cannot give: an escape that
// decoded to the wrong character would still produce a stable, wrong ID.
func TestPostgresAdministrativeAuthorityStringDecoding(t *testing.T) {
	cases := []struct {
		name     string
		document string
		want     string
	}{
		{"quotation mark", adminDeclDocument(`{"name":"a\"b"}`), "a\"b"},
		{"reverse solidus", adminDeclDocument(`{"name":"a\\b"}`), "a\\b"},
		{"solidus", adminDeclDocument(`{"name":"a\/b"}`), "a/b"},
		{"backspace", adminDeclDocument(`{"name":"a\bb"}`), "a\bb"},
		{"form feed", adminDeclDocument(`{"name":"a\fb"}`), "a" + adminDeclFormFeed + "b"},
		{"line feed", adminDeclDocument(`{"name":"a\nb"}`), "a" + adminDeclLineFeed + "b"},
		{"carriage return", adminDeclDocument(`{"name":"a\rb"}`), "a\rb"},
		{"tab", adminDeclDocument(`{"name":"a\tb"}`), "a\tb"},
		{"basic multilingual plane escape", adminDeclDocument(adminDeclNamed(adminDeclJSONNFC)), adminDeclNFCName},
		{"combining sequence", adminDeclDocument(adminDeclNamed(adminDeclJSONNFD)), adminDeclNFDName},
		{"surrogate pair", adminDeclDocument(adminDeclNamed(adminDeclJSONEmoji)), adminDeclEmojiRune},
		{"ASCII escape", adminDeclDocument(adminDeclNamed(adminDeclJSONCapitalA)), "A"},
		{"literal multi-byte name", adminDeclDocument(adminDeclNamed(adminDeclNFCName)), adminDeclNFCName},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			roles := mustParseAdminDecl(t, testCase.document).Roles()
			if len(roles) != 1 {
				t.Fatalf("Roles() returned %d entries, want 1", len(roles))
			}
			if roles[0].Name != testCase.want {
				t.Errorf("decoded name = %q, want %q", roles[0].Name, testCase.want)
			}
		})
	}
}

// TestPostgresAdministrativeAuthorityDocumentRefusals is the JSON-language table.
// The accept rows are controls: without them a table like this passes even against
// a parser that refuses everything it is given.
func TestPostgresAdministrativeAuthorityDocumentRefusals(t *testing.T) {
	schema := adminDeclSchemaLiteral
	cases := []struct {
		name     string
		document string
		accept   bool
		want     string
	}{
		// Controls.
		{name: "control: empty roles array", document: adminDeclDocument(``), accept: true},
		{name: "control: one unpinned role", document: adminDeclDocument(`{"name":"estate_dba"}`), accept: true},
		{name: "control: one pinned role", document: adminDeclDocument(`{"name":"estate_dba","expected_oid":16394}`), accept: true},
		{name: "control: lowest pin", document: adminDeclDocument(`{"name":"estate_dba","expected_oid":1}`), accept: true},
		{name: "control: highest pin", document: adminDeclDocument(`{"name":"estate_dba","expected_oid":4294967295}`), accept: true},

		// The document is not one JSON object.
		{name: "empty bytes", document: ``, want: reasonNotObject},
		{name: "only whitespace", document: adminDeclSpaced, want: reasonNotObject},
		{name: "byte order mark before the object", document: adminDeclBOM + adminDeclDocument(``), want: reasonNotObject},
		{name: "NUL before the object", document: adminDeclNUL + adminDeclDocument(``), want: reasonNotObject},
		{name: "no-break space before the object", document: adminDeclNBSP + adminDeclDocument(``), want: reasonNotObject},
		{name: "vertical tab before the object", document: adminDeclVerticalTab + adminDeclDocument(``), want: reasonNotObject},
		{name: "top-level array", document: `[]`, want: reasonNotObject},
		{name: "top-level string", document: `"` + schema + `"`, want: reasonNotObject},
		{name: "top-level number", document: `1`, want: reasonNotObject},
		{name: "top-level null", document: `null`, want: reasonNotObject},

		// Trailing content.
		{name: "form feed after the object", document: adminDeclDocument(``) + adminDeclFormFeed, want: reasonTrailingContent},
		{name: "text after the object", document: adminDeclDocument(``) + "x", want: reasonTrailingContent},
		{name: "a second object after the first", document: adminDeclDocument(``) + adminDeclDocument(``), want: reasonTrailingContent},
		{name: "NUL after the object", document: adminDeclDocument(``) + adminDeclNUL, want: reasonTrailingContent},

		// Malformed or truncated.
		{name: "trailing comma after the last member", document: `{"schema":"` + schema + `","roles":[],}`, want: reasonMalformed},
		{name: "truncated after the roles bracket", document: `{"schema":"` + schema + `","roles":[`, want: reasonMalformed},
		{name: "truncated after a member name", document: `{"schema"`, want: reasonMalformed},
		{name: "missing colon", document: `{"schema" "` + schema + `","roles":[]}`, want: reasonMalformed},
		{name: "single-quoted member name", document: `{'schema':'x','roles':[]}`, want: reasonMalformed},
		{name: "unquoted member name", document: `{schema:"x","roles":[]}`, want: reasonMalformed},
		{name: "unterminated string", document: `{"schema":"` + schema + `","roles":[{"name":"estate_dba}]}`, want: reasonMalformed},
		{name: "raw control character in a name", document: adminDeclDocument(adminDeclNamed("estate" + adminDeclControlSOH + "dba")), want: reasonMalformed},
		{name: "raw line feed in a name", document: adminDeclDocument(adminDeclNamed("estate" + adminDeclLineFeed + "dba")), want: reasonMalformed},
		{name: "truncated unicode escape", document: adminDeclDocument(adminDeclNamed(adminDeclJSONShortEsc)), want: reasonMalformed},
		{name: "non-hexadecimal unicode escape", document: adminDeclDocument(adminDeclNamed(adminDeclJSONBadHex)), want: reasonMalformed},
		{name: "unknown escape", document: adminDeclDocument(adminDeclNamed(adminDeclJSONUnknownEsc)), want: reasonMalformed},

		// Members.
		{name: "empty object", document: `{}`, want: reasonMissingMember},
		{name: "missing schema", document: `{"roles":[]}`, want: reasonMissingMember},
		{name: "missing roles", document: `{"schema":"` + schema + `"}`, want: reasonMissingMember},
		{name: "unknown member", document: `{"schema":"` + schema + `","roles":[],"source":"/etc/olivares/admins.json"}`, want: reasonUnknownMember},
		{name: "differently cased schema member", document: `{"Schema":"` + schema + `","roles":[]}`, want: reasonUnknownMember},
		{name: "differently cased roles member", document: `{"schema":"` + schema + `","ROLES":[]}`, want: reasonUnknownMember},
		{name: "duplicate schema member", document: `{"schema":"` + schema + `","schema":"` + schema + `","roles":[]}`, want: reasonDuplicateMember},
		{name: "duplicate schema member, one escaped", document: `{"schema":"` + schema + `","` + adminDeclJSONSchemaKey + `":"` + schema + `","roles":[]}`, want: reasonDuplicateMember},
		{name: "duplicate roles member", document: `{"schema":"` + schema + `","roles":[],"roles":[]}`, want: reasonDuplicateMember},
		{name: "schema is a number", document: `{"schema":1,"roles":[]}`, want: reasonMemberType},
		{name: "schema is null", document: `{"schema":null,"roles":[]}`, want: reasonMemberType},
		{name: "roles is an object", document: `{"schema":"` + schema + `","roles":{}}`, want: reasonMemberType},
		{name: "roles is null", document: `{"schema":"` + schema + `","roles":null}`, want: reasonMemberType},
		{name: "unsupported schema version", document: adminDeclDocumentWithSchema("olivares.postgres.administrative-authority.v2", ``), want: reasonSchemaUnsupported},
		{name: "empty schema", document: adminDeclDocumentWithSchema("", ``), want: reasonSchemaUnsupported},

		// Role objects.
		{name: "role element is a number", document: adminDeclDocument(`1`), want: reasonMemberType},
		{name: "role element is a string", document: adminDeclDocument(`"estate_dba"`), want: reasonMemberType},
		{name: "role element is null", document: adminDeclDocument(`null`), want: reasonMemberType},
		{name: "trailing comma in the roles array", document: adminDeclDocument(`{"name":"estate_dba"},`), want: reasonMemberType},
		{name: "role object with no members", document: adminDeclDocument(`{}`), want: reasonMissingMember},
		{name: "role object with only a pin", document: adminDeclDocument(`{"expected_oid":16394}`), want: reasonMissingMember},
		{name: "role name is a number", document: adminDeclDocument(`{"name":16394}`), want: reasonMemberType},
		{name: "role name is null", document: adminDeclDocument(`{"name":null}`), want: reasonMemberType},
		{name: "duplicate name member", document: adminDeclDocument(`{"name":"estate_dba","name":"estate_dba"}`), want: reasonDuplicateMember},
		{name: "duplicate name member, one escaped", document: adminDeclDocument(`{"name":"estate_dba","` + adminDeclJSONNameKey + `":"other_dba"}`), want: reasonDuplicateMember},
		{name: "duplicate expected_oid member", document: adminDeclDocument(`{"name":"estate_dba","expected_oid":1,"expected_oid":2}`), want: reasonDuplicateMember},
		{name: "unknown role member", document: adminDeclDocument(`{"name":"estate_dba","comment":"the estate DBA"}`), want: reasonUnknownMember},
		{name: "differently cased name member", document: adminDeclDocument(`{"Name":"estate_dba"}`), want: reasonUnknownMember},
		{name: "differently cased pin member", document: adminDeclDocument(`{"name":"estate_dba","expected_OID":16394}`), want: reasonUnknownMember},

		// Names, refused by the rules the constructor owns.
		{name: "empty role name", document: adminDeclDocument(`{"name":""}`), want: reasonRoleNameEmpty},
		{name: "role name containing U+0000", document: adminDeclDocument(adminDeclNamed(adminDeclJSONNULName)), want: reasonRoleNameNUL},
		{name: "duplicate role names", document: adminDeclDocument(`{"name":"estate_dba"},{"name":"estate_dba"}`), want: reasonRoleNameDuplicate},
		{name: "duplicate role names, one escaped", document: adminDeclDocument(`{"name":"estate_dba"},` + adminDeclNamed(adminDeclJSONEstateA)), want: reasonRoleNameDuplicate},
		{name: "duplicate role names with different pins", document: adminDeclDocument(`{"name":"estate_dba","expected_oid":1},{"name":"estate_dba","expected_oid":2}`), want: reasonRoleNameDuplicate},

		// UTF-8 and surrogates.
		{name: "invalid UTF-8 inside a name", document: adminDeclDocument(adminDeclNamed("estate" + adminDeclInvalidUTF8 + "dba")), want: reasonDocumentNotUTF8},
		{name: "invalid UTF-8 outside the strings", document: adminDeclDocument(``) + adminDeclInvalidUTF8, want: reasonDocumentNotUTF8},
		{name: "lone high surrogate", document: adminDeclDocument(adminDeclNamed(adminDeclJSONHigh)), want: reasonUnpairedSurrogate},
		{name: "lone low surrogate", document: adminDeclDocument(adminDeclNamed(adminDeclJSONLow)), want: reasonUnpairedSurrogate},
		{name: "high surrogate followed by a plain escape", document: adminDeclDocument(adminDeclNamed(adminDeclJSONHigh + adminDeclJSONCapitalA)), want: reasonUnpairedSurrogate},
		{name: "high surrogate followed by a literal", document: adminDeclDocument(adminDeclNamed(adminDeclJSONHigh + "A")), want: reasonUnpairedSurrogate},
		{name: "two high surrogates", document: adminDeclDocument(adminDeclNamed(adminDeclJSONHigh + adminDeclJSONHigh)), want: reasonUnpairedSurrogate},

		// expected_oid.
		{name: "pin is null", document: adminDeclDocument(`{"name":"estate_dba","expected_oid":null}`), want: reasonPinFormat},
		{name: "pin is quoted", document: adminDeclDocument(`{"name":"estate_dba","expected_oid":"16394"}`), want: reasonPinFormat},
		{name: "pin is true", document: adminDeclDocument(`{"name":"estate_dba","expected_oid":true}`), want: reasonPinFormat},
		{name: "pin is an array", document: adminDeclDocument(`{"name":"estate_dba","expected_oid":[16394]}`), want: reasonPinFormat},
		{name: "pin is negative", document: adminDeclDocument(`{"name":"estate_dba","expected_oid":-16394}`), want: reasonPinFormat},
		{name: "pin is explicitly signed", document: adminDeclDocument(`{"name":"estate_dba","expected_oid":+16394}`), want: reasonPinFormat},
		{name: "pin is a fraction", document: adminDeclDocument(`{"name":"estate_dba","expected_oid":16394.0}`), want: reasonPinFormat},
		{name: "pin uses exponent notation", document: adminDeclDocument(`{"name":"estate_dba","expected_oid":1e3}`), want: reasonPinFormat},
		{name: "pin has a leading zero", document: adminDeclDocument(`{"name":"estate_dba","expected_oid":016394}`), want: reasonPinFormat},
		{name: "pin is zero", document: adminDeclDocument(`{"name":"estate_dba","expected_oid":0}`), want: reasonPinRange},
		{name: "pin is one above uint32", document: adminDeclDocument(`{"name":"estate_dba","expected_oid":4294967296}`), want: reasonPinRange},
		{name: "pin overflows uint64", document: adminDeclDocument(`{"name":"estate_dba","expected_oid":99999999999999999999999999}`), want: reasonPinRange},
	}

	accepted := 0
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			authority, err := ParsePostgresAdministrativeAuthority([]byte(testCase.document))
			if testCase.accept {
				if err != nil {
					t.Fatalf("ParsePostgresAdministrativeAuthority(%q) = error %v, want acceptance", testCase.document, err)
				}
				if authority.ID() == "" {
					t.Error("an accepted declaration has an empty ID")
				}
				return
			}
			if err == nil {
				t.Fatalf("ParsePostgresAdministrativeAuthority(%q) accepted the document, want a refusal", testCase.document)
			}
			if !errors.Is(err, ErrPostgresAdministrativeAuthorityInvalid) {
				t.Errorf("error %v does not wrap ErrPostgresAdministrativeAuthorityInvalid", err)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("error %q does not report category %q", err.Error(), testCase.want)
			}
			if roles := authority.Roles(); roles != nil {
				t.Errorf("a refused document returned roles %s, want the zero value", adminDeclRolesText(roles))
			}
		})
		if testCase.accept {
			accepted++
		}
	}
	if accepted == 0 {
		t.Fatal("the refusal table has no acceptance control, so it would pass against a parser that refuses everything")
	}
}

// TestPostgresAdministrativeAuthorityConstructorRefusals covers the rules only the
// constructor can reach. Invalid UTF-8 in particular cannot arrive through the JSON
// path, where the whole document is checked before anything is decoded.
func TestPostgresAdministrativeAuthorityConstructorRefusals(t *testing.T) {
	cases := []struct {
		name   string
		roles  []PostgresAdministrativeRole
		accept bool
		want   string
	}{
		{name: "control: nil", roles: nil, accept: true},
		{name: "control: empty", roles: []PostgresAdministrativeRole{}, accept: true},
		{name: "control: one role", roles: []PostgresAdministrativeRole{{Name: "estate_dba"}}, accept: true},
		{name: "control: lowest pin", roles: []PostgresAdministrativeRole{{Name: "estate_dba", ExpectedOID: adminDeclPin(1)}}, accept: true},
		{name: "control: highest pin", roles: []PostgresAdministrativeRole{{Name: "estate_dba", ExpectedOID: adminDeclPin(4294967295)}}, accept: true},
		{
			// No lexical length limit belongs to this module: whether a server would
			// truncate this name is a server fact, and RA2-A2 reads it there.
			name:   "control: a name longer than a PostgreSQL identifier",
			roles:  []PostgresAdministrativeRole{{Name: strings.Repeat("d", 200)}},
			accept: true,
		},
		{name: "empty name", roles: []PostgresAdministrativeRole{{Name: ""}}, want: reasonRoleNameEmpty},
		{name: "invalid UTF-8 name", roles: []PostgresAdministrativeRole{{Name: "estate" + adminDeclInvalidUTF8 + "dba"}}, want: reasonRoleNameNotUTF8},
		{name: "truncated UTF-8 sequence", roles: []PostgresAdministrativeRole{{Name: adminDeclCutUTF8}}, want: reasonRoleNameNotUTF8},
		{name: "surrogate encoded as UTF-8", roles: []PostgresAdministrativeRole{{Name: adminDeclSurrogateUTF8}}, want: reasonRoleNameNotUTF8},
		{name: "name containing U+0000", roles: []PostgresAdministrativeRole{{Name: "estate" + adminDeclNUL + "dba"}}, want: reasonRoleNameNUL},
		{name: "name that is only U+0000", roles: []PostgresAdministrativeRole{{Name: adminDeclNUL}}, want: reasonRoleNameNUL},
		{
			name:  "duplicate names",
			roles: []PostgresAdministrativeRole{{Name: "estate_dba"}, {Name: "estate_dba"}},
			want:  reasonRoleNameDuplicate,
		},
		{
			name: "duplicate names with different pins",
			roles: []PostgresAdministrativeRole{
				{Name: "estate_dba", ExpectedOID: adminDeclPin(1)},
				{Name: "estate_dba", ExpectedOID: adminDeclPin(2)},
			},
			want: reasonRoleNameDuplicate,
		},
		{
			name:  "zero pin",
			roles: []PostgresAdministrativeRole{{Name: "estate_dba", ExpectedOID: adminDeclPin(0)}},
			want:  reasonRolePinZero,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			authority, err := NewPostgresAdministrativeAuthority(testCase.roles)
			if testCase.accept {
				if err != nil {
					t.Fatalf("NewPostgresAdministrativeAuthority(%s) = error %v, want acceptance", adminDeclRolesText(testCase.roles), err)
				}
				return
			}
			if err == nil {
				t.Fatalf("NewPostgresAdministrativeAuthority(%s) accepted the input, want a refusal", adminDeclRolesText(testCase.roles))
			}
			if !errors.Is(err, ErrPostgresAdministrativeAuthorityInvalid) {
				t.Errorf("error %v does not wrap ErrPostgresAdministrativeAuthorityInvalid", err)
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("error %q does not report category %q", err.Error(), testCase.want)
			}
			if got := authority.ID(); got != adminDeclEmptyID {
				t.Errorf("a refused input returned ID %s, want the zero value's %s", got, adminDeclEmptyID)
			}
		})
	}
}

// TestPostgresAdministrativeAuthoritySentinelIsStable checks the one thing a caller
// may depend on: errors.Is against the package sentinel, from both entry points, and
// no accidental kinship with the store's other sentinels.
func TestPostgresAdministrativeAuthoritySentinelIsStable(t *testing.T) {
	refusals := map[string]error{
		"constructor": adminDeclErrorFromNew([]PostgresAdministrativeRole{{Name: ""}}),
		"parser":      adminDeclErrorFromParse(`{}`),
	}
	for name, err := range refusals {
		if err == nil {
			t.Fatalf("%s: expected a refusal", name)
		}
		if !errors.Is(err, ErrPostgresAdministrativeAuthorityInvalid) {
			t.Errorf("%s: error %v does not wrap the sentinel", name, err)
		}
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrInvalidID) || errors.Is(err, ErrInvalidDescriptor) {
			t.Errorf("%s: error %v also matches an unrelated store sentinel", name, err)
		}
		if !strings.Contains(err.Error(), ErrPostgresAdministrativeAuthorityInvalid.Error()) {
			t.Errorf("%s: error %q does not carry the sentinel's own text", name, err.Error())
		}
	}
	if !errors.Is(ErrPostgresAdministrativeAuthorityInvalid, ErrPostgresAdministrativeAuthorityInvalid) {
		t.Error("the sentinel does not match itself")
	}
}

// TestPostgresAdministrativeAuthorityDiagnosticsOmitSuppliedInput checks that a
// refusal names its category and nothing else. A declaration is operator
// configuration, and a diagnostic is the one place it would reach a log.
func TestPostgresAdministrativeAuthorityDiagnosticsOmitSuppliedInput(t *testing.T) {
	const marker = "SENSITIVE-MARKER-THAT-MUST-NOT-BE-COPIED"
	cases := []struct {
		name   string
		err    error
		absent string
	}{
		{
			name:   "constructor, duplicate name",
			err:    adminDeclErrorFromNew([]PostgresAdministrativeRole{{Name: marker}, {Name: marker}}),
			absent: marker,
		},
		{
			name:   "constructor, name containing U+0000",
			err:    adminDeclErrorFromNew([]PostgresAdministrativeRole{{Name: marker + adminDeclNUL}}),
			absent: marker,
		},
		{
			name:   "constructor, invalid UTF-8 name",
			err:    adminDeclErrorFromNew([]PostgresAdministrativeRole{{Name: marker + adminDeclInvalidUTF8}}),
			absent: marker,
		},
		{
			name:   "parser, unsupported schema value",
			err:    adminDeclErrorFromParse(adminDeclDocumentWithSchema(marker, ``)),
			absent: marker,
		},
		{
			name:   "parser, unknown document member",
			err:    adminDeclErrorFromParse(`{"schema":"` + adminDeclSchemaLiteral + `","roles":[],"` + marker + `":1}`),
			absent: marker,
		},
		{
			name:   "parser, unknown role member",
			err:    adminDeclErrorFromParse(adminDeclDocument(`{"name":"estate_dba","` + marker + `":1}`)),
			absent: marker,
		},
		{
			name:   "parser, duplicate role name",
			err:    adminDeclErrorFromParse(adminDeclDocument(adminDeclNamed(marker) + `,` + adminDeclNamed(marker))),
			absent: marker,
		},
		{
			name:   "parser, empty role name does not echo the document",
			err:    adminDeclErrorFromParse(adminDeclDocument(`{"name":""}`)),
			absent: adminDeclSchemaLiteral,
		},
		{
			name:   "parser, pin above uint32",
			err:    adminDeclErrorFromParse(adminDeclDocument(`{"name":"estate_dba","expected_oid":4294967296}`)),
			absent: "4294967296",
		},
		{
			name:   "parser, quoted pin carrying a marker",
			err:    adminDeclErrorFromParse(adminDeclDocument(`{"name":"estate_dba","expected_oid":"` + marker + `"}`)),
			absent: marker,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.err == nil {
				t.Fatal("expected a refusal")
			}
			if !errors.Is(testCase.err, ErrPostgresAdministrativeAuthorityInvalid) {
				t.Errorf("error %v does not wrap the sentinel", testCase.err)
			}
			if strings.Contains(testCase.err.Error(), testCase.absent) {
				t.Errorf("diagnostic %q copies supplied input %q", testCase.err.Error(), testCase.absent)
			}
		})
	}
}

// TestPostgresAdministrativeAuthorityOwnsConstructorInput proves the constructor
// copies the slice, each entry and every pin, and that it does not reorder the
// caller's slice in place either.
func TestPostgresAdministrativeAuthorityOwnsConstructorInput(t *testing.T) {
	callerPin := uint32(4294967295)
	input := []PostgresAdministrativeRole{
		{Name: "recovery_dba", ExpectedOID: &callerPin},
		{Name: "estate_dba"},
	}
	authority := mustNewAdminDecl(t, input)
	if got := authority.ID(); got != adminDeclSortedPairID {
		t.Fatalf("ID = %s, want root's sorted-pair ID %s", got, adminDeclSortedPairID)
	}
	if input[0].Name != "recovery_dba" {
		t.Errorf("the constructor reordered the caller's slice in place: input[0] is now %q", input[0].Name)
	}

	// Everything the caller still holds is now written through.
	callerPin = 1
	input[0].Name = "unauthorized_dba"
	replacement := uint32(2)
	input[0].ExpectedOID = &replacement
	input[1].ExpectedOID = adminDeclPin(3)
	input = append(input, PostgresAdministrativeRole{Name: "another_dba"})

	if got := authority.ID(); got != adminDeclSortedPairID {
		t.Errorf("after mutating the constructor input, ID = %s, want the unchanged %s", got, adminDeclSortedPairID)
	}
	expected := []PostgresAdministrativeRole{
		{Name: "estate_dba"},
		{Name: "recovery_dba", ExpectedOID: adminDeclPin(4294967295)},
	}
	if got := authority.Roles(); !adminDeclRolesEqual(got, expected) {
		t.Errorf("Roles() = %s, want %s", adminDeclRolesText(got), adminDeclRolesText(expected))
	}
	if len(input) != 3 {
		t.Errorf("the bank's own input was not extended: len = %d", len(input))
	}
}

// TestPostgresAdministrativeAuthorityReturnedCopiesAreIndependent proves that
// writing through anything Roles returns cannot change a later result, and that two
// calls never share a pin pointer.
func TestPostgresAdministrativeAuthorityReturnedCopiesAreIndependent(t *testing.T) {
	authority := mustNewAdminDecl(t, []PostgresAdministrativeRole{
		{Name: "estate_dba", ExpectedOID: adminDeclPin(16394)},
		{Name: "recovery_dba"},
	})
	want := authority.ID()

	first := authority.Roles()
	second := authority.Roles()
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("Roles() returned %d and %d entries, want 2", len(first), len(second))
	}
	if first[0].ExpectedOID == second[0].ExpectedOID {
		t.Error("two Roles() calls returned the same pin pointer; the copies are not independent")
	}
	if !adminDeclRolesEqual(first, second) {
		t.Errorf("two Roles() calls disagree: %s and %s", adminDeclRolesText(first), adminDeclRolesText(second))
	}

	first[0].Name = "unauthorized_dba"
	*first[0].ExpectedOID = 1
	first[1].ExpectedOID = adminDeclPin(2)
	first = append(first, PostgresAdministrativeRole{Name: "another_dba"})

	expected := []PostgresAdministrativeRole{
		{Name: "estate_dba", ExpectedOID: adminDeclPin(16394)},
		{Name: "recovery_dba"},
	}
	if got := authority.Roles(); !adminDeclRolesEqual(got, expected) {
		t.Errorf("after mutating a returned copy, Roles() = %s, want %s", adminDeclRolesText(got), adminDeclRolesText(expected))
	}
	if !adminDeclRolesEqual(second, expected) {
		t.Errorf("mutating one returned copy changed another: %s", adminDeclRolesText(second))
	}
	if got := authority.ID(); got != want {
		t.Errorf("after mutating a returned copy, ID = %s, want the unchanged %s", got, want)
	}
	if len(first) != 3 {
		t.Errorf("the bank's own copy was not extended: len = %d", len(first))
	}
}

// TestPostgresAdministrativeAuthorityConcurrentReadsAndCopies is the race-detector
// bank: many readers share one constructed value with no lock, each mutating only
// the copies it was handed. The constructor input is NOT touched here, because a
// test that mutated it concurrently would be measuring its own data race and then
// blaming the constructor for it.
func TestPostgresAdministrativeAuthorityConcurrentReadsAndCopies(t *testing.T) {
	authority := mustNewAdminDecl(t, []PostgresAdministrativeRole{
		{Name: "recovery_dba", ExpectedOID: adminDeclPin(4294967295)},
		{Name: "estate_dba"},
	})
	zero := PostgresAdministrativeAuthority{}

	const (
		readers    = 8
		iterations = 250
	)
	observed := make([]string, readers)
	observedZero := make([]string, readers)
	var waiting sync.WaitGroup
	for reader := 0; reader < readers; reader++ {
		waiting.Add(1)
		go func(index int) {
			defer waiting.Done()
			var lastID, lastZeroID string
			for i := 0; i < iterations; i++ {
				lastID = authority.ID()
				lastZeroID = zero.ID()
				copies := authority.Roles()
				for j := range copies {
					copies[j].Name = "unauthorized_dba"
					if copies[j].ExpectedOID != nil {
						*copies[j].ExpectedOID = uint32(index + 1)
					}
				}
				_ = zero.Roles()
			}
			observed[index] = lastID
			observedZero[index] = lastZeroID
		}(reader)
	}
	waiting.Wait()

	for index, got := range observed {
		if got != adminDeclSortedPairID {
			t.Errorf("reader %d observed ID %s, want %s", index, got, adminDeclSortedPairID)
		}
		if observedZero[index] != adminDeclEmptyID {
			t.Errorf("reader %d observed zero-value ID %s, want %s", index, observedZero[index], adminDeclEmptyID)
		}
	}
	if got := authority.ID(); got != adminDeclSortedPairID {
		t.Errorf("after the concurrent readers, ID = %s, want %s", got, adminDeclSortedPairID)
	}
	expected := []PostgresAdministrativeRole{
		{Name: "estate_dba"},
		{Name: "recovery_dba", ExpectedOID: adminDeclPin(4294967295)},
	}
	if got := authority.Roles(); !adminDeclRolesEqual(got, expected) {
		t.Errorf("after the concurrent readers, Roles() = %s, want %s", adminDeclRolesText(got), adminDeclRolesText(expected))
	}
}
