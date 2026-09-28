// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// standingPort is the one standing reader the composition hands every fenced
// writer: a writer reads the standing of the accounts it names through it, then
// opens its transaction by pinning the authority versions it read.
type standingPort struct {
	reader auth.StandingReader
	// afterReadHook runs after a standing read returns. It is nil in production.
	afterReadHook func(tenant model.TenantID, users []model.ID)
}

// errNoStandingReader refuses a standing read through a port no reader was
// wired to: an absent reader never answers that nothing is being removed.
var errNoStandingReader = errors.New("standing port: no standing reader is wired")

// Standing implements auth.StandingReader.
func (p *standingPort) Standing(ctx context.Context, tenant model.TenantID, users []model.ID) (map[model.ID]auth.Standing, error) {
	if p == nil || p.reader == nil {
		return nil, errNoStandingReader
	}
	out, err := p.reader.Standing(ctx, tenant, users)
	if err == nil && p.afterReadHook != nil {
		p.afterReadHook(tenant, users)
	}
	return out, err
}

// AccountsByExternalID implements auth.ExternalIDResolver through the reader,
// for writers that store a directory external id rather than an account id.
func (p *standingPort) AccountsByExternalID(ctx context.Context, externalIDs []string) ([]model.ID, error) {
	var resolver auth.ExternalIDResolver
	if p != nil {
		resolver, _ = p.reader.(auth.ExternalIDResolver)
	}
	if resolver == nil {
		return nil, errors.New("standing port: no reader resolves external ids")
	}
	return resolver.AccountsByExternalID(ctx, externalIDs)
}

// AccountsByAlias implements auth.AliasResolver through the reader, for writers
// of untyped content that may name an account by id, credential or email.
func (p *standingPort) AccountsByAlias(ctx context.Context, aliases auth.Aliases) ([]model.ID, error) {
	var resolver auth.AliasResolver
	if p != nil {
		resolver, _ = p.reader.(auth.AliasResolver)
	}
	if resolver == nil {
		return nil, errors.New("standing port: no reader resolves aliases")
	}
	return resolver.AccountsByAlias(ctx, aliases)
}

// CorrelateContent implements auth.Correlator through the reader, for writers
// of counted content.
func (p *standingPort) CorrelateContent(ctx context.Context, tenant model.TenantID, doc auth.Document, lim auth.CensusLimits) (auth.Correlation, error) {
	var correlator auth.Correlator
	if p != nil {
		correlator, _ = p.reader.(auth.Correlator)
	}
	if correlator == nil {
		return auth.Correlation{}, errors.New("standing port: no reader correlates")
	}
	return correlator.CorrelateContent(ctx, tenant, doc, lim)
}

// CorrelateOwnerKeys implements auth.Correlator through the reader, for writers
// that store an owner's qualified subject key.
func (p *standingPort) CorrelateOwnerKeys(ctx context.Context, tenant model.TenantID, keys []auth.QualifiedKey) (auth.Correlation, error) {
	var correlator auth.Correlator
	if p != nil {
		correlator, _ = p.reader.(auth.Correlator)
	}
	if correlator == nil {
		return auth.Correlation{}, errors.New("standing port: no reader correlates")
	}
	return correlator.CorrelateOwnerKeys(ctx, tenant, keys)
}

var (
	_ auth.StandingReader     = (*standingPort)(nil)
	_ auth.ExternalIDResolver = (*standingPort)(nil)
	_ auth.AliasResolver      = (*standingPort)(nil)
	_ auth.Correlator         = (*standingPort)(nil)
)
