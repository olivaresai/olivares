// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package sessioncockpit is the PUBLIC, AGPL half of the session cockpit: an honest
// availability descriptor and nothing else.
//
// ⛔ THE COMMERCIAL ENGINE IS NOT HERE AND MUST NEVER BE. Inventory, the mTLS agent
// listener, the input sessions, the recorder and the CA live in the overlay under
// enterprise/sessioncockpit*, link only under `enterprise && addon_ids`, and are the
// bytes a customer who did not buy Identity & Scale must not receive
// (docs/contracts/COCKPIT-07-edition-cut.md; canon "the frontier lives in the BYTES").
// Adding a route here that DOES something is not a shortcut — it moves a paid
// capability into the open artifact.
//
// What this package is for: a build without the add-on still has to answer the console
// and the CLI honestly. A missing route is a 404 an operator reads as a broken install;
// a 501 with a code says "this artifact does not include it", which is true and
// actionable. It is the same polarity the activation and log-broker seams already use
// (core/api/errors.go: activation_unavailable, log_broker_unavailable).
//
// Contract: docs/contracts/COCKPIT-06-api-cli.md §3.
package sessioncockpit

import (
	"context"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/sdk"
)

// Namespace is the module's API namespace. It is the ONE place the string lives, so
// the placeholder, the permission and the community allowlist cannot drift apart.
const Namespace = "session-cockpit"

// PermAvailabilityRead is the only permission the placeholder declares. A build without
// the add-on must not advertise operational permissions it cannot honor: a role that
// could be granted `session-cockpit:input:write` on a community binary would be a
// promise the artifact cannot keep.
const PermAvailabilityRead auth.Permission = "session-cockpit:availability:read"

// Placeholder is the availability-only module. It is what editionModuleRegistrars
// returns in a build without `addon_ids`; the overlay returns the real engine instead,
// under the SAME namespace, so exactly one module ever owns /v1/m/session-cockpit.
type Placeholder struct{}

// NewPlaceholder returns the availability-only module.
func NewPlaceholder() *Placeholder { return &Placeholder{} }

var (
	_ api.Module = (*Placeholder)(nil)
	_ sdk.Module = (*Placeholder)(nil)
)

// Descriptor / lifecycle (sdk.Module).
//
// ⛔ IT IMPLEMENTS sdk.Module TOO, AND THAT IS NOT BOILERPLATE — a gate refused the build
// without it. `scripts/check-schema-parity.sh` walks the modules the composition root
// registers and builds the schema manifest from each one's Descriptor; a registered module
// that satisfies only api.Module fails it with «does not satisfy sdk.Module», which is the
// parity gate doing exactly what it exists for: every module the binary mounts has to be
// describable in the manifest both editions are compared through.
//
// The lifecycle is genuinely empty and says so rather than pretending: this module owns no
// entities, no goroutines and no timers, and since the availability route was retired it
// answers nothing at all: it exists to prove the seam carries a real module.
func (p *Placeholder) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name: "olivares.session-cockpit", Version: "0.1.0", APIVersion: sdk.APIVersion,
		Type: sdk.TypeModule, Title: "Session cockpit (availability)",
		Description: "Availability descriptor for the session cockpit. This artifact does not include the cockpit itself, and serves no route for it: a build without the add-on has no handler, so the router answers 404 by absence.",
	}
}

func (p *Placeholder) Init(context.Context, sdk.Host) error { return nil }
func (p *Placeholder) Start(context.Context) error          { return nil }
func (p *Placeholder) Stop(context.Context) error           { return nil }

// APINamespace mounts the module under /v1/m/session-cockpit.
func (p *Placeholder) APINamespace() string { return Namespace }

// Permissions declares the availability read, and it is NOT a routed permission.
//
// ⛔ SE RETIRO EL 2026-09-04 Y SE RESTAURA EN EL MISMO TURNO, con la medida que faltaba.
// El razonamiento de la retirada era correcto en su forma —permsdump dice que una permission
// sin ruta que la exija es «una declaracion que nada aplica», y el comentario de
// nonRoutePermissions manda quitarla EN LA DECLARACION, no bendecirla en la excepcion— pero
// su premisa era FALSA: yo medi que este literal solo existia en este fichero, buscando en
// .go/.ts/.json y web/. Hay SIETE consumidores, y uno es un GATE:
//
//	scripts/check-community-cockpit-strings.sh   ← lo usa como CONTROL POSITIVO
//	scripts/community-session-cockpit-strings.allow
//	docs/contracts/COCKPIT-07-edition-cut.md     ← el contrato de la edicion
//	an internal design note (not shipped)
//	Taskfile.yml · placeholder_test.go · este fichero
//
// Sin este literal en el binario de community, ese gate no puede distinguir «no hay traza
// inesperada» de «este binario nunca llevo el placeholder»: su verde seria cierto POR VACIO.
// Quitarlo no cerro un agujero — cego un control.
func (p *Placeholder) Permissions() []auth.Permission {
	return []auth.Permission{PermAvailabilityRead}
}

// APIRoutes mounts NOTHING, and that is the contract — not an omission.
//
// ⛔ IT USED TO MOUNT `GET /availability` ANSWERING 501, AND THAT ROUTE WAS RETIRED HERE
// on 2026-09-02 for a reason worth keeping, because the next person will want to add it
// back. Two facts collided:
//
//  1. `lint:public-counts` requires the set of module-registered routes to EQUAL the set
//     of operations in `web/openapi/openapi.beta.json`. It has no waiver — by design:
//     `scripts/openapi-op-descriptions/routes.go` says the mismatch is reported "instead
//     of skipped", so a route here means an edit to `web/`.
//  2. This lane may not touch `web/` (GOAL §5), and the v4 architecture retires this stub
//     outright: the edition cut closes by ABSENCE — a build without `addon_ids` has no
//     handler and the router answers 404 — not by a 501 from an AGPL placeholder
//     (v4 §13.3 and §7.4).
//
// So publishing the operation would have meant editing `web/` twice for an operation
// with a two-commit life: once to add it, once when the v4 deletes it. The honest move is
// not to register it.
//
// What survives is what phase 0 actually needs this package for: it proves the
// `editionModuleRegistrars` seam carries a real `api.Module`, and its Descriptor strings
// are the literals the community-artifact gate hunts for in a build without the add-on.
func (p *Placeholder) APIRoutes(api.RouteRegistrar) {}
