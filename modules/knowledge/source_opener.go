// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package knowledge

import (
	"context"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/store"
)

// SourceOpener admits and opens a request-owned document source for the stored KB
// ID and authenticated request. It returns only safe refusal text and an HTTP
// status. Knowledge owns the returned source and closes it after the pull. Static
// boot sources keep their existing lifecycle and take precedence.
type SourceOpener func(context.Context, api.ModuleContext, string, string) (*OpenedSource, string, int)

// SourceValidation names the three native checks around an ingest. Read checks
// current stored evidence in a View; Pin acquires it before writes; Freshness
// verifies the original horizon after audit while the pins remain held.
type SourceValidation uint8

const (
	SourceRead SourceValidation = iota
	SourcePin
	SourceFreshness
)

// OpenedSource couples request-owned content to the authority for its stored KB.
// Validate makes no external call and uses only the supplied tenant scope.
type OpenedSource struct {
	Source   contentsource.Source
	Validate func(context.Context, store.Scope, SourceValidation) error
}

// UseSourceOpener binds the composition-root source authority before Start, when
// its authorizer and tenant vault exist. It must not change during requests.
func (m *Module) UseSourceOpener(open SourceOpener) { m.sourceOpener = open }
