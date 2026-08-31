// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build !enterprise

package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/spf13/cobra"

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
)

// buildEdition is the edition of THIS binary — f(build tag), independent of any
// license. The default AGPL artifact is the community edition; the license
// install/status surface reports it for the in-place-upgrade UX. A hot-apply can
// never change it — only a binary swap to the `-tags enterprise` superset does.
const buildEdition = "community"

// withEnterpriseHTTP wires NO extra HTTP surface in the default (AGPL) build: the
// unauthenticated SP-metadata ENDPOINT stays an enterprise nicety. Since
// The SAML provider (and its SAMLMetadata method) is open-core, so crewjam IS
// linked in the default artifact and the method exists — but publishing the
// SP-metadata route remains reserved to -tags enterprise (see wire_enterprise.go).
// Returning next unchanged makes /v1/auth/federation/saml/metadata a 404 here.
func withEnterpriseHTTP(next http.Handler, _ *engine, _ *slog.Logger) http.Handler {
	return next
}

// newFederationMultiIDP returns NO multi-IdP capability in the DEFAULT (AGPL)
// build: single-IdP OIDC/SAML SSO is open-core (wired build-independently
// in federationwire.go — the base binary DOES link go-oidc/crewjam and does
// real single-IdP login), but resolving MORE than one active IdP (per-tenant
// multi-IdP / by-domain) is the reserved enterprise line. A nil capability is
// what makes auth.FederationService enforce the single-IdP cap (a second active
// config returns multi_idp_requires_enterprise). This file imports nothing from
// enterprise/, so the default artifact never links the reserved selection code.
// Build with -tags enterprise (wire_enterprise.go) to wire it.
func newFederationMultiIDP() auth.MultiIDP {
	return nil
}

// newGroupMapper returns NO login-time group-mapping capability in the DEFAULT
// (AGPL) build (U2): the base binary EXTRACTS the groups an IdP asserts
// (open-core, U1 — FederatedIdentity.Groups is populated) but has no code to turn
// an asserted group into a grant. A nil GroupMapper is what makes CompleteSSO skip
// group reconciliation entirely (the honest cap, symmetric with the multi-IdP and
// login-policy caps), and the console reports groups_mapped_by=unavailable — it
// never fakes the mapping. This file references nothing from enterprise/, so the
// default artifact never links the reserved mapping code. Build with -tags
// enterprise (wire_enterprise.go) to wire it.
func newGroupMapper() auth.GroupMapper {
	return nil
}

// newLoginPolicy wires NO login-enforcement policy in the default (AGPL) build:
// require-SSO (block password login) and the network/IP allow-list are the reserved
// enterprise add-on (enterprise/ssoenforce). A nil policy means the open binary NEVER
// enforces — Authenticator.Login and CompleteSSO behave exactly as before this seam (no
// rug-pull), and the console reports enforced_by=unavailable (it never fakes the
// control). The posture an operator stores on the SSO config (require_sso / CIDRs) is
// inert dead data here. The enterprise build (wire_enterprise.go) injects the closed
// engine, reading fedSvc.Posture; this file references nothing from enterprise/, so the
// default artifact never links the enforcement code. Build with -tags enterprise to wire it.
func newLoginPolicy(_ func(string) string, _ *auth.FederationService, _ *slog.Logger) auth.LoginPolicy {
	return nil
}

// newSeatPolicy wires the community seat policy in the default (AGPL) build. Since
// B10 that policy reports UNLIMITED active accounts (auth.CommunitySeatLimit = 0):
// self-hosted has no user cap in any tier, so there is nothing here to lift. The
// license claims AND the observed license CRL are ignored, as before — the open
// binary never reads a license to change behavior.
//
// The seam itself is retained (not deleted): the enterprise build
// (wire_enterprise.go) still injects its own policy, whose figure is display-only
// now — core/auth.enforceSeatCapTx refuses nothing in any build.
func newSeatPolicy(_ licenseClaimsFunc, _ crlViewFunc) auth.SeatPolicy {
	return auth.NewCommunitySeatPolicy()
}

// bindEnterpriseEntitlement is the open half of the grant-list seam. The
// enterprise overlay implements the real binder (it publishes the list to
// every add-on gate). The AGPL build has nothing to entitle, so this is a
// no-op: the signature exists so boot.go can call it in both builds without
// a build tag. It does not gate reads, export, or deny-closed evaluation.
func bindEnterpriseEntitlement(_ licenseGrantsFunc) {}

// newServerToolEgressGate returns NO server-tool egress gate in the default (AGPL)
// build (P0 #1): the inline inference PEP keeps its observe-only behavior — req.Tools
// is not enforced, exactly as before this add-on (no rug-pull). The commercial gate
// (enterprise/servertoolegress) is additive and links only under -tags enterprise (see
// wire_enterprise.go), so the default artifact never references the closed module. A nil
// gate is the decider's inert default (servertoolegressgate.go).
func newServerToolEgressGate(_ func(string) string, _ *slog.Logger) serverToolEgressGate {
	return nil
}

// newContentInspector returns NO content firewall in the default (AGPL) build (P1): the inline
// inference PEP keeps its prior behavior — the core's text DLP and the (build-independent,
// now-extended) deny-closed unscanned posture still run, but there is no deep prompt-injection
// / exfiltration / unsafe-action inspection (no rug-pull). The commercial firewall
// (enterprise/contentfirewall) is additive and links only under -tags enterprise (see
// wire_enterprise.go), so the default artifact never references the closed module. A nil
// inspector is the decider's inert default (contentinspectorgate.go).
func newContentInspector(_ func(string) string, _ *slog.Logger) contentInspector {
	return nil
}

// newHookContentInspector returns NO hooks-hardening DLP firewall in the default (AGPL) build
//: the governed hooks PEP keeps its prior behavior — the tool_input arguments are reduced
// to a sanitized resource ref for the decision but never inspected for sensitive values or
// dangerous structure (no rug-pull). The commercial firewall (enterprise/hookhardening) is
// additive and links only under -tags enterprise (see wire_enterprise.go), so the default
// artifact never references the closed module. A nil inspector is the decider's inert default
// (claudehookfirewall.go).
func newHookContentInspector(_ func(string) string, _ *slog.Logger) contentInspector {
	return nil
}

// newThreatIntelSource returns NO threat-intel feed engine in the default (AGPL)
// build: there is no curated-feed surface at all — the open detection engine
// (modules/redteam, modules/security, modules/compliance) behaves exactly as before
// (no rug-pull). The commercial engine (enterprise/threatintel) is additive and
// links only under -tags enterprise (see wire_enterprise.go). A nil source makes
// subscribeThreatIntel a no-op (threatintelgate.go) and the CLI answer honestly that
// the add-on requires an enterprise build.
func newThreatIntelSource(_ func(string) string, _ *slog.Logger) threatIntelSource {
	return nil
}

// enterpriseOutputConnector resolves NO extra output-connector kinds in the default
// (AGPL) build: the "teamsbot" registered-bot Action.Execute destination is an
// additive enterprise capability and links only under -tags enterprise (see
// wire_enterprise.go). The Apache connectors/teams Workflows-webhook destination is
// unchanged (no rug-pull). (nil,false) makes "teamsbot" an unknown kind here, exactly
// as before this add-on.
func enterpriseOutputConnector(_ string) (sdk.OutputConnector, bool) {
	return nil, false
}

// newIncidentCloseLoop returns NO governance→incident close-loop in the default
// (AGPL) build: the passive PagerDuty/Opsgenie notify sinks behave exactly as
// before (they create alerts from finding.reported but never carry governance state
// onto them — no rug-pull). The commercial close-loop (enterprise/incidentloop) is
// additive and links only under -tags enterprise. A nil close-loop makes the bus
// subscription a no-op (incidentloopwiring.go).
func newIncidentCloseLoop(_ context.Context, _ func(string) string, _ *slog.Logger) incidentCloseLoop {
	return nil
}

// newOscalProfileResolver returns NO OSCAL ingestion resolver in the default (AGPL)
// build: profile/SSP ingestion is the commercial enterprise/oscalingest add-on.
// A nil resolver makes the ingestion endpoint answer 501 and the OSCAL export keep its
// include-all behavior, byte-identical to before this add-on (no rug-pull). This file
// imports nothing from enterprise/, so the default artifact never links the resolver.
// Build with -tags enterprise (wire_enterprise.go) to wire it.
func newOscalProfileResolver() compliance.ProfileResolver {
	return nil
}

// newRetentionGovernor returns NO retention governor in the default (AGPL) build:
// named regulatory retention floors (SEC 17a-4 / FINRA 4511 / CFTC 1.31) and the
// compliance-mode lock are the commercial enterprise/wormretention add-on. A nil governor
// makes the retention engine behave exactly as before — no floor is enforced, the sweep
// cuts on the operator's own retention_days, and schedules are freely relaxed/deleted
// (modules/compliance byte-identical, no rug-pull). Build with -tags enterprise to wire it.
func newRetentionGovernor(_ func(string) string, _ *slog.Logger) compliance.RetentionGovernor {
	return nil
}

// newRegulatoryPackager returns NO regulatory packager in the default (AGPL) build:
// the DORA Register-of-Information generator (Commission Implementing Regulation (EU)
// 2024/2956) and the major-incident classifier/report drafter (RTS (EU) 2024/1772 / 2025/301)
// are the commercial enterprise/doraregister add-on. A nil packager makes the
// /dora/register and /dora/incidents endpoints answer 501 while the open dora.go ICT-risk view
// (GET /dora) is unchanged (byte-identical, no rug-pull). This file imports nothing from
// enterprise/. Build with -tags enterprise (wire_enterprise.go) to wire it.
func newRegulatoryPackager() compliance.RegulatoryPackager {
	return nil
}

// newPOAMBuilder returns NO OSCAL POA&M builder in the default (AGPL) build: the
// FedRAMP-adjacent plan-of-action-and-milestones is the commercial enterprise/oscalingest
// add-on. A nil builder keeps the evidence OSCAL export at its three open models
// (byte-identical, no rug-pull). Build with -tags enterprise to wire it.
func newPOAMBuilder() compliance.POAMBuilder {
	return nil
}

// newLongHorizonHold returns NO long-horizon legal-hold orchestrator in the default (AGPL)
// build: reconciling object-lock legal holds on the WORM archive with the engine's
// active legal holds is the commercial enterprise/wormretention add-on. A nil reconciler
// makes newLongHorizonHoldLoop return nil (no reconciliation loop registered), so the
// archive and the holds plane behave exactly as before (no rug-pull). This file references
// nothing from enterprise/. Build with -tags enterprise to wire it.
func newLongHorizonHold(_ func(string) string, _ audit.ArchiveSink, _ *compliance.Module, _ *slog.Logger) longHorizonHold {
	return nil
}

// enterpriseArchiveSink resolves NO extra WORM sink kinds in the default (AGPL) build
//: the Azure immutable-LOCKED and GCS Bucket-Lock sinks are commercial add-ons
// (enterprise/wormsinks) that link only under -tags enterprise. (nil,false) makes any
// kind other than "dir"/"s3archive" an unknown sink here — archival OFF — exactly as
// before this seam (the open dir/s3archive WORM paths are untouched; no rug-pull).
func enterpriseArchiveSink(_ string, _ auditArchiveConfig, _ *slog.Logger) (audit.ArchiveSink, bool) {
	return nil, false
}

// enterpriseArchiveCommands adds NO extra `audit archive` subcommands in the default (AGPL)
// build: the examiner-grade evidence bundle (`audit archive bundle`) is the commercial
// enterprise/wormretention add-on, linked only under -tags enterprise. The open export/verify
// subcommands are unchanged (no rug-pull). Build with -tags enterprise to add it.
func enterpriseArchiveCommands() []*cobra.Command { return nil }

// enterpriseRootCommands adds NO top-level commands in the default (AGPL) build:
// the activation pack (`olivares enterprise enable/disable/status/promote <preset>`)
// writes the enterprise activation manifest and is the commercial add-on's writer,
// linked only under -tags enterprise (enterprise_cmd_enterprise.go). The community
// binary still READS a manifest across an in-place upgrade (activation.go), but never
// links the catalog/writer here. A nil slice adds nothing to the root command.
func enterpriseRootCommands() []*cobra.Command { return nil }

// newActivationService wires NO enterprise activation surface in the default (AGPL)
// build: reading per-add-on state and enabling/disabling a preset is the
// commercial add-on's writer, linked only under -tags enterprise. A nil service
// makes the /v1/console/activation endpoints answer 501 (the honest not-wired seam,
// like the DR / log-broker surfaces) — the console shows "available in the enterprise
// edition" rather than a broken control. This file imports nothing from enterprise/.
func newActivationService(_ string, _ store.Store, _ string, _ *slog.Logger) api.ActivationService {
	return nil
}

// newDurableBus wires NO durable event-bus backend in the default (AGPL) build:
// the HA durable JetStream bus (at-least-once + dedup, enterprise/durablebus) is the
// reserved enterprise add-on. The open in-proc bus and the open Core-NATS bridge are
// the only backends here, byte-identical to before this seam (no rug-pull).
//
// It is NOT silently inert, though: if the operator SET OLIVARES_DURABLE_BUS_CONFIG on a
// community binary, returning nil would run the cluster non-durably while the operator
// believes enforcement events are durable — a silent gap (docs/SECURITY-HARDENING.md). So a configured
// durable backend on the community edition FAILS the boot with an honest error directing
// to the enterprise edition; unset returns (nil,nil) so boot's bus selection is unchanged.
// This file imports nothing from enterprise/, so the default artifact never links the
// durable backend. Build with -tags enterprise (durablebus_enterprise.go) to wire it.
func newDurableBus(getenv func(string) string, _ map[event.Type]natsbus.PayloadDecoder, _ func(error) bool, _ *slog.Logger, _, _ string) (injectGatedBus, error) {
	if strings.TrimSpace(getenv(envDurableBusConfig)) == "" {
		return nil, nil
	}
	return nil, fmt.Errorf("%s is set but this is the community edition: the HA durable event-bus "+
		"(at-least-once + dedup over NATS JetStream) is an enterprise add-on — run the enterprise "+
		"edition, or unset %s to use the open in-proc / Core-NATS bridge backend", envDurableBusConfig, envDurableBusConfig)
}

// newUpstreamCredentialProvider returns a staticCredentialProvider in the default
// (AGPL) build: the community edition uses the same operator-configured
// upstream credential for every target — identical to the pre authHeader
// behavior. The enterprise build (wire_enterprise.go) wires the token-exchange
// minter (short-lived, audience-bound tokens via RFC 8693) instead. This file
// imports nothing from enterprise/, so the default artifact never links the minter.
// Build with -tags enterprise to wire it.
func newUpstreamCredentialProvider(staticAuth string) UpstreamCredentialProvider {
	return &staticCredentialProvider{authHeader: staticAuth}
}

// newRetrievalDeepScanner returns NO deep scanner in the default (AGPL) build
//: the three deterministic detectors (prompt-injection / exfiltration /
// unsafe-action via enterprise/contentfirewall) are the reserved enterprise
// depth. The CORE scanner (coreRetrievalScanner) runs the textscan injection
// markers unconditionally regardless; a nil deep scanner means only the core
// markers run. This file imports nothing from enterprise/, so the default
// artifact never links the enterprise depth. Build with -tags enterprise to wire it.
func newRetrievalDeepScanner() knowledge.RetrievalContentScanner { return nil }

// newCryptoShredCoordinator returns NO enterprise RTBF-depth coordinator in the default
// (AGPL) build: the open-core workflow still performs real
// per-subject crypto-shredding, legal-hold gating and ledger verification. The
// commercial coordinator (enterprise/rtbf) adds policy readiness, WORM
// coordination and enhanced verification only under -tags enterprise. nil keeps
// the open-core path byte-identical.
func newCryptoShredCoordinator(_ func(string) string, _ *slog.Logger) any { return nil }

// bindCryptoShredPorts binds NOTHING in the default (AGPL) build: there is
// no coordinator to bind (newCryptoShredCoordinator above returns nil), so the
// boot()-time port binding — the legal-hold checker and the WORM archive sink the
// enterprise coordinator verifies against — is a no-op that links nothing from
// enterprise/. Build with -tags enterprise to wire the real binding.
func bindCryptoShredPorts(_ any, _ audit.ArchiveSink, _ string, _ *compliance.Module, _ *slog.Logger) {
}

// enterpriseInProcSource resolves NO commercial in-process source connectors in
// the default (AGPL) build: CyberArk Conjur is an additive commercial
// add-on that links only under -tags enterprise (wire_enterprise.go). (nil,false)
// makes "conjur" an unknown source kind here, so the default artifact never
// references enterprise/connectors/conjur (no rug-pull).
func enterpriseInProcSource(string) (sdk.SourceConnector, bool) {
	return nil, false
}

// enterpriseRosterProvider resolves NO commercial identity roster providers in the
// default (AGPL) build: "conjur" is an unknown identity connector kind here.
// The commercial connector links only under -tags enterprise.
func enterpriseRosterProvider(string) (identitysource.GraphProvider, sdk.SourceConnector, bool) {
	return nil, nil, false
}

// enterpriseNHIActuator wires NO commercial NHI lifecycle actuator in the default
// (AGPL) build: a Conjur credential in OLIVARES_NHI_ACTUATORS_CONFIG is
// ignored here (the module degrades honestly to manual rotation), and the binary
// never links enterprise/connectors/conjur. Build with -tags enterprise to wire it.
func enterpriseNHIActuator(nhiActuatorTenant, string, *slog.Logger) (governance.LifecycleActuatorBinding, bool) {
	return governance.LifecycleActuatorBinding{}, false
}

// newComputerUseGate returns NO computer-use governance gate in the default (AGPL) build
//: computer-use tool declarations (computer_20241022/computer_20250124) in req.Tools
// pass through ungoverned, exactly as before this add-on (no rug-pull). The commercial gate
// (enterprise/computeruse) is additive and links only under -tags enterprise (see
// wire_enterprise.go), so the default artifact never references the closed module. A nil gate
// is the decider's inert default (computerusegate.go). The open-core response-side audit
// (action extraction + typed-text DLP) runs unconditionally regardless — it is build-
// independent, not gated on the enterprise seam.
func newComputerUseGate(_ func(string) string, _ *slog.Logger) computerUseGate {
	return nil
}

// newToolPinVerifier returns NO tool-pin verifier in the default (AGPL) build:
// pin verification (deny-closed on a tool-definition change / rug-pull) is the
// reserved enterprise add-on. A nil verifier makes the PEP skip the pin-verification
// block entirely — tools/call proceeds exactly as before (additive gate, no rug-pull).
// The enterprise build (wire_enterprise.go) injects the closed verifier that stores
// and compares definition fingerprints. This file references nothing from enterprise/,
// so the default artifact never links the pin-store code.
func newToolPinVerifier(_ func(string) string, _ *slog.Logger) mcpc.ToolPinVerifier { return nil }

// bindToolPinAudit is inert in the community build because there is no verifier.
func bindToolPinAudit(_ mcpc.ToolPinVerifier, _ store.Store, _ *slog.Logger) {}

// bindToolPinPersistence is inert in the community build because there is no
// verifier to persist for; the schema still registers so both editions
// share one deterministic data model.
func bindToolPinPersistence(_ context.Context, _ mcpc.ToolPinVerifier, _ store.Store, _ *slog.Logger) {
}

// newMCPRenderInspector returns NO render content inspector in the default (AGPL) build
//: the deep HTML inspection of MCP App templates (prompt-injection / exfiltration
// / unsafe-action in the rendered HTML) is the reserved enterprise add-on — it extends
// enterprise/contentfirewall with the mcp_app_render channel. A nil inspector makes the
// RS skip the content-inspection block in handleUIRead entirely — the render-gate +
// consent + deny-closed inventory keep working exactly as before (no rug-pull). The
// enterprise build (wire_enterprise.go) injects the closed inspector. This file
// references nothing from enterprise/, so the default artifact never links it.
func newMCPRenderInspector(_ func(string) string, _ *slog.Logger) mcpc.RenderInspector {
	return nil
}

// newMCPElicitationMediator returns NO elicitation/sampling mediator in the default
// (AGPL) build: the runtime governance of elicitation prompts, user responses,
// and sampling injection is the reserved enterprise add-on. A nil mediator makes the RS
// skip the mediation blocks in handleElicitation/handleSampling entirely — the
// surface.go detective still inventories the capability advertisement (no rug-pull). The
// enterprise build (wire_enterprise.go) injects the closed mediator. This file
// references nothing from enterprise/, so the default artifact never links it.
func newMCPElicitationMediator(_ func(string) string, _ *slog.Logger) mcpc.ElicitationMediator {
	return nil
}

// newCAEPTransmitter wires NO CAEP transmitter in the default (AGPL) build:
// emitting CAEP agent-risk events to external SSF receivers (signing SETs + RFC 8935
// HTTP push) is the reserved enterprise add-on (caeptransmit_enterprise.go). A nil
// transmitter makes any bus-subscription call on it a no-op — the open receiver
// (core/auth/caep_events.go) is unaffected (no rug-pull). Build with -tags enterprise
// (caeptransmit_enterprise.go) to wire the closed implementation that reads
// OLIVARES_CAEP_TRANSMITTER_CONFIG and pushes signed SETs to configured endpoints.
func newCAEPTransmitter(_ func(string) string, _ *slog.Logger) (caepTransmitter, error) {
	return nil, nil
}

// newCircuitBreakerEngine returns NO circuit-breaker engine in the default (AGPL) build
//: threshold-based automatic agent suspension with auto-reset, cooldown, and
// escalation to the kill-switch is the reserved enterprise add-on. A nil engine
// makes the inference PEP's circuit-breaker gate a no-op — the kill-switch, guardian,
// and tier-floor enforcement all continue to work (no rug-pull). Build with -tags
// enterprise to wire the closed implementation.
//
// It takes getenv and a logger so both builds share ONE call site in boot.go. The
// signature has to be identical in the two wire files or the composition root could not
// call it at all — which is exactly the state this seam was in until: declared here,
// called by nobody, with the enterprise overlay never declaring it. The parameters are
// unused on this side; the closed side reads OLIVARES_CIRCUIT_BREAKER_CONFIG from getenv.
func newCircuitBreakerEngine(_ func(string) string, _ circuitBreakerDeps, _ *slog.Logger) circuitBreakerEngine {
	return nil
}

// newAttackGraphScanner returns NO attack-graph continuous scanner in the default (AGPL)
// build: the continuous scanner, risk scoring, and exfil anomaly detection are
// the reserved enterprise add-on. A nil scanner means the AGPL attack-path queries
// (modules/access-map/attackpath.go) still work — they are build-independent reads over
// the existing AccessEdge data (no rug-pull). Build with -tags enterprise to wire it.
// `unused` reports this function. It is the NULL HALF of a build-tagged pair, so deleting it
// does not remove one caller: it removes the AGPL side of the seam, and the enterprise build
// is then replacing nothing.
//
//nolint:unused // open-core seam: the null half of a `//go:build !enterprise` pair
func newAttackGraphScanner() attackGraphScanner {
	return nil
}

// newAIMSPackager returns NO AIMS certification-readiness packager in the default (AGPL)
// build: the ISO/IEC 42001 AIMS pack (Statement of Applicability, AI policy, risk
// register, impact assessments, lifecycle controls, supplier governance) is the commercial
// enterprise/iso42001 add-on. A nil packager makes the /aims/pack endpoints answer 501
// while the open iso_42001 catalog (GET /frameworks/iso_42001), the evidence engine and
// the risk classifier are unchanged (byte-identical, no rug-pull). This file imports
// nothing from enterprise/. Build with -tags enterprise (wire_enterprise.go) to wire it.
func newAIMSPackager() compliance.AIMSPackager {
	return nil
}

// newComplianceDepthPackager returns NO compliance-depth packager in the default (AGPL)
// build: the US state AI law packs (TX TRAIGA, CA SB 53, IL HB 3773, CO SB 26-189),
// sector-overlay packs (HIPAA/PCI/FINRA), continuous controls monitoring (CCM) and FedRAMP
// 20x KSIs are the commercial enterprise/compliancedepth add-on. A nil packager makes the
// /depth/* endpoints answer 501 while the open catalog (7 new frameworks), the regulatory
// calendar (sourced milestones), the evidence engine and the risk classifier are unchanged
// (byte-identical, no rug-pull). This file imports nothing from enterprise/. Build with
// -tags enterprise (wire_enterprise.go) to wire it.
func newComplianceDepthPackager() compliance.ComplianceDepthPackager {
	return nil
}

// newNIS2IncidentPackager returns NO NIS 2 incident packager in the default (AGPL)
// build: the NIS 2 Directive Art 23 significant-incident classifier is the
// commercial enterprise/nis2incident add-on. A nil packager makes the /nis2/incidents
// classification endpoints answer 501 while the open nis2 catalog and the regulatory
// calendar are unchanged (byte-identical, no rug-pull). The signature matches the
// enterprise twin (opt-in there via OLIVARES_NIS2INCIDENT_CONFIG); the default build
// ignores both arguments. This file imports nothing from enterprise/. Build with
// -tags enterprise (wire_enterprise.go) to wire it.
func newNIS2IncidentPackager(func(string) string, *slog.Logger) compliance.NIS2IncidentPackager {
	return nil
}

// newReportScheduler returns NO report scheduler in the default (AGPL) build:
// scheduled periodic report generation (cron) is the commercial enterprise/reporting
// add-on. A nil scheduler means the open binary never schedules reports — on-demand
// generation via GET /v1/m/reporting/reports/{type} is unchanged (no rug-pull). This
// file imports nothing from enterprise/. Build with -tags enterprise to wire it.
func newReportScheduler() reporting.ReportScheduler {
	return nil
}

// newReportBranding returns NO branding provider in the default (AGPL) build:
// custom branding (logo, corporate colors, footer) is the commercial enterprise/
// reporting add-on. A nil branding means the open binary uses the default professional
// theme (no rug-pull). Build with -tags enterprise to wire it.
func newReportBranding() reporting.BrandingProvider {
	return nil
}

// newReportCustomTemplates returns NO custom template provider in the default (AGPL)
// build: operator-uploaded HTML templates are the commercial enterprise/reporting
// add-on. A nil provider means the open binary uses the five built-in templates
// (no rug-pull). Build with -tags enterprise to wire it.
func newReportCustomTemplates() reporting.CustomTemplateProvider {
	return nil
}

// newEnterpriseReportSource returns NO enterprise report engine in the default (AGPL)
// build: the compliance-posture / executive-risk / signed-evidence-bundle engine
// (enterprise/reporting) is the commercial add-on. A nil source means the /enterprise/*
// reporting routes answer 501 and the five built-in on-demand reports are unchanged
// (no rug-pull). This file imports nothing from enterprise/; build with -tags enterprise
// to wire it. The parameters are the real data sources the enterprise build feeds the
// engine; here they are ignored.
func newEnterpriseReportSource(_ func(string) string, _ *compliance.Module, _ *governance.Module, _ mcpc.ToolPinVerifier, _ *slog.Logger) reporting.EnterpriseReportSource {
	return nil
}

// newManagedSCIM returns NO managed-SCIM provisioning client in the default
// (AGPL) build (ALC-01-S1). The inbound SCIM *server* is open-core and stays
// that way. Managed SCIM is the reserved client that pushes
// create/update/deprovision toward the IdP. A nil seam is the honest reserved
// line: the community artifact never fakes a provisioning client. This file
// imports nothing from enterprise/. The shared boot path does not call this
// yet — that would require the matching symbol on the overlay wire. S2 names
// the contract; S3 is the motor.
func newManagedSCIM() any {
	return nil
}
