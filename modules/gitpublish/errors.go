// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package gitpublish

import (
	"context"
	"errors"
	"net/http"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// Error is the closed refusal vocabulary of CONTRACT-J10-S3 §3.9.
type Error struct {
	Code   string
	Status int
	// Intent names the intent the refusal concerns, when one exists.
	Intent model.ID
}

func (e *Error) Error() string { return "gitpublish: " + e.Code }

func refuse(code string, status int) *Error { return &Error{Code: code, Status: status} }

var (
	errNotFound          = refuse("not_found", http.StatusNotFound)
	errInvalid           = refuse("invalid_request", http.StatusBadRequest)
	errUnavailable       = refuse("authority_unavailable", http.StatusServiceUnavailable)
	errRuntimeCredential = refuse("runtime_credential_refused", http.StatusForbidden)
	errUnsupported       = refuse("unsupported_requirement", http.StatusUnprocessableEntity)
	errSeparationOfDuty  = refuse("separation_of_duty", http.StatusForbidden)
)

func withIntent(e *Error, id model.ID) *Error {
	c := *e
	c.Intent = id
	return &c
}

// authError maps an A1 or A4 authorization error. A policy denial on a stored
// target is concealed as not_found; step-up and scoped grant keep their own
// answers; anything else is "nothing could be decided".
func authError(err error) *Error {
	switch {
	case errors.Is(err, auth.ErrStepUpRequired):
		return refuse("step_up_required", http.StatusForbidden)
	case errors.Is(err, auth.ErrScopedGrantRequired):
		return refuse("scoped_grant_required", http.StatusForbidden)
	case errors.Is(err, auth.ErrRouteDenied):
		return errNotFound
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return refuse("authority_expired", http.StatusServiceUnavailable)
	}
	return errUnavailable
}

// hostCode keeps only the bounded code of a host error.
func hostCode(err error) string {
	var he *gp.HostError
	if errors.As(err, &he) {
		return he.Code
	}
	return "unavailable"
}
