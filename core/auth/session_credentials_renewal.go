// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

func renewableOwner(item sessionCredential, ref PrincipalRef, now time.Time) bool {
	return !item.revoked && now.Before(item.expires) && item.launcher.kind == ref.kind &&
		item.launcher.credentialID == ref.credentialID && item.launcher.version == ref.version
}

// The runtime checks run outside auth locks/transactions. Remember excluded
// bindings too: only a newly admitted old-credential binding requires a retry.
func (a *Authenticator) liveOwnerRenewals(ctx context.Context, ref PrincipalRef) (map[*SessionCredentials]map[[32]byte]bool, error) {
	a.ownerRenewal.RLock()
	snapshots := make(map[*SessionCredentials]map[[32]byte]sessionCredential)
	now := a.clock.Now().Time()
	for _, c := range a.ownerIssuers {
		c.mu.Lock()
		for _, hash := range c.runs {
			item := c.entries[hash]
			if renewableOwner(item, ref, now) {
				if snapshots[c] == nil {
					snapshots[c] = make(map[[32]byte]sessionCredential)
				}
				item.scope.AllowedTools = slices.Clone(item.scope.AllowedTools)
				snapshots[c][hash] = item
			}
		}
		c.mu.Unlock()
	}
	a.ownerRenewal.RUnlock()
	live := make(map[*SessionCredentials]map[[32]byte]bool)
	for c, entries := range snapshots {
		live[c] = make(map[[32]byte]bool)
		for hash, item := range entries {
			err := c.validate(ctx, item.scope)
			if err != nil && !errors.Is(err, ErrUnauthenticated) {
				return nil, err
			}
			live[c][hash] = err == nil
		}
	}
	return live, nil
}

// Caller holds ownerRenewal exclusively through commit and publication. A new
// launch that raced the validation must be checked by a subsequent refresh.
func (a *Authenticator) ownerRenewalsChanged(ref PrincipalRef, live map[*SessionCredentials]map[[32]byte]bool) bool {
	now := a.clock.Now().Time()
	for _, c := range a.ownerIssuers {
		c.mu.Lock()
		changed := false
		for _, hash := range c.runs {
			if renewableOwner(c.entries[hash], ref, now) {
				if _, checked := live[c][hash]; !checked {
					changed = true
					break
				}
			}
		}
		c.mu.Unlock()
		if changed {
			return true
		}
	}
	return false
}

func (a *Authenticator) publishOwnerRenewal(ref PrincipalRef, oldSeal string, row model.AuthSession, live map[*SessionCredentials]map[[32]byte]bool) {
	now := a.clock.Now().Time()
	for c, entries := range live {
		c.mu.Lock()
		for hash, admitted := range entries {
			item, exists := c.entries[hash]
			if admitted && exists && renewableOwner(item, ref, now) && oldSeal != "" && item.seal == oldSeal &&
				item.ceiling.UserID == row.UserID && c.runs[sessionRunKey{item.scope.TenantID, item.scope.RunRef}] == hash {
				item.launcher = PrincipalRef{kind: KindUser, credentialID: row.ID, version: row.Version}
				item.seal = queuedCredentialSeal(row.SecretHash)
				c.entries[hash] = item
			}
		}
		c.mu.Unlock()
	}
}
