// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance_test

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/governance"
)

// storePolicy writes an approval policy straight to the store and returns its id:
// authoring one that raises a tier or a quorum, or matches by change class, is a
// Business act, but a stored one is honored by the engine whichever edition wrote it.
func storePolicy(t *testing.T, h *harness, tenant model.TenantID, name string, spec map[string]any) string {
	t.Helper()
	var id string
	if err := h.st.Mutate(context.Background(), tenant, func(sc store.Scope) error {
		p, err := sc.Policies().Create(context.Background(), model.Policy{Name: name, Kind: "approval", Enabled: true, Spec: spec})
		id = p.ID.String()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatalf("stored policy %q has no id", name)
	}
	return id
}

func requestPublication(t *testing.T, service *governance.EngineApprovals, tenant model.TenantID, p auth.Principal, ref, launcher string, class governance.ChangeClassification) governance.Approval {
	t.Helper()
	out, err := service.Request(context.Background(), tenant, p, governance.ApprovalRequest{
		Action: "gitpublish.push", SubjectKind: "gitpublish.target", SubjectRef: ref, SessionRef: p.SessionIdentity,
		LaunchedBy: launcher, ChangeClass: class.Class, ChangePaths: class.Paths,
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A policy that matches the class raises the quorum of a sensitive change and
// leaves a docs-only one at one person; the class and its paths ride the card
// and the ledger entry.
func TestApprovalPolicyByChangeClassRaisesTheQuorumOfASensitiveChange(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "class-quorum")
	_, second := h.roleUser(admin, tenant, "second@x.io", "admin")
	principal := cancellationSession(t, h, tenant, admin, "quorum")
	storePolicy(t, h, tenant, "sensitive publications need two", map[string]any{
		"required_approvals": 2, "match": map[string]any{"action": "gitpublish.push", "change_class": "sensitive"},
	})
	service := h.gov.EngineApprovals()

	sensitive := requestPublication(t, service, tenant, principal, "a", "", governance.ChangeClassification{
		Class: "sensitive", Paths: []string{".github/workflows/ci.yml"}})
	if sensitive.RequiredApprovals != 2 || sensitive.ChangeClass != "sensitive" || !reflect.DeepEqual(sensitive.ChangePaths, []string{".github/workflows/ci.yml"}) {
		t.Fatalf("sensitive request = required %d class %q paths %q, want 2 sensitive [.github/workflows/ci.yml]", sensitive.RequiredApprovals, sensitive.ChangeClass, sensitive.ChangePaths)
	}
	if r := h.decide(admin, tenant, sensitive.ID, "approve"); r.code != http.StatusOK || r.body["status"] != "pending" {
		t.Fatalf("first of two approvals = %d %s, want pending", r.code, r.raw)
	}
	if r := h.decide(second, tenant, sensitive.ID, "approve"); r.code != http.StatusOK || r.body["status"] != "approved" {
		t.Fatalf("second approval = %d %s, want approved", r.code, r.raw)
	}

	docs := requestPublication(t, service, tenant, principal, "b", "", governance.ChangeClassification{Class: "docs_only", Paths: []string{"README.md"}})
	if docs.RequiredApprovals != 1 || docs.PolicyRef != "" {
		t.Fatalf("docs_only request = required %d policy %q, want 1 and no policy", docs.RequiredApprovals, docs.PolicyRef)
	}
	if r := h.decide(admin, tenant, docs.ID, "approve"); r.code != http.StatusOK || r.body["status"] != "approved" {
		t.Fatalf("one approval of a docs_only change = %d %s, want approved", r.code, r.raw)
	}
	// A request no one classified is governed as before.
	plain := requestPublication(t, service, tenant, principal, "c", "", governance.ChangeClassification{})
	if plain.RequiredApprovals != 1 || plain.ChangeClass != "" {
		t.Fatalf("unclassified request = required %d class %q, want 1 and none", plain.RequiredApprovals, plain.ChangeClass)
	}

	if r := h.do("GET", govPath+"/approvals/"+sensitive.ID, admin, nil, tenantHdr(tenant)); r.code != http.StatusOK || r.body["change_class"] != "sensitive" {
		t.Fatalf("approval read = %d %s, want the class on the card", r.code, r.raw)
	}
}

// A policy may require someone other than the launcher: the launcher's own
// decision is refused and another person's stands.
func TestApprovalPolicyCanExcludeTheLauncher(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "class-launcher")
	_, other := h.roleUser(admin, tenant, "other@x.io", "admin")
	principal := cancellationSession(t, h, tenant, admin, "launcher")
	launcher, err := h.authr.Authenticate(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	storePolicy(t, h, tenant, "not the launcher", map[string]any{
		"require_other_than_launcher": true, "match": map[string]any{"action": "gitpublish.push", "change_class": "sensitive"},
	})
	service := h.gov.EngineApprovals()

	sensitive := requestPublication(t, service, tenant, principal, "a", launcher.Actor(), governance.ChangeClassification{
		Class: "sensitive", Paths: []string{"go.mod"}})
	if r := h.decide(admin, tenant, sensitive.ID, "approve"); r.code != http.StatusForbidden {
		t.Fatalf("the launcher deciding a sensitive change = %d %s, want 403", r.code, r.raw)
	}
	if r := h.decide(other, tenant, sensitive.ID, "approve"); r.code != http.StatusOK || r.body["status"] != "approved" {
		t.Fatalf("another person deciding = %d %s, want approved", r.code, r.raw)
	}
	// The launcher may still reject their own session's change: rejecting is the
	// safe direction.
	again := requestPublication(t, service, tenant, principal, "r", launcher.Actor(), governance.ChangeClassification{Class: "sensitive", Paths: []string{"go.mod"}})
	if r := h.decide(admin, tenant, again.ID, "reject"); r.code != http.StatusOK || r.body["status"] != "rejected" {
		t.Fatalf("the launcher rejecting a sensitive change = %d %s, want rejected", r.code, r.raw)
	}
	// The same launcher still decides a change the policy does not name.
	docs := requestPublication(t, service, tenant, principal, "b", launcher.Actor(), governance.ChangeClassification{Class: "docs_only", Paths: []string{"README.md"}})
	if r := h.decide(admin, tenant, docs.ID, "approve"); r.code != http.StatusOK || r.body["status"] != "approved" {
		t.Fatalf("the launcher deciding a docs_only change = %d %s, want approved", r.code, r.raw)
	}
}

// The tenant's sensitive globs are the sensitive_paths of its policies, else
// the defaults; a policy that lists none changes nothing.
func TestChangeClassPolicyActiveAndTheTenantsSensitiveGlobs(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "class-globs")
	service := h.gov.EngineApprovals()
	ctx := context.Background()
	active := func() bool {
		t.Helper()
		on, err := service.ChangeClassPolicyActive(ctx, tenant, "gitpublish.push")
		if err != nil {
			t.Fatal(err)
		}
		return on
	}
	classify := func(paths ...string) governance.ChangeClassification {
		t.Helper()
		out, err := service.ClassifyChange(ctx, tenant, "gitpublish.push", paths)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if active() {
		t.Fatal("a tenant with no policy reports a class policy")
	}
	storePolicy(t, h, tenant, "plain quorum", map[string]any{"required_approvals": 2, "match": map[string]any{"action": "gitpublish.push"}})
	if active() {
		t.Fatal("a policy that names no class reports a class policy")
	}
	if got := classify("infra/main.tf"); got.Class != "code" {
		t.Fatalf("infra/main.tf by default = %+v, want code", got)
	}
	storePolicy(t, h, tenant, "exclusion alone", map[string]any{"require_other_than_launcher": true, "match": map[string]any{"action": "gitpublish.pull_request"}})
	if on, err := service.ChangeClassPolicyActive(ctx, tenant, "gitpublish.pull_request"); err != nil || !on {
		t.Fatalf("a policy that excludes the launcher is active = %v %v, want true", on, err)
	}
	storePolicy(t, h, tenant, "infra is sensitive", map[string]any{
		"required_approvals": 2, "match": map[string]any{"action": "gitpublish.push", "change_class": "sensitive"},
		"sensitive_paths": []string{"infra/**"},
	})
	if !active() {
		t.Fatal("a class policy is not reported")
	}
	if got := classify("infra/main.tf", "README.md"); got.Class != "sensitive" || !reflect.DeepEqual(got.Paths, []string{"infra/main.tf"}) {
		t.Fatalf("infra/main.tf with a tenant glob = %+v, want sensitive [infra/main.tf]", got)
	}
	// A tenant's globs are added to the defaults, never taken from them.
	if got := classify("go.mod"); got.Class != "sensitive" {
		t.Fatalf("go.mod with tenant globs = %+v, want sensitive by default", got)
	}
	// The tenant globs belong to the action their policy governs.
	other, err := service.ClassifyChange(ctx, tenant, "gitpublish.pull_request", []string{"infra/main.tf"})
	if err != nil || other.Class != "code" {
		t.Fatalf("infra/main.tf for an action the globs do not govern = %+v %v, want code", other, err)
	}
}

// A policy that names a class that does not exist, or a glob that cannot
// match a repository path, is malformed in either edition.
func TestApprovalPolicyByChangeClassIsValidated(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "class-authoring")
	for name, spec := range map[string]map[string]any{
		"unknown class":    {"match": map[string]any{"change_class": "everything"}},
		"absolute glob":    {"sensitive_paths": []string{"/etc/passwd"}},
		"parent glob":      {"sensitive_paths": []string{"../x"}},
		"unclosed bracket": {"sensitive_paths": []string{"[a"}},
		"empty glob":       {"sensitive_paths": []string{""}},
		"too many stars":   {"sensitive_paths": []string{"**/**/**/x"}},
	} {
		r := h.do("POST", govPath+"/policies", admin, map[string]any{"name": "p", "kind": "approval", "enabled": true, "spec": spec}, tenantHdr(tenant))
		if r.code != http.StatusBadRequest {
			t.Errorf("%s = %d %s, want 400", name, r.code, r.raw)
		}
	}
}

// A policy that names the class governs a change of that class in place of one
// that names none, whatever order they were stored in; a change of any other
// class, and one no one classified, keeps the policy that names none. The
// exclusion of the launcher is the exception: it is the sum of the policies.
func TestApprovalPolicyThatNamesTheClassIsPreferredAndExclusionAdds(t *testing.T) {
	h := newHarness(t)
	admin := h.adminLogin()
	tenant := h.createOrg(admin, "class-precedence")
	_, other := h.roleUser(admin, tenant, "other@x.io", "admin")
	principal := cancellationSession(t, h, tenant, admin, "precedence")
	launcher, err := h.authr.Authenticate(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	// The class policy is stored first and the generic one after it, then a
	// second class policy for another class.
	storePolicy(t, h, tenant, "sensitive two", map[string]any{"required_approvals": 2, "match": map[string]any{"action": "gitpublish.push", "change_class": "sensitive"}})
	storePolicy(t, h, tenant, "generic three and not the launcher", map[string]any{"required_approvals": 3, "require_other_than_launcher": true, "match": map[string]any{"action": "gitpublish.push"}})
	storePolicy(t, h, tenant, "docs one", map[string]any{"required_approvals": 1, "match": map[string]any{"action": "gitpublish.push", "change_class": "docs_only"}})
	service := h.gov.EngineApprovals()
	want := map[string]int64{"sensitive": 2, "docs_only": 1, "code": 3, "": 3}
	for class, required := range want {
		paths := []string{"x"}
		if class == "" {
			paths = nil
		}
		got := requestPublication(t, service, tenant, principal, "ref-"+class, launcher.Actor(), governance.ChangeClassification{Class: class, Paths: paths})
		if got.RequiredApprovals != required {
			t.Errorf("class %q needs %d approvals, want %d (policy %s)", class, got.RequiredApprovals, required, got.PolicyRef)
		}
	}
	// A docs_only change is governed by its own policy for the quorum and still
	// by the generic one for the launcher.
	docs := requestPublication(t, service, tenant, principal, "docs-launcher", launcher.Actor(), governance.ChangeClassification{Class: "docs_only", Paths: []string{"README.md"}})
	if r := h.decide(admin, tenant, docs.ID, "approve"); r.code != http.StatusForbidden {
		t.Fatalf("the launcher approving a docs_only change = %d %s, want 403 from the generic policy", r.code, r.raw)
	}
	if r := h.decide(other, tenant, docs.ID, "approve"); r.code != http.StatusOK || r.body["status"] != "approved" {
		t.Fatalf("another person approving = %d %s, want approved with the docs_only quorum of one", r.code, r.raw)
	}
}
