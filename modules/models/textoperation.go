// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/store"
)

// TextExecutionInput is one text turn. SessionRef is cost attribution only;
// authority comes from the authenticated ModuleContext and stored routing policy.
type TextExecutionInput = executeRequestDTO

// TextExecutionReport describes a Chat attempt without input, output, secrets,
// provider error bodies or endpoint paths.
type TextExecutionReport = chatExecutionDTO

// TextExecutionResult retains the execution observation even on refusal. Output
// is nil on every error. Missing usage remains nil rather than becoming zero.
type TextExecutionResult struct {
	StatusCode   int
	Output       *string
	InputTokens  *int64
	OutputTokens *int64
	Refusal      bool
	Execution    *TextExecutionReport
	body         any
	retryAfter   *int64
}

// MarshalJSON retains the supported HTTP response envelope for both callers.
func (r TextExecutionResult) MarshalJSON() ([]byte, error) { return json.Marshal(r.body) }

// TextExecutionError contains only a safe operation answer, never its cause.
type TextExecutionError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *TextExecutionError) Error() string { return e.Message }

func textAdmissionError(err error) (TextExecutionResult, error) {
	status, body := api.ModuleOperationErrorBody(err)
	return textResponse(status, body)
}

func textStoreError(err error) (TextExecutionResult, error) {
	if errors.Is(err, store.ErrConflict) {
		return textResponse(http.StatusConflict, errorBody("version conflict"))
	}
	status, body, _ := api.StoreErrorBody(err)
	return textResponse(status, body)
}

func textProfileError(err *executionProfileHTTPError) (TextExecutionResult, error) {
	return textResponse(err.status, executionProfileErrorBody(err))
}

// textResponse preserves the HTTP envelopes and projects their safe observation
// for library callers. An error always withholds output, even if an adapter
// returned text alongside the failure.
func textResponse(status int, body any) (TextExecutionResult, error) {
	result := TextExecutionResult{StatusCode: status, body: body}
	failure := &TextExecutionError{StatusCode: status, Code: "execution_refused", Message: "text execution refused"}
	switch b := body.(type) {
	case executeResponseDTO:
		if status < 400 {
			result.Output = &b.Output
		}
		result.InputTokens, result.OutputTokens = &b.InputTokens, &b.OutputTokens
		result.Refusal = b.Refusal
		if b.Decision.Reason != "" {
			failure.Message = b.Decision.Reason
		}
	case chatExecuteResponseDTO:
		result.Output, result.InputTokens, result.OutputTokens = b.Output, b.InputTokens, b.OutputTokens
		result.Refusal, result.Execution = b.Refusal, &b.Execution
	case chatErrorResponseDTO:
		result.InputTokens, result.OutputTokens = b.InputTokens, b.OutputTokens
		result.Refusal, result.Execution = b.Refusal, &b.Execution
		failure.Code, failure.Message = b.Error.Code, b.Error.Message
	case map[string]any:
		if e, ok := b["error"].(map[string]string); ok {
			failure.Code, failure.Message = e["code"], e["message"]
		}
	}
	if status >= 400 {
		result.Output = nil
		return result, failure
	}
	return result, nil
}
