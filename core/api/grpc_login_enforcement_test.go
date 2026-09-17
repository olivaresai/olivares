// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/olivaresai/olivares/core/auth"
)

// TestGRPCError_LoginEnforcementUnavailable (R5): the gRPC translation of the central
// refusal for a build without the recorded login enforcement component is Unavailable
// with the curated message, never Internal or Unauthenticated, and never the wrapper's
// own text. Invalid credentials stay Unauthenticated.
func TestGRPCError_LoginEnforcementUnavailable(t *testing.T) {
	const wrapper = "open capability at postgres://app:hunter2@db.internal:5432/olivares"
	err := grpcError(fmt.Errorf("%s: %w", wrapper, auth.ErrLoginEnforcementComponentAbsent))
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("grpcError returned a non-status error: %v", err)
	}
	if st.Code() != codes.Unavailable {
		t.Fatalf("code = %s, want Unavailable", st.Code())
	}
	if !strings.HasPrefix(st.Message(), "login_enforcement_unavailable: ") {
		t.Fatalf("message = %q, want the login_enforcement_unavailable code prefix", st.Message())
	}
	if !strings.Contains(st.Message(), "this build cannot enforce it") {
		t.Fatalf("message = %q, want the curated refusal text", st.Message())
	}
	for _, leak := range []string{"hunter2", "db.internal", "open capability"} {
		if strings.Contains(st.Message(), leak) {
			t.Fatalf("message %q echoes the wrapper context %q", st.Message(), leak)
		}
	}

	if got := status.Code(grpcError(auth.ErrInvalidCredentials)); got != codes.Unauthenticated {
		t.Fatalf("invalid credentials code = %s, want Unauthenticated", got)
	}
	if got := status.Code(grpcError(errors.New("plain failure"))); got != codes.Internal {
		t.Fatalf("an unmapped error code = %s, want Internal", got)
	}
}
