// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"context"
	"log/slog"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/sdk"
)

// Name is the module's globally unique identifier (the runtime registry key).
const Name = "olivares.compliance"

// Namespace is the module's store and API namespace: entities are
// "compliance.<entity>" and routes mount under /v1/m/compliance/.
const Namespace = "compliance"

// The module's permissions, granted by verb tier (viewer→read, editor→write,
// admin/owner→admin; docs/SECURITY-HARDENING.md). Reading the catalog, the live status/gap analysis,
// the capability evidence map and listing packages is read-tier; SEALING an evidence
// package, classifying risk and attesting/scanning residency are the privileged,
// audited write-tier actions; REVIEWING (approve/override) a risk classification — the
// governance decision surface — is admin-tier. The records-management plane has
// NO write tier on purpose: reading schedules/holds/certificates is read-tier, while
// authoring a schedule, sweeping, setting a hold and (gated, dual-control) releasing
// one are all admin-tier — destruction and preservation are administration, not
// day-to-day editing.
const (
	permFrameworkRead  auth.Permission = "compliance:framework:read"
	permEvidenceWrite  auth.Permission = "compliance:evidence:write"
	permRiskRead       auth.Permission = "compliance:risk:read"
	permRiskWrite      auth.Permission = "compliance:risk:write"
	permRiskAdmin      auth.Permission = "compliance:risk:admin"
	permResidencyRead  auth.Permission = "compliance:residency:read"
	permResidencyWrite auth.Permission = "compliance:residency:write"
	permRetentionRead  auth.Permission = "compliance:retention:read"
	permRetentionAdmin auth.Permission = "compliance:retention:admin"
	permHoldRead       auth.Permission = "compliance:hold:read"
	permHoldAdmin      auth.Permission = "compliance:hold:admin"
	// the RTBF plane keeps the split — reading requests/custody/receipts
	// is read-tier; registering a DSR and EXECUTING an erasure (irreversible
	// destruction, additionally CRITICAL dual-control gated) are admin-tier.
	permErasureRead  auth.Permission = "compliance:erasure:read"
	permErasureAdmin auth.Permission = "compliance:erasure:admin"
	// the OSCAL profile/SSP ingestion plane. Listing/reading a registered
	// selection is read-tier; REGISTERING (ingesting a profile/SSP that scopes the
	// assessment-results export) and UNREGISTERING are admin-tier — defining the audit
	// scope a buyer's GRC pipeline consumes is a governance decision, not day-to-day
	// editing. Both write verbs are additionally deny-closed: without the enterprise
	// resolver they answer 501.
	permOscalRead  auth.Permission = "compliance:oscal:read"
	permOscalAdmin auth.Permission = "compliance:oscal:admin"
	// the DORA named-regulation depth plane. Reading/exporting the maintained
	// Register of Information and the major-incident classifications is read-tier;
	// GENERATING the register and CLASSIFYING an incident (defining the regulatory artifact
	// a financial entity submits to its competent authority is a governance decision) are
	// admin-tier — and both are additionally deny-closed: without the enterprise packager
	// they answer 501.
	permDoraRead  auth.Permission = "compliance:dora:read"
	permDoraAdmin auth.Permission = "compliance:dora:admin"
	// the ISO/IEC 42001 AIMS certification-readiness pack plane. Reading/exporting
	// the maintained pack is read-tier; GENERATING the pack (defining the certification-
	// readiness artifact an organization submits to an auditor or a buyer is a governance
	// decision) is admin-tier — and additionally deny-closed: without the enterprise
	// packager it answers 501.
	permAimsRead  auth.Permission = "compliance:aims:read"
	permAimsAdmin auth.Permission = "compliance:aims:admin"
	// the compliance-depth plane (US state AI laws + sector overlays + CCM +
	// FedRAMP 20x). Reading/exporting packs, snapshots and KSIs is read-tier;
	// GENERATING them (defining the compliance artifact) is admin-tier — and all are
	// additionally deny-closed: without the enterprise depth packager they answer 501.
	permDepthRead  auth.Permission = "compliance:depth:read"
	permDepthAdmin auth.Permission = "compliance:depth:admin"
	permCCMRead    auth.Permission = "compliance:ccm:read"
	permCCMAdmin   auth.Permission = "compliance:ccm:admin"
	// the NIS 2 Directive significant-incident classification plane. Reading/exporting
	// the maintained classifications is read-tier; CLASSIFYING an incident and UPDATING
	// the report phase (defining the regulatory artifact an entity submits to its CSIRT or
	// competent authority is a governance decision) are admin-tier — and both are additionally
	// deny-closed: without the enterprise NIS2 incident packager they answer 501.
	permNIS2Read  auth.Permission = "compliance:nis2:read"
	permNIS2Admin auth.Permission = "compliance:nis2:admin"
)

// Option configures a Module at construction.
type Option func(*Module)

// WithClock overrides the module clock (tests inject a deterministic clock).
func WithClock(c model.Clock) Option { return func(m *Module) { m.clock = c } }

// WithAutonomySource wires a richer agent-autonomy signal for the risk classifier
// (e.g. module IV's schedule graph). Without it the classifier uses only observed
// access edges and findings — it never invents an autonomy signal.
func WithAutonomySource(s AutonomySource) Option { return func(m *Module) { m.autonomy = s } }

// WithLineageSource wires a richer perimeter-egress signal source for the residency
// scan (e.g. module VIII's data lineage). Without it the scan reads the
// knowledge.lineage ext entity inline, degrading to "no signal" if absent.
func WithLineageSource(s LineageSource) Option { return func(m *Module) { m.lineage = s } }

// WithApprovalGate wires the dual-control gate for the dangerous verbs
// (enabling a purge disposition; releasing a legal hold). nil keeps the deny-closed
// default: both verbs are DENIED until the composition root wires the bridge.
func WithApprovalGate(g ApprovalGate) Option {
	return func(m *Module) {
		if g != nil {
			m.gate = g
		}
	}
}

// WithProviderRetention wires the Covered-Models forced-retention floor source (§7).
// nil keeps the honest default: provider_floor_known=false, never a fabricated floor.
func WithProviderRetention(p ProviderRetention) Option {
	return func(m *Module) {
		if p != nil {
			m.provider = p
		}
	}
}

// WithAccountEraser wires the account-anonymization seam into the engine's
// auth partition (composition root only — user rows live in the system tenant,
// unreachable from any module scope). nil keeps the honest default: the account leg
// reports not_attempted on every receipt.
func WithAccountEraser(a AccountEraser) Option {
	return func(m *Module) {
		if a != nil {
			m.accounts = a
		}
	}
}

// WithProviderEraser wires the Anthropic Compliance DELETE passthrough
// (connectors/claude-compliance, governed by its own dual-control PEP). nil keeps
// the honest default: the provider leg reports not_wired on every receipt.
func WithProviderEraser(p ProviderEraser) Option {
	return func(m *Module) {
		if p != nil {
			m.providerEraser = p
		}
	}
}

// WithFileStoreEraser wires the governed Files-store plane (the claude-api Files API
// adapter). nil keeps the honest default: the inventory + erasure report the store as
// not_wired, and the RTBF disclosure leg is skipped.
func WithFileStoreEraser(f FileStoreEraser) Option {
	return func(m *Module) {
		if f != nil {
			m.fileEraser = f
		}
	}
}

// WithCryptoShredCoordinator wires the enterprise RTBF-depth coordinator
// (enterprise/rtbf, build-tag gated) into the open-core erasure workflow. nil keeps
// the open-core flow byte-identical. The argument is intentionally any:
// public tests can pass the typed CryptoShredCoordinator, while the private overlay's
// existing coordinator is reflect-adapted without importing enterprise code here.
func WithCryptoShredCoordinator(c any) Option {
	return func(m *Module) {
		if c != nil {
			m.shredCoordinator = c
		}
	}
}

// WithProfileResolver wires the OSCAL profile/SSP resolver (the commercial
// enterprise/oscalingest add-on, wired only under -tags enterprise). nil keeps the
// open-core default: the ingestion endpoint answers 501 and the OSCAL export keeps its
// include-all behavior (byte-identical, no rug-pull). The open module never imports the
// closed add-on; it consumes it only through this interface.
func WithProfileResolver(p ProfileResolver) Option {
	return func(m *Module) {
		if p != nil {
			m.profileResolver = p
		}
	}
}

// WithRetentionGovernor wires the records-management policy seam (the commercial
// enterprise/wormretention add-on, wired only under -tags enterprise): named regulatory
// retention floors (SEC 17a-4 / FINRA 4511 / CFTC 1.31) + a compliance-mode lock layered
// on top of the open per-class schedules. nil keeps the open-core default: no floor is
// enforced, the sweep cuts on the operator's own retention_days, and schedules are freely
// authored/relaxed/deleted (retention.go byte-identical, no rug-pull). The open module
// never imports the closed add-on; it consumes it only through RetentionGovernor.
func WithRetentionGovernor(g RetentionGovernor) Option {
	return func(m *Module) {
		if g != nil {
			m.governor = g
		}
	}
}

// WithRegulatoryPackager wires the named-regulation depth seam (the commercial
// enterprise/doraregister add-on, wired only under -tags enterprise): the DORA Register of
// Information generator (Commission Implementing Regulation (EU) 2024/2956) and the
// major-incident classifier + report drafter (RTS (EU) 2024/1772 / 2025/301). nil keeps the
// open-core default: the /dora/register and /dora/incidents endpoints answer 501 and the open
// dora.go ICT-risk view (GET /dora) is unchanged (byte-identical, no rug-pull). The open
// module never imports the closed add-on; it consumes it only through RegulatoryPackager.
func WithRegulatoryPackager(p RegulatoryPackager) Option {
	return func(m *Module) {
		if p != nil {
			m.regPackager = p
		}
	}
}

// WithPOAMBuilder wires the OSCAL reinforcement seam (the commercial enterprise/
// oscalingest add-on, wired only under -tags enterprise): a FedRAMP-adjacent
// plan-of-action-and-milestones model emitted alongside the evidence OSCAL export from the
// sealed package's not-satisfied controls. nil keeps the open-core default: the OSCAL export
// emits its three models byte-identically (no POA&M, no rug-pull). The open module never
// imports the closed add-on; it consumes it only through POAMBuilder.
func WithPOAMBuilder(b POAMBuilder) Option {
	return func(m *Module) {
		if b != nil {
			m.poamBuilder = b
		}
	}
}

// WithAIMSPackager wires the ISO/IEC 42001 AIMS certification-readiness seam (the
// commercial enterprise/iso42001 add-on, wired only under -tags enterprise): the
// Statement of Applicability, AI policy, AI risk register, impact assessments, lifecycle-
// control mapping and supplier governance structured from the live assessment + operator
// context. nil keeps the open-core default: the /aims/pack endpoints answer 501 and the
// open catalog/evidence/risk surfaces are unchanged (byte-identical, no rug-pull). The
// open module never imports the closed add-on; it consumes it only through AIMSPackager.
func WithAIMSPackager(p AIMSPackager) Option {
	return func(m *Module) {
		if p != nil {
			m.aimsPackager = p
		}
	}
}

// WithComplianceDepth wires the compliance-depth seam (the commercial
// enterprise/compliancedepth add-on, wired only under -tags enterprise): US state AI law
// packs (TX TRAIGA, CA SB 53, IL HB 3773, CO SB 26-189), sector-overlay packs
// (HIPAA/PCI/FINRA), continuous controls monitoring (CCM) and FedRAMP 20x KSIs. nil
// keeps the open-core default: the depth endpoints answer 501 and the open
// catalog/calendar/evidence/risk surfaces are unchanged (byte-identical, no rug-pull).
// The open module never imports the closed add-on; it consumes it only through
// ComplianceDepthPackager.
func WithComplianceDepth(p ComplianceDepthPackager) Option {
	return func(m *Module) {
		if p != nil {
			m.depthPackager = p
		}
	}
}

// WithNIS2IncidentPackager wires the NIS 2 Directive significant-incident
// classification seam (the commercial enterprise/nis2incident add-on, wired only under
// -tags enterprise): Art 23(3) criteria application, deadline computation and tiered
// report drafting from operator-supplied impact data. nil keeps the open-core default: the
// /nis2/incidents endpoints answer 501 and the open nis2 catalog/calendar surfaces are
// unchanged (byte-identical, no rug-pull). The open module never imports the closed add-on;
// it consumes it only through NIS2IncidentPackager.
func WithNIS2IncidentPackager(p NIS2IncidentPackager) Option {
	return func(m *Module) {
		if p != nil {
			m.nis2Packager = p
		}
	}
}

// Module is module XIII — compliance and regulatory (see doc.go for the bounded
// context and the docs/SECURITY-HARDENING.md honesty invariant). It aggregates and transforms what
// the core and the other modules already record into control status, exportable
// evidence, risk classification, residency and reporting — it captures nothing new
// and never claims certification. Adds the one deliberate exception to
// "transforms only": the records-management plane (retention schedules, legal holds,
// the governed sweep) OWNS a destruction path — see doc.go for why that does not
// break the evidence invariants (destruction is itself evidence-producing,
// hold-gated and append-only certified).
type Module struct {
	log              *slog.Logger
	data             api.ModuleData
	host             sdk.Host
	clock            model.Clock
	autonomy         AutonomySource
	lineage          LineageSource
	gate             ApprovalGate            // Dual-control seam; deny-closed default
	provider         ProviderRetention       // Floor source; nil ⇒ floor unknown (honest)
	accounts         AccountEraser           // Auth-partition seam; honest not-attempted default
	providerEraser   ProviderEraser          // passthrough seam; honest not-wired default
	fileEraser       FileStoreEraser         // Files-store plane seam; honest not-wired default
	shredCoordinator any                     // Enterprise/rtbf coordinator seam; nil ⇒ open-core flow
	profileResolver  ProfileResolver         // OSCAL ingestion seam; nil ⇒ ingestion 501 + export include-all (open-core default)
	governor         RetentionGovernor       // Records-vault floor/compliance-mode seam; nil ⇒ no floor enforced, schedules freely relaxed (open-core default)
	regPackager      RegulatoryPackager      // Named-regulation depth seam (DORA register + major-incident); nil ⇒ register/incident endpoints 501, dora.go ICT-risk view unchanged (open-core default)
	poamBuilder      POAMBuilder             // OSCAL POA&M reinforcement seam; nil ⇒ evidence OSCAL export byte-identical (no POA&M model) (open-core default)
	aimsPackager     AIMSPackager            // ISO/IEC 42001 AIMS cert-readiness seam; nil ⇒ AIMS endpoints 501, catalog/evidence/risk unchanged (open-core default)
	depthPackager    ComplianceDepthPackager // Compliance-depth seam (US laws + sector + CCM + FedRAMP); nil ⇒ depth endpoints 501, catalog/calendar/evidence unchanged (open-core default)
	nis2Packager     NIS2IncidentPackager    // NIS 2 incident seam; nil ⇒ NIS2 incident endpoints 501, catalog/calendar unchanged (open-core default)
}

// Compile-time proof the module satisfies the SDK lifecycle, the API route/permission
// seam and the data-consumer seam.
var (
	_ sdk.Module       = (*Module)(nil)
	_ api.Module       = (*Module)(nil)
	_ api.DataConsumer = (*Module)(nil)
)

// New returns a compliance module with a system clock and the fail-closed default
// seams (no autonomy signal; inline lineage reading). The composition root injects
// the real adapters via the With* options.
func New(opts ...Option) *Module {
	m := &Module{
		clock:          model.SystemClock{},
		autonomy:       coreAutonomy{},
		lineage:        coreLineage{},
		gate:           denyApprovalGate{},
		accounts:       notWiredAccountEraser{},
		providerEraser: notWiredProviderEraser{},
		fileEraser:     notWiredFileStoreEraser{},
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Descriptor returns the module's self-description.
func (m *Module) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{
		Name:        Name,
		Version:     "0.1.0",
		APIVersion:  sdk.APIVersion,
		Type:        sdk.TypeModule,
		Title:       "Compliance — regulatory mapping & audit evidence",
		Description: "Maps EU AI Act, NIS 2 Directive, NIST AI RMF (+GenAI Profile), ISO/IEC 42001, SOC 2 / ISO 27001, GDPR, the revised EU PLD and the agentic-security crosswalks (OWASP Agentic Top 10 2026, Five Eyes agentic adoption, CISA/NSA AI Data Security, CSA MAESTRO, COSAiS) onto the capabilities the control plane already produces — every framework version-pinned to its primary source. Maintains the regulatory calendar as verified data (omnibus-aware), derives auditor-consumable evidence packages from the append-only/hash-chained ledger (with a live integrity proof), exports a DORA ICT-risk-compatible view, classifies NIS 2 Art 23 significant incidents with tiered report drafting (enterprise add-on), structures an ISO/IEC 42001 AIMS certification-readiness pack (Statement of Applicability, AI policy, risk register, impact assessments, lifecycle controls, supplier governance — enterprise add-on), classifies agent risk (EU AI Act tiers cross-mapped to NIST AI RMF, governed and audited), attests data residency, governs records management (per-class retention schedules with approved purge dispositions, legal holds with an append-only chain of custody, hold-gated sweeps sealing disposition certificates —), and reports control status + gap analysis. It designs-for-audit; it never certifies, and never marks a control satisfied without linked evidence.",
	}
}

// UseData receives the tenant-parameterized data handle (the api.DataConsumer seam).
func (m *Module) UseData(d api.ModuleData) { m.data = d }

// SetLogger attaches a logger (optional).
func (m *Module) SetLogger(l *slog.Logger) { m.log = l }

// Init keeps the host (for emitting residency-violation findings) and the logger. The
// module holds no bus subscription — it is request-driven (it reacts to other modules'
// data when it reads, not to their events). It must not block.
func (m *Module) Init(_ context.Context, host sdk.Host) error {
	m.log = host.Logger()
	m.host = host
	return nil
}

// Start has no background work. It warns once per un-wired seam so a degraded
// deployment is visible (docs/SECURITY-HARDENING.md): without a data handle nothing persists; an
// un-wired autonomy/lineage seam yields LESS evidence, never a fabricated pass.
func (m *Module) Start(context.Context) error {
	if m.log == nil {
		return nil
	}
	if m.data == nil {
		m.log.Warn("compliance: started without a data handle; evidence packages, risk classifications and residency will not persist")
	}
	if _, ok := m.autonomy.(coreAutonomy); ok {
		m.log.Info("compliance: no autonomy source wired; risk uses observed edges + findings only")
	}
	if _, ok := m.gate.(denyApprovalGate); ok {
		// DENIED is a refusal, so this is the WARN side of the rule — and it was the
		// outlier: deploy, orchestration and voice already warn for the very same
		// "no approval gate wired" predicate. Consistency, not noise reduction.
		m.log.Warn("compliance: no approval gate wired; enabling purge dispositions and releasing legal holds are denied (deny-closed)")
	}
	if m.provider == nil {
		m.log.Info("compliance: no provider-retention source wired; classes report provider_floor_known=false")
	}
	if _, ok := m.accounts.(notWiredAccountEraser); ok {
		m.log.Info("compliance: no account eraser wired; erasure receipts will report the account leg as not_attempted")
	}
	if _, ok := m.providerEraser.(notWiredProviderEraser); ok {
		m.log.Info("compliance: no provider eraser wired; erasure receipts will report the provider leg as not_wired")
	}
	if !m.fileEraser.Wired() {
		m.log.Info("compliance: no Files-store eraser wired; the Claude Files inventory + governed delete are inert and the RTBF disclosure leg is skipped")
	}
	if m.shredCoordinator == nil {
		m.log.Info("compliance: no enterprise RTBF shred coordinator wired; erasure uses open-core crypto-shredding only")
	}
	if m.profileResolver == nil {
		m.log.Info("compliance: no OSCAL profile resolver wired (enterprise add-on absent); profile/SSP ingestion returns 501 and the OSCAL export stays include-all")
	}
	if m.governor == nil {
		m.log.Info("compliance: no retention governor wired (enterprise add-on absent); no regulatory retention floor is enforced and schedules may be freely relaxed/deleted")
	}
	if m.regPackager == nil {
		m.log.Info("compliance: no regulatory packager wired (enterprise add-on absent); DORA Register-of-Information generation and major-incident classification return 501 (the open ICT-risk view GET /dora is unchanged)")
	}
	if m.poamBuilder == nil {
		m.log.Info("compliance: no OSCAL POA&M builder wired (enterprise add-on absent); the evidence OSCAL export emits its three models without a plan-of-action-and-milestones")
	}
	if m.aimsPackager == nil {
		m.log.Info("compliance: no AIMS packager wired (enterprise add-on absent); ISO/IEC 42001 certification-readiness pack generation returns 501 (the open iso_42001 catalog, evidence engine and risk classifier are unchanged)")
	}
	if m.nis2Packager == nil {
		m.log.Info("compliance: no NIS2 incident packager wired (enterprise add-on absent); NIS 2 significant-incident classification returns 501 (the open nis2 catalog and calendar are unchanged)")
	}
	return nil
}

// Stop is a no-op (no background work, no subscription); idempotent.
func (m *Module) Stop(context.Context) error { return nil }

// APINamespace returns the module's namespace; it roots routes at /v1/m/compliance/.
func (m *Module) APINamespace() string { return Namespace }

// Permissions declares the module's permissions so the built-in roles grant them by
// verb tier.
func (m *Module) Permissions() []auth.Permission {
	return []auth.Permission{
		permFrameworkRead, permEvidenceWrite,
		permRiskRead, permRiskWrite, permRiskAdmin,
		permResidencyRead, permResidencyWrite,
		permRetentionRead, permRetentionAdmin,
		permHoldRead, permHoldAdmin,
		permErasureRead, permErasureAdmin,
		permOscalRead, permOscalAdmin,
		permDoraRead, permDoraAdmin,
		permAimsRead, permAimsAdmin,
		permDepthRead, permDepthAdmin,
		permCCMRead, permCCMAdmin,
		permNIS2Read, permNIS2Admin,
	}
}

// APIRoutes mounts the module's routes. The engine wraps each with authentication,
// tenant resolution and the declared permission check; the privileged actions (seal a
// package, read/export a sealed package, classify/review risk, attest/scan residency)
// additionally SELF-AUDIT in the caller's transaction (docs/SECURITY-HARDENING.md).
func (m *Module) APIRoutes(reg api.RouteRegistrar) {
	// Catalog + live assessment (read-tier; the in-repo frameworks and the on-read
	// status/gap analysis — derived aggregates, not raw ledger evidence).
	reg.Handle("GET", "/frameworks", permFrameworkRead, m.handleListFrameworks)
	reg.Handle("GET", "/frameworks/{id}", permFrameworkRead, m.handleGetFramework)
	reg.Handle("GET", "/frameworks/{id}/status", permFrameworkRead, m.handleFrameworkStatus)
	reg.Handle("GET", "/frameworks/{id}/gaps", permFrameworkRead, m.handleFrameworkGaps)
	reg.Handle("GET", "/capabilities", permFrameworkRead, m.handleCapabilities)
	reg.Handle("GET", "/summary", permFrameworkRead, m.handleSummary)
	reg.Handle("GET", "/hipaa/gap-report", permFrameworkRead, m.handleHIPAAGapReport)

	// the regulatory calendar + watchlist (verified dates as data, read-tier)
	// and the DORA ICT-risk-compatible export. The export ships the tenant's risk
	// register, so it is gated by the RISK permission (not framework:read — gating it
	// lower would bypass the module's own permission split and hide the read from an
	// ABAC policy keyed on the "risk" resource), and it self-audits like the evidence
	// export (sensitive evidence read).
	reg.Handle("GET", "/calendar", permFrameworkRead, m.handleCalendar)
	reg.Handle("GET", "/dora", permRiskRead, m.handleDORAExport)

	// Evidence packages: SEALING one is the privileged measurement action (write-tier,
	// audited). Reading/exporting a sealed package is a sensitive evidence read that
	// self-audits (docs/SECURITY-HARDENING.md); the list of package metadata is plain read-tier.
	reg.Handle("POST", "/frameworks/{id}/evidence", permEvidenceWrite, m.handleSealEvidence)
	reg.Handle("GET", "/evidence", permFrameworkRead, m.handleListEvidence)
	reg.Handle("GET", "/evidence/{id}", permFrameworkRead, m.handleGetEvidence)
	reg.Handle("GET", "/evidence/{id}/export", permFrameworkRead, m.handleExportEvidence)

	// OSCAL profile/SSP ingestion (closed-loop GRC). Registering an
	// operator-supplied OSCAL profile/catalog/SSP scopes the assessment-results export
	// to its resolved control selection and references it back. Register/unregister are
	// admin-tier governance acts (and deny-closed: 501 without the enterprise resolver);
	// listing/reading a registered selection is read-tier.
	reg.Handle("POST", "/oscal/profiles", permOscalAdmin, m.handleRegisterOSCALProfile)
	reg.Handle("GET", "/oscal/profiles", permOscalRead, m.handleListOSCALProfiles)
	reg.Handle("GET", "/oscal/profiles/{id}", permOscalRead, m.handleGetOSCALProfile)
	reg.Handle("DELETE", "/oscal/profiles/{id}", permOscalAdmin, m.handleDeleteOSCALProfile)

	// the DORA named-regulation depth plane (enterprise/doraregister). GENERATING the
	// Register of Information (Commission Implementing Regulation (EU) 2024/2956) and
	// CLASSIFYING a major incident (RTS (EU) 2024/1772) are admin-tier governance acts and
	// deny-closed (501 without the enterprise packager); reading/exporting the maintained
	// register and the classifications is read-tier and self-audits (sensitive evidence read).
	// The open dora.go ICT-risk view (GET /dora) above is untouched (no rug-pull).
	reg.Handle("POST", "/dora/register", permDoraAdmin, m.handleGenerateDORARegister)
	reg.Handle("GET", "/dora/register", permDoraRead, m.handleListDORARegisters)
	reg.Handle("GET", "/dora/register/{id}", permDoraRead, m.handleGetDORARegister)
	reg.Handle("GET", "/dora/register/{id}/export", permDoraRead, m.handleExportDORARegister)
	reg.Handle("DELETE", "/dora/register/{id}", permDoraAdmin, m.handleDeleteDORARegister)
	reg.Handle("POST", "/dora/incidents", permDoraAdmin, m.handleClassifyIncident)
	reg.Handle("GET", "/dora/incidents", permDoraRead, m.handleListIncidents)
	reg.Handle("GET", "/dora/incidents/{id}", permDoraRead, m.handleGetIncident)
	reg.Handle("GET", "/dora/incidents/{id}/report", permDoraRead, m.handleExportIncidentReport)
	reg.Handle("DELETE", "/dora/incidents/{id}", permDoraAdmin, m.handleDeleteIncident)

	// the ISO/IEC 42001 AIMS certification-readiness pack (enterprise/iso42001).
	// GENERATING a pack (structuring the live assessment + operator context into SoA/policy/
	// risk-register/impact-assessments/lifecycle/supplier governance) is admin-tier and
	// deny-closed (501 without the enterprise packager); reading/exporting the maintained
	// pack is read-tier and self-audits (sensitive evidence read). The open catalog iso_42001
	// (GET /frameworks/iso_42001), the evidence engine and the risk classifier are untouched
	// (no rug-pull).
	reg.Handle("POST", "/aims/pack", permAimsAdmin, m.handleGenerateAIMSPack)
	reg.Handle("GET", "/aims/pack", permAimsRead, m.handleListAIMSPacks)
	reg.Handle("GET", "/aims/pack/{id}", permAimsRead, m.handleGetAIMSPack)
	reg.Handle("GET", "/aims/pack/{id}/export", permAimsRead, m.handleExportAIMSPack)
	reg.Handle("DELETE", "/aims/pack/{id}", permAimsAdmin, m.handleDeleteAIMSPack)

	// the compliance-depth plane (enterprise/compliancedepth). US state AI law packs,
	// sector-overlay packs, CCM snapshots/drift and FedRAMP KSIs. GENERATING is admin-tier
	// and deny-closed (501 without the enterprise depth packager); reading/exporting is
	// read-tier and self-audits (sensitive evidence read). The open catalog/calendar/evidence
	// surfaces are untouched (no rug-pull).
	reg.Handle("POST", "/depth/us-law", permDepthAdmin, m.handleGenerateUSStatePack)
	reg.Handle("GET", "/depth/us-law", permDepthRead, m.handleListUSStatePacks)
	reg.Handle("GET", "/depth/us-law/{id}", permDepthRead, m.handleGetUSStatePack)
	reg.Handle("GET", "/depth/us-law/{id}/export", permDepthRead, m.handleExportUSStatePack)
	reg.Handle("DELETE", "/depth/us-law/{id}", permDepthAdmin, m.handleDeleteUSStatePack)
	reg.Handle("POST", "/depth/sector", permDepthAdmin, m.handleGenerateSectorPack)
	reg.Handle("GET", "/depth/sector", permDepthRead, m.handleListSectorPacks)
	reg.Handle("GET", "/depth/sector/{id}", permDepthRead, m.handleGetSectorPack)
	reg.Handle("GET", "/depth/sector/{id}/export", permDepthRead, m.handleExportSectorPack)
	reg.Handle("DELETE", "/depth/sector/{id}", permDepthAdmin, m.handleDeleteSectorPack)
	reg.Handle("POST", "/depth/ccm/snapshot", permCCMAdmin, m.handleTriggerCCMSnapshot)
	reg.Handle("GET", "/depth/ccm/snapshots", permCCMRead, m.handleListCCMSnapshots)
	reg.Handle("GET", "/depth/ccm/snapshots/{id}", permCCMRead, m.handleGetCCMSnapshot)
	reg.Handle("POST", "/depth/ccm/drift", permCCMAdmin, m.handleDetectDrift)
	reg.Handle("GET", "/depth/ccm/drift", permCCMRead, m.handleListDriftFindings)
	reg.Handle("POST", "/depth/fedramp", permDepthAdmin, m.handleGenerateFedRAMPKSIs)
	reg.Handle("GET", "/depth/fedramp", permDepthRead, m.handleListFedRAMPKSIs)
	reg.Handle("GET", "/depth/fedramp/{id}", permDepthRead, m.handleGetFedRAMPKSI)
	reg.Handle("GET", "/depth/fedramp/{id}/export", permDepthRead, m.handleExportFedRAMPKSI)
	reg.Handle("DELETE", "/depth/fedramp/{id}", permDepthAdmin, m.handleDeleteFedRAMPKSI)

	// the NIS 2 Directive significant-incident classification plane
	// (enterprise/nis2incident). CLASSIFYING an incident (applying Art 23(3) criteria to
	// operator-supplied impact data) is admin-tier and deny-closed (501 without the
	// enterprise packager); reading/exporting the maintained classifications is read-tier
	// and self-audits (sensitive evidence read). The open nis2 catalog and calendar are
	// untouched (no rug-pull).
	reg.Handle("POST", "/nis2/incidents/classify", permNIS2Admin, m.handleClassifyNIS2Incident)
	reg.Handle("GET", "/nis2/incidents", permNIS2Read, m.handleListNIS2Incidents)
	reg.Handle("GET", "/nis2/incidents/{id}", permNIS2Read, m.handleGetNIS2Incident)
	reg.Handle("PUT", "/nis2/incidents/{id}", permNIS2Admin, m.handleUpdateNIS2Incident)
	reg.Handle("GET", "/nis2/incidents/{id}/export", permNIS2Read, m.handleExportNIS2Incident)
	reg.Handle("DELETE", "/nis2/incidents/{id}", permNIS2Admin, m.handleDeleteNIS2Incident)

	// Agent risk classification (EU AI Act / NIST AI RMF). Classifying is write-tier
	// and audited; reviewing (approve/override) is the admin-tier decision surface.
	reg.Handle("GET", "/risk", permRiskRead, m.handleListRisk)
	reg.Handle("POST", "/risk/classify", permRiskWrite, m.handleClassifyRisk)
	reg.Handle("POST", "/risk/{id}/review", permRiskAdmin, m.handleReviewRisk)

	// Data residency (self-hosted = data does not leave the perimeter). Attesting and
	// scanning are write-tier and audited; listing is read-tier.
	// the governed Claude Files plane: an observe inventory (read-tier) over the
	// persistent, non-ZDR Files store, and a CRITICAL dual-control, hold-gated point DELETE
	// (admin-tier). Erasure permissions reused — a file delete IS an erasure action.
	reg.Handle("GET", "/claude-files", permErasureRead, m.handleFilesInventory)
	reg.Handle("POST", "/claude-files/{id}/erase", permErasureAdmin, m.handleEraseFile)

	reg.Handle("GET", "/residency", permResidencyRead, m.handleListResidency)
	reg.Handle("POST", "/residency", permResidencyWrite, m.handleAttestResidency)
	reg.Handle("POST", "/residency/scan", permResidencyWrite, m.handleScanResidency)

	// Retention. Reading the registry/schedules/certificates is read-tier;
	// authoring a schedule and sweeping are admin-tier and self-audited — and enabling
	// disposition=purge additionally passes the approval gate (≥1 human, SoD).
	reg.Handle("GET", "/retention/classes", permRetentionRead, m.handleRetentionClasses)
	reg.Handle("GET", "/retention/policies", permRetentionRead, m.handleListRetentionPolicies)
	reg.Handle("PUT", "/retention/policies/{class}", permRetentionAdmin, m.handlePutRetentionPolicy)
	reg.Handle("DELETE", "/retention/policies/{class}", permRetentionAdmin, m.handleDeleteRetentionPolicy)
	reg.Handle("POST", "/retention/sweep", permRetentionAdmin, m.handleRetentionSweep)
	reg.Handle("GET", "/retention/runs", permRetentionRead, m.handleListRetentionRuns)

	// Legal holds. SETTING a hold is admin-tier but ungated (preservation is the
	// safe direction and must be immediate — duty to preserve); RELEASING one is the
	// dangerous verb: CRITICAL, dual-control (≥2 distinct humans, re-verified here),
	// no break-glass. /holds/check is the hold-gate HTTP face consumes.
	reg.Handle("POST", "/holds", permHoldAdmin, m.handleCreateHold)
	reg.Handle("GET", "/holds", permHoldRead, m.handleListHolds)
	reg.Handle("GET", "/holds/check", permHoldRead, m.handleCheckHoldHTTP)
	reg.Handle("GET", "/holds/{id}", permHoldRead, m.handleGetHold)
	reg.Handle("GET", "/holds/{id}/events", permHoldRead, m.handleListHoldEvents)
	reg.Handle("POST", "/holds/{id}/release", permHoldAdmin, m.handleReleaseHold)

	// Right-to-erasure. Registering a DSR and EXECUTING it are
	// admin-tier; the execute additionally clears TWO independent deny-closed gates
	// in fixed order — the legal-hold gate (423 under any covering hold), then
	// the CRITICAL dual-control approval "compliance.subject.erase" (two distinct
	// humans, no break-glass). Requests, custody and receipts are read-tier.
	reg.Handle("POST", "/erasure", permErasureAdmin, m.handleCreateErasure)
	reg.Handle("GET", "/erasure", permErasureRead, m.handleListErasure)
	reg.Handle("GET", "/erasure/{id}", permErasureRead, m.handleGetErasure)
	reg.Handle("GET", "/erasure/{id}/events", permErasureRead, m.handleListErasureEvents)
	reg.Handle("GET", "/erasure/{id}/receipt", permErasureRead, m.handleGetErasureReceipt)
	reg.Handle("POST", "/erasure/{id}/execute", permErasureAdmin, m.handleExecuteErasure)
	reg.Handle("POST", "/data-subjects/{id}/erase", permErasureAdmin, m.handleDataSubjectErase)
	reg.Handle("GET", "/data-subjects/{id}/erasure-status", permErasureRead, m.handleDataSubjectErasureStatus)
}

func (m *Module) debugf(msg string, args ...any) {
	if m.log != nil {
		m.log.Debug(msg, args...)
	}
}
