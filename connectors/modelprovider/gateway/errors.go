// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gateway

import (
	"context"
	"errors"
	"net/http"

	"github.com/olivaresai/olivares/connectors/modelprovider"
)

// Product envelope codes. Names match core/api.statusFor where the status
// already exists. budget_denied and canceled are gateway-specific: the models
// execute path already answers a budget block as HTTP 402, and a canceled
// upstream call is not an HTTP response from this process.
const (
	CodeBadRequest      = "bad_request"
	CodeUnauthenticated = "unauthenticated"
	CodeForbidden       = "forbidden"
	CodeNotFound        = "not_found"
	CodeRateLimited     = "rate_limited"
	CodeBudgetDenied    = "budget_denied"
	CodeNotImplemented  = "not_implemented"
	CodeUnavailable     = "unavailable"
	CodeInternal        = "internal"
	CodeCanceled        = "canceled"
)

// Error is the mapped failure. Message never includes a credential or a prompt.
// Unwrap returns the cause when one exists (context.Canceled, *modelprovider.APIError).
type Error struct {
	Code       string
	HTTPStatus int
	Message    string
	err        error
}

func (e *Error) Error() string {
	if e == nil {
		return "modelgateway: <nil>"
	}
	return "modelgateway: " + e.Code + ": " + e.Message
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// Envelope is the product JSON error shape {error:{code,message}}.
type Envelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// EnvelopeOf maps an error to the product envelope and HTTP status.
func EnvelopeOf(err error) (status int, env Envelope) {
	if err == nil {
		return http.StatusOK, Envelope{}
	}
	var ge *Error
	if errors.As(err, &ge) && ge != nil {
		env.Error.Code = ge.Code
		env.Error.Message = ge.Message
		status = ge.HTTPStatus
		if status == 0 {
			status = http.StatusInternalServerError
		}
		return status, env
	}
	if errors.Is(err, context.Canceled) {
		env.Error.Code = CodeCanceled
		env.Error.Message = "request canceled"
		return 499, env
	}
	if errors.Is(err, context.DeadlineExceeded) {
		env.Error.Code = CodeUnavailable
		env.Error.Message = "upstream deadline exceeded"
		return http.StatusGatewayTimeout, env
	}
	env.Error.Code = CodeInternal
	env.Error.Message = "internal error"
	return http.StatusInternalServerError, env
}

// MapHTTPStatus translates an upstream HTTP status to a product code.
func MapHTTPStatus(status int) (code string, httpStatus int, message string) {
	switch status {
	case http.StatusBadRequest:
		return CodeBadRequest, status, "invalid request"
	case http.StatusUnauthorized:
		return CodeUnauthenticated, status, "unauthenticated"
	case http.StatusForbidden:
		return CodeForbidden, status, "forbidden"
	case http.StatusNotFound:
		return CodeNotFound, status, "not found"
	case http.StatusPaymentRequired:
		return CodeBudgetDenied, status, "budget denied"
	case http.StatusTooManyRequests:
		return CodeRateLimited, status, "rate limited"
	case http.StatusNotImplemented:
		return CodeNotImplemented, status, "not implemented"
	case http.StatusRequestEntityTooLarge:
		return CodeBadRequest, status, "request too large"
	case http.StatusInternalServerError:
		return CodeInternal, status, "upstream internal error"
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
		return CodeUnavailable, status, "upstream unavailable"
	default:
		if status >= 400 && status < 500 {
			return CodeBadRequest, status, "invalid request"
		}
		return CodeUnavailable, status, "upstream unavailable"
	}
}

func mapTransportError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return &Error{Code: CodeCanceled, HTTPStatus: 499, Message: "request canceled", err: err}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: CodeUnavailable, HTTPStatus: http.StatusGatewayTimeout, Message: "upstream deadline exceeded", err: err}
	}
	var api *modelprovider.APIError
	if errors.As(err, &api) && api != nil {
		code, status, msg := MapHTTPStatus(api.Status)
		return &Error{Code: code, HTTPStatus: status, Message: msg, err: api}
	}
	return &Error{Code: CodeUnavailable, HTTPStatus: http.StatusBadGateway, Message: "upstream transport error", err: err}
}
