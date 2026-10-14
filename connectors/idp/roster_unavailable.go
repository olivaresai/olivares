//go:build !enterprise || !addon_ids

// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package idp

import (
	"context"
	"errors"

	"github.com/olivaresai/olivares/connectors/identitysource"
	"github.com/olivaresai/olivares/sdk"
)

var errRosterUnavailable = errors.New("directory inventory requires Business")

// Source preserves the connector contract when directory inventory is unavailable.
type Source struct{}

func New() *Source { return &Source{} }
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: Name, Version: "0.1.0", APIVersion: sdk.APIVersion, Type: sdk.TypeSource, Title: "Directory inventory", Description: "Directory inventory requires Business."}
}
func (s *Source) Open(context.Context, sdk.Config) error { return errRosterUnavailable }
func (s *Source) Gather(context.Context, sdk.Sink) error { return errRosterUnavailable }
func (s *Source) Close(context.Context) error            { return nil }
func (s *Source) Snapshot(context.Context) (identitysource.Graph, error) {
	return identitysource.Graph{}, errRosterUnavailable
}
