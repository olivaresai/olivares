// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package hookpep

import (
	"context"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/connectors/claude"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/governance"
)

// sessionPlaneAuthn satisfies PrincipalAuthenticator and SessionCredentials without
// authenticating anyone: the session review gate only type-asserts it.
type sessionPlaneAuthn struct{}

func (sessionPlaneAuthn) Authenticate(context.Context, string) (auth.Principal, error) {
	return auth.Principal{}, auth.ErrUnauthenticated
}

func (sessionPlaneAuthn) Resolve(context.Context, string) (auth.Principal, auth.SessionScope, error) {
	return auth.Principal{}, auth.SessionScope{}, auth.ErrUnauthenticated
}

func (sessionPlaneAuthn) ResolveRun(context.Context, model.TenantID, string) (auth.Principal, auth.SessionScope, error) {
	return auth.Principal{}, auth.SessionScope{}, auth.ErrUnauthenticated
}

// A decider whose composition root did not supply the bridge's subject_ref encoding cannot
// queue a session review, so the ask is refused before anything is queued.
func TestSessionReviewDeniesWhenTheSubjectRefEncodingIsMissing(t *testing.T) {
	d := &Decider{Authr: sessionPlaneAuthn{}, Approvals: &governance.EngineApprovals{}}
	var tenant model.TenantID
	got := d.gateViaSessionApproval(context.Background(), tenant, auth.Principal{},
		claude.HookDecisionInput{Tool: "Bash"}, hookDisposition{}, "actor", "firm", "v1")
	if got.Permission != claude.DecisionDeny || !strings.Contains(got.Reason, "human approval service is unavailable") {
		t.Fatalf("got %q (%s); want deny naming the unavailable approval service", got.Permission, got.Reason)
	}
}
