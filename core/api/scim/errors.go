// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package scim

import "strconv"

// SCIM scimType keywords (RFC 7644 §3.12, Table 9).
const (
	TypeInvalidFilter = "invalidFilter"
	TypeInvalidSyntax = "invalidSyntax"
	TypeInvalidValue  = "invalidValue"
	TypeInvalidPath   = "invalidPath"
	TypeNoTarget      = "noTarget"
	TypeUniqueness    = "uniqueness"
	TypeMutability    = "mutability"
)

// Error is a SCIM error response (RFC 7644 §3.12). Critically, Status is a
// STRING (e.g. "409"), not a number — serializing it as an int makes IdPs reject
// the body. ScimType is omitted when empty.
type Error struct {
	// Schemas is always [MsgError].
	Schemas []string `json:"schemas"`
	// Detail is a human-readable message.
	Detail string `json:"detail"`
	// Status is the HTTP status as a STRING.
	Status string `json:"status"`
	// ScimType is the optional error keyword.
	ScimType string `json:"scimType,omitempty"`
}

// NewError builds a SCIM error for an HTTP status, optional scimType, and detail.
func NewError(httpStatus int, scimType, detail string) Error {
	return Error{
		Schemas:  []string{MsgError},
		Detail:   detail,
		Status:   strconv.Itoa(httpStatus),
		ScimType: scimType,
	}
}

// HTTPStatus returns the numeric status carried by the error (for the writer).
func (e Error) HTTPStatus() int {
	n, _ := strconv.Atoi(e.Status)
	if n == 0 {
		return 500
	}
	return n
}
