// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"net/http"

	"github.com/olivaresai/olivares/connectors/identitysource"
	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/eventbus/natsbus"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/compliance"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/knowledge"
	"github.com/olivaresai/olivares/modules/reporting"
	"github.com/olivaresai/olivares/sdk"
	"github.com/olivaresai/olivares/sdk/event"
	"github.com/spf13/cobra"
)

// thisEdition is what this binary's edition adds. One build-tagged function fills it:
// editionPortsForBuild in wire_noenterprise.go for Community, and the Business overlay's
// twin for the commercial builds. Everything else reads these fields.
//
// It is assigned in init rather than in its declaration because the overlay's
// constructors reach code that reads thisEdition, which a package-level initializer may
// not do. No package-level initializer and no other init function reads it.
var thisEdition editionPorts

func init() { thisEdition = editionPortsForBuild() }

// editionPorts is every capability the Business overlay adds to the shared composition.
//
// Community is the zero value plus the behaviors it really has: its name, the unlimited
// seat policy, the static upstream credential, the refusal of a configured durable bus,
// the cockpit placeholder module and the circuit-breaker column declarations. A nil
// port means the capability is not in this edition; every caller keeps the answer it
// gave before (501 *_unavailable, 403, CLI exit code 9, an inert gate).
type editionPorts struct {
	// Reported by license status and the activation service.
	name string
	// The binary links commercial add-ons; Community help hides the add-on-only commands.
	addOnsLinked bool
	// false refuses startup on a deployment whose store records the login-enforcement component and carries a configured global/default demand.
	loginEnforcementLinked bool

	// nil adds no route: next is served unchanged.
	httpRoutes func(next http.Handler, eng *engine, log *slog.Logger) http.Handler
	// nil serves the AGPL console bundle; the public dist keeps its path.
	consoleFS func(base fs.FS) fs.FS

	// Identity.
	// nil: no managed-SCIM client pushes provisioning to an IdP; the inbound SCIM server is open-core. Boot constructs its module and binds the live identity owners.
	managedSCIM editionPort[any]
	// Every edition fills it; Community's policy reports UNLIMITED active accounts.
	// This is the only license port: the overlay binds claims, grants and coherent
	// observations from this same live holder. Community consults neither it nor the CRL;
	// it does not gate reads, export, or deny-closed evaluation.
	seatPolicy func(*licenseHolder, crlViewFunc) auth.SeatPolicy
	// nil is what makes auth.FederationService enforce the single-IdP cap.
	federationMultiIDP editionPort[auth.MultiIDP]
	// nil makes CompleteSSO skip group reconciliation; the console reports groups_mapped_by=unavailable.
	groupMapper editionPort[auth.GroupMapper]
	// nil never enforces: a stored require_sso / CIDR posture is inert dead data.
	loginPolicy func(getenv func(string) string, fed *auth.FederationService, log *slog.Logger) auth.LoginPolicy
	// nil pushes no CAEP event; the open receiver (core/auth/caep_events.go) is unaffected.
	caepTransmitter func(getenv func(string) string, log *slog.Logger) (caepTransmitter, error)
	// nil makes "conjur" an unknown source kind.
	inProcSource func(kind string) (sdk.SourceConnector, bool)
	// nil makes "conjur" an unknown identity connector kind.
	rosterProvider func(kind string) (identitysource.GraphProvider, sdk.SourceConnector, bool)
	// nil ignores a Conjur credential in OLIVARES_NHI_ACTUATORS_CONFIG; rotation degrades to manual.
	nhiActuator func(nhiActuatorTenant, string, *slog.Logger) (governance.LifecycleActuatorBinding, bool)
	// nil makes placing a department and filing a group in a workspace answer 501
	// departments_unavailable; the tree and places already stored stay readable.
	departmentService func(store.Store, *auth.Authenticator) api.DepartmentService
	// nil adds no department verbs (`tree`, `set-parent`, `place`) to `olivares workspaces`.
	workspaceCommands func(bootstrapClient) []*cobra.Command

	// Inference, hooks and MCP gates.
	// nil leaves req.Tools not enforced, exactly as before the add-on (observe-only).
	serverToolEgressGate editionEnvPort[serverToolEgressGate]
	// nil: the core's text DLP and the deny-closed unscanned posture still run, with no deep inspection.
	contentInspector editionEnvPort[contentInspector]
	// nil reduces tool_input to a sanitized resource ref for the decision, never inspected.
	hookContentInspector editionEnvPort[contentInspector]
	// nil lets computer-use tool declarations pass through ungoverned; the response-side audit still runs.
	// The commercial gate (enterprise/computeruse) is additive, so Community never references it.
	computerUseGate editionEnvPort[computerUseGate]
	// nil makes the inference PEP's circuit-breaker gate a no-op; the kill-switch still works.
	circuitBreakerEngine func(getenv func(string) string, deps circuitBreakerDeps, log *slog.Logger) circuitBreakerEngine
	// Every edition fills it: the breaker rule table's column declarations.
	circuitBreakerDeclarations func() circuitBreakerRuleDecls
	// nil makes hooks attest and conform answer that the capability is not in this edition.
	hookHardening editionPort[hookHardeningEngine]
	// nil subscribes no feed, and the CLI answers honestly that the add-on is not in this edition.
	threatIntelSource editionEnvPort[threatIntelSource]
	// Every edition fills it; Community sends the operator-configured upstream credential to every target.
	upstreamCredentialProvider func(staticAuth string) UpstreamCredentialProvider
	// nil means only the core injection markers run over retrieved chunks.
	retrievalDeepScanner editionPort[knowledge.RetrievalContentScanner]
	// nil makes the PEP skip pin verification: tools/call proceeds exactly as before.
	toolPinVerifier editionEnvPort[mcpc.ToolPinVerifier]
	// nil binds no pin audit: there is no verifier.
	bindToolPinAudit func(mcpc.ToolPinVerifier, store.Store, *slog.Logger)
	// nil persists no pins (there is no verifier); the schema still registers in both editions.
	bindToolPinPersistence func(context.Context, mcpc.ToolPinVerifier, store.Store, *slog.Logger)
	// nil skips MCP App template inspection; the render gate, consent and deny-closed inventory keep working.
	mcpRenderInspector editionEnvPort[mcpc.RenderInspector]
	// nil skips elicitation and sampling mediation; surface.go still inventories the capability advertisement.
	mcpElicitationMediator editionEnvPort[mcpc.ElicitationMediator]

	// Compliance, records and reporting.
	// nil makes ingestion answer 501; Business OSCAL export includes all stored controls.
	oscalProfileResolver editionPort[compliance.ProfileResolver]
	// nil enforces no floor: the sweep cuts on the operator's own retention_days, and schedules are freely relaxed or deleted.
	retentionGovernor editionEnvPort[compliance.RetentionGovernor]
	// nil makes /dora/register and /dora/incidents answer 501; GET /dora belongs to Business Compliance Packs.
	regulatoryPackager editionPort[compliance.RegulatoryPackager]
	// nil keeps Business OSCAL export at its three base models; Community OSCAL is 501.
	poamBuilder editionPort[compliance.POAMBuilder]
	// nil makes the /aims/pack endpoints answer 501.
	aimsPackager editionPort[compliance.AIMSPackager]
	// nil makes the /depth/* endpoints answer 501.
	complianceDepthPackager editionPort[compliance.ComplianceDepthPackager]
	// nil makes the /nis2/incidents classification endpoints answer 501.
	nis2IncidentPackager editionEnvPort[compliance.NIS2IncidentPackager]
	// nil: the open-core workflow still performs real per-subject crypto-shredding, legal-hold gating and ledger verification.
	cryptoShredCoordinator editionEnvPort[any]
	// nil binds nothing: there is no coordinator to bind.
	bindCryptoShredPorts func(coordinator any, sink audit.ArchiveSink, sinkKind string, comp *compliance.Module, log *slog.Logger)
	// nil registers no legal-hold reconciliation loop.
	longHorizonHold func(getenv func(string) string, sink audit.ArchiveSink, comp *compliance.Module, log *slog.Logger) longHorizonHold
	// nil leaves external sink kinds unavailable in Business; Community refuses all configured archival.
	archiveSink func(kind string, cfg auditArchiveConfig, log *slog.Logger) (audit.ArchiveSink, bool)
	// nil adds no further archive subcommands; export/verify use their build-time seams.
	archiveCommands editionPort[[]*cobra.Command]
	// nil adds nothing to the retirement census beyond the Community composition.
	censusContributions editionPort[[]store.CompositionContribution]
	// nil means the binary never schedules reports.
	reportScheduler editionPort[reporting.ReportScheduler]
	// nil registers no report schedule pump; the implementation belongs to Business.
	reportScheduleJob func(*bootState)
	// nil means Business reports use the default professional theme.
	reportBranding editionPort[reporting.BrandingProvider]
	// nil means Business reports use the five built-in templates.
	reportCustomTemplates editionPort[reporting.CustomTemplateProvider]
	// nil makes /enterprise/* reporting routes answer 501; on-demand reports belong to Business Compliance Packs.
	enterpriseReportSource func(getenv func(string) string, comp *compliance.Module, gov *governance.Module, pins mcpc.ToolPinVerifier, log *slog.Logger) reporting.EnterpriseReportSource
	// nil makes "teamsbot" an unknown output kind, exactly as before the add-on.
	outputConnector func(kind string) (sdk.OutputConnector, bool)
	// nil: the passive PagerDuty/Opsgenie notify sinks behave exactly as before.
	incidentCloseLoop func(ctx context.Context, getenv func(string) string, log *slog.Logger) incidentCloseLoop

	// Platform: event bus, activation and the session cockpit.
	// Every edition fills it; Community is NOT silently inert: a set OLIVARES_DURABLE_BUS_CONFIG refuses the boot.
	durableBus func(getenv func(string) string, decoders map[event.Type]natsbus.PayloadDecoder, demote func(error) bool, log *slog.Logger, licenseFile, dataDir string) (injectGatedBus, error)
	// nil adds no top-level command; the binary still READS an activation manifest but never links its writer.
	rootCommands editionPort[[]*cobra.Command]
	// nil makes the /v1/console/activation endpoints answer 501.
	activationService func(dataDir string, st store.Store, edition string, log *slog.Logger) api.ActivationService
	// nil: no add-on runs a module.
	activationModules func(addOn string) []string
	// Every edition fills it; Community mounts only the session-cockpit placeholder and links none of the engine.
	moduleRegistrars func(EditionConfig) []api.Module
	// nil opens no extra port.
	agentServers editionPort[[]editionAuxServer]
	// nil: there are no private read resources to bind or close.
	bindModuleDependencies func(context.Context, EditionConfig, []api.Module, EditionDependencies, *slog.Logger) ([]io.Closer, error)
}

// editionPort builds one capability. A nil port builds the zero value: the capability is absent.
type editionPort[T any] func() T

func (p editionPort[T]) get() (zero T) {
	if p == nil {
		return zero
	}
	return p()
}

// editionEnvPort builds one capability from the operator's environment. A nil port builds the
// zero value: the capability is absent.
type editionEnvPort[T any] func(getenv func(string) string, log *slog.Logger) T

func (p editionEnvPort[T]) get(getenv func(string) string, log *slog.Logger) (zero T) {
	if p == nil {
		return zero
	}
	return p(getenv, log)
}

// routes adds this edition's HTTP surface in front of next. Community adds none.
func (e *editionPorts) routes(next http.Handler, eng *engine, log *slog.Logger) http.Handler {
	if e.httpRoutes == nil {
		return next
	}
	return e.httpRoutes(next, eng, log)
}

// console is the console bundle this edition serves. Community serves base unchanged.
func (e *editionPorts) console(base fs.FS) fs.FS {
	if e.consoleFS == nil {
		return base
	}
	return e.consoleFS(base)
}
