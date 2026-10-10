// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package gitbinding is the first-party source kind "git": the roster row a
// plain-git publication binding lives in. It OBSERVES nothing — a plain git
// remote has no API to read — so Gather emits no report and returns at once.
// Its only product work is Open: it refuses a row whose remote is not an
// admitted ssh:// or https:// destination at apply time, not at first push.
// The publication credential (config key publish_credential, a
// store:git-host/ reference) stays a custody concern, exactly as on github
// and gitlab rows, and never reaches this connector.
package gitbinding

import (
	"context"
	"errors"

	gp "github.com/olivaresai/olivares/connectors/gitpublish"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the connector's globally unique identifier.
const Name = "olivares.git-binding"

// Source is the plain-git publication binding source. The zero value is not
// usable; call New.
type Source struct{}

// Compile-time proof that Source satisfies the SourceConnector contract.
var _ sdk.SourceConnector = (*Source)(nil)

// New returns a git publication-binding source.
func New() *Source { return &Source{} }

// Descriptor returns the connector's self-description and configuration
// schema.
func (s *Source) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeSource,
		Title:       "Git publication binding (plain remote)",
		Description: "The roster row a plain-git publication binding lives in: one SSH or HTTPS git remote that gitpublish pushes to (push only; a plain remote offers no pull-request or merge API). Observes nothing.",
		ConfigFields: []sdk.ConfigField{
			{Key: gp.PublicationRemoteKey, Type: sdk.FieldString, Required: true, Description: "The one approved remote, ssh://<user>@<host>[:<port>]/<path> or https://<host>/<path>. SSH takes any host and port (the host key is pinned on first use); HTTPS keeps the endpoint rules (a DNS name, port 443)."},
		},
	}
}

// Open reads and validates configuration: the remote must be an admitted
// plain-git destination, with the same rule the publication adapter applies.
func (s *Source) Open(_ context.Context, cfg sdk.Config) error {
	kind, _ := gp.LookupTargetKind("git")
	if _, err := kind.Bind(cfg.Settings); err != nil {
		return errors.New("git-binding: the remote must be ssh://<user>@<host>[:<port>]/<path> or https://<host>/<path>")
	}
	return nil
}

// Gather emits nothing: a plain git remote offers nothing to observe. It is a
// batch source that completes immediately unless ctx has already ended.
func (s *Source) Gather(ctx context.Context, _ sdk.Sink) error { return ctx.Err() }

// Close releases resources; this connector holds none.
func (s *Source) Close(context.Context) error { return nil }
