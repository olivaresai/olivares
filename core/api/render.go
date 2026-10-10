// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// maxBodyBytes caps a request body (1 MiB): the control-plane API takes small
// JSON, never bulk uploads, so a generous-but-bounded cap stops a memory DoS.
const maxBodyBytes = 1 << 20

// WriteJSON writes v with the given status, omitting the body when v is nil.
// The optional media type preserves protocol and legacy route contracts; the
// default is application/json; charset=utf-8. It never reshapes the payload:
// even an error status can carry an operation's structured result.
func WriteJSON(w http.ResponseWriter, status int, v any, mediaType ...string) {
	contentType := "application/json; charset=utf-8"
	if len(mediaType) > 0 {
		contentType = mediaType[0]
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status is committed. Do not append a second response or log the
		// payload or encoder error: a custom marshaler may include private data.
		slog.Error("api: JSON response write failed", "status", status)
	}
}

var writeJSON = WriteJSON

// decodeJSON reads and strictly decodes a JSON request body into v, enforcing the
// body-size cap and rejecting unknown fields and trailing data. It is the strict
// default of DecodeRequestBody; the property lives there, once.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	return DecodeRequestBody(w, r, v, RequestBodySpec{})
}
