// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "github.com/olivaresai/olivares/core/license"

// errorEdition is the edition a refusal code sends a person to: the edition field of its body,
// the same one its remedy names (the add-on code stands for Business). A client reads `edition`
// from every edition refusal, this table's and edition_required's, and never parses a sentence.
var errorEdition = map[string]string{
	"addon_requires_license":           license.EditionBusiness,
	"audit_export_unavailable":         license.EditionBusiness,
	"business_required":                license.EditionBusiness,
	"caep_requires_business":           license.EditionBusiness,
	"connector_unavailable":            license.EditionBusiness,
	"departments_unavailable":          license.EditionBusiness,
	"multi_idp_requires_enterprise":    license.EditionBusiness,
	"observability_export_unavailable": license.EditionBusiness,
	"organizations_unavailable":        license.EditionEnterprise,
	"posture_export_unavailable":       license.EditionBusiness,
}

// ErrorBody constructs the documented REST error envelope. The message must
// already be safe for the client; this function does not sanitize internal errors.
// It adds error.remedy, the code's fixed next step in errorRemedy, when it has one.
// Callers may attach operation-specific fields without discarding this envelope.
func ErrorBody(code, message string) map[string]any {
	e := map[string]string{"code": code, "message": message}
	if remedy := errorRemedy[code]; remedy != "" {
		e["remedy"] = remedy
	}
	if edition := errorEdition[code]; edition != "" {
		e["edition"] = edition
	}
	return map[string]any{"error": e}
}

// ModuleErrorBody preserves a beta handler's existing message when it has no
// specific machine code. The HTTP status still identifies the failure category.
func ModuleErrorBody(message string) map[string]any {
	return ErrorBody("module_error", message)
}
