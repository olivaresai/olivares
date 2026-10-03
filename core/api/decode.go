// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// RequestBodySpec tunes DecodeRequestBody for the routes whose contract predates
// the strict default. The zero value is the strict default every route should take.
type RequestBodySpec struct {
	// MaxBytes caps the request body; zero selects the control-plane default
	// (maxBodyBytes, 1 MiB). The cap is exact: http.MaxBytesReader fails the read
	// past the limit, where io.LimitReader silently truncated — a body cut at
	// exactly the cap could decode as if it were whole.
	MaxBytes int64
	// AllowUnknownFields restores encoding/json's default field matching. The
	// strict default rejects unknown members; a route whose clients legitimately
	// send attributes the provider does not model (the SCIM provisioning path
	// takes whatever the IdP emits) keeps the lenient form here.
	AllowUnknownFields bool
	// Optional treats an empty or whitespace-only body as valid and leaves v at
	// its zero value, for POSTs whose every field has a default.
	Optional bool
}

// DecodeRequestBody reads r.Body as exactly one JSON document into v.
//
// A BODY IS ONE JSON DOCUMENT (measured 2026-08-06 against a live engine, and
// again 2026-10-01 on the stray-bracket tails). json.Decoder.Decode reads the
// FIRST value and stops, so a handler that decodes once applies `{...}{...}` as
// the first document and silently discards the rest — a request-smuggling shape
// with a durable write on the end of it. dec.More() is NOT the rejection: it
// peeks one byte and answers false for a tail that opens with ']' or '}', so it
// accepts `{...}}`, `{...}]`, `{...}\n]{}` and `{...}\n}null`. The only check
// that covers both the second value and the malformed tail is a second Decode
// that must reach io.EOF — a parsed value (err == nil) or a syntax error is a
// body that was not one document. A whitespace-only tail still ends in io.EOF.
//
// Every request-body decode in core/, modules/ and cmd/olivares goes through
// this function (scripts/check-json-decoders.sh asserts it), so the property
// holds once, here, instead of drifting across per-package copies.
func DecodeRequestBody(w http.ResponseWriter, r *http.Request, v any, spec RequestBodySpec) error {
	maxBytes := spec.MaxBytes
	if maxBytes <= 0 {
		maxBytes = maxBodyBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	if !spec.AllowUnknownFields {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(v); err != nil {
		if spec.Optional && errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	var extra json.RawMessage
	switch err := dec.Decode(&extra); {
	case err == nil:
		// A whole second value parsed: the body was two documents.
		return ErrTrailingJSON
	case !errors.Is(err, io.EOF):
		// A malformed tail, or the read failed past the size cap: not one
		// document either. Keep the cause visible under the sentinel.
		return fmt.Errorf("%w: %w", ErrTrailingJSON, err)
	}
	return nil
}

// ErrTrailingJSON reports a request body that is not exactly one JSON document:
// a second value or a malformed tail after the first document.
var ErrTrailingJSON = errors.New("request body is not exactly one JSON document")
