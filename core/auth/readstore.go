// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"

	"github.com/olivaresai/olivares/core/store"
)

type authViewer interface {
	AuthView(context.Context, func(store.AuthScope) error) error
}

type authStorage interface {
	authViewer
	AuthMutate(context.Context, func(store.AuthScope) error) error
}

type readOnlyAuthStore struct{ authViewer }

func (readOnlyAuthStore) AuthMutate(context.Context, func(store.AuthScope) error) error {
	return store.ErrReadOnly
}

// NewSourceReader reuses roster reads and refuses mutations.
func NewSourceReader(reader authViewer) *SourceStore {
	return &SourceStore{st: readOnlyAuthStore{reader}}
}

// NewSecretReader reads only non-secret metadata; it cannot seal or open values.
func NewSecretReader(reader authViewer) *SecretStore {
	return &SecretStore{st: readOnlyAuthStore{reader}}
}
