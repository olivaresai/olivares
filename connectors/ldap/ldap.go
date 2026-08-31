// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package ldap is the Olivares AI identity connector for Active Directory and any
// LDAP v3 directory. It discovers users, service/computer accounts and groups,
// and the membership edges between them, and exposes them as an
// identitysource.Graph to module VI (governance). It is read-only and
// minimal-data (docs/SECURITY-HARDENING.md-3): it performs only LDAP searches with a fixed, safe
// attribute allowlist that NEVER includes a password attribute (userPassword,
// unicodePwd, …); it binds with the operator's own read-only service account,
// whose credential is held in memory and never persisted or logged; and it
// carries identity METADATA — DNs, names, account status, memberships — never the
// secrets behind the identities.
//
// Roster data travels the typed Snapshot (the pattern): an ordinary group
// membership is identity→group reference data, not an access edge. Gather emits
// the one membership class that IS an access edge — membership of a PRIVILEGED
// group, which grants administrative access over the directory ITSELF — as
// identity→directory permitted-grant observations (see Gather). It imports only
// the SDK, the Apache identitysource contract, and the MIT go-ldap client —
// never the engine.
package ldap

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	goldap "github.com/go-ldap/ldap/v3"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.ldap"

// safeAttributes is the fixed allowlist of attributes the connector requests. It
// is deliberately closed and contains NO credential attribute: a directory read
// must never pull userPassword, unicodePwd, ntPwdHistory or the like. Reviewers
// and tests assert this set never grows to include a secret (docs/SECURITY-HARDENING.md-3).
var safeAttributes = []string{
	"objectClass",
	"cn",
	"displayName",
	"sAMAccountName",
	"uid",
	"userPrincipalName",
	"mail",
	"userAccountControl", // AD account flags (parsed for the disabled bit only)
	"objectSid",          // security IDENTIFIER, never a credential (matched against well-known admin SIDs)
	"member",             // group → member DNs
	"memberOf",           // identity → group DNs (reverse, used as a fallback)
}

// uacAccountDisable is the ACCOUNTDISABLE bit of the AD userAccountControl
// attribute (0x0002): when set, the account is disabled.
const uacAccountDisable = 0x0002

// Default configuration values.
const (
	defaultUserFilter  = "(|(objectClass=person)(objectClass=user))"
	defaultGroupFilter = "(objectClass=group)"
	defaultPageSize    = 500
	defaultTimeout     = 30 * time.Second
)

// Conn is the minimal slice of the go-ldap client the connector uses, so a test
// injects a fake directory without a live server. *goldap.Conn satisfies a thin
// wrapper around it (see dialer).
type Conn interface {
	// Bind authenticates the connection with the read-only service account.
	Bind(username, password string) error
	// StartTLS upgrades a plaintext connection to TLS.
	StartTLS(cfg *tls.Config) error
	// Search runs one paged search and returns all entries.
	Search(req *goldap.SearchRequest) (*goldap.SearchResult, error)
	// Close releases the connection.
	Close() error
}

// Dialer opens a connection to an LDAP URL. It is injectable so a test supplies a
// fake; production dials go-ldap with the configured page size (see dialer).
type Dialer func(url string) (Conn, error)

// Source is the LDAP identity connector. It satisfies sdk.SourceConnector
// (Gather emits the privileged-group permitted grants) and
// identitysource.GraphProvider (the directory roster).
type Source struct {
	url          string
	bindDN       string
	bindPassword string
	baseDN       string
	userFilter   string
	groupFilter  string
	pageSize     uint32
	startTLS     bool
	skipVerify   bool
	nhiDNSuffix  string // DN substring that marks an account as a non-human identity
	// privilegedDNs holds the lowercase operator-declared privileged group DNs
	// (Gather grant scan). DN here is a *Distinguished Name* (RFC 4514) — an LDAP
	// AUTHORIZATION concept — and NOT the Domain Name System. staticcheck's ST1003
	// reads the "DNs" suffix as the DNS initialism and proposes `privilegedDNS`;
	// taking that rename would turn a set of directory group identifiers into
	// something the next reader reasons about as network names. The name stays,
	// and TestPrivilegedDNsAreDistinguishedNamesNotDNS reddens if a later sweep
	// "finishes" the job by pattern-matching.
	//nolint:staticcheck // ST1003 false positive: LDAP Distinguished Names (RFC 4514), not DNS. Guarded by TestPrivilegedDNsAreDistinguishedNamesNotDNS.
	privilegedDNs map[string]bool
	timeout       time.Duration

	dial Dialer           // injectable (tests); nil => production dialer
	now  func() time.Time // injectable clock (tests); nil => time.Now
}

// Compile-time proof that Source satisfies both contracts.
var (
	_ sdk.SourceConnector          = (*Source)(nil)
	_ identitysource.GraphProvider = (*Source)(nil)
)

// New returns an LDAP connector with default configuration.
func New() *Source {
	return &Source{
		userFilter:  defaultUserFilter,
		groupFilter: defaultGroupFilter,
		pageSize:    defaultPageSize,
		timeout:     defaultTimeout,
	}
}

// Descriptor returns the connector's self-description and declared configuration.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "AD / LDAP",
		Description: "Reads users, service accounts and groups from Active Directory / LDAP (read-only metadata; never passwords).",
		ConfigFields: []sdk.ConfigField{
			{Key: "url", Type: sdk.FieldString, Description: "LDAP URL (ldap://host:389 or ldaps://host:636). Empty = offline (empty graph)."},
			{Key: "bind_dn", Type: sdk.FieldString, Description: "Read-only service account DN used to bind."},
			{Key: "bind_password", Type: sdk.FieldString, Secret: true, Description: "Bind password reference (read-only; never persisted)."},
			{Key: "base_dn", Type: sdk.FieldString, Required: true, Description: "Search base DN (e.g. dc=corp,dc=example,dc=com)."},
			{Key: "user_filter", Type: sdk.FieldString, Default: defaultUserFilter, Description: "LDAP filter selecting identities."},
			{Key: "group_filter", Type: sdk.FieldString, Default: defaultGroupFilter, Description: "LDAP filter selecting groups."},
			{Key: "privileged_group_dns", Type: sdk.FieldString, Description: "Semicolon-separated group DNs the operator declares privileged (matched case-insensitively); members gain a directory permitted-grant edge of unknown mode. Semicolon because a DN itself contains commas (RFC 4514 keeps ';' escaped inside a DN)."},
			{Key: "page_size", Type: sdk.FieldInt, Default: strconv.Itoa(defaultPageSize), Description: "Paged-search page size."},
			{Key: "start_tls", Type: sdk.FieldBool, Default: "false", Description: "Issue StartTLS on a plaintext (ldap://) connection."},
			{Key: "insecure_skip_verify", Type: sdk.FieldBool, Default: "false", Description: "Skip TLS certificate verification (test only; insecure)."},
			{Key: "nhi_dn_suffix", Type: sdk.FieldString, Description: "DN substring marking an account as a non-human identity (e.g. ou=ServiceAccounts)."},
			{Key: "timeout", Type: sdk.FieldDuration, Default: "30s", Description: "Per-search timeout."},
		},
	}
}

// Open reads configuration. It never dials here (the connection lifetime belongs
// to a Snapshot call), so a configuration error is a missing base_dn only; an
// unreachable server surfaces on Snapshot.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	s.url = cfg.Get("url")
	s.bindDN = cfg.Get("bind_dn")
	s.bindPassword = cfg.Get("bind_password")
	s.baseDN = cfg.Get("base_dn")
	if v := cfg.Get("user_filter"); v != "" {
		s.userFilter = v
	}
	if v := cfg.Get("group_filter"); v != "" {
		s.groupFilter = v
	}
	if n := cfg.GetInt("page_size", int(s.pageSize)); n > 0 {
		s.pageSize = uint32(n)
	}
	s.startTLS = cfg.GetBool("start_tls", false)
	s.skipVerify = cfg.GetBool("insecure_skip_verify", false)
	s.nhiDNSuffix = strings.ToLower(cfg.Get("nhi_dn_suffix"))
	// Semicolon-separated: a DN is itself comma-separated, and RFC 4514 only
	// allows ';' inside a DN escaped, so an unescaped semicolon is a safe split.
	if v := cfg.Get("privileged_group_dns"); v != "" {
		s.privilegedDNs = map[string]bool{}
		for _, dn := range strings.Split(v, ";") {
			if dn = strings.TrimSpace(dn); dn != "" {
				s.privilegedDNs[strings.ToLower(dn)] = true
			}
		}
	}
	s.timeout = cfg.GetDuration("timeout", s.timeout)

	if s.url != "" && s.baseDN == "" {
		return fmt.Errorf("ldap: base_dn is required when url is set")
	}
	return nil
}

// resDirectory is the ResourceKind of a privileged grant: the directory ITSELF
// is the resource an admin-group membership grants access over.
const resDirectory = "ldap.directory"

// Gather emits the PERMITTED grant edges of the directory's privileged groups:
// one identity→directory edge per (member, privileged group), with the group's
// DN as ToolRef (the grant carrier) and Source=SignalPolicy — the PERMITTED side
// of the permitted-vs-observed diff. Membership of an administrative group IS a
// genuine identity→resource grant, while an ordinary group membership remains
// reference data and travels ONLY the typed Snapshot Graph (that rule is
// unchanged). Unlike vault, which parses a grant document, the grant here is
// INFERRED from well-known group semantics, so the mode is per-group-class and
// never guessed:
//
//   - Well-known AD admin groups — builtin Administrators (S-1-5-32-544) and the
//     domain-relative Domain/Schema/Enterprise Admins (RIDs 512/518/519), matched
//     on the group's binary objectSid — carry ModeReadWrite: their rights are
//     genuinely directory-wide administrative RW. The operator groups (Account/
//     Backup/Server Operators) are deliberately NOT matched: their rights are
//     real but not directory-wide RW, and the connector does not guess for them.
//   - Operator-declared groups (privileged_group_dns) carry ModeUnknown: the
//     connector cannot know what a custom group grants. A declared group that is
//     also a well-known admin group keeps the firmer ModeReadWrite.
//
// Members expand transitively through nested fetched groups (a visited set
// bounds cycles); an identity member emits only when its DN resolves to a
// user-search entry, and the edge carries that entry's exact DN so the origin
// converges byte-for-byte with the Snapshot roster (a hard invariant). Members
// resolving to nothing in scope (foreignSecurityPrincipals, computers outside
// the user filter, out-of-scope DNs) are never emitted — they are counted into
// one Info coverage finding per run (docs/SECURITY-HARDENING.md: never silent). AD primary-group
// membership (primaryGroupID) never appears in the member attribute, so a grant
// held ONLY via the primary group is under-detected — a known, documented blind
// spot, not covered by the finding either. Disabled accounts still emit: a
// disabled account still HOLDS its grant (vault precedent); the roster carries
// Disabled as the governance signal.
//
// Delivery is at-least-once: any transport or Emit error returns immediately —
// the engine retries, and a re-emitted edge converges on its natural key
// (origin, resource, mode; the engine's upsert OR-merges the flags and only the
// occurrence count accumulates per pass). One per-run clock stamps every
// observation so first/last_seen stay stable. With no url it returns nil
// (offline).
func (s *Source) Gather(ctx context.Context, sink sdk.Sink) error {
	if s.url == "" {
		return nil // offline
	}
	now := s.clock().UTC()

	conn, err := s.connect()
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	groups, err := s.search(ctx, conn, s.groupFilter)
	if err != nil {
		return fmt.Errorf("ldap: group search: %w", err)
	}
	users, err := s.search(ctx, conn, s.userFilter)
	if err != nil {
		return fmt.Errorf("ldap: user search: %w", err)
	}

	groupsByDN := make(map[string]*goldap.Entry, len(groups))
	for _, g := range groups {
		groupsByDN[strings.ToLower(g.DN)] = g
	}
	usersByDN := make(map[string]*goldap.Entry, len(users))
	for _, u := range users {
		usersByDN[strings.ToLower(u.DN)] = u
	}

	// Origins a privileged group names that the roster cannot vouch for: never
	// emitted (convergence is the invariant), only counted — distinctly, so the
	// finding reports origins, not membership rows.
	outside := map[string]struct{}{}
	for _, pg := range s.privilegedGroups(groups) {
		if err := ctx.Err(); err != nil {
			return err
		}
		resolved, unresolved := expandPrivileged(pg.entry, groupsByDN, usersByDN)
		for _, dn := range unresolved {
			outside[dn] = struct{}{}
		}
		for _, u := range resolved {
			if err := sink.Emit(ctx, model.EdgeObservation{
				OriginKind:   "identity",
				OriginRef:    u.DN,
				ResourceKind: resDirectory,
				ResourceRef:  s.baseDN,
				Mode:         pg.mode,
				Source:       model.SignalPolicy,
				Confidence:   model.ConfidenceAttributed,
				ToolRef:      pg.entry.DN,
				ObservedAt:   now,
			}); err != nil {
				return err
			}
		}
	}
	if len(outside) > 0 {
		if err := sink.Emit(ctx, model.FindingReport{
			Kind:        "coverage",
			Severity:    model.SeverityInfo,
			SubjectKind: "identity_source",
			SubjectRef:  Name,
			Title:       strconv.Itoa(len(outside)) + " permitted-grant origins outside the rostered identity set were not emitted",
			OccurredAt:  now,
		}); err != nil {
			return err
		}
	}
	return nil
}

// Close releases resources; the connector holds no long-lived connection (each
// Snapshot or Gather opens and closes its own).
func (s *Source) Close(context.Context) error { return nil }

// Snapshot connects read-only, binds with the service account, searches users and
// groups, and assembles the identity graph. With no url it returns an empty graph
// (offline). It never returns credential material.
func (s *Source) Snapshot(ctx context.Context) (identitysource.Graph, error) {
	g := identitysource.Graph{Source: identitysource.SourceLDAP, CapturedAt: s.clock().UTC()}
	if s.url == "" {
		return g, nil // offline
	}

	conn, err := s.connect()
	if err != nil {
		return identitysource.Graph{}, err
	}
	defer func() { _ = conn.Close() }()

	// Track which refs are groups so member edges can mark a nested group.
	groupRefs := map[string]struct{}{}

	groups, err := s.search(ctx, conn, s.groupFilter)
	if err != nil {
		return identitysource.Graph{}, fmt.Errorf("ldap: group search: %w", err)
	}
	for _, e := range groups {
		groupRefs[strings.ToLower(e.DN)] = struct{}{}
	}

	users, err := s.search(ctx, conn, s.userFilter)
	if err != nil {
		return identitysource.Graph{}, fmt.Errorf("ldap: user search: %w", err)
	}
	for _, e := range users {
		g.Identities = append(g.Identities, s.toIdentity(e))
	}

	for _, e := range groups {
		g.Collections = append(g.Collections, identitysource.Collection{
			Ref:         e.DN,
			Kind:        identitysource.KindGroup,
			DisplayName: firstNonEmpty(e.GetAttributeValue("displayName"), e.GetAttributeValue("cn")),
			Source:      identitysource.SourceLDAP,
		})
		for _, member := range e.GetAttributeValues("member") {
			mk := identitysource.MemberIdentity
			if _, ok := groupRefs[strings.ToLower(member)]; ok {
				mk = identitysource.MemberCollection
			}
			g.Memberships = append(g.Memberships, identitysource.Membership{
				MemberRef:     member,
				MemberKind:    mk,
				CollectionRef: e.DN,
				Source:        identitysource.SourceLDAP,
			})
		}
	}
	return g, nil
}

// connect dials, optionally upgrades to TLS, and binds with the read-only
// service account. Snapshot and Gather share it, but each call owns its own
// connection — there is no cross-half cache. Errors never carry the bind
// credential.
func (s *Source) connect() (Conn, error) {
	conn, err := s.dialer()(s.url)
	if err != nil {
		return nil, fmt.Errorf("ldap: dial %s: %w", s.url, err)
	}
	if s.startTLS {
		if err := conn.StartTLS(&tls.Config{InsecureSkipVerify: s.skipVerify, MinVersion: tls.VersionTLS12}); err != nil { // #nosec G402 -- skipVerify is an explicit, documented operator opt-in; default verifies
			_ = conn.Close()
			return nil, fmt.Errorf("ldap: starttls: %w", err)
		}
	}
	if s.bindDN != "" {
		if err := conn.Bind(s.bindDN, s.bindPassword); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("ldap: bind: %w", err)
		}
	}
	return conn, nil
}

// search runs one paged subtree search for filter, returning all entries. It
// applies the per-search timeout and the safe attribute allowlist.
func (s *Source) search(ctx context.Context, conn Conn, filter string) ([]*goldap.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	req := goldap.NewSearchRequest(
		s.baseDN,
		goldap.ScopeWholeSubtree, goldap.NeverDerefAliases, 0, int(s.timeout.Seconds()), false,
		filter, safeAttributes, nil,
	)
	res, err := conn.Search(req)
	if err != nil {
		return nil, err
	}
	return res.Entries, nil
}

// privGroup pairs a privileged group entry with the access mode its class
// carries (per-group-class, never per-member, never guessed).
type privGroup struct {
	entry *goldap.Entry
	mode  model.AccessMode
}

// privilegedGroups selects, from the groups ALREADY fetched with groupFilter,
// the ones whose membership constitutes a directory grant: well-known AD admin
// SIDs (ModeReadWrite) and operator-declared DNs (ModeUnknown; the well-known
// match wins when both apply — it is the firmer knowledge). It performs no LDAP
// search of its own — matching is in-memory, so the privileged set is by
// construction a subset of the fetched groups. The result is sorted by DN so
// emission order — and the downstream last-writer-wins tool_ref — is
// deterministic.
func (s *Source) privilegedGroups(groups []*goldap.Entry) []privGroup {
	var out []privGroup
	for _, g := range groups {
		switch {
		case isWellKnownAdmin(g.GetRawAttributeValue("objectSid")):
			out = append(out, privGroup{entry: g, mode: model.ModeReadWrite})
		case s.privilegedDNs[strings.ToLower(g.DN)]:
			out = append(out, privGroup{entry: g, mode: model.ModeUnknown})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].entry.DN < out[j].entry.DN })
	return out
}

// expandPrivileged walks group's member chain, recursing through nested fetched
// groups; the lowercase-DN visited set (seeded with the group itself) bounds
// membership cycles. A member emits only as the entry the USER SEARCH resolved —
// its exact DN is what Snapshot rosters, so the edge origin converges
// byte-for-byte — never as the raw member attribute value. Members resolving to
// neither a fetched group nor a rostered user (foreignSecurityPrincipals,
// computers outside the user filter, out-of-scope DNs) return as unresolved
// lowercase DNs for the caller's coverage count.
func expandPrivileged(group *goldap.Entry, groupsByDN, usersByDN map[string]*goldap.Entry) (resolved []*goldap.Entry, unresolved []string) {
	visited := map[string]struct{}{strings.ToLower(group.DN): {}}
	var walk func(g *goldap.Entry)
	walk = func(g *goldap.Entry) {
		for _, m := range g.GetAttributeValues("member") {
			key := strings.ToLower(m)
			if _, seen := visited[key]; seen {
				continue
			}
			visited[key] = struct{}{}
			switch {
			case groupsByDN[key] != nil:
				walk(groupsByDN[key])
			case usersByDN[key] != nil:
				resolved = append(resolved, usersByDN[key])
			default:
				unresolved = append(unresolved, key)
			}
		}
	}
	walk(group)
	return resolved, unresolved
}

// The well-known AD administrative SID constants. The operator groups (Account/
// Backup/Server Operators — RIDs 548/551/549) are deliberately absent: their
// rights are real but NOT directory-wide RW, and the connector never guesses.
const (
	sidAuthorityNT      = 5   // the NT authority (S-1-5-…)
	sidSubBuiltin       = 32  // S-1-5-32-…: the BUILTIN domain
	ridBuiltinAdmins    = 544 // S-1-5-32-544: builtin Administrators
	sidSubDomain        = 21  // S-1-5-21-…: a machine/domain SID
	ridDomainAdmins     = 512
	ridSchemaAdmins     = 518
	ridEnterpriseAdmins = 519
)

// sid is a decoded Windows security identifier: the 48-bit identifier authority
// plus the sub-authority chain (the revision byte is validated, not kept).
type sid struct {
	authority uint64
	subAuth   []uint32
}

// parseSID decodes a binary objectSid: revision (1 byte, must be 1),
// subAuthorityCount (1 byte, at most 15), identifierAuthority (6 bytes,
// big-endian), then count little-endian 4-byte sub-authorities. A malformed
// value returns ok=false and the caller treats the group as not privileged —
// the connector never derives privilege from a value it cannot parse.
func parseSID(b []byte) (sid, bool) {
	if len(b) < 8 || b[0] != 1 {
		return sid{}, false
	}
	n := int(b[1])
	if n > 15 || len(b) != 8+4*n {
		return sid{}, false
	}
	var auth uint64
	for _, c := range b[2:8] {
		auth = auth<<8 | uint64(c)
	}
	s := sid{authority: auth, subAuth: make([]uint32, n)}
	for i := range s.subAuth {
		s.subAuth[i] = binary.LittleEndian.Uint32(b[8+4*i:])
	}
	return s, true
}

// isWellKnownAdmin reports whether a group's binary objectSid is one of the
// well-known AD administrative groups: builtin Administrators (sub-authorities
// exactly [32 544]) or a domain-relative Domain/Schema/Enterprise Admins group
// (S-1-5-21-…-512/518/519). Anything else — including a missing or malformed
// SID — is not privileged by SID.
func isWellKnownAdmin(raw []byte) bool {
	s, ok := parseSID(raw)
	if !ok || s.authority != sidAuthorityNT || len(s.subAuth) < 2 {
		return false
	}
	if len(s.subAuth) == 2 && s.subAuth[0] == sidSubBuiltin && s.subAuth[1] == ridBuiltinAdmins {
		return true
	}
	if s.subAuth[0] != sidSubDomain {
		return false
	}
	switch s.subAuth[len(s.subAuth)-1] {
	case ridDomainAdmins, ridSchemaAdmins, ridEnterpriseAdmins:
		return true
	}
	return false
}

// toIdentity maps one directory entry to an Identity, classifying it and reading
// only the safe attributes. It never reads a password attribute (none is in the
// allowlist) and puts only governance metadata in Attributes.
func (s *Source) toIdentity(e *goldap.Entry) identitysource.Identity {
	ptype, kind := s.classify(e)
	id := identitysource.Identity{
		Ref:         e.DN,
		Type:        ptype,
		Kind:        kind,
		DisplayName: firstNonEmpty(e.GetAttributeValue("displayName"), e.GetAttributeValue("cn")),
		Source:      identitysource.SourceLDAP,
		Disabled:    disabledFromUAC(e.GetAttributeValue("userAccountControl")),
		Attributes:  map[string]string{},
	}
	for _, k := range []string{"sAMAccountName", "uid", "userPrincipalName", "mail"} {
		if v := e.GetAttributeValue(k); v != "" {
			id.Attributes[strings.ToLower(k)] = v
		}
	}
	if len(id.Attributes) == 0 {
		id.Attributes = nil
	}
	return id
}

// classify decides the human/NHI nature of an entry, never guessing: an explicit
// operator NHI marker or a machine objectClass yields NHI; a person/user yields
// human; anything else is PrincipalUnknown (shown honestly). The computer case is
// checked before the user case because in AD a computer is a subclass of user.
func (s *Source) classify(e *goldap.Entry) (identitysource.PrincipalType, string) {
	ocs := lowerSet(e.GetAttributeValues("objectClass"))
	if s.nhiDNSuffix != "" && strings.Contains(strings.ToLower(e.DN), s.nhiDNSuffix) {
		return identitysource.PrincipalNHI, "service_account"
	}
	switch {
	case ocs["computer"]:
		return identitysource.PrincipalNHI, "computer"
	case ocs["msds-managedserviceaccount"], ocs["msds-groupmanagedserviceaccount"]:
		return identitysource.PrincipalNHI, "managed_service_account"
	case ocs["inetorgperson"], ocs["organizationalperson"], ocs["person"], ocs["user"]:
		return identitysource.PrincipalHuman, "user"
	default:
		return identitysource.PrincipalUnknown, "user"
	}
}

// dialer returns the injected dialer or the production one. The production
// dialer captures the CONFIGURED page size (Open may have changed it from the
// default) so SearchWithPaging pages with the operator's page_size.
func (s *Source) dialer() Dialer {
	if s.dial != nil {
		return s.dial
	}
	pageSize := s.pageSize
	return func(url string) (Conn, error) {
		c, err := goldap.DialURL(url)
		if err != nil {
			return nil, err
		}
		return &realConn{c: c, pageSize: pageSize}, nil
	}
}

func (s *Source) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// realConn adapts *goldap.Conn to the Conn interface, routing Search through the
// paged variant so large directories page transparently.
type realConn struct {
	c        *goldap.Conn
	pageSize uint32
}

func (r *realConn) Bind(u, p string) error         { return r.c.Bind(u, p) }
func (r *realConn) StartTLS(cfg *tls.Config) error { return r.c.StartTLS(cfg) }
func (r *realConn) Close() error                   { return r.c.Close() }
func (r *realConn) Search(req *goldap.SearchRequest) (*goldap.SearchResult, error) {
	if r.pageSize > 0 {
		return r.c.SearchWithPaging(req, r.pageSize)
	}
	return r.c.Search(req)
}

// disabledFromUAC reports whether an AD userAccountControl value has the
// ACCOUNTDISABLE bit set. A missing/unparseable value is treated as not disabled
// (a non-AD directory has no UAC, and the connector never guesses a disable).
func disabledFromUAC(v string) bool {
	if v == "" {
		return false
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return false
	}
	return n&uacAccountDisable != 0
}

func lowerSet(vs []string) map[string]bool {
	m := make(map[string]bool, len(vs))
	for _, v := range vs {
		m[strings.ToLower(strings.TrimSpace(v))] = true
	}
	return m
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
