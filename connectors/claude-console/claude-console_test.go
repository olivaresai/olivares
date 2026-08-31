// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudeconsole

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/model"
)

func fixedClock() time.Time { return time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC) }

// routeDoer answers requests from a handler keyed by path, recording every request
// so a test can assert the connector is GET-only.
type routeDoer struct {
	t       *testing.T
	reqs    []*http.Request
	handler func(*http.Request) (int, string)
}

func (d *routeDoer) Do(req *http.Request) (*http.Response, error) {
	d.reqs = append(d.reqs, req)
	st, body := d.handler(req)
	return &http.Response{StatusCode: st, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}

type captureSink struct{ obs []model.Observation }

func (s *captureSink) Emit(_ context.Context, o model.Observation) error {
	s.obs = append(s.obs, o)
	return nil
}

func TestGatherEmitsPostureFindingEvenOffline(t *testing.T) {
	s := New()
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{}); err != nil { // no admin_key => offline
		t.Fatal(err)
	}
	sink := &captureSink{}
	if err := s.Gather(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	// Two structural posture findings: the SSO/SCIM blind spot + the model-access gap.
	if len(sink.obs) != 2 {
		t.Fatalf("emitted %d observations, want 2", len(sink.obs))
	}
	f, ok := sink.obs[0].(model.FindingReport)
	if !ok {
		t.Fatalf("emitted %T, want FindingReport", sink.obs[0])
	}
	if f.Kind != "iam_posture" || f.Severity != model.SeverityMedium || f.SubjectKind != "claude_org" {
		t.Errorf("finding = %+v", f)
	}
	// Recalibrated wording: still names the SSO + SCIM blind spot, and now records that
	// groups + custom roles ARE listable via the ce-user-management beta.
	if f.DetailHash == "" || !strings.Contains(f.Title, "SSO") || !strings.Contains(f.Title, "SCIM") {
		t.Errorf("finding title not the honest blind-spot statement: %q", f.Title)
	}
	if !strings.Contains(f.Title, "groups") || !strings.Contains(f.Title, "custom roles") {
		t.Errorf("blind-spot finding not recalibrated for the RBAC beta: %q", f.Title)
	}
	// Second finding: the model-access (entitlements) Console-only gap (E2).
	gap, ok := sink.obs[1].(model.FindingReport)
	if !ok {
		t.Fatalf("second obs %T, want FindingReport", sink.obs[1])
	}
	if !strings.Contains(gap.Title, "model access") || !strings.Contains(gap.Title, "Console-only") {
		t.Errorf("model-access gap finding not emitted: %q", gap.Title)
	}
}

func TestSnapshotReconcilesAdminAPISurface(t *testing.T) {
	doer := &routeDoer{t: t, handler: func(req *http.Request) (int, string) {
		switch {
		case req.URL.Path == usersPath:
			if req.URL.Query().Get("after_id") == "" {
				return 200, `{"data":[{"id":"user_1","email":"a@acme.com","name":"Ann","role":"admin"}],"has_more":true,"last_id":"user_1"}`
			}
			return 200, `{"data":[{"id":"user_2","email":"b@acme.com","role":"developer"}],"has_more":false,"last_id":"user_2"}`
		case req.URL.Path == invitesPath:
			return 200, `{"data":[{"id":"inv_1","email":"c@acme.com","role":"user","status":"pending"}],"has_more":false}`
		case req.URL.Path == workspacesPath:
			return 200, `{"data":[{"id":"ws_1","name":"Engineering"}],"has_more":false}`
		case strings.HasPrefix(req.URL.Path, workspacesPath+"/"):
			return 200, `{"data":[{"user_id":"user_1","workspace_role":"workspace_admin"}],"has_more":false}`
		case strings.HasPrefix(req.URL.Path, rbacGroupsPath), strings.HasPrefix(req.URL.Path, rbacRolesPath):
			// A non-CE / under-scoped key: the RBAC beta 404s. Snapshot must DEGRADE
			// (keep the member/workspace roster) rather than fail.
			return 404, `{"error":{"type":"not_found_error","message":"beta not enabled"}}`
		default:
			t.Fatalf("unexpected path %q", req.URL.Path)
			return 0, ""
		}
	}}
	s := New()
	s.doer = doer
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"admin_key": "sk-ant-admin-test", "org_ref": "acme-org"}}); err != nil {
		t.Fatal(err)
	}

	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if g.Source != SourceClaudeConsole {
		t.Errorf("source = %q", g.Source)
	}
	// 2 members (pagination followed) + 1 invite.
	if len(g.Identities) != 3 {
		t.Fatalf("identities = %d, want 3 (got %+v)", len(g.Identities), g.Identities)
	}
	user1, ok := g.FindIdentity("user_1")
	if !ok || user1.Type != identitysource.PrincipalHuman || user1.Attributes["role"] != "admin" {
		t.Errorf("user_1 = %+v", user1)
	}
	if _, ok := g.FindIdentity("user_2"); !ok {
		t.Error("pagination did not follow to the second page (user_2 missing)")
	}
	if len(g.Collections) != 1 || g.Collections[0].Ref != "ws_1" || g.Collections[0].Kind != identitysource.KindGroup {
		t.Errorf("collections = %+v", g.Collections)
	}
	if len(g.Memberships) != 1 || g.Memberships[0].MemberRef != "user_1" || g.Memberships[0].CollectionRef != "ws_1" {
		t.Errorf("memberships = %+v", g.Memberships)
	}
	// Read-only: every request is a GET.
	for _, r := range doer.reqs {
		if r.Method != http.MethodGet {
			t.Errorf("non-GET request %s %s", r.Method, r.URL.Path)
		}
	}
}

func TestSnapshotReadsRBACGroupsAndCustomRoles(t *testing.T) {
	doer := &routeDoer{t: t, handler: func(req *http.Request) (int, string) {
		switch {
		case req.URL.Path == usersPath:
			return 200, `{"data":[{"id":"user_1","email":"a@acme.com","name":"Ann","role":"managed"}],"has_more":false,"last_id":"user_1"}`
		case req.URL.Path == invitesPath:
			return 200, `{"data":[],"has_more":false}`
		case req.URL.Path == workspacesPath:
			return 200, `{"data":[],"has_more":false}`
		case req.URL.Path == rbacGroupsPath:
			return 200, `{"data":[{"id":"rbac_group_1","name":"Platform","source_type":"scim","roles":["rbac_role_1"]}],"has_more":false}`
		case req.URL.Path == rbacGroupsPath+"/rbac_group_1/members":
			return 200, `{"data":[{"group_id":"rbac_group_1","user_id":"user_1","email":"a@acme.com"}],"has_more":false}`
		case req.URL.Path == rbacRolesPath:
			return 200, `{"data":[{"id":"rbac_role_1","name":"Deployer"}],"has_more":false}`
		default:
			t.Fatalf("unexpected path %q", req.URL.Path)
			return 0, ""
		}
	}}
	s := New()
	s.doer = doer
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"admin_key": "sk-ant-admin-test", "org_ref": "acme-org"}}); err != nil {
		t.Fatal(err)
	}
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// The RBAC group is a KindGroup collection carrying source_type + roles; the custom
	// role is a KindRole collection. Workspaces are absent here, so the only group-kind
	// collection is the rbac_group and the only role-kind is the custom role.
	var group, role *identitysource.Collection
	for i := range g.Collections {
		switch g.Collections[i].Ref {
		case "rbac_group_1":
			group = &g.Collections[i]
		case "rbac_role_1":
			role = &g.Collections[i]
		}
	}
	if group == nil || group.Kind != identitysource.KindGroup {
		t.Fatalf("rbac_group_1 collection missing/mis-kinded: %+v", g.Collections)
	}
	if group.Attributes["source_type"] != "scim" || group.Attributes["collection_type"] != "rbac_group" || group.Attributes["roles"] != "rbac_role_1" {
		t.Errorf("rbac_group attributes = %+v", group.Attributes)
	}
	if role == nil || role.Kind != identitysource.KindRole || role.Attributes["collection_type"] != "custom_role" {
		t.Fatalf("rbac_role_1 collection missing/mis-kinded: %+v", g.Collections)
	}
	// The group membership edge (user_1 → rbac_group_1) is present.
	var found bool
	for _, m := range g.Memberships {
		if m.MemberRef == "user_1" && m.CollectionRef == "rbac_group_1" {
			found = true
		}
	}
	if !found {
		t.Errorf("group membership user_1→rbac_group_1 missing: %+v", g.Memberships)
	}

	// The beta header rides ONLY the rbac_* requests; member/invite/workspace calls
	// must not carry it (they 404-free without it and do not need it).
	for _, r := range doer.reqs {
		isRBAC := strings.HasPrefix(r.URL.Path, rbacGroupsPath) || strings.HasPrefix(r.URL.Path, rbacRolesPath)
		got := r.Header.Get("anthropic-beta")
		if isRBAC && got != ceUserMgmtBeta {
			t.Errorf("rbac request %s missing beta header, got %q", r.URL.Path, got)
		}
		if !isRBAC && got != "" {
			t.Errorf("non-rbac request %s carried beta header %q", r.URL.Path, got)
		}
		if r.Method != http.MethodGet {
			t.Errorf("non-GET request %s %s", r.Method, r.URL.Path)
		}
	}
}

// TestSnapshotRBACPartialReadDegradesCleanly proves the all-or-nothing degrade (review fix): when the rbac_groups LIST succeeds (200) but the per-group /members read
// 403s (a key with read:rbac_groups but not read:members), the Snapshot must NOT leak a
// memberless group into the roster — it discards ALL staged RBAC data, keeps the
// member/workspace roster, and returns no error.
func TestSnapshotRBACPartialReadDegradesCleanly(t *testing.T) {
	doer := &routeDoer{t: t, handler: func(req *http.Request) (int, string) {
		switch {
		case req.URL.Path == usersPath:
			return 200, `{"data":[{"id":"user_1","email":"a@acme.com","role":"managed"}],"has_more":false,"last_id":"user_1"}`
		case req.URL.Path == invitesPath:
			return 200, `{"data":[],"has_more":false}`
		case req.URL.Path == workspacesPath:
			return 200, `{"data":[],"has_more":false}`
		case req.URL.Path == rbacGroupsPath:
			return 200, `{"data":[{"id":"rbac_group_1","name":"Platform","source_type":"direct","roles":[]}],"has_more":false}`
		case req.URL.Path == rbacGroupsPath+"/rbac_group_1/members":
			return 403, `{"error":{"type":"permission_error","message":"missing read:members"}}`
		case req.URL.Path == rbacRolesPath:
			return 200, `{"data":[{"id":"rbac_role_1","name":"Deployer"}],"has_more":false}`
		default:
			t.Fatalf("unexpected path %q", req.URL.Path)
			return 0, ""
		}
	}}
	s := New()
	s.doer = doer
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{Settings: map[string]string{"admin_key": "sk-ant-admin-test", "org_ref": "acme-org"}}); err != nil {
		t.Fatal(err)
	}
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("partial RBAC read must degrade cleanly, got error: %v", err)
	}
	// The member roster survives.
	if _, ok := g.FindIdentity("user_1"); !ok {
		t.Error("member roster lost on RBAC partial-read degrade")
	}
	// NOT ONE rbac_group collection leaked (the whole group surface was discarded because
	// its members could not be read). The custom-role read succeeded independently, so the
	// role collection MAY be present — but no group and no group membership.
	for _, c := range g.Collections {
		if c.Ref == "rbac_group_1" {
			t.Errorf("memberless rbac_group leaked into roster on partial read: %+v", c)
		}
	}
	if len(g.Memberships) != 0 {
		t.Errorf("no group memberships must be present after members 403: %+v", g.Memberships)
	}
}

func TestSnapshotOfflineReturnsEmptyRoster(t *testing.T) {
	s := New()
	s.now = fixedClock
	if err := s.Open(context.Background(), sdk.Config{}); err != nil { // no admin_key
		t.Fatal(err)
	}
	g, err := s.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("offline snapshot: %v", err)
	}
	if len(g.Identities) != 0 || len(g.Collections) != 0 {
		t.Errorf("offline roster not empty: %+v", g)
	}
}
