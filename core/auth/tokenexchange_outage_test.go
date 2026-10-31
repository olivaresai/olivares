// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/olivaresai/olivares/core/store"
)

// unavailableAuthStore fails the auth partition's reads the way a pool reset
// does: with the store's own unavailable sentinel and the driver's cause.
type unavailableAuthStore struct{ store.Store }

func (unavailableAuthStore) AuthView(context.Context, func(store.AuthScope) error) error {
	return fmt.Errorf("%w: %w", store.ErrStoreUnavailable, errors.New("conn closed"))
}

// the regression: a token exchange whose subject or actor credential could not be
// checked because the store was unreachable must not say the credential is
// invalid. The caller is told the token is bad and re-authenticates against a
// database that is simply down. A credential the store answered for as unknown
// keeps ErrInvalidSubjectToken.
func TestExchangeTokenKeepsAStoreOutageOutOfTheCredentialRefusal(t *testing.T) {
	ctx := context.Background()
	token := PrefixToken + "_selector_secret" // well-formed: it reaches the store
	healthy := newCredentialBindingFixture(t).st
	down := NewAuthenticator(unavailableAuthStore{Store: healthy}, nil)
	up := NewAuthenticator(healthy, nil)

	for _, tc := range []struct {
		name string
		req  ExchangeRequest
	}{
		{"subject", ExchangeRequest{SubjectToken: token, SubjectTokenType: TokenTypeAccessToken}},
		{"actor", ExchangeRequest{
			SubjectToken: token, SubjectTokenType: TokenTypeAccessToken,
			ActorToken: token, ActorTokenType: TokenTypeAccessToken,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := down.ExchangeToken(ctx, Principal{}, tc.req)
			if !errors.Is(err, store.ErrStoreUnavailable) {
				t.Fatalf("ExchangeToken during an outage = %v, want store.ErrStoreUnavailable in its chain", err)
			}
			if errors.Is(err, ErrInvalidSubjectToken) {
				t.Fatalf("ExchangeToken during an outage = %v, told the caller its token is invalid", err)
			}
			// The same request against a healthy store: an unknown selector is a
			// bad credential, and says so.
			_, err = up.ExchangeToken(ctx, Principal{}, tc.req)
			if !errors.Is(err, ErrInvalidSubjectToken) || errors.Is(err, store.ErrStoreUnavailable) {
				t.Fatalf("ExchangeToken for an unknown token = %v, want ErrInvalidSubjectToken", err)
			}
		})
	}
}
