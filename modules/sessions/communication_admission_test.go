// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
package sessions

import (
	"context"
	"errors"
	"fmt"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"net/http"
	"testing"
	"time"
)

func TestCommunicationAdmissionClassificationNeverFallsBack(t *testing.T) {
	f := newCommunicationAuthorityTestFixture(t)
	scope := DirectoryScopeRef{TenantID: f.tenant, WorkspaceID: f.workspace}
	question, err := newCommunicationAuthorityQuestion(scope, channelKind, model.NewID(), CommunicationChannelWrite)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, tc := range []struct {
		name   string
		cause  error
		status int
		code   string
	}{
		{"established global scope refusal", fmt.Errorf("reconstruction: %w: %w", auth.ErrPrincipalEvidenceUnavailable, auth.ErrPrincipalScopeAdmissionRequired), 403, "tenant_admission_required"},
		{"unavailable directory", auth.ErrPrincipalEvidenceUnavailable, 503, "evidence_unavailable"},
		{"malformed evidence", errors.New("corrupt directory witness"), 503, "evidence_unavailable"},
		{"untyped admission wording", errors.New(auth.ErrPrincipalScopeAdmissionRequired.Error()), 503, "evidence_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, collection := range []bool{false, true} {
				resolver := &communicationAuthorityResolverRecorder{err: tc.cause}
				source := &communicationAuthoritySourceRecorder{}
				var err error
				if collection {
					f.module.communicationAuthoritySources = &communicationRequestAuthoritySources{resolver: resolver, source: source}
					_, err = f.module.bindCurrentCommunicationIdentity(ctx, scope, f.ref, requireChannelCatalogPrincipal)
				} else {
					_, err = bindCommunicationRequestAuthority(ctx, resolver, source, f.ref, question)
				}
				status, code, verdict, ok := communicationHTTPDisposition(err)
				if !ok || status != tc.status || code != tc.code || resolver.calls != 1 || source.calls != 0 {
					t.Fatalf("classification/fallback: status=%d code=%s err=%v calls=%d/%d", status, code, err, resolver.calls, source.calls)
				}
				if tc.status == http.StatusForbidden {
					if verdict != VerdictBroken || !errors.Is(err, ErrCommunicationForbidden) || errors.Is(err, ErrCommunicationEvidenceUnknown) {
						t.Fatal("known refusal mislabeled as unknown")
					}
				} else if verdict != VerdictUnknown {
					t.Fatal("unavailable evidence labeled as denial")
				}
			}
		})
	}
}
