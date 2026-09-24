// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

// TestWorkHTTPErrorCommitOutcomePreserved is HP-9c: the CLI end of the work-seam
// residual (E9).
//
// The work plane does not project the commit-outcome sentinel, so an undetermined
// commit reached through a work route arrives at the CLI as one of the two
// answers it already has — 500 internal_error, or 503 observation_unavailable
// when the cause also carried ErrStoreUnavailable. Those map to two DIFFERENT
// exit codes, and an operator scripts against them: 6 says a server failed, 8
// says no verdict could be reached.
//
// C32 changes neither, and this control is the proof of that rather than a
// promise. Repairing the split is a semantic decision for whoever owns the work
// vocabulary; doing it here as a side effect of a store change would move every
// caller's exit code in a release nobody asked for it in.
//
// Class: P-GREEN at every cut.
func TestWorkHTTPErrorCommitOutcomePreserved(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   int
	}{
		{
			name:   "canceled_commit_is_a_server_failure",
			status: 500,
			body:   `{"verdict":"NO_HE_PODIDO_MIRAR","code":"internal_error","error":{"code":"internal_error","message":"internal_error"}}`,
			want:   exitcode.Server,
		},
		{
			name:   "deadline_commit_is_indeterminate",
			status: 503,
			body:   `{"verdict":"NO_HE_PODIDO_MIRAR","code":"observation_unavailable","error":{"code":"observation_unavailable","message":"observation_unavailable"}}`,
			want:   exitcode.Indeterminate,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := exitcode.From(workHTTPError(tc.status, []byte(tc.body)))
			if got != tc.want {
				t.Fatalf("exit code = %d, want %d: the work seam's two answers are an unchanged residual of C32, and an operator's script reads them apart",
					got, tc.want)
			}
		})
	}
}
