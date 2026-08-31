// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"sort"

	claudeapi "github.com/olivaresai/olivares/connectors/claude-api"
	"github.com/olivaresai/olivares/modules/models"
)

// This file is the composition-root glue for module X's declared platforms
// reference: the deployment-surface matrix (ANT2-01/17) and the per-platform
// model lifecycle (ANT2-03) the platforms view renders. Like the rate-limit
// inventory adapter (modelsactuate.go), it bridges the AGPL module seam to the
// Apache connector's exported accessors HERE — modules/models keeps its "does not
// import claude-api" property (the admin-dashboards contract). Unlike the
// rate-limit adapter it needs NO credential and NO connector instance: the data is
// static declared reference (AllSurfaces / LifecycleReference /
// ParamDeprecationReference), so the provider is always constructible and never
// returns an error.

// claudePlatformsProvider adapts the claude-api declared reference accessors to the
// module's PlatformsProvider seam. Read-only and deterministic by construction.
type claudePlatformsProvider struct{}

// Compile-time proof the adapter satisfies the module's read seam.
var _ models.PlatformsProvider = claudePlatformsProvider{}

// newPlatformsProvider builds the credential-less platforms reference provider. It
// never fails: unlike the rate-limit inventory there is no credential gate, so the
// route is live wherever the module is mounted.
func newPlatformsProvider() models.PlatformsProvider { return claudePlatformsProvider{} }

// Platforms materializes the connector's declared reference into the module-owned
// shapes. Honesty mapping: confirmed rows come from the verified
// per-surface schedule and carry the family's published deprecation date;
// to-confirm rows come from the connector's verified-but-unpublished surface list
// with an EMPTY retires_on and NO deprecated_on (the partner authority published
// neither — only the row's existence is verified). The published replacement and
// the AsOf stamp are family-wide on every row.
func (claudePlatformsProvider) Platforms(context.Context) (models.PlatformsReference, error) {
	ref := models.PlatformsReference{
		SurfacesAsOf:    claudeapi.SurfacesAsOf(),
		SurfacesSource:  "connectors/claude-api/surfaces.go",
		LifecycleAsOf:   claudeapi.LifecycleAsOf(),
		LifecycleSource: "connectors/claude-api/lifecycle.go",
	}
	for _, s := range claudeapi.AllSurfaces() {
		ref.Surfaces = append(ref.Surfaces, models.PlatformSurface{
			Gateway:            string(s.Gateway),
			DisplayName:        s.DisplayName,
			Operator:           s.Operator,
			OperatorDataAccess: s.OperatorDataAccess,
			BaseURLPattern:     s.BaseURLPattern,
			AuthScheme:         s.AuthScheme,
			SigV4Service:       s.SigV4Service,
			WorkspaceHeader:    s.WorkspaceHeader,
			ModelIDForm:        s.ModelIDForm,
			APIs: models.PlatformAPISupport{
				Messages:     s.APIs.Messages,
				Admin:        s.APIs.Admin,
				Compliance:   s.APIs.Compliance,
				Models:       s.APIs.Models,
				Batches:      s.APIs.Batches,
				MCPConnector: s.APIs.MCPConnector,
			},
			Billing:     s.Billing,
			HIPAA:       s.HIPAA,
			HIPAAStatus: string(s.HIPAAStatus),
			ZDR:         s.ZDR,
			Residency:   s.Residency,
			Deprecated:  s.Deprecated,
			AsOf:        s.AsOf,
			Notes:       s.Notes,
		})
	}
	for _, f := range claudeapi.LifecycleReference() {
		l := models.PlatformLifecycle{
			ModelID:     f.ModelID,
			DisplayName: f.DisplayName,
			Retirements: make([]models.PlatformRetirement, 0, len(f.PerSurface)+len(f.ToConfirm)),
		}
		for _, sd := range f.PerSurface {
			// `confirmed` means the authority PUBLISHED the retirement date. A
			// per-surface entry with an empty date (mythos-preview "retired after
			// Mythos 5 GA", the claude-2.x dateless registry hit) is a verified
			// announcement whose DATE is to-confirm — the same semantic the web
			// established for unpublished dates, so the status badge and the
			// "date not published / to-confirm" cell never contradict each other.
			status := "confirmed"
			if sd.RetiresOn == "" {
				status = "to-confirm"
			}
			l.Retirements = append(l.Retirements, models.PlatformRetirement{
				Surface:        string(sd.Surface),
				RetiresOn:      sd.RetiresOn,
				Status:         status,
				ReplacementRef: f.Replacement,
				DeprecatedOn:   f.DeprecatedOn,
				AsOf:           f.AsOf,
			})
		}
		for _, g := range f.ToConfirm {
			l.Retirements = append(l.Retirements, models.PlatformRetirement{
				Surface:        string(g),
				RetiresOn:      "", // the authority published no date — to-confirm, never fabricated
				Status:         "to-confirm",
				ReplacementRef: f.Replacement,
				AsOf:           f.AsOf,
			})
		}
		// One stable surface-ascending order across confirmed and to-confirm rows,
		// matching the connector's sortRetirements convention.
		sort.Slice(l.Retirements, func(i, j int) bool { return l.Retirements[i].Surface < l.Retirements[j].Surface })
		ref.Lifecycles = append(ref.Lifecycles, l)
	}
	pd := claudeapi.ParamDeprecationReference()
	ref.ParamDeprecation = models.PlatformParamDeprecation{
		Params:     pd.Params,
		Affected:   pd.Affected,
		HTTPStatus: pd.HTTPStatus,
	}
	return ref, nil
}
