// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package modelprovider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type failedErrorBody struct{ sent bool }

func (b *failedErrorBody) Read(p []byte) (int, error) {
	if b.sent {
		return 0, io.EOF
	}
	b.sent = true
	return copy(p, "rejected q7-"), io.ErrUnexpectedEOF
}

func (*failedErrorBody) Close() error { return nil }

type failedErrorDoer struct{}

func (failedErrorDoer) Do(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusForbidden, Body: &failedErrorBody{}, Header: make(http.Header)}, nil
}

func TestProviderErrorReadFailureOmitsPartialCredential(t *testing.T) {
	err := NewClient("https://fixture.invalid", failedErrorDoer{}, AuthBearer, "q7-canary-opaque", nil).GetJSON(context.Background(), "/fail", nil, nil)
	var api *APIError
	if !errors.As(err, &api) || api.Status != http.StatusForbidden {
		t.Fatal("typed status lost")
	}
	if strings.Contains(api.Body, "q7-") || strings.Contains(err.Error(), "q7-") {
		t.Fatal("partial credential survived a failed error-body read")
	}
}
