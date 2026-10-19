// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sourcescope

import (
	"errors"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/driverfacts"
)

// Who may use which AI tool, account and model path (#2420).
//
// A provider profile is one tool on one account and names the provider record it runs on,
// so a binding on the profile (source type "profile", source_ref its ppf_ reference) is a
// binding on all three; a binding on a tool (source type "tool", source_ref its driver key)
// covers every profile of it. They are bindings like any other: a department (workspace)
// reaches its sub-departments, a directory group or a person can be named, a forbid is
// absolute, and an inheritance filter on class "provider" cuts the tenant-wide role and
// the grants that reach the node from above. A profile or tool with no binding stays open.
//
// A department's people are reached through a directory group, through a scoped grant on the
// department (a delegated administrator, whose grant reaches the sub-departments), and for the
// agents and sessions that run in it, through the workspace binding itself. A person confined
// to one workspace is not one of them: provider profiles carry no workspace lineage, so the
// store refuses a confined caller every read of them (core/store/workspace_scope.go).
//
// Deciding is every edition, and the bindings already stored keep applying in Community.
// Writing them is Business: a Community engine answers 501 business_required to a create, an
// update or the approval of one queued earlier, and keeps read, revoke and disable-scoping.

// errProfileAssignmentBusiness is the sentinel the update closure returns so its handler
// can answer the Community refusal outside the transaction.
var errProfileAssignmentBusiness = errors.New("sourcescope: assigning profiles and tools requires Business")

func isProfileSource(sourceType string) bool {
	return sourceType == sourceProfile || sourceType == sourceTool
}

// mayWriteSource reports whether this build may create or change a binding of sourceType.
func (m *Module) mayWriteSource(sourceType string) bool {
	return !isProfileSource(sourceType) || m.profileAssignment
}

func writeProfileAssignmentBusiness(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotImplemented, api.ErrorBody("business_required",
		"Assigning provider profiles and AI tools to departments and groups requires Olivares Business."))
}

// profileAdminPermission is the tenant-wide term for a profile or tool: the role that
// administers provider profiles keeps oversight of every one of them, as a tenant
// administrator sees every workspace's sources. Any other role does not, and a person
// confined to a department never does. It is the permission modules/sessions declares as
// permProfileAdmin (provider_profile_api.go); a module cannot import another, so the two
// strings have to stay equal, and cmd/olivares TestProfileAssignmentFollowsTheDepartmentTree
// (a tenant administrator who is not the bootstrap superadmin lists every profile) fails when
// they do not. The superadmin passes before this term is read, so it proves nothing here.
const profileAdminPermission = auth.Permission("sessions:profile:admin")

// normalizeProfileSourceRef checks the reference of a profile or tool assignment, which the
// gate matches byte for byte: a misspelt tool or a profile name would otherwise be stored,
// match nothing and leave the profile open without a word. Other source types are untouched.
func normalizeProfileSourceRef(sourceType string, ref *string) string {
	switch sourceType {
	case sourceProfile:
		if !strings.HasPrefix(*ref, "ppf_") {
			return "source_ref must be a provider profile reference (ppf_…)"
		}
	case sourceTool:
		*ref = strings.ToLower(*ref)
		for _, key := range driverfacts.SessionKeys() {
			if key == *ref {
				return ""
			}
		}
		return "source_ref must be a tool: " + driverfacts.JoinList(driverfacts.SessionKeys(), "or")
	}
	return ""
}
