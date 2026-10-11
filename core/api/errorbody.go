// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

// ErrorBody constructs the documented REST error envelope. The message must
// already be safe for the client; this function does not sanitize internal errors.
// Callers may attach operation-specific fields without discarding this envelope.
func ErrorBody(code, message string) map[string]any {
	return map[string]any{"error": map[string]string{"code": code, "message": message}}
}

// ModuleErrorBody preserves a beta handler's existing message when it has no
// specific machine code. The HTTP status still identifies the failure category.
func ModuleErrorBody(message string) map[string]any {
	return ErrorBody("module_error", message)
}
