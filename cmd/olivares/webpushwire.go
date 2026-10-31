// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"github.com/olivaresai/olivares/core/webpush"
)

// webpushwire.go is the composition-root half of Web Push (#2840): the application server the
// sessions module pushes through (modules/sessions/push.go).
//
// Its VAPID key is derived from the engine-held eventing key (eventingKeyMaterial), so the
// engine has no second key file to mint, protect or back up: the disaster-recovery bundle that
// carries the eventing key carries this one with it, and nodes that share the eventing key
// through its environment variable are one application server. Rotating that key changes the
// application server key; the console notices on its next visit and subscribes again.

// newPushSender builds the Web Push sender from the same key material as the eventing sealer.
// An error means the engine has no usable key, and the caller leaves Web Push off rather than
// sign with a key nobody can reproduce.
func newPushSender(dataDir string, getenv func(string) string) (*webpush.Sender, error) {
	seed, err := eventingKeyMaterial(dataDir, getenv)
	if err != nil {
		return nil, err
	}
	key, err := webpush.KeyFromSeed(seed)
	if err != nil {
		return nil, err
	}
	return webpush.NewSender(key), nil
}
