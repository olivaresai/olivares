// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package store

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// postgres_administrative_authority.go — RA2-A1, the immutable administrative
// declaration. It carries one validated operator statement of "these role names
// are additional administrators", its JSON language, and its canonical identity.
//
// # What this value is, and what it is not
//
// It is DATA. It performs no file access, catalog query, Store creation, role
// change, logging or reload, and it cannot admit a database operation by itself.
// A declaration records operator INTENT: it does not prove that a role exists,
// that a provider trusts it, or that it holds the privileges an operation needs.
// Binding a declared name to a server role — including reading that server's
// actual identifier limit so a name it would truncate is refused before binding —
// belongs to RA2-A2, not here. The Config field, the CLI flag and the accepting
// SQL predicate belong to the complete RA2-A2/B/C composition.
//
// The ID authenticates nothing either. It identifies a declaration's SEMANTICS so
// two deployments can be compared; it is not a signature, a secret, a catalog
// incarnation UUID or proof that an administrator is authorized.
//
// # Why a hand-written parser
//
// encoding/json with DisallowUnknownFields is insufficient for this closed shape:
// the default decoder accepts duplicate members (last one wins), matches struct
// keys case-insensitively, and replaces invalid UTF-8 and lone surrogates with
// U+FFFD. Each of those turns a malformed administrative declaration into an
// accepted one that names a role the operator did not write. The scanner below
// reads the exact grammar and refuses everything else.

// ErrPostgresAdministrativeAuthorityInvalid is the single sentinel every refusal in
// this module wraps; callers match it with errors.Is. Its message identifies the
// validation CATEGORY from the closed list below and never copies input bytes, a
// role name or an unsupported field value, because a declaration is operator
// configuration and a diagnostic is the one place it would leak into a log.
//
// It is never a database lookup failure: nothing here reaches a server. A malformed
// input is an ordinary error result, never a panic.
var ErrPostgresAdministrativeAuthorityInvalid = errors.New("postgres administrative authority declaration invalid")

// These fixed categories keep current diagnostics bounded. Call sites must pass
// fixed categories and must not include input.
const (
	reasonDocumentNotUTF8   = "declaration bytes are not valid UTF-8"
	reasonNotObject         = "declaration is not a single JSON object"
	reasonTrailingContent   = "declaration has content after the JSON object"
	reasonMalformed         = "declaration is malformed or truncated JSON"
	reasonUnknownMember     = "declaration has an unknown or differently cased member"
	reasonDuplicateMember   = "declaration has a duplicate member"
	reasonMissingMember     = "declaration is missing a required member"
	reasonMemberType        = "declaration member has the wrong JSON type"
	reasonSchemaUnsupported = "declaration schema is unsupported"
	reasonUnpairedSurrogate = "declaration string has an unpaired Unicode surrogate"
	reasonPinFormat         = "declaration expected_oid is not an unsigned decimal integer"
	reasonPinRange          = "declaration expected_oid is outside 1..4294967295"
	reasonRoleNameEmpty     = "role name is empty"
	reasonRoleNameNotUTF8   = "role name is not valid UTF-8"
	reasonRoleNameNUL       = "role name contains U+0000"
	reasonRoleNameDuplicate = "role name is declared more than once"
	reasonRolePinZero       = "role expected_oid is zero"
)

// administrativeAuthoritySchema is both the accepted JSON schema member and the
// domain prefix of the canonical bytes. It fixes this version's semantics: changing
// it, or the framing in canonicalBytes, is a versioned identity change and not a
// refactor.
const administrativeAuthoritySchema = "olivares.postgres.administrative-authority.v1"

// PostgresAdministrativeRole is one declared administrator.
type PostgresAdministrativeRole struct {
	// Name is the exact UTF-8 role name the operator wrote. Case, whitespace and
	// Unicode normalization form are preserved and significant: this module does
	// not parse SQL quoting, fold case, trim or normalize, so "Estate_DBA",
	// "estate_dba" and the NFC/NFD spellings of one name are distinct declarations.
	Name string
	// ExpectedOID, when non-nil, pins the catalog OID the operator expects this
	// name to resolve to. It must be nonzero. Absent is not the same declaration as
	// pinned, and no OID is ever resolved, observed or invented for an absent pin.
	ExpectedOID *uint32
}

// PostgresAdministrativeAuthority is a validated, immutable administrative
// declaration. Construct it with NewPostgresAdministrativeAuthority or
// ParsePostgresAdministrativeAuthority; both validate their whole input before
// returning a usable value.
//
// The zero value is meaningful and equals an empty declaration: no additional
// administrators. It has the same nonempty canonical ID as a nil input slice, an
// empty input slice and a document whose roles array is empty.
//
// It owns everything it holds, so a constructed value cannot be changed by anyone:
// not through the slice the constructor was given, not through a caller-held OID
// pointer, and not through a slice Roles returned. Concurrent readers therefore
// need no lock. The one duty left to the caller is not to mutate the input slice
// WHILE New is reading it.
type PostgresAdministrativeAuthority struct {
	// roles is validated, deep-copied and in canonical order. Nothing writes it
	// after construction, which is what makes concurrent reads safe without a lock.
	roles []PostgresAdministrativeRole
}

// NewPostgresAdministrativeAuthority validates roles and returns the declaration
// they describe. It refuses an empty name, invalid UTF-8, a name containing U+0000,
// a duplicate exact name and a zero OID pin, wrapping
// ErrPostgresAdministrativeAuthorityInvalid; on refusal it returns the zero value.
//
// No identifier length is imposed here. A byte length the server would truncate is
// a SERVER fact, so RA2-A2 reads the actual limit and refuses before binding; a
// lexical limit invented here would be a different rule that happens to agree.
func NewPostgresAdministrativeAuthority(roles []PostgresAdministrativeRole) (PostgresAdministrativeAuthority, error) {
	owned := make([]PostgresAdministrativeRole, 0, len(roles))
	seen := make(map[string]struct{}, len(roles))
	for _, role := range roles {
		if err := validateAdministrativeRoleName(role.Name); err != nil {
			return PostgresAdministrativeAuthority{}, err
		}
		if _, duplicate := seen[role.Name]; duplicate {
			return PostgresAdministrativeAuthority{}, invalidDeclaration(reasonRoleNameDuplicate)
		}
		seen[role.Name] = struct{}{}
		copied := PostgresAdministrativeRole{Name: role.Name}
		if role.ExpectedOID != nil {
			if *role.ExpectedOID == 0 {
				return PostgresAdministrativeAuthority{}, invalidDeclaration(reasonRolePinZero)
			}
			// Own the VALUE, not the caller's pointer: a caller that keeps writing
			// through its own pointer must not be able to move a validated pin.
			pin := *role.ExpectedOID
			copied.ExpectedOID = &pin
		}
		owned = append(owned, copied)
	}
	if len(owned) == 0 {
		// Nil, empty and the zero value are one declaration, so they are one value.
		return PostgresAdministrativeAuthority{}, nil
	}
	// Canonical order is bytewise on the UTF-8 name, which is exactly what Go's
	// string comparison does. Duplicates are already refused, so the order is total.
	slices.SortFunc(owned, func(a, b PostgresAdministrativeRole) int {
		return strings.Compare(a.Name, b.Name)
	})
	return PostgresAdministrativeAuthority{roles: owned}, nil
}

// ParsePostgresAdministrativeAuthority reads one administrative declaration document
// and returns the declaration it describes, wrapping
// ErrPostgresAdministrativeAuthorityInvalid on any refusal.
//
// It decodes bytes only. It does not fetch, open or bound an input file: empty bytes
// are malformed JSON, not absence. "No file was configured" is the future CLI
// owner's choice and its answer is this type's zero value; a configured file that is
// missing is that owner's refusal to make, not this function's.
//
// The semantic rules are not duplicated here: the document is decoded and then
// handed to NewPostgresAdministrativeAuthority, so both entry points agree by
// construction rather than by review.
func ParsePostgresAdministrativeAuthority(document []byte) (PostgresAdministrativeAuthority, error) {
	roles, err := parseAdministrativeAuthorityDocument(document)
	if err != nil {
		return PostgresAdministrativeAuthority{}, err
	}
	return NewPostgresAdministrativeAuthority(roles)
}

// Roles returns a fresh deep copy of the declared roles in canonical order, or nil
// for an empty declaration. Every returned OID pointer is freshly allocated, so
// writing through anything this returns cannot change a later Roles or ID result,
// and two calls never share a pointer. Compare entries by name and pin value;
// separate copies of a pinned role contain different pointers.
func (a PostgresAdministrativeAuthority) Roles() []PostgresAdministrativeRole {
	if len(a.roles) == 0 {
		return nil
	}
	copies := make([]PostgresAdministrativeRole, len(a.roles))
	for i, role := range a.roles {
		copies[i] = PostgresAdministrativeRole{Name: role.Name}
		if role.ExpectedOID != nil {
			pin := *role.ExpectedOID
			copies[i].ExpectedOID = &pin
		}
	}
	return copies
}

// ID returns the canonical identity of this declaration: "sha256:" followed by 64
// lowercase hexadecimal digits over the bytes described in canonicalBytes.
//
// It is computed purely on every call, from state that never changes after
// construction. That is deliberate rather than thrifty: a lazily cached digest
// would be an unsynchronized write during a read and would break the concurrent-read
// property this type promises, while a field alone could not answer for the zero
// value. Whitespace, member order and role order in a source document do not reach
// this computation, so they cannot change the answer.
func (a PostgresAdministrativeAuthority) ID() string {
	digest := sha256.Sum256(a.canonicalBytes())
	return "sha256:" + hex.EncodeToString(digest[:])
}

// canonicalBytes builds the semantic identity bytes: the ASCII domain, one 00 byte,
// the role count as u64 big-endian, then per role in canonical order its UTF-8 name
// length as u64 big-endian, the exact name bytes, one pin-presence byte (00 absent,
// 01 present) and, only when present, the pin as u32 big-endian.
//
// There is no padding, no terminator after a name, no observed role, no resolved OID
// for an unpinned name, no timestamp, no source path and no JSON in these bytes. The
// length prefixes are what make the framing unambiguous: without them, two different
// role lists could serialize to the same bytes.
func (a PostgresAdministrativeAuthority) canonicalBytes() []byte {
	size := len(administrativeAuthoritySchema) + 1 + 8
	for _, role := range a.roles {
		size += 8 + len(role.Name) + 1
		if role.ExpectedOID != nil {
			size += 4
		}
	}
	out := make([]byte, 0, size)
	out = append(out, administrativeAuthoritySchema...)
	out = append(out, 0x00)
	out = binary.BigEndian.AppendUint64(out, uint64(len(a.roles)))
	for _, role := range a.roles {
		out = binary.BigEndian.AppendUint64(out, uint64(len(role.Name)))
		out = append(out, role.Name...)
		if role.ExpectedOID == nil {
			out = append(out, 0x00)
			continue
		}
		out = append(out, 0x01)
		out = binary.BigEndian.AppendUint32(out, *role.ExpectedOID)
	}
	return out
}

// validateAdministrativeRoleName applies the lexical rules a name must satisfy in
// both entry points. It checks the bytes as written and normalizes nothing.
func validateAdministrativeRoleName(name string) error {
	switch {
	case name == "":
		return invalidDeclaration(reasonRoleNameEmpty)
	case !utf8.ValidString(name):
		return invalidDeclaration(reasonRoleNameNotUTF8)
	case strings.IndexByte(name, 0x00) >= 0:
		return invalidDeclaration(reasonRoleNameNUL)
	}
	return nil
}

// invalidDeclaration wraps the sentinel with one category from the closed list.
func invalidDeclaration(reason string) error {
	return fmt.Errorf("%w: %s", ErrPostgresAdministrativeAuthorityInvalid, reason)
}

// declarationScanner reads the closed declaration grammar over a byte slice it does
// not own and never modifies.
type declarationScanner struct {
	src []byte
	pos int
}

// parseAdministrativeAuthorityDocument decodes the document into declared roles.
// It resolves SHAPE only: names and pins are handed on exactly as written, and the
// semantic rules stay with the constructor.
func parseAdministrativeAuthorityDocument(document []byte) ([]PostgresAdministrativeRole, error) {
	// Checked once over the whole input so raw string bytes can be copied verbatim
	// afterwards, which is what preserves an exact NFC/NFD spelling.
	if !utf8.Valid(document) {
		return nil, invalidDeclaration(reasonDocumentNotUTF8)
	}
	s := &declarationScanner{src: document}
	s.skipWhitespace()
	if !s.take('{') {
		// Empty bytes, a BOM, a leading NUL, a non-ASCII space and a top-level
		// array, string or number all land here.
		return nil, invalidDeclaration(reasonNotObject)
	}
	var (
		haveSchema bool
		haveRoles  bool
		roles      []PostgresAdministrativeRole
	)
	s.skipWhitespace()
	if !s.take('}') {
		for {
			s.skipWhitespace()
			member, err := s.readString(reasonMalformed)
			if err != nil {
				return nil, err
			}
			s.skipWhitespace()
			if !s.take(':') {
				return nil, invalidDeclaration(reasonMalformed)
			}
			s.skipWhitespace()
			// Members are matched on the DECODED name, so "schema" is the same
			// member as "schema" and cannot be used to declare it twice.
			switch member {
			case "schema":
				if haveSchema {
					return nil, invalidDeclaration(reasonDuplicateMember)
				}
				haveSchema = true
				schema, err := s.readString(reasonMemberType)
				if err != nil {
					return nil, err
				}
				if schema != administrativeAuthoritySchema {
					return nil, invalidDeclaration(reasonSchemaUnsupported)
				}
			case "roles":
				if haveRoles {
					return nil, invalidDeclaration(reasonDuplicateMember)
				}
				haveRoles = true
				roles, err = s.readRoles()
				if err != nil {
					return nil, err
				}
			default:
				return nil, invalidDeclaration(reasonUnknownMember)
			}
			s.skipWhitespace()
			if s.take(',') {
				continue
			}
			if s.take('}') {
				break
			}
			return nil, invalidDeclaration(reasonMalformed)
		}
	}
	if !haveSchema || !haveRoles {
		return nil, invalidDeclaration(reasonMissingMember)
	}
	s.skipWhitespace()
	if s.pos != len(s.src) {
		return nil, invalidDeclaration(reasonTrailingContent)
	}
	return roles, nil
}

// readRoles reads the roles array, including an intentionally empty one.
func (s *declarationScanner) readRoles() ([]PostgresAdministrativeRole, error) {
	if s.exhausted() {
		return nil, invalidDeclaration(reasonMalformed)
	}
	if !s.take('[') {
		return nil, invalidDeclaration(reasonMemberType)
	}
	roles := []PostgresAdministrativeRole{}
	s.skipWhitespace()
	if s.take(']') {
		return roles, nil
	}
	for {
		s.skipWhitespace()
		role, err := s.readRole()
		if err != nil {
			return nil, err
		}
		roles = append(roles, role)
		s.skipWhitespace()
		if s.take(',') {
			continue
		}
		if s.take(']') {
			return roles, nil
		}
		return nil, invalidDeclaration(reasonMalformed)
	}
}

// readRole reads one role object: exactly one name and at most one expected_oid.
func (s *declarationScanner) readRole() (PostgresAdministrativeRole, error) {
	if s.exhausted() {
		return PostgresAdministrativeRole{}, invalidDeclaration(reasonMalformed)
	}
	if !s.take('{') {
		return PostgresAdministrativeRole{}, invalidDeclaration(reasonMemberType)
	}
	var (
		role     PostgresAdministrativeRole
		haveName bool
		havePin  bool
	)
	s.skipWhitespace()
	if !s.take('}') {
		for {
			s.skipWhitespace()
			member, err := s.readString(reasonMalformed)
			if err != nil {
				return PostgresAdministrativeRole{}, err
			}
			s.skipWhitespace()
			if !s.take(':') {
				return PostgresAdministrativeRole{}, invalidDeclaration(reasonMalformed)
			}
			s.skipWhitespace()
			switch member {
			case "name":
				if haveName {
					return PostgresAdministrativeRole{}, invalidDeclaration(reasonDuplicateMember)
				}
				haveName = true
				if role.Name, err = s.readString(reasonMemberType); err != nil {
					return PostgresAdministrativeRole{}, err
				}
			case "expected_oid":
				if havePin {
					return PostgresAdministrativeRole{}, invalidDeclaration(reasonDuplicateMember)
				}
				havePin = true
				pin, err := s.readOID()
				if err != nil {
					return PostgresAdministrativeRole{}, err
				}
				role.ExpectedOID = &pin
			default:
				return PostgresAdministrativeRole{}, invalidDeclaration(reasonUnknownMember)
			}
			s.skipWhitespace()
			if s.take(',') {
				continue
			}
			if s.take('}') {
				break
			}
			return PostgresAdministrativeRole{}, invalidDeclaration(reasonMalformed)
		}
	}
	if !haveName {
		return PostgresAdministrativeRole{}, invalidDeclaration(reasonMissingMember)
	}
	return role, nil
}

// readOID reads an expected_oid: an unsigned decimal JSON integer in 1..4294967295.
// Null, a quoted integer, a sign, a fraction and exponent notation are all refused,
// and an absent pin never reaches this function, so "malformed pin" and "no pin"
// cannot be confused.
func (s *declarationScanner) readOID() (uint32, error) {
	if s.exhausted() {
		return 0, invalidDeclaration(reasonMalformed)
	}
	start := s.pos
	for s.pos < len(s.src) && isNumericLiteralByte(s.src[s.pos]) {
		s.pos++
	}
	literal := string(s.src[start:s.pos])
	if !isUnsignedDecimalInteger(literal) {
		return 0, invalidDeclaration(reasonPinFormat)
	}
	value, err := strconv.ParseUint(literal, 10, 64)
	if err != nil {
		// The literal is all digits here, so the only failure left is a value too
		// wide for uint64: refuse it before any narrowing conversion can wrap it.
		return 0, invalidDeclaration(reasonPinRange)
	}
	if value < 1 || value > math.MaxUint32 {
		return 0, invalidDeclaration(reasonPinRange)
	}
	return uint32(value), nil
}

// isNumericLiteralByte reports whether b can occur inside any JSON number. The run
// is taken first and judged afterwards so that "1e3" is refused as an unsupported
// pin format rather than read as 1 followed by malformed input.
func isNumericLiteralByte(b byte) bool {
	return b >= '0' && b <= '9' || b == '-' || b == '+' || b == '.' || b == 'e' || b == 'E'
}

// isUnsignedDecimalInteger reports whether literal is a JSON number narrowed to the
// unsigned decimal integer grammar: at least one digit, no sign, no fraction, no
// exponent and no leading zero.
func isUnsignedDecimalInteger(literal string) bool {
	if literal == "" {
		return false
	}
	if len(literal) > 1 && literal[0] == '0' {
		return false
	}
	for i := 0; i < len(literal); i++ {
		if literal[i] < '0' || literal[i] > '9' {
			return false
		}
	}
	return true
}

// readString reads one JSON string and returns its decoded value. notString is the
// category reported when the value is not a string at all, which lets a caller
// distinguish "this member has the wrong type" from "this document is malformed".
func (s *declarationScanner) readString(notString string) (string, error) {
	if s.exhausted() {
		return "", invalidDeclaration(reasonMalformed)
	}
	if s.src[s.pos] != '"' {
		return "", invalidDeclaration(notString)
	}
	s.pos++
	var decoded []byte
	for {
		if s.exhausted() {
			return "", invalidDeclaration(reasonMalformed)
		}
		switch c := s.src[s.pos]; {
		case c == '"':
			s.pos++
			return string(decoded), nil
		case c == '\\':
			s.pos++
			r, err := s.readEscape()
			if err != nil {
				return "", err
			}
			decoded = utf8.AppendRune(decoded, r)
		case c < 0x20:
			// An unescaped control character; JSON requires the escape.
			return "", invalidDeclaration(reasonMalformed)
		default:
			// Copied verbatim: the whole document is already known to be valid
			// UTF-8, and copying bytes is what keeps the exact spelling.
			decoded = append(decoded, c)
			s.pos++
		}
	}
}

// readEscape reads the character after a backslash.
func (s *declarationScanner) readEscape() (rune, error) {
	if s.exhausted() {
		return 0, invalidDeclaration(reasonMalformed)
	}
	c := s.src[s.pos]
	s.pos++
	switch c {
	case '"':
		return '"', nil
	case '\\':
		return '\\', nil
	case '/':
		return '/', nil
	case 'b':
		return '\b', nil
	case 'f':
		return '\f', nil
	case 'n':
		return '\n', nil
	case 'r':
		return '\r', nil
	case 't':
		return '\t', nil
	case 'u':
		return s.readUnicodeEscape()
	default:
		return 0, invalidDeclaration(reasonMalformed)
	}
}

// readUnicodeEscape reads a \uXXXX escape, joining a valid surrogate pair into the
// one code point it spells. An unpaired surrogate is REFUSED rather than replaced
// with U+FFFD: silently substituting a character would let a declaration name a role
// the operator never wrote.
func (s *declarationScanner) readUnicodeEscape() (rune, error) {
	first, err := s.readHex4()
	if err != nil {
		return 0, err
	}
	if first < 0xD800 || first > 0xDFFF {
		return rune(first), nil
	}
	if first > 0xDBFF {
		// A low surrogate with no high surrogate before it.
		return 0, invalidDeclaration(reasonUnpairedSurrogate)
	}
	if s.pos+1 >= len(s.src) || s.src[s.pos] != '\\' || s.src[s.pos+1] != 'u' {
		return 0, invalidDeclaration(reasonUnpairedSurrogate)
	}
	s.pos += 2
	second, err := s.readHex4()
	if err != nil {
		return 0, err
	}
	if second < 0xDC00 || second > 0xDFFF {
		return 0, invalidDeclaration(reasonUnpairedSurrogate)
	}
	return utf16.DecodeRune(rune(first), rune(second)), nil
}

// readHex4 reads exactly four hexadecimal digits.
func (s *declarationScanner) readHex4() (uint32, error) {
	if s.pos+4 > len(s.src) {
		return 0, invalidDeclaration(reasonMalformed)
	}
	var value uint32
	for i := 0; i < 4; i++ {
		digit, ok := hexDigitValue(s.src[s.pos+i])
		if !ok {
			return 0, invalidDeclaration(reasonMalformed)
		}
		value = value<<4 | digit
	}
	s.pos += 4
	return value, nil
}

// hexDigitValue decodes one hexadecimal digit in either case.
func hexDigitValue(b byte) (uint32, bool) {
	switch {
	case b >= '0' && b <= '9':
		return uint32(b - '0'), true
	case b >= 'a' && b <= 'f':
		return uint32(b-'a') + 10, true
	case b >= 'A' && b <= 'F':
		return uint32(b-'A') + 10, true
	}
	return 0, false
}

// skipWhitespace consumes RFC 8259 whitespace only. Any other byte around or inside
// the object — a BOM, a NUL, U+00A0, a vertical tab — is left in place and becomes a
// refusal at the position that expected a token.
func (s *declarationScanner) skipWhitespace() {
	for s.pos < len(s.src) {
		switch s.src[s.pos] {
		case ' ', '\t', '\n', '\r':
			s.pos++
		default:
			return
		}
	}
}

// exhausted reports whether the input ended. Every place that needs a byte and has
// none reports truncation, so a cut-off document is never described as a value of
// the wrong type.
func (s *declarationScanner) exhausted() bool {
	return s.pos >= len(s.src)
}

// take consumes b when it is the next byte and reports whether it did.
func (s *declarationScanner) take(b byte) bool {
	if !s.exhausted() && s.src[s.pos] == b {
		s.pos++
		return true
	}
	return false
}
