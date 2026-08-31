// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import "encoding/json"

// The list envelope is the ONE shape every collection endpoint returns — the
// engine's own routes and every module's /v1/m/<ns>/ routes alike. It lives here,
// once, because 24 packages had each declared their own identical copy and the
// copies drifted where it mattered: an empty page serialized as `{"items":null}`
// wherever a handler happened to leave its accumulator as a nil slice, and as
// `{"items":[]}` wherever it happened to initialize it. Both shapes shipped from
// the same API.
//
// null is not a valid empty collection on this wire. A client that receives it
// cannot iterate the result without first defending against a type the contract
// never declared (the console crashed on exactly this: `.map()` over null in
// three Compliance views of a CLEAN install), and four generated SDKs decode the
// same JSON. The guarantee therefore belongs to the type, not to the discipline
// of each handler: JSONArray marshals a nil slice as [], so an empty list is
// empty in the response no matter how the handler that produced it was written.

// JSONArray is a JSON array that is NEVER null on the wire: a nil JSONArray
// marshals as []. Use it for any response field whose contract says "array" —
// the zero value of a Go slice is nil, and encoding/json renders that as null,
// which is a different JSON type than the one the field promises.
//
// It is assignable from and to a plain []T (identical underlying type, and []T is
// unnamed), so a handler keeps accumulating into an ordinary slice and the
// conversion at the assignment costs nothing.
//
// It deliberately implements only MarshalJSON: DECODING null into a nil slice is
// correct and lossless, so a client of this package (and every test that decodes
// a response into the envelope) keeps working unchanged.
type JSONArray[T any] []T

// MarshalJSON renders the array, substituting [] for a nil slice.
func (a JSONArray[T]) MarshalJSON() ([]byte, error) {
	if a == nil {
		return []byte("[]"), nil
	}
	// The conversion to []T is what stops this from recursing: the plain slice
	// type carries no MarshalJSON method.
	return json.Marshal([]T(a))
}

// ListResponse is the paginated envelope every list endpoint returns. Items is a
// JSONArray, so an empty page is `{"items":[],"has_more":false}` — never
// `{"items":null,…}` — whether the handler assigned an empty slice, a nil slice,
// or never assigned at all.
//
// Cursor is omitted when empty (the last page carries no cursor); has_more is
// always present because a client pages on it.
type ListResponse[T any] struct {
	Items   JSONArray[T] `json:"items"`
	Cursor  string       `json:"cursor,omitempty"`
	HasMore bool         `json:"has_more"`
}
