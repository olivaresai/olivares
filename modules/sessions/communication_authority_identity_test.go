// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
)

// TestCommunicationIdentityBindingKeepsDistinctDiagnostics proves that the
// neutral identity binder reports each of its failure causes distinctly through
// BOTH adapters: the personal inbox in the exact wording its callers and logs
// named before the binder was made resource-neutral, and the catalog in the
// neutral wording. Every cause keeps the one public disposition (evidence
// unavailable, HTTP 503) and names no tenant, principal or credential. A binder
// that collapsed the causes to one message fails the distinct-wording
// assertions; an adapter that rewrote evidence failures it did not produce fails
// the pass-through assertions.
func TestCommunicationIdentityBindingKeepsDistinctDiagnostics(t *testing.T) {
	fixture := newCommunicationAuthorityTestFixture(t)
	scope := DirectoryScopeRef{TenantID: fixture.tenant, WorkspaceID: fixture.workspace}
	source := &communicationAuthoritySourceRecorder{}
	withDeadline, cancel := context.WithDeadline(context.Background(), time.Now().Add(10*time.Minute))
	defer cancel()

	type binding struct {
		name     string
		ctx      context.Context
		ref      auth.PrincipalRef
		resolver communicationPrincipalAuthorityResolver
		sources  bool
		cause    communicationIdentityCause
	}
	cases := []binding{
		{
			name: "empty credential reference", ctx: withDeadline, ref: auth.PrincipalRef{},
			resolver: &communicationAuthorityResolverRecorder{resolved: fixture.principal}, sources: true,
			cause: communicationIdentityUnavailable,
		},
		{
			name: "no finite deadline", ctx: context.Background(), ref: fixture.ref,
			resolver: &communicationAuthorityResolverRecorder{resolved: fixture.principal}, sources: true,
			cause: communicationIdentityDeadlineMissing,
		},
		{
			name: "authority sources unbound", ctx: withDeadline, ref: fixture.ref,
			resolver: nil, sources: false,
			cause: communicationIdentitySourcesUnavailable,
		},
		{
			name: "credential no longer current", ctx: withDeadline, ref: fixture.ref,
			resolver: &communicationAuthorityResolverRecorder{err: auth.ErrUnauthenticated}, sources: true,
			cause: communicationIdentityCredentialStale,
		},
		{
			name: "resolver failure of another class", ctx: withDeadline, ref: fixture.ref,
			resolver: &communicationAuthorityResolverRecorder{err: errors.New("directory unreachable")}, sources: true,
			cause: communicationIdentityUnavailable,
		},
		{
			name: "resolved principal crossed its reference", ctx: withDeadline, ref: fixture.ref,
			resolver: &communicationAuthorityResolverRecorder{resolved: fixture.secondPrincipal}, sources: true,
			cause: communicationIdentityReferenceCrossed,
		},
	}
	seenInbox := map[string]string{}
	seenNeutral := map[string]string{}
	for _, tc := range cases {
		if tc.sources {
			fixture.module.useCommunicationRequestAuthoritySources(tc.resolver, source)
		} else {
			fixture.module.communicationAuthoritySources = nil
		}
		_, inboxErr := fixture.module.bindCurrentCommunicationInboxIdentity(tc.ctx, scope, tc.ref)
		_, neutralErr := fixture.module.bindCurrentCommunicationIdentity(
			tc.ctx, scope, tc.ref, requireChannelCatalogPrincipal,
		)
		for surface, got := range map[string]error{"inbox": inboxErr, "neutral": neutralErr} {
			if !errors.Is(got, ErrCommunicationEvidenceUnknown) {
				t.Fatalf("%s/%s: error class = %v, want evidence unavailable", tc.name, surface, got)
			}
			status, code, verdict, ok := communicationHTTPDisposition(got)
			if !ok || status != http.StatusServiceUnavailable || code != "evidence_unavailable" || verdict != VerdictUnknown {
				t.Fatalf("%s/%s: disposition = %d %s %s %t", tc.name, surface, status, code, verdict, ok)
			}
			for _, secret := range []string{fixture.tenant.String(), fixture.workspace.String()} {
				if secret != "" && strings.Contains(got.Error(), secret) {
					t.Fatalf("%s/%s: diagnostic discloses %q: %s", tc.name, surface, secret, got.Error())
				}
			}
		}
		wantInbox := ErrCommunicationEvidenceUnknown.Error() + ": " + communicationInboxIdentityWording[tc.cause]
		if inboxErr.Error() != wantInbox {
			t.Fatalf("%s: inbox diagnostic = %q, want %q", tc.name, inboxErr.Error(), wantInbox)
		}
		wantNeutral := ErrCommunicationEvidenceUnknown.Error() + ": " + communicationRequestIdentityWording[tc.cause]
		if neutralErr.Error() != wantNeutral {
			t.Fatalf("%s: neutral diagnostic = %q, want %q", tc.name, neutralErr.Error(), wantNeutral)
		}
		var identity *communicationIdentityError
		if !errors.As(neutralErr, &identity) || identity.cause != tc.cause {
			t.Fatalf("%s: neutral error does not carry cause %d: %#v", tc.name, tc.cause, neutralErr)
		}
		if errors.As(inboxErr, &identity) {
			t.Fatalf("%s: inbox adapter leaked the neutral error type instead of its own wording", tc.name)
		}
		seenInbox[inboxErr.Error()] = tc.name
		seenNeutral[neutralErr.Error()] = tc.name
	}
	// Five causes, five distinct diagnostics per surface (two cases share the
	// "unavailable" cause on purpose: a malformed reference and an unclassified
	// resolver failure are the same operator signal).
	if len(seenInbox) != int(communicationIdentityCauseCount) || len(seenNeutral) != int(communicationIdentityCauseCount) {
		t.Fatalf("distinct diagnostics inbox=%d neutral=%d, want %d each: %v / %v",
			len(seenInbox), len(seenNeutral), communicationIdentityCauseCount, seenInbox, seenNeutral)
	}

	// The pre-extraction inbox wording, verbatim, so a future edit of the table
	// cannot drift the historical strings without failing here.
	historical := map[communicationIdentityCause]string{
		communicationIdentityUnavailable:        "communication inbox identity is unavailable",
		communicationIdentityDeadlineMissing:    "communication inbox identity requires a finite deadline",
		communicationIdentitySourcesUnavailable: "communication inbox identity sources are unavailable",
		communicationIdentityCredentialStale:    "authenticated inbox credential is no longer current",
		communicationIdentityReferenceCrossed:   "resolved inbox principal crossed its credential reference",
	}
	for cause, want := range historical {
		if communicationInboxIdentityWording[cause] != want {
			t.Fatalf("inbox wording for cause %d = %q, want the historical %q", cause, communicationInboxIdentityWording[cause], want)
		}
		if communicationRequestIdentityWording[cause] == "" {
			t.Fatalf("neutral wording for cause %d is empty", cause)
		}
	}

	// Pass-through: evidence failures that did not originate in the binder, and
	// errors of another class, reach the inbox caller exactly as produced.
	foreign := communicationError(ErrCommunicationEvidenceUnknown, "channel catalog reader authority expired during discovery")
	if got := communicationInboxIdentityError(foreign); got != foreign {
		t.Fatalf("inbox adapter rewrote a foreign evidence failure: %v", got)
	}
	forbidden := communicationError(ErrCommunicationForbidden, "direct notice inbox requires a directory principal credential")
	if got := communicationInboxIdentityError(forbidden); got != forbidden {
		t.Fatalf("inbox adapter rewrote a forbidden error: %v", got)
	}

	// Control: with a current resolver both adapters bind the same identity, so
	// the distinct diagnostics were not bought with a refusing binder.
	fixture.module.useCommunicationRequestAuthoritySources(
		&communicationAuthorityResolverRecorder{resolved: fixture.principal}, source,
	)
	inbox, err := fixture.module.bindCurrentCommunicationInboxIdentity(withDeadline, scope, fixture.ref)
	if err != nil {
		t.Fatalf("inbox binding with a current credential: %v", err)
	}
	neutral, err := fixture.module.bindCurrentCommunicationIdentity(
		withDeadline, scope, fixture.ref, requireChannelCatalogPrincipal,
	)
	if err != nil {
		t.Fatalf("neutral binding with a current credential: %v", err)
	}
	if inbox.ref != fixture.ref || neutral.ref != fixture.ref || inbox.principal != neutral.principal ||
		inbox.scope != scope || neutral.scope != scope || inbox.deadline.IsZero() {
		t.Fatalf("bound identities differ: inbox=%+v neutral=%+v", inbox, neutral)
	}
}
