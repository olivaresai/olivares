// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import "github.com/olivaresai/olivares/core/api/oas"

func sessionsModelCatalogSchema() map[string]any {
	text := oas.Obj("type", "string")
	boolean := oas.Obj("type", "boolean")
	strings := oas.Obj("type", "array", "maxItems", 32, "items", text)
	entry := oas.Obj("type", "object", "required", []string{"id"}, "properties", oas.Obj(
		"id", text, "display_name", text, "effort_levels", strings, "default_effort", text,
		"is_default", boolean, "is_new", boolean, "is_legacy", boolean, "supports_fast_mode", boolean, "service_tiers", strings,
	))
	return oas.Obj("type", "object", "required", []string{"profile_ref", "driver", "state", "models", "default_model", "free_text", "checked_at", "refresh_interval_seconds"}, "properties", oas.Obj(
		"profile_ref", text, "driver", text, "state", oas.Obj("type", "string", "enum", oas.Enum("fresh", "unsupported", "failed")),
		"models", oas.Obj("type", "array", "maxItems", 1000, "items", entry),
		"default_model", oas.Obj("type", "string", "description", "Empty selects the installed tool's own default. Optional model markers and capabilities are present only when the tool reports them."),
		"free_text", boolean, "message", text, "checked_at", sessionsTimestampSchema(), "refresh_interval_seconds", oas.Obj("type", "integer"),
	))
}

func sessionsModelCatalogResponses() map[string]any {
	out := oas.Obj("400", oas.JSONResp("Invalid profile reference or refresh query"), "401", oas.JSONResp("Unauthenticated"), "403", oas.JSONResp("Profile read permission required"), "404", oas.JSONResp("Profile absent in this tenant"), "409", oas.JSONResp("Profile unavailable or changed during discovery"), "422", oas.JSONResp("Profile driver or authentication source is unavailable"), "429", oas.JSONResp("Rate limited"), "500", oas.JSONResp("Store or audit failure"), "503", oas.JSONResp("Required runtime wiring is unavailable"))
	out["200"] = oas.JSONRespSchema("Live catalog from the profile's installed tool or bound provider. Cached per profile for two minutes; refresh=true bypasses the cache. Free text remains available. Vendor errors, paths and credentials are never returned.", sessionsModelCatalogSchema())
	return out
}
