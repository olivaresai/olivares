// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package ldap

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	goldap "github.com/go-ldap/ldap/v3"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// testBindPassword is a distinctive sentinel so tests can assert the credential
// never leaks into a returned error.
const testBindPassword = "s3cr3t-bind-pw"

// fakeConn is an in-memory directory: it answers a Search by returning the
// entries whose filter matches, recording every requested attribute set so a test
// can assert the connector never asks for a password attribute.
type fakeConn struct {
	users      []*goldap.Entry
	groups     []*goldap.Entry
	bindCalls  []string
	bindErr    error
	startTLS   bool
	askedAttrs [][]string
	closed     bool
}

func entry(dn string, attrs map[string][]string) *goldap.Entry {
	e := &goldap.Entry{DN: dn}
	for k, v := range attrs {
		e.Attributes = append(e.Attributes, &goldap.EntryAttribute{Name: k, Values: v})
	}
	return e
}

// withSID attaches a binary objectSid the way the wire delivers it (ByteValues).
func withSID(e *goldap.Entry, sid []byte) *goldap.Entry {
	e.Attributes = append(e.Attributes, &goldap.EntryAttribute{Name: "objectSid", ByteValues: [][]byte{sid}})
	return e
}

// sidBytes builds a binary SID: revision 1, sub-authority count, 6-byte
// big-endian identifier authority, little-endian 4-byte sub-authorities.
func sidBytes(authority uint64, subs ...uint32) []byte {
	b := []byte{1, byte(len(subs))}
	for shift := 40; shift >= 0; shift -= 8 {
		b = append(b, byte(authority>>shift))
	}
	for _, s := range subs {
		var w [4]byte
		binary.LittleEndian.PutUint32(w[:], s)
		b = append(b, w[:]...)
	}
	return b
}

func (f *fakeConn) Bind(u, _ string) error     { f.bindCalls = append(f.bindCalls, u); return f.bindErr }
func (f *fakeConn) StartTLS(*tls.Config) error { f.startTLS = true; return nil }
func (f *fakeConn) Close() error               { f.closed = true; return nil }
func (f *fakeConn) Search(req *goldap.SearchRequest) (*goldap.SearchResult, error) {
	f.askedAttrs = append(f.askedAttrs, req.Attributes)
	if strings.Contains(req.Filter, "group") {
		return &goldap.SearchResult{Entries: f.groups}, nil
	}
	return &goldap.SearchResult{Entries: f.users}, nil
}

func sampleDir() *fakeConn {
	return &fakeConn{
		users: []*goldap.Entry{
			entry("cn=Alice,ou=People,dc=corp", map[string][]string{
				"objectClass": {"top", "person", "organizationalPerson", "user"},
				"displayName": {"Alice Smith"}, "sAMAccountName": {"asmith"},
				"mail": {"alice@corp.example"}, "userAccountControl": {"512"},
			}),
			entry("cn=svc-deploy,ou=ServiceAccounts,dc=corp", map[string][]string{
				"objectClass": {"top", "person", "user"},
				"displayName": {"Deploy Bot"}, "sAMAccountName": {"svc-deploy"},
				"userAccountControl": {"514"}, // 512 normal + 2 disabled
			}),
			entry("cn=web01,ou=Computers,dc=corp", map[string][]string{
				"objectClass":    {"top", "person", "organizationalPerson", "user", "computer"},
				"sAMAccountName": {"web01$"},
			}),
		},
		groups: []*goldap.Entry{
			entry("cn=Engineers,ou=Groups,dc=corp", map[string][]string{
				"objectClass": {"top", "group"}, "cn": {"Engineers"},
				"member": {"cn=Alice,ou=People,dc=corp", "cn=Admins,ou=Groups,dc=corp"},
			}),
			entry("cn=Admins,ou=Groups,dc=corp", map[string][]string{
				"objectClass": {"top", "group"}, "cn": {"Admins"},
				"member": {"cn=svc-deploy,ou=ServiceAccounts,dc=corp"},
			}),
		},
	}
}

// The grant-scan fixture DNs (exact case matters: the user-search entries carry
// the canonical casing the edges must converge to).
const (
	dnAlice  = "CN=Alice,OU=People,DC=corp"
	dnBob    = "CN=Bob,OU=People,DC=corp"
	dnSvc    = "CN=svc-deploy,OU=ServiceAccounts,DC=corp"
	dnDA     = "CN=Domain Admins,CN=Users,DC=corp"
	dnTier0  = "CN=Tier0,OU=Groups,DC=corp"
	dnOwners = "CN=App Owners,OU=Groups,DC=corp"
	dnBackup = "CN=Backup Operators,CN=Builtin,DC=corp"
	dnFSP    = "CN=S-1-5-21-999-888-777-1109,CN=ForeignSecurityPrincipals,DC=corp"
)

// privDir is the grant-scan fixture: Domain Admins (well-known SID …-512) holds
// Alice directly (member value cased differently from her entry), the plain
// nested group Tier0 (which holds the DISABLED svc-deploy and cycles back to
// Domain Admins) and a foreignSecurityPrincipal outside the user search; App
// Owners is privileged only by operator declaration; Backup Operators
// (S-1-5-32-551) is a well-known OPERATOR group and must not match.
func privDir() *fakeConn {
	return &fakeConn{
		users: []*goldap.Entry{
			entry(dnAlice, map[string][]string{
				"objectClass": {"top", "person", "user"}, "userAccountControl": {"512"},
			}),
			entry(dnBob, map[string][]string{
				"objectClass": {"top", "person", "user"}, "userAccountControl": {"512"},
			}),
			entry(dnSvc, map[string][]string{
				"objectClass": {"top", "person", "user"}, "userAccountControl": {"514"}, // disabled
			}),
		},
		groups: []*goldap.Entry{
			withSID(entry(dnDA, map[string][]string{
				"objectClass": {"top", "group"}, "cn": {"Domain Admins"},
				"member": {"cn=alice,ou=people,dc=corp", dnTier0, dnFSP},
			}), sidBytes(5, 21, 111, 222, 333, 512)),
			entry(dnTier0, map[string][]string{
				"objectClass": {"top", "group"}, "cn": {"Tier0"},
				"member": {dnSvc, dnDA}, // dnDA closes the cycle
			}),
			entry(dnOwners, map[string][]string{
				"objectClass": {"top", "group"}, "cn": {"App Owners"},
				"member": {dnBob},
			}),
			withSID(entry(dnBackup, map[string][]string{
				"objectClass": {"top", "group"}, "cn": {"Backup Operators"},
				"member": {dnBob},
			}), sidBytes(5, 32, 551)),
		},
	}
}

func openSource(t *testing.T, fc *fakeConn, extra map[string]string) *Source {
	t.Helper()
	s := New()
	s.dial = func(string) (Conn, error) { return fc, nil }
	settings := map[string]string{"url": "ldap://dir:389", "base_dn": "dc=corp", "bind_dn": "cn=reader,dc=corp", "bind_password": testBindPassword}
	for k, v := range extra {
		settings[k] = v
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

var fixedTime = time.Date(2026, 6, 12, 9, 0, 0, 0, time.UTC)

// openGatherSource opens a Source over fc with a fixed clock, so a test asserts
// the exact per-run ObservedAt stamp.
func openGatherSource(t *testing.T, fc *fakeConn, extra map[string]string) *Source {
	t.Helper()
	s := openSource(t, fc, extra)
	s.now = func() time.Time { return fixedTime }
	return s
}

// captureSink records every emitted observation so a test asserts exact shapes.
type captureSink struct{ obs []model.Observation }

func (s *captureSink) Emit(_ context.Context, o model.Observation) error {
	s.obs = append(s.obs, o)
	return nil
}

// errSink fails every Emit, proving Gather surfaces a sink error immediately.
type errSink struct{ err error }

func (s errSink) Emit(context.Context, model.Observation) error { return s.err }

func TestSnapshotBuildsGraph(t *testing.T) {
	fc := sampleDir()
	s := openSource(t, fc, map[string]string{"nhi_dn_suffix": "ou=serviceaccounts"})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if g.Source != identitysource.SourceLDAP {
		t.Errorf("source = %q", g.Source)
	}
	if len(g.Identities) != 3 {
		t.Fatalf("want 3 identities, got %d", len(g.Identities))
	}

	alice, _ := g.FindIdentity("cn=Alice,ou=People,dc=corp")
	if alice.Type != identitysource.PrincipalHuman {
		t.Errorf("Alice should be human, got %q", alice.Type)
	}
	if alice.Disabled {
		t.Error("Alice (UAC 512) should not be disabled")
	}
	if alice.Attributes["mail"] != "alice@corp.example" {
		t.Errorf("Alice mail attribute missing: %v", alice.Attributes)
	}

	svc, _ := g.FindIdentity("cn=svc-deploy,ou=ServiceAccounts,dc=corp")
	if svc.Type != identitysource.PrincipalNHI {
		t.Errorf("svc-deploy should be NHI (nhi_dn_suffix), got %q", svc.Type)
	}
	if !svc.Disabled {
		t.Error("svc-deploy (UAC 514) should be disabled")
	}

	web, _ := g.FindIdentity("cn=web01,ou=Computers,dc=corp")
	if web.Type != identitysource.PrincipalNHI || web.Kind != "computer" {
		t.Errorf("web01 should be NHI/computer, got %q/%q", web.Type, web.Kind)
	}

	if len(g.Collections) != 2 {
		t.Fatalf("want 2 groups, got %d", len(g.Collections))
	}
	// Engineers has a nested group (Admins) and a user (Alice).
	var nested, ident int
	for _, m := range g.Memberships {
		switch m.MemberKind {
		case identitysource.MemberCollection:
			nested++
		case identitysource.MemberIdentity:
			ident++
		}
	}
	if nested != 1 {
		t.Errorf("want 1 nested-group membership (Admins∈Engineers), got %d", nested)
	}
	if ident != 2 {
		t.Errorf("want 2 identity memberships, got %d", ident)
	}
}

// assertNoForbiddenAttrs is the security guard shared by the Snapshot and Gather
// halves: every search may only ask the safe allowlist, never a password
// attribute (objectSid is an identifier, not a credential).
func assertNoForbiddenAttrs(t *testing.T, fc *fakeConn) {
	t.Helper()
	if len(fc.askedAttrs) == 0 {
		t.Fatal("expected at least one search")
	}
	forbidden := []string{"userpassword", "unicodepwd", "ntpwdhistory", "dbcspwd", "lmpwdhistory", "supplementalcredentials"}
	for _, asked := range fc.askedAttrs {
		for _, a := range asked {
			la := strings.ToLower(a)
			for _, f := range forbidden {
				if la == f {
					t.Errorf("connector requested forbidden secret attribute %q", a)
				}
			}
		}
	}
}

func TestNeverRequestsPasswordAttributes(t *testing.T) {
	fc := sampleDir()
	s := openSource(t, fc, nil)
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	assertNoForbiddenAttrs(t, fc)
}

func TestSnapshotBindsAndCloses(t *testing.T) {
	fc := sampleDir()
	s := openSource(t, fc, map[string]string{"start_tls": "true"})
	if _, err := s.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(fc.bindCalls) != 1 || fc.bindCalls[0] != "cn=reader,dc=corp" {
		t.Errorf("bind calls = %v", fc.bindCalls)
	}
	if !fc.startTLS {
		t.Error("StartTLS should have been issued")
	}
	if !fc.closed {
		t.Error("connection should be closed")
	}
}

func TestOfflineNoURL(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("offline Snapshot should not error: %v", err)
	}
	if len(g.Identities) != 0 {
		t.Errorf("offline graph should be empty, got %d identities", len(g.Identities))
	}
}

func TestOpenRequiresBaseDNWhenURLSet(t *testing.T) {
	s := New()
	err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"url": "ldap://x"}})
	if err == nil {
		t.Fatal("expected error: base_dn required when url set")
	}
}

func TestGatherOfflineEmitsNothingAndNeverDials(t *testing.T) {
	s := New()
	s.dial = func(string) (Conn, error) {
		t.Error("offline Gather must not dial")
		return nil, errors.New("dialed offline")
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{}}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("offline Gather should return nil, got %v", err)
	}
	if len(sink.obs) != 0 {
		t.Errorf("offline Gather should emit nothing, got %d observations", len(sink.obs))
	}
}

// TestGatherEmitsPrivilegedGrants is the happy path: exact edge shapes, the
// sorted-DN group order (App Owners < Domain Admins), case-insensitive member
// resolution to the user search's exact DN, transitive expansion through Tier0
// terminating on the cycle, the disabled svc-deploy still emitting, the excluded
// Backup Operators emitting nothing, and the FSP counted into one coverage
// finding.
func TestGatherEmitsPrivilegedGrants(t *testing.T) {
	fc := privDir()
	s := openGatherSource(t, fc, map[string]string{
		// Deliberately lowercased, semicolon-separated, with a declared group the
		// directory does not have (it matches nothing and emits nothing).
		"privileged_group_dns": "cn=app owners,ou=groups,dc=corp; cn=ghost,ou=groups,dc=corp",
		"start_tls":            "true",
	})
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}

	var edges []model.EdgeObservation
	var findings []model.FindingReport
	for _, o := range sink.obs {
		switch v := o.(type) {
		case model.EdgeObservation:
			edges = append(edges, v)
		case model.FindingReport:
			findings = append(findings, v)
		default:
			t.Fatalf("unexpected observation type %T", o)
		}
	}

	wantEdges := []model.EdgeObservation{
		{
			OriginKind: "identity", OriginRef: dnBob,
			ResourceKind: "ldap.directory", ResourceRef: "dc=corp",
			Mode: model.ModeUnknown, Source: model.SignalPolicy,
			Confidence: model.ConfidenceAttributed, ToolRef: dnOwners, ObservedAt: fixedTime,
		},
		{
			OriginKind: "identity", OriginRef: dnAlice,
			ResourceKind: "ldap.directory", ResourceRef: "dc=corp",
			Mode: model.ModeReadWrite, Source: model.SignalPolicy,
			Confidence: model.ConfidenceAttributed, ToolRef: dnDA, ObservedAt: fixedTime,
		},
		{
			OriginKind: "identity", OriginRef: dnSvc,
			ResourceKind: "ldap.directory", ResourceRef: "dc=corp",
			Mode: model.ModeReadWrite, Source: model.SignalPolicy,
			Confidence: model.ConfidenceAttributed, ToolRef: dnDA, ObservedAt: fixedTime,
		},
	}
	if !reflect.DeepEqual(edges, wantEdges) {
		t.Errorf("edges mismatch:\n got: %+v\nwant: %+v", edges, wantEdges)
	}

	wantFinding := model.FindingReport{
		Kind:        "coverage",
		Severity:    model.SeverityInfo,
		SubjectKind: "identity_source",
		SubjectRef:  Name,
		Title:       "1 permitted-grant origins outside the rostered identity set were not emitted",
		OccurredAt:  fixedTime,
	}
	if len(findings) != 1 {
		t.Fatalf("want exactly 1 coverage finding, got %d", len(findings))
	}
	if !reflect.DeepEqual(findings[0], wantFinding) {
		t.Errorf("finding mismatch:\n got: %+v\nwant: %+v", findings[0], wantFinding)
	}

	// Gather dials, upgrades and binds exactly like Snapshot, and closes.
	if len(fc.bindCalls) != 1 || fc.bindCalls[0] != "cn=reader,dc=corp" {
		t.Errorf("bind calls = %v", fc.bindCalls)
	}
	if !fc.startTLS {
		t.Error("StartTLS should have been issued")
	}
	if !fc.closed {
		t.Error("connection should be closed")
	}
}

// TestGatherConvergesWithSnapshotRoster pins the hard invariant: over the same
// fixtures, every emitted OriginRef is an Identity.Ref the Snapshot rosters.
func TestGatherConvergesWithSnapshotRoster(t *testing.T) {
	s := openGatherSource(t, privDir(), map[string]string{"privileged_group_dns": dnOwners})
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	roster := map[string]bool{}
	for _, id := range g.Identities {
		roster[id.Ref] = true
	}

	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var edges int
	for _, o := range sink.obs {
		e, ok := o.(model.EdgeObservation)
		if !ok {
			continue
		}
		edges++
		if !roster[e.OriginRef] {
			t.Errorf("emitted OriginRef %q is not rostered by Snapshot", e.OriginRef)
		}
	}
	if edges == 0 {
		t.Fatal("expected at least one edge to check convergence against")
	}
}

func TestGatherNeverRequestsPasswordAttributes(t *testing.T) {
	fc := privDir()
	s := openGatherSource(t, fc, nil)
	if err := s.Gather(context.Background(), &captureSink{}); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	assertNoForbiddenAttrs(t, fc)
	var sawSID bool
	for _, asked := range fc.askedAttrs {
		for _, a := range asked {
			if a == "objectSid" {
				sawSID = true
			}
		}
	}
	if !sawSID {
		t.Error("objectSid should be requested (needed to match well-known admin SIDs)")
	}
}

func TestGatherReturnsEmitError(t *testing.T) {
	s := openGatherSource(t, privDir(), nil) // Domain Admins alone yields edges
	wantErr := errors.New("sink full")
	if err := s.Gather(context.Background(), errSink{err: wantErr}); !errors.Is(err, wantErr) {
		t.Fatalf("Gather should surface the Emit error immediately, got %v", err)
	}
}

func TestGatherHonorsContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := openGatherSource(t, privDir(), nil)
	if err := s.Gather(ctx, &captureSink{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

// TestErrorsNeverContainCredential asserts a failed bind surfaces an error on
// both halves WITHOUT the bind password in it.
func TestErrorsNeverContainCredential(t *testing.T) {
	fc := privDir()
	fc.bindErr = errors.New("LDAP Result Code 49: Invalid Credentials")
	s := openGatherSource(t, fc, nil)

	err := s.Gather(context.Background(), &captureSink{})
	if err == nil {
		t.Fatal("expected bind error from Gather")
	}
	if strings.Contains(err.Error(), testBindPassword) {
		t.Errorf("Gather error leaks the bind credential: %v", err)
	}

	_, err = s.Snapshot(context.Background())
	if err == nil {
		t.Fatal("expected bind error from Snapshot")
	}
	if strings.Contains(err.Error(), testBindPassword) {
		t.Errorf("Snapshot error leaks the bind credential: %v", err)
	}
}

func TestParseSID(t *testing.T) {
	got, ok := parseSID(sidBytes(5, 21, 1, 2, 3, 512))
	if !ok || got.authority != 5 || !reflect.DeepEqual(got.subAuth, []uint32{21, 1, 2, 3, 512}) {
		t.Errorf("parseSID round-trip = %+v ok=%v", got, ok)
	}

	malformed := [][]byte{
		nil,
		{},
		{1, 1, 0, 0, 0, 0, 0, 5}, // count 1 but no sub-authority bytes
		append([]byte{2}, sidBytes(5, 32, 544)[1:]...), // revision 2
		sidBytes(5, 32, 544)[:10],                      // truncated mid sub-authority
		append(sidBytes(5, 32, 544), 0),                // trailing garbage
		sidBytes(5, make([]uint32, 16)...),             // count 16 > the spec maximum 15
	}
	for i, b := range malformed {
		if _, ok := parseSID(b); ok {
			t.Errorf("malformed[%d] should not parse", i)
		}
	}
}

func TestWellKnownAdminSID(t *testing.T) {
	priv := [][]byte{
		sidBytes(5, 32, 544),                // builtin Administrators
		sidBytes(5, 21, 111, 222, 333, 512), // Domain Admins
		sidBytes(5, 21, 111, 222, 333, 518), // Schema Admins
		sidBytes(5, 21, 111, 222, 333, 519), // Enterprise Admins
	}
	for i, b := range priv {
		if !isWellKnownAdmin(b) {
			t.Errorf("priv[%d] should match", i)
		}
	}
	notPriv := [][]byte{
		nil,                                 // missing objectSid
		sidBytes(5, 32, 548),                // Account Operators: excluded, not directory-wide RW
		sidBytes(5, 32, 549),                // Server Operators: excluded
		sidBytes(5, 32, 551),                // Backup Operators: excluded
		sidBytes(5, 21, 111, 222, 333, 513), // Domain Users
		sidBytes(5, 32, 544, 0),             // builtin match requires exactly [32 544]
		sidBytes(4, 21, 111, 222, 333, 512), // wrong identifier authority
		sidBytes(5, 22, 111, 222, 333, 512), // not the domain sub-authority
	}
	for i, b := range notPriv {
		if isWellKnownAdmin(b) {
			t.Errorf("notPriv[%d] should not match", i)
		}
	}
}

func TestDialError(t *testing.T) {
	s := New()
	s.dial = func(string) (Conn, error) { return nil, errors.New("conn refused") }
	_ = s.Open(context.Background(), sdk.Config{Settings: map[string]string{"url": "ldap://x", "base_dn": "dc=x"}})
	if _, err := s.Snapshot(context.Background()); err == nil {
		t.Fatal("expected dial error")
	}
}
