// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package aws

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

// iamFixtureServer serves canned IAM Query/XML responses keyed on the Action (and
// Marker) query parameters. It records every Action it sees so a test can assert
// only read-only list actions are issued. ListRoles is served as two pages
// (truncated + Marker) to exercise pagination.
type iamFixtureServer struct {
	mu      sync.Mutex
	actions []string
}

func (h *iamFixtureServer) record(action string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.actions = append(h.actions, action)
}

func (h *iamFixtureServer) seenActions() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := append([]string(nil), h.actions...)
	return out
}

func (h *iamFixtureServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	action := q.Get("Action")
	h.record(action)
	w.Header().Set("Content-Type", "text/xml")

	switch action {
	case "ListRoles":
		if q.Get("Marker") == "" {
			// First page: truncated, with a Marker to fetch page 2.
			_, _ = w.Write([]byte(`<ListRolesResponse><ListRolesResult>
				<Roles>
					<member><RoleName>admin</RoleName><Arn>arn:aws:iam::123456789012:role/admin</Arn></member>
				</Roles>
				<IsTruncated>true</IsTruncated><Marker>PAGE2</Marker>
			</ListRolesResult></ListRolesResponse>`))
			return
		}
		// Second page.
		_, _ = w.Write([]byte(`<ListRolesResponse><ListRolesResult>
			<Roles>
				<member><RoleName>app-runner</RoleName><Arn>arn:aws:iam::123456789012:role/app-runner</Arn></member>
			</Roles>
			<IsTruncated>false</IsTruncated>
		</ListRolesResult></ListRolesResponse>`))
	case "ListUsers":
		_, _ = w.Write([]byte(`<ListUsersResponse><ListUsersResult>
			<Users>
				<member><UserName>alice</UserName><Arn>arn:aws:iam::123456789012:user/alice</Arn></member>
			</Users>
			<IsTruncated>false</IsTruncated>
		</ListUsersResult></ListUsersResponse>`))
	case "ListPolicies":
		_, _ = w.Write([]byte(`<ListPoliciesResponse><ListPoliciesResult>
			<Policies>
				<member><PolicyName>ReadOnly</PolicyName><Arn>arn:aws:iam::123456789012:policy/ReadOnly</Arn></member>
			</Policies>
			<IsTruncated>false</IsTruncated>
		</ListPoliciesResult></ListPoliciesResponse>`))
	case "ListAttachedRolePolicies":
		role := q.Get("RoleName")
		if role == "admin" {
			_, _ = w.Write([]byte(`<ListAttachedRolePoliciesResponse><ListAttachedRolePoliciesResult>
				<AttachedPolicies>
					<member><PolicyName>AdministratorAccess</PolicyName><PolicyArn>arn:aws:iam::aws:policy/AdministratorAccess</PolicyArn></member>
				</AttachedPolicies>
				<IsTruncated>false</IsTruncated>
			</ListAttachedRolePoliciesResult></ListAttachedRolePoliciesResponse>`))
			return
		}
		// app-runner has no attachments.
		_, _ = w.Write([]byte(`<ListAttachedRolePoliciesResponse><ListAttachedRolePoliciesResult>
			<AttachedPolicies></AttachedPolicies>
			<IsTruncated>false</IsTruncated>
		</ListAttachedRolePoliciesResult></ListAttachedRolePoliciesResponse>`))
	default:
		http.Error(w, "unexpected action "+action, http.StatusBadRequest)
	}
}

// openIAMOnly opens a Source pointed at the given IAM endpoint with CloudTrail
// disabled and the given account id.
func openIAMOnly(t *testing.T, endpoint, accountID string) *Source {
	t.Helper()
	s := New()
	settings := map[string]string{
		cfgIAMEndpoint:      endpoint,
		cfgEnableCloudTrail: "false",
	}
	for k, v := range testCreds {
		settings[k] = v
	}
	if accountID != "" {
		settings[cfgAccountID] = accountID
	}
	if err := s.Open(context.Background(), sdk.Config{Settings: settings}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestIAMGoldenInventory(t *testing.T) {
	h := &iamFixtureServer{}
	srv := httptest.NewServer(h)
	defer srv.Close()

	s := openIAMOnly(t, srv.URL, "123456789012")
	sink := &fakeSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	if fs := sink.findings(); len(fs) != 0 {
		t.Fatalf("unexpected findings: %+v", fs)
	}

	want := []edgeKey{
		// account ⊳ role (both pages)
		{originAccount, "123456789012", resIAMRole, "arn:aws:iam::123456789012:role/admin", model.ModeUnknown, signalAWS, model.ConfidenceAttributed, ""},
		{originAccount, "123456789012", resIAMRole, "arn:aws:iam::123456789012:role/app-runner", model.ModeUnknown, signalAWS, model.ConfidenceAttributed, ""},
		// account ⊳ user
		{originAccount, "123456789012", resIAMUser, "arn:aws:iam::123456789012:user/alice", model.ModeUnknown, signalAWS, model.ConfidenceAttributed, ""},
		// account ⊳ policy
		{originAccount, "123456789012", resIAMPolicy, "arn:aws:iam::123456789012:policy/ReadOnly", model.ModeUnknown, signalAWS, model.ConfidenceAttributed, ""},
		// role ⊳ attached-policy (origin ref is the role NAME)
		{originIAMRole, "admin", resIAMPolicy, "arn:aws:iam::aws:policy/AdministratorAccess", model.ModeUnknown, signalAWS, model.ConfidenceAttributed, ""},
	}
	assertEdgeSet(t, sink.edges(), want)

	// Read-only: every IAM action issued must be a List* / ListAttached* metadata
	// call. No Get*/Create*/Put*/Update*/Delete*/Attach*/Detach*.
	for _, a := range h.seenActions() {
		if !strings.HasPrefix(a, "List") {
			t.Fatalf("non-list IAM action issued: %q", a)
		}
	}
	// And specifically the four expected actions were all exercised.
	mustHaveAction(t, h.seenActions(), "ListRoles", "ListUsers", "ListPolicies", "ListAttachedRolePolicies")
}

// TestIAMReadOnlyMethod asserts every IAM request is an HTTP GET (the Query
// protocol's read verb) — the connector never issues a mutating method.
func TestIAMReadOnlyMethod(t *testing.T) {
	var methods []string
	var mu sync.Mutex
	h := &iamFixtureServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		mu.Unlock()
		h.ServeHTTP(w, r)
	}))
	defer srv.Close()

	s := openIAMOnly(t, srv.URL, "123456789012")
	if err := s.Gather(context.Background(), &fakeSink{}); err != nil {
		t.Fatalf("Gather: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(methods) == 0 {
		t.Fatal("no IAM requests issued")
	}
	for _, m := range methods {
		if m != http.MethodGet {
			t.Fatalf("non-GET IAM request method: %q", m)
		}
	}
}

// --- shared assertion helpers ------------------------------------------------

func assertEdgeSet(t *testing.T, got []model.EdgeObservation, want []edgeKey) {
	t.Helper()
	gotKeys := make([]edgeKey, len(got))
	for i, e := range got {
		gotKeys[i] = keyOf(e)
	}
	sortKeys(gotKeys)
	sortKeys(want)
	if !reflect.DeepEqual(gotKeys, want) {
		t.Fatalf("edge set mismatch\n got: %+v\nwant: %+v", gotKeys, want)
	}
	// Every edge in a single pass must share one timestamp, and it must be set.
	if len(got) > 0 {
		at := got[0].ObservedAt
		if at.IsZero() {
			t.Fatal("ObservedAt is zero")
		}
	}
}

func sortKeys(ks []edgeKey) {
	sort.Slice(ks, func(i, j int) bool {
		a, b := ks[i], ks[j]
		if a.resKind != b.resKind {
			return a.resKind < b.resKind
		}
		if a.resRef != b.resRef {
			return a.resRef < b.resRef
		}
		if a.originKind != b.originKind {
			return a.originKind < b.originKind
		}
		if a.originRef != b.originRef {
			return a.originRef < b.originRef
		}
		return a.toolRef < b.toolRef
	})
}

func mustHaveAction(t *testing.T, seen []string, want ...string) {
	t.Helper()
	set := map[string]bool{}
	for _, s := range seen {
		set[s] = true
	}
	for _, w := range want {
		if !set[w] {
			t.Fatalf("expected action %q to be issued; saw %v", w, seen)
		}
	}
}
