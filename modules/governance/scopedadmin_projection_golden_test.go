// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import "testing"

// Fixed projection bytes protect stored policy revisions from formatting changes.
// These expectations cover the domain caller, including its ordering and delegation.
func TestProjectionIsByteIdenticalToGolden(t *testing.T) {
	roles := map[string]customRole{
		"reader": {Perms: []string{"model:read", "agent:read"}},
		"empty":  {},
		"capped": {Perms: []string{"agent:admin"}, Excludes: []string{"governance:rbac:admin"}},
		"open":   {Perms: []string{"agent:admin"}},
		"quiet":  {Perms: []string{"agent:admin"}, Excludes: []string{"governance:rbac:read", "governance:rbac:admin"}},
	}
	tests := []struct {
		name   string
		grants []scopedGrant
		want   string
	}{
		{name: "no grants"},
		{
			name: "empty and unrenderable grants",
			grants: []scopedGrant{
				{SubjectKind: subjectUser, SubjectRef: "u", Role: "empty", RoleCustom: true},
				{SubjectKind: subjectUser, SubjectRef: "u", Role: "missing", RoleCustom: true},
				{SubjectKind: subjectUser, Role: "reader", RoleCustom: true},
				{SubjectKind: subjectGroup, Role: "reader", RoleCustom: true},
				{SubjectKind: subjectRole, SubjectRef: "not-a-role", Role: "reader", RoleCustom: true},
				{SubjectKind: "unknown", SubjectRef: "u", Role: "reader", RoleCustom: true},
			},
		},
		{
			name: "subjects scopes class filtering and escaping",
			grants: []scopedGrant{
				{SubjectKind: subjectUser, SubjectRef: `q"u\ser`, Role: "reader", RoleCustom: true, Scope: scopeSpec{Tree: scopeWorkspace, Ref: `w"s\path`}},
				{SubjectKind: subjectUser, SubjectRef: "b", Role: "reader", RoleCustom: true, Scope: scopeSpec{Tree: scopeTenant}},
				{SubjectKind: subjectUser, SubjectRef: "a", Role: "reader", RoleCustom: true, Scope: scopeSpec{Tree: scopeFolder, Ref: "folder-1", Class: "model"}},
				{SubjectKind: subjectRole, SubjectRef: "viewer", Role: "reader", RoleCustom: true, Scope: scopeSpec{Tree: scopeAgentGroup, Ref: "bots"}},
				{SubjectKind: subjectGroup, SubjectRef: "team", Role: "reader", RoleCustom: true, Scope: scopeSpec{Tree: scopeWorkspace, Ref: "w"}},
			},
			want: `permit(principal in Group::"team", action in [Action::"agent:read", Action::"model:read"], resource) when { resource in Workspace::"w" };
permit(principal in Role::"viewer", action in [Action::"agent:read", Action::"model:read"], resource) when { resource in AgentGroup::"bots" };
permit(principal in User::"a", action in [Action::"model:read"], resource) when { resource in Resource::"folder-1" };
permit(principal in User::"b", action in [Action::"agent:read", Action::"model:read"], resource);
permit(principal in User::"q\"u\\ser", action in [Action::"agent:read", Action::"model:read"], resource) when { resource in Workspace::"w\"s\\path" };
`,
		},
		{
			name: "delegation union exclusions and canonical order",
			grants: []scopedGrant{
				{SubjectKind: subjectUser, SubjectRef: "v", Role: "quiet", RoleCustom: true, Scope: scopeSpec{Tree: scopeTenant}},
				{SubjectKind: subjectUser, SubjectRef: "u", Role: "open", RoleCustom: true, Scope: scopeSpec{Tree: scopeWorkspace, Ref: "z"}},
				{SubjectKind: subjectUser, SubjectRef: "u", Role: "capped", RoleCustom: true, Scope: scopeSpec{Tree: scopeWorkspace, Ref: "a"}},
				{SubjectKind: subjectRole, SubjectRef: "viewer", Role: "open", RoleCustom: true, Scope: scopeSpec{Tree: scopeWorkspace, Ref: "w"}},
				{SubjectKind: subjectGroup, SubjectRef: "z", Role: "open", RoleCustom: true, Scope: scopeSpec{Tree: scopeFolder, Ref: "f"}},
			},
			want: `permit(principal in Group::"z", action in [Action::"agent:admin"], resource) when { resource in Resource::"f" };
permit(principal in Role::"viewer", action in [Action::"agent:admin"], resource) when { resource in Workspace::"w" };
permit(principal in User::"u", action in [Action::"agent:admin"], resource) when { resource in Workspace::"a" };
permit(principal in User::"u", action in [Action::"agent:admin"], resource) when { resource in Workspace::"z" };
permit(principal in User::"v", action in [Action::"agent:admin"], resource);
permit(principal in Group::"z", action in [Action::"governance:rbac:read", Action::"governance:rbac:admin"], resource);
permit(principal in User::"u", action in [Action::"governance:rbac:read", Action::"governance:rbac:admin"], resource);
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := projectManagedCedar(tt.grants, roles, nil); got != tt.want {
				t.Fatalf("projection = %q, want %q", got, tt.want)
			}
			reversed := append([]scopedGrant(nil), tt.grants...)
			for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
				reversed[i], reversed[j] = reversed[j], reversed[i]
			}
			if got := projectManagedCedar(reversed, roles, nil); got != tt.want {
				t.Fatalf("reversed grants project = %q, want %q", got, tt.want)
			}
		})
	}
}
