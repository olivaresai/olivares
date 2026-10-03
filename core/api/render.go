// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"encoding/json"
	"net/http"
)

// maxBodyBytes caps a request body (1 MiB): the control-plane API takes small
// JSON, never bulk uploads, so a generous-but-bounded cap stops a memory DoS.
const maxBodyBytes = 1 << 20

// writeJSON writes v as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}

// decodeJSON reads and strictly decodes a JSON request body into v, enforcing the
// body-size cap and rejecting unknown fields and trailing data. It is the strict
// default of DecodeRequestBody; the property lives there, once.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	return DecodeRequestBody(w, r, v, RequestBodySpec{})
}
