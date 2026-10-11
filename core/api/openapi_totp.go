// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "github.com/olivaresai/olivares/core/api/oas"

// addTOTPContract publishes the actual native second-factor wire protocol.
// LoginResponse remains a completed session; password login also permits the
// disjoint pending challenge, which carries no session credential.
func addTOTPContract(paths, schemas map[string]any) {
	ref := func(name string) map[string]any { return oas.Obj("$ref", "#/components/schemas/"+name) }
	str := func() map[string]any { return oas.Obj("type", "string") }
	object := func(properties map[string]any, required ...string) map[string]any {
		out := oas.Obj("type", "object", "properties", properties)
		if len(required) > 0 {
			names := make([]any, len(required))
			for i, n := range required {
				names[i] = n
			}
			out["required"] = names
		}
		return out
	}
	response := func(name string) map[string]any { return oas.JSONRespSchema("OK", ref(name)) }
	codes := oas.Obj("type", "array", "items", str(), "description", "Single-use recovery codes, revealed only in this response.")
	schemas["TOTPLoginChallenge"] = object(oas.Obj(
		"mfa_required", oas.Obj("type", "boolean", "const", true), "mfa_token", str(),
		"enrolment_required", oas.Obj("type", "boolean"), "pending_expires_in_s", oas.Obj("type", "integer")),
		"mfa_required", "mfa_token", "enrolment_required", "pending_expires_in_s")
	schemas["TOTPEnrolInput"] = object(oas.Obj("mfa_token", str()))
	schemas["TOTPActivateInput"] = object(oas.Obj("mfa_token", str(), "code", str()), "code")
	schemas["TOTPChallengeInput"] = object(oas.Obj("mfa_token", str(), "code", str(), "recovery_code", str()), "mfa_token")
	schemas["TOTPChallengeInput"].(map[string]any)["anyOf"] = []any{oas.Obj("required", []any{"code"}), oas.Obj("required", []any{"recovery_code"})}
	schemas["TOTPEnrolment"] = object(oas.Obj("secret", str(), "uri", str(), "algorithm", str(),
		"digits", oas.Obj("type", "integer"), "period", oas.Obj("type", "integer"), "qr_png_base64", oas.Obj("type", "string", "contentEncoding", "base64")),
		"secret", "uri", "algorithm", "digits", "period", "qr_png_base64")
	schemas["TOTPActivationResponse"] = object(oas.Obj("recovery_codes", codes, "token", str(), "csrf_token", str(), "session_id", oas.Obj("type", "string", "format", "uuid"),
		"expires_at", oas.Obj("type", "string", "format", "date-time")), "recovery_codes")
	schemas["TOTPStatus"] = object(oas.Obj("enrolled", oas.Obj("type", "boolean"), "algorithm", str(), "digits", oas.Obj("type", "integer"),
		"period", oas.Obj("type", "integer"), "seed_hint", str(), "activated_at", oas.Obj("type", "string", "format", "date-time"),
		"recovery_codes_remaining", oas.Obj("type", "integer")), "enrolled", "recovery_codes_remaining")
	schemas["TOTPPolicy"] = object(oas.Obj("require_for_admins", oas.Obj("type", "boolean")), "require_for_admins")
	schemas["StepUpPolicy"] = object(oas.Obj("admin_step_up", oas.Obj("type", "string", "enum", []any{"none", "totp", "passkey"})), "admin_step_up")
	schemas["TOTPOK"] = object(oas.Obj("ok", oas.Obj("type", "boolean", "const", true)), "ok")
	add := func(path, method, id, summary, output, input string, security []any, params ...any) {
		responses := oas.Obj("200", response(output))
		for _, status := range []string{"400", "401", "403", "404", "409", "429", "503"} {
			responses[status] = oas.JSONRespSchema("Request refused", ref("Error"))
		}
		op := oas.Obj("operationId", id, "summary", summary, "tags", []any{"auth"}, "x-stability", "stable", "security", security, "responses", responses)
		if input != "" {
			op["requestBody"] = oas.Obj("required", true, "content", oas.Obj("application/json", oas.Obj("schema", ref(input))))
		}
		if len(params) > 0 {
			op["parameters"] = params
		}
		item, ok := paths[path].(map[string]any)
		if !ok {
			item = oas.Obj()
			paths[path] = item
		}
		item[method] = op
	}
	dual := append(oaBearer(), oas.Obj())
	add("/v1/auth/totp/enrol", "post", "enrolTOTP", "Start a TOTP enrolment with a session or a pending login", "TOTPEnrolment", "TOTPEnrolInput", dual)
	add("/v1/auth/totp/activate", "post", "activateTOTP", "Prove possession and reveal recovery codes once", "TOTPActivationResponse", "TOTPActivateInput", dual)
	add("/v1/auth/totp/challenge", "post", "completeTOTPLogin", "Complete a pending login with a code or recovery code", "SessionResponse", "TOTPChallengeInput", []any{oas.Obj()})
	add("/v1/auth/totp/status", "get", "getTOTPStatus", "Read the calling account's factor status", "TOTPStatus", "", oaBearer())
	add("/v1/auth/totp", "delete", "removeTOTP", "Remove the calling account's factor behind the administrative step-up", "TOTPOK", "", oaBearer())
	add("/v1/auth/totp/policy", "get", "getTOTPPolicy", "Read the administrator factor policy", "TOTPPolicy", "", oaBearer())
	add("/v1/auth/totp/policy", "put", "setTOTPPolicy", "Set the administrator factor policy behind the administrative step-up", "TOTPPolicy", "TOTPPolicy", oaBearer())
	add("/v1/auth/step-up-policy", "get", "getStepUpPolicy", "Read what administrative actions demand beyond the sign-in", "StepUpPolicy", "", oaBearer())
	add("/v1/auth/step-up-policy", "put", "setStepUpPolicy", "Set what administrative actions demand beyond the sign-in", "StepUpPolicy", "StepUpPolicy", oaBearer())
	id := oas.Obj("name", "id", "in", "path", "required", true, "schema", oas.Obj("type", "string", "format", "uuid"))
	add("/v1/users/{id}/totp", "get", "getUserTOTPStatus", "Read a member's factor status in the selected tenant", "TOTPStatus", "", oaBearer(), id, oaTenantParam())
	add("/v1/users/{id}/totp/reset", "post", "resetUserTOTP", "Reset a tenant-governed member's factor behind the administrative step-up", "TOTPOK", "", oaBearer(), id, oaTenantParam())
	login := paths["/v1/auth/login"].(map[string]any)["post"].(map[string]any)
	login["responses"].(map[string]any)["200"] = oas.JSONRespSchema("Completed session or pending second-factor challenge", oas.Obj("oneOf", []any{ref("SessionResponse"), ref("TOTPLoginChallenge")}))
}
