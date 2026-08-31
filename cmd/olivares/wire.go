// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	mcpc "github.com/olivaresai/olivares/connectors/mcp"
	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/audit"
	"github.com/olivaresai/olivares/core/model"

	accessmap "github.com/olivaresai/olivares/modules/access-map"
	"github.com/olivaresai/olivares/modules/capabilities"
	"github.com/olivaresai/olivares/modules/catalog"
	"github.com/olivaresai/olivares/modules/claudeadoption"
	"github.com/olivaresai/olivares/modules/compliance"
	"github.com/olivaresai/olivares/modules/consoleviews"
	"github.com/olivaresai/olivares/modules/deploy"
	"github.com/olivaresai/olivares/modules/evals"
	"github.com/olivaresai/olivares/modules/eventing"
	"github.com/olivaresai/olivares/modules/finops"
	"github.com/olivaresai/olivares/modules/governance"
	"github.com/olivaresai/olivares/modules/health"
	"github.com/olivaresai/olivares/modules/inferenceproxy"
	"github.com/olivaresai/olivares/modules/inventory"
	"github.com/olivaresai/olivares/modules/knowledge"
	"github.com/olivaresai/olivares/modules/liveingest"
	"github.com/olivaresai/olivares/modules/models"
	"github.com/olivaresai/olivares/modules/notify"
	"github.com/olivaresai/olivares/modules/observability"
	"github.com/olivaresai/olivares/modules/orchestration"
	postureexport "github.com/olivaresai/olivares/modules/posture-export"
	"github.com/olivaresai/olivares/modules/recording"
	"github.com/olivaresai/olivares/modules/redteam"
	"github.com/olivaresai/olivares/modules/reporting"
	"github.com/olivaresai/olivares/modules/sandbox"
	"github.com/olivaresai/olivares/modules/security"
	"github.com/olivaresai/olivares/modules/sessions"
	"github.com/olivaresai/olivares/modules/siemforward"
	"github.com/olivaresai/olivares/modules/sourcescope"
	"github.com/olivaresai/olivares/modules/voice"
)

// This file is the wiring half of the composition root: the ONLY place that imports
// every product module. It constructs the Fase C module set and wires the
// inter-module seam adapters that EXIST today; every other seam keeps its honest,
// fail-closed default (each module warns once per un-wired seam in Start()).
//
// Wired today (real adapters):
//   - governance ABAC -> the core authorizer (set.gov.Evaluator() in boot.go).
//   - sandbox.Scorer  -> evals.ScoreOutputs (the XII<->XVII integration, below).
//   - security checkpoint key -> the audit signer's public key.
//   - the FinOps pre-flight budget gate (FIN-08) -> finops.CheckBudget, wired into
//     the orchestration fire / voice open actuation seams AND the model-router resolve
//     (budgetgate.go). ALWAYS wired (FinOps is in-process, no operator config): an
//     enforcing budget at its cap now DENIES the spend, not just emits a finding. Opt-in
//     (no enforcing budget => never denies) and fail-open on a FinOps read error.
//
// Wired on demand from env (CLA-17-A; fail-closed when unconfigured):
//   - evals.Judge -> the Claude Messages API (claudeJudgeAdapter); unwired => llm_judge
//     stays SKIPPED (offlineJudge), never a false pass.
//   - knowledge.Embedder -> a model-backed embeddings provider (claudeEmbedderAdapter,
//     egressing); unwired => the zero-egress LocalHashEmbedder. With
//     OLIVARES_EMBEDDINGS_REQUIRE the boot REFUSES to start unconfigured rather than
//     serve lexical vectors as semantic. See claude_inference.go.
//   - knowledge.VectorIndex -> a production ANN backend (connectors/vectorindex:
//     pgvector/Qdrant) wired when OLIVARES_VECTOR_BACKEND+DSN are set; unwired => the
//     in-process exact cosineIndex (the air-gap default). A configured-but-down backend
//     fails retrieval deny-closed (no silent fallback). See knowledgevector.go.
//   - the approval gates (deploy/orchestration/voice/security) -> the governance
//     engine, via the OUTBOUND ApprovalGate→HITL bridge (approvalbridge.go). The
//     bridge resolves the original caveat — governance exposes no in-process Go
//     approval API, only HTTP routes — by PROPOSING governed approvals over the
//     engine's own handler in-process (the mirror of hitl.go's inbound apiDecider). It
//     never decides: SoD / duplicate-decider / threshold / expiry stay enforced by
//     on the inbound decision. Unwired (no OLIVARES_APPROVAL_BRIDGE_CONFIG) => each
//     module keeps its deny-closed default (denyGate) and warns once; even wired, every
//     edge is deny-closed (a new pending approval reports DENY/"ask"). See
//     loadApprovalBridgeConfig.
//   - the deploy executor and the orchestration/voice dispatchers -> the
//     real, governed actuation engine (core/runtime/executor) and the A2A/voice
//     connectors. The orchestration runtime fire route REUSES the same executor engine
//     the deploy module acts through (shared, never re-implemented). Unwired (no
//     OLIVARES_DEPLOY_EXECUTOR_CONFIG / OLIVARES_ORCH_DISPATCH_CONFIG /
//     OLIVARES_VOICE_DISPATCH_CONFIG) => each module keeps its deny-closed default and
//     an approved apply/fire/open is honestly "declared, not actuated". See
//     deployexec_load.go, orchdispatch_load.go, voicedispatch_load.go.
//   - sandbox.Runner (XVII) & redteam.Sandbox (XVIII) -> the real, isolated,
//     egress-controlled execution runtime (core/runtime/sandboxrt): a hardened,
//     ephemeral, attested instance behind a deny-by-default egress proxy, with
//     pluggable gVisor/Firecracker backends selected by policy and gated by a
//     preflight (closes gap 943). Wired ONLY when an operator provisions it
//     (OLIVARES_SANDBOX_RUNTIME_CONFIG). Unwired => the modules keep their honest
//     defaults: the in-proc-mock runner (isolated by construction, synthetic-only)
//     and the offline sandbox (red-team runs DEGRADED). A configured-but-host-
//     incapable runtime fails CLOSED per run (ErrNoIsolation), never a faked
//     microVM. See sandboxrt.go, sandboxrt_load.go.
//
// Always wired (in-process, no operator config):
//   - knowledge.RetrievalGuard -> the governanceRetrievalGuard: resolves an
//     agent's groups/clearance/region from so retrieval governs by real grants,
//     not just "public". Fail-closed to public on any unresolvable subject/error. Its
//     store handle is late-bound by boot() (knowledgeGuard.useData). See knowledgeguard.go.
//   - the claude-policy TRUTH LOOP: the distribution seam -> the signed-
//     artifact PolicyArtifactDistributor (the plane's policy signing key, minted/shared
//     like the catalog key; deny-closed — publish reports "distributed" only after the
//     signed record committed) and the OBSERVED-config seam -> the check-in-backed
//     PolicyObservedStore. Both store handles are late-bound by boot(). The agents
//     console's ThreadEventProvider is wired from the configured claude-managed-agents
//     sources (a dedicated request-time reader per tenant — the claude-api pattern);
//     none configured ⇒ the console keeps its honest empty. See claudetruth.go.
//   - evals.SessionSource & sandbox.HistorySource -> the module-II (sessions) live
//     read-model (sessionadapters.go). The monitor samples REAL sessions
//     (derived cc_state, live tokens/cost, core findings joined by the external-UUID
//     ref convention) within a short configurable window
//     (OLIVARES_EVALS_MONITOR_WINDOW, default 24h); replay/compare reconstruct the
//     ordered tool/mcp action sequence from sessions.timeline. Module II is bus-fed
//     and far richer than the core discovery stubs (whose State never leaves
//     "running" and whose finding join never matches a live external ref); the one
//     coverage trade: core sessions inventory materializes from RESOURCE-side CMA
//     edges have no sessions.live row, so the monitor no longer scores those
//     perpetual "running" stubs. Zero rows stays an honest empty sample / DEGRADED
//     replay, never fabricated.
//
// Wired on demand (operator config, no code change):
//   - knowledge.Source: the `documents` section of OLIVARES_SOURCES_CONFIG wires
//     contentsource connectors (gdrive/confluence/notion/sharepoint/s3content) via
//     knowledge.WithSource. See sources.go (knowledgeContentOptions,
//     buildContentSource). No documents configured => the module has no pull sources.

// moduleSet is the constructed Fase C module set plus the handles the composition
// root needs after construction. all is every module (each satisfies api.Module,
// sdk.Module and api.DataConsumer); gov's evaluator backs the core authorizer;
// approvalBridge (nil unless configured) is the OUTBOUND ApprovalGate adapter
// whose engine handler boot() late-binds after api.New (it cannot exist yet here).
type moduleSet struct {
	all []api.Module
	gov *governance.Module
	// the identity console, held so boot() can late-bind the SSO posture source
	// once the federation service exists (it is built after the module set).
	identityConsole *governance.IdentityConsole
	approvalBridge  *approvalBridge
	// connectors constructed here (before the store) whose secret-bearing
	// config is resolved and opened by boot()'s deferredSecrets.openAll once the
	// store — and thus a `store:` reference — exists (the late-bind pattern).
	deferredSecrets *deferredSecretWiring
	// compliance is the records-management module instance: the retention
	// sweep loop (retentionsweep.go) calls its exported RunRetention per tenant,
	// and the knowledge hold-gate adapter wraps its CheckHold.
	compliance *compliance.Module
	// the firm IdentityBinder and the drift loop, both late-bound to the engine
	// handler by boot() after api.New (nil when unconfigured).
	deployBinder *deployIdentityBinder
	deployDrift  *deployDriftLoop
	// the knowledge RetrievalGuard, late-bound to the store handle by boot()
	// after api.NewModuleData (always present); vectorIndex is the optional ANN backend
	// adapter (nil unless OLIVARES_VECTOR_BACKEND is configured) kept for Close().
	knowledgeGuard *governanceRetrievalGuard
	vectorIndex    *vectorIndexAdapter
	// liveingest (module XXIV) is the in-process publisher of runtime cost/forensic
	// (CLA-15/ANT2-15) and detective events. Kept here so the models routing-execute
	// executor can share it as its cost sink, the same instance the judge
	// adapter already emits through.
	live *liveingest.Module
	// the WIF-graph adapter the identity console reads, populated by wireRoster
	// from the configured claude-wif sources (operator-declared federation rules).
	wif *wifGraphAdapter
	// the in-process WIF credential broker (sessions + executor planes). Kept so
	// boot() can Close its lazily-dialed SPIRE Workload API connection on shutdown.
	wifBroker *wifCredentialBroker
	// the privileged-session recorder, handed to api.Options.Recorder by
	// boot() (the engine wraps every module route through it).
	recorder *recording.Module
	// the RTBF account-anonymization adapter into the auth partition,
	// late-bound to the store handle by boot() (the knowledgeGuard pattern).
	accountEraser *accountEraserAdapter
	// the eventing platform, kept so boot() can late-bind its authorizer
	// (built after the modules — the ABAC evaluator comes from governance) and
	// its secret sealer (the key lives in the data dir), and so the dispatch
	// pump (eventingpump.go) can call its tenant-scoped DispatchDue/PruneExpired.
	eventing *eventing.Module
	// the orchestration module, kept so the leader-gated cadence pump
	// (orchcadencepump.go) can call its tenant-scoped RunCadenceScan per
	// business tenant — unattended cadence-miss detection instead of the
	// read-time-only piggyback.
	orchestration *orchestration.Module
	// module XV (notify), kept so the leader-gated durable-outbox pump
	// (notifypump.go) can call its tenant-scoped NotifyDispatchDue — the retry/DLQ
	// driver that delivers enqueued notifications out of band of the bus handler.
	notify *notify.Module
	// the SIEM ledger forwarder, kept so the leader-gated ledger-forward pump
	// (ledgerforwardpump.go) can call its tenant-scoped ForwardDue per business
	// tenant — the at-least-once driver that walks the ledger into the eventing
	// engine for SIEM control-tower delivery.
	siemforward *siemforward.Module
	// the policy truth loop's store-backed seams, late-bound to the data
	// handle by boot() (the knowledgeGuard pattern). policyDist is nil when no
	// policy signing key was provided (e.g. the e2e harness) — the console then
	// keeps the honest seam-pending posture; policyObserved is always present.
	policyDist     *governance.PolicyArtifactDistributor
	policyObserved *governance.PolicyObservedStore
	// module II (sessions) and FinOps, kept so boot() can late-bind the OPERATE
	// governance (the kill-switch StopGate, the budget/HITL/PEP LaunchGate, the I/O
	// Recorder, the active-termination sweep) once the store / bridge are live — module
	// II is constructed before those dependencies, so its governance gates late-bind.
	sessions                  *sessions.Module
	protocolBindingReconciler *protocolBindingReconcileMux
	finops                    *finops.Module
	// module X (models) and the inference-proxy governance module, kept so boot()
	// can hand them (with the residency registry, FinOps and the killswitch) to the
	// inline inference PEP server built post-boot (the ANTHROPIC_BASE_URL gateway
	// already wires an env-ref for — this is the listener it points at).
	models         *models.Module
	inferenceProxy *inferenceproxy.Module
	// the FinOps→Anthropic-org upstream-cap backstop (nil unless opt-in + provisioned
	// via OLIVARES_CLAUDE_ADMIN_ACTUATOR_CONFIG). boot() subscribes it to the bus after the
	// approval bridge's handler is bound (its governed actuator gates through that bridge).
	finopsBackstop *finopsBackstop
	// the knowledge module, kept so the MCP retrieval upstream can invoke its
	// programmatic Query/FetchDocument/ListKBs API in-process (the governed RAG
	// exposed as MCP tools). The module is already in the `all` slice; this is the
	// typed reference the composition root needs.
	knowledge           *knowledge.Module
	knowledgeStatus     *knowledgePlaneStatus
	knowledgeEmbedder   *governedKnowledgeEmbedder
	sourceScopeResolver *sourcescope.Resolver
	// the enterprise RTBF crypto-shred coordinator (nil / typed-nil in the
	// default build). Kept as `any` — the AGPL root never imports enterprise/rtbf —
	// so boot() can hand it to bindCryptoShredPorts once the compliance module and
	// the audit archive sink exist (its evidence ports: legal holds + WORM sinks).
	rtbfCoordinator any
	// the shared tool-pin verifier (nil in the community build). boot()
	// threads it to the MCP gateway PEP so the reporting tool-pin source and the
	// gateway enforce over the SAME store.
	pinVerifier mcpc.ToolPinVerifier
	// the reporting module, held so boot() registers the schedule pump.
	reporting *reporting.Module
}

// buildModules constructs all Fase C modules and wires the inter-module adapters that
// exist today. signer provides the audit public key the security module verifies
// checkpoints against; catalogSigner is the catalog's INDEPENDENT Ed25519 artifact-
// signing key (loaded fail-closed at boot), injected into module XIV so approved
// entries ship SIGNED by default (nil = the honest unsigned/unpinned posture);
// inferenceDoer is the trace-instrumented HTTP transport for the engine→Claude hop
// (OBS-03; nil = untraced default); log backs the connector-dispatcher provisioning
// warnings.
func buildModules(signer *audit.Signer, catalogSigner, policySigner ed25519.PrivateKey, auditPriors []ed25519.PublicKey, inferenceDoer modelprovider.Doer, srcCfg sourcesConfig, log *slog.Logger) (moduleSet, error) {
	sandboxCfg, err := loadSandboxRuntimeConfig(log)
	if err != nil {
		return moduleSet{}, err
	}
	approvalCfg, err := loadApprovalBridgeConfig(log)
	if err != nil {
		return moduleSet{}, err
	}
	nhiCfg, err := loadNHIActuatorsConfig(log)
	if err != nil {
		return moduleSet{}, err
	}
	eraserCfg, err := loadClaudeEraserConfig(log)
	if err != nil {
		return moduleSet{}, err
	}
	filesCfg, err := loadClaudeFilesConfig(log)
	if err != nil {
		return moduleSet{}, err
	}
	deployExecCfg, err := loadDeployExecutorConfig(log)
	if err != nil {
		return moduleSet{}, err
	}
	orchDispatchCfg, err := loadOrchDispatchConfig(log)
	if err != nil {
		return moduleSet{}, err
	}
	voiceDispatchCfg, err := loadVoiceDispatchConfig(log)
	if err != nil {
		return moduleSet{}, err
	}
	voiceCallCfg, err := loadVoiceCallConfig(log)
	if err != nil {
		return moduleSet{}, err
	}
	adminActuatorCfg, err := loadClaudeAdminActuatorConfig(log)
	if err != nil {
		return moduleSet{}, err
	}
	notifyDestinations, err := loadNotifyDestinations(log)
	if err != nil {
		return moduleSet{}, err
	}
	agentCoreCfg, err := loadAgentCoreExportConfig(osGetenv, log)
	if err != nil {
		return moduleSet{}, err
	}

	govOpts, err := loadGovernanceOptions(osGetenv, log)
	if err != nil {
		return moduleSet{}, err
	}
	gov := governance.New(govOpts...)
	// the source-scoping plane. It owns the source→workspace/agent-group binding
	// table and resolves, at the point an agent/session resolves a source, whether the
	// actor is in scope (containment ∨ the grant engine ∨ tenant RBAC; a
	// scoped forbid overrides) and which scoped credential reference applies. The
	// resolver is injected into the models ScopeGate and the knowledge RetrievalScopeGate
	// below; its store handle is late-bound by boot() (UseData), exactly like the
	// scoped-grant engine it consults (gov.ScopedGrants()).
	ss := sourcescope.New(sourcescope.WithScopedAuthorizer(gov.ScopedGrants()))
	ssResolver := ss.Resolver()
	// CLA-17-A: resolve the Claude inference seams (Judge / model-backed Embedder)
	// from env. Both stay fail-closed (offline judge / zero-egress local embedder) when
	// no credential is configured — never a false pass, never a silent egress.
	ci := loadClaudeInference(osGetenv, inferenceDoer, log)
	// the WIF-graph adapter — the identity console reads it AND the in-process WIF
	// credential broker resolves per-tenant federation rules from it. Created HERE (before the
	// sessions runtime and the deploy executor the broker feeds) though it is POPULATED later
	// by wireRoster as it Opens the claude-wif sources; the broker resolves lazily at mint
	// time (a launch/actuation), long after boot, so the timing is fine.
	wifAdapter := newWifGraphAdapter(log)
	// the in-process WIF credential broker mints short-lived sk-ant-oat tokens for BOTH
	// the sessions plane (governed Claude Code launches) and the executor plane (deploy/sandbox
	// actuation), replacing the static token-in-file. Constructed unconditionally (no I/O until
	// the first mint dials SPIRE); the per-plane adapters gate use (opt-in, deny-closed). It
	// reuses the trace-instrumented inference transport so the OAuth exchange is traced too.
	wifBroker := newWIFCredentialBroker(osGetenv, wifAdapter, inferenceDoer, log)
	// module II (sessions) is constructed FIRST so its live read-model can back
	// the evals monitor sampling and the sandbox replay timeline through the
	// composition-root adapters (sessionadapters.go). Construction order carries no
	// data dependency: every module receives the same store handle later via the
	// generic UseData loop in boot().
	// (FASE V): module II also OPERATES governed Claude Code sessions. The
	// native streaming runner is always wired; the inference credential source is
	// wired only when configured (else stream-json launches are deny-closed). The
	// governance gates are late-bound.
	sm := sessions.New(buildSessionRuntimeOptions(osGetenv, wifBroker, log)...)
	// II→XII: the monitor samples REAL sessions from the module-II live read-model
	// within a short configurable recency window (OLIVARES_EVALS_MONITOR_WINDOW).
	evOpts := append([]evals.Option{}, ci.evalsJudgeOptions()...)
	evOpts = append(evOpts, evals.WithSessionSource(sessionsSampleAdapter{
		ss: sm, window: loadEvalsMonitorWindow(osGetenv, log), log: log,
	}))
	// the regression gate's budget pre-flight over the CI's own judge
	// spend. FinOps is constructed further down, so the adapter is LATE-BOUND
	// (evBudget.bind(fin) below); until then it allows — the same fail-open posture
	// as the sibling budget gates in budgetgate.go.
	evBudget := &evalsBudgetGate{log: log}
	evOpts = append(evOpts, evals.WithBudgetGate(evBudget))
	ev := evals.New(evOpts...)

	// module VIII's governed RAG defaults. The RetrievalGuard bridges so
	// retrieval resolves REAL grants (groups/clearance/region), not just "public"; its
	// store handle is late-bound by boot() (knowledgeGuard.useData). The VectorIndex is
	// a production ANN backend (pgvector/Qdrant) wired only when OLIVARES_VECTOR_BACKEND
	// is configured — otherwise the module keeps its in-process exact cosineIndex (the
	// air-gap default). The model-backed embedder is wired later, after Module X exists,
	// so /cmd can bridge it through model-access without coupling knowledge to
	// models; boot() already refused to start if the operator REQUIRED it but left it
	// unconfigured (checkEmbeddingsRequirement).
	knowledgeGuard := newGovernanceRetrievalGuard(log)
	knowledgeGuard.useGuardPostureResolver(ssResolver)
	knowledgeOpts := []knowledge.Option{}
	knowledgeOpts = append(knowledgeOpts, knowledge.WithRetrievalGuard(knowledgeGuard))
	// the source-scope gate (orthogonal to the guard's sensitivity/ACL) — is the
	// requesting agent in the KB's workspace/agent-group scope? Deny-closed; an unbound
	// KB is allowed (back-compat).
	knowledgeOpts = append(knowledgeOpts, knowledge.WithRetrievalScopeGate(knowledgeScopeGate{resolver: ssResolver, log: log}))
	vecOpts, vecAdapter := knowledgeVectorOptions(osGetenv, log)
	knowledgeOpts = append(knowledgeOpts, vecOpts...)
	// (B1): wire the configured knowledge DOCUMENT sources (gdrive/confluence/
	// notion/sharepoint/s3content) the module drives on ingest. They are NOT runtime
	// observation sources (no R/RW edge) — they are pulled by the module — so they are
	// wired here at construction, not in wireSources. Deny-closed: none configured ⇒ no
	// pull sources (knowledgeContentOptions warns per honest-posture).
	// the configured document sources are resolved+opened post-store (their
	// credential references can only resolve once the store exists), then registered
	// on the knowledge module (km, below) by deferredSecretWiring.openAll.
	pendingContent := knowledgeContentSources(srcCfg, log)
	// the PII/sensitivity classifier — ALWAYS wired (deterministic
	// default-on), backed by the security module's catalog. Discovery scans,
	// ingest-time labeling and the deny-closed DLP egress gate all key on it.
	knowledgeOpts = append(knowledgeOpts, knowledge.WithSensitivityClassifier(securitySensitivityClassifier{}))
	// B-02: the WRITE-path minimizer, wired from the same catalog as the
	// classifier above. Unwired, the knowledge module falls back to its built-in
	// shapes, which remove credentials and email only — which is how IBANs, cards,
	// SSNs and NIF/NIEs reached the chunk store in the clear while the classifier
	// dutifully reported them. Detection and minimization are one catalog now.
	knowledgeOpts = append(knowledgeOpts, knowledge.WithRedactor(securityRedactor{}))
	// scan each retrieved chunk as UNTRUSTED DATA before returning it to
	// the caller. The CORE scanner runs textscan injection markers (HIGH severity
	// = deny-closed block; LOW/MEDIUM = advisory finding, not blocked —).
	// Enterprise depth (3 deterministic detectors) is nil in the community build.
	knowledgeOpts = append(knowledgeOpts, knowledge.WithRetrievalContentScanner(
		&coreRetrievalScanner{deepScanner: newRetrievalDeepScanner()},
	))
	// the real isolated, egress-controlled execution runtime (core/runtime/
	// sandboxrt) backing BOTH the sandbox.Runner (XVII) and the redteam.Sandbox
	// (XVIII) seams. nil when no operator config (OLIVARES_SANDBOX_RUNTIME_CONFIG) —
	// the modules then keep their honest defaults (in-proc-mock runner / offline
	// sandbox). When wired, gVisor/Firecracker are selected by policy and gated by a
	// preflight; a host without the primitive fails CLOSED, never a faked microVM.
	sbrt := newSandboxRuntime(sandboxCfg, log)

	// XII<->XVII: the sandbox scores its outputs through the evals module (the seam is
	// expressed in the sandbox's own terms; this adapter is the only cross-module glue,
	// and it lives here in the composition root, never in either module's code).
	// II→XVII: replay and the session branch of compare reconstruct a
	// session's ordered tool/mcp action sequence from the module-II timeline.
	sbOpts := []sandbox.Option{
		sandbox.WithScorer(evalsScorerAdapter{ev: ev}),
		sandbox.WithHistorySource(sessionsHistoryAdapter{ss: sm}),
	}
	var redteamOpts []redteam.Option
	if sbrt != nil {
		// XVII: the OS-level isolated runner replaces the in-proc-mock default.
		sbOpts = append(sbOpts, sandbox.WithRunner(sandboxRunnerAdapter{eng: sbrt}))
		// XVIII: the same runtime, with egress scoped to the authorized target, is
		// the red-team execution environment (docs/SECURITY-HARDENING.md RED LINE).
		redteamOpts = append(redteamOpts, redteam.WithSandbox(redteamSandboxAdapter{eng: sbrt}))
	}
	sb := sandbox.New(sbOpts...)

	// the OUTBOUND ApprovalGate bridge. Constructed from the operator config
	// (nil = honest absence; the four modules keep their deny-closed defaults). When
	// present it implements the deploy/orchestration/voice/security gates by proposing
	// governed approvals over the engine's own handler (late-bound by boot() after
	// api.New). It must never be a hard dependency: an un-configured deployment is
	// fully functional and simply denies governed actuation, exactly as before.
	bridge := newApprovalBridge(approvalCfg, log)
	var (
		deployOpts []deploy.Option
		orchOpts   []orchestration.Option
		voiceOpts  []voice.Option
		// IX: the security module verifies audit checkpoints against the engine's key.
		secOpts = []security.Option{
			security.WithCheckpointKey(signer.PublicKey()),
			security.WithEngineVersion(version),
		}
	)
	// with an off-box (KMS/HSM) checkpoint signer wired, the on-box key alone
	// cannot satisfy off-box-signed checkpoints; likewise, after a `keys rotate`
	// the PRIOR on-box generations signed past checkpoints. Hand module IX a lazy
	// multi-candidate verifier covering all of them, so GET /integrity/verify
	// never reports checkpoint-sig-invalid for a healthy custody posture.
	if signer.OffBoxCheckpoints() || len(auditPriors) > 0 {
		secOpts = append(secOpts, security.WithCheckpointVerifierSource(func(ctx context.Context) (*audit.CheckpointVerifier, error) {
			v, err := signer.CheckpointVerifier(ctx)
			if err != nil {
				return nil, err
			}
			for _, p := range auditPriors {
				v.AddEd25519(p)
			}
			return v, nil
		}))
	}
	if bridge != nil {
		deployOpts = append(deployOpts, deploy.WithApprovalGate(bridge.deployGate()))
		orchOpts = append(orchOpts, orchestration.WithApprovalGate(bridge.orchestrationGate()))
		voiceOpts = append(voiceOpts, voice.WithApprovalGate(bridge.voiceGate()))
		secOpts = append(secOpts, security.WithApprovalGate(bridge.securityGate()))
		// NHI lifecycle rotation/offboarding inherit the SAME governed approval
		// path (CRITICAL two-person floor + break-glass), via the module's gate seam.
		gov.UseLifecycleGate(bridge.lifecycleGate())
	}
	// the write-capable NHI lifecycle actuators (Vault/Anthropic), opt-in per
	// tenant. Absent ⇒ the module degrades honestly (manual rotation + coverage finding).
	gov.UseLifecycleActuators(buildNHIActuatorBindings(nhiCfg, log))

	// Records management. The compliance module gets (a) the Covered-Models
	// forced-retention floor (always wired — models is in-process and read-only;
	// §7's annotate-not-reject disclosure on model_io classes) and (b) the
	// dual-control gate for its two dangerous verbs, compliance.retention.enable
	// and compliance.hold.release — BOTH over gateOnceNoBreakGlass (no emergency
	// path lifts a preservation or enables destruction; compliancegate.go). With
	// no bridge the module keeps its deny-closed denyApprovalGate. knowledge gets
	// the hold-gate over the SAME compliance instance (CheckHold), so an active
	// legal hold vetoes KB/memory destruction (423; gate error ⇒ 503, fail closed)
	// — the composition root is the only place that can speak both modules' types.
	compOpts := []compliance.Option{compliance.WithProviderRetention(modelsProviderRetention{})}
	if bridge != nil {
		compOpts = append(compOpts, compliance.WithApprovalGate(bridge.complianceGate()))
	}
	// Right-to-erasure legs. The ACCOUNT eraser is always constructed (its store
	// handle is late-bound by boot(), the knowledgeGuard pattern) — engine users live
	// in the auth partition only this root can reach. The PROVIDER eraser (the
	// Anthropic DELETE passthrough) is wired only when the operator provisioned the
	// delete credential AND the approval bridge exists — its every deletion runs the
	// connector's own dual-control PEP over bridge.eraseGate (CRITICAL
	// "compliance.content.erase", no break-glass). Unwired ⇒ the module keeps its
	// honest not-attempted / not-wired defaults and every receipt records the gap.
	accEraser := &accountEraserAdapter{log: log}
	compOpts = append(compOpts, compliance.WithAccountEraser(accEraser))
	if provEraser := newProviderEraserAdapter(eraserCfg, bridge, log); provEraser != nil {
		compOpts = append(compOpts, compliance.WithProviderEraser(provEraser))
		log.Info("erasure: Provider-side RTBF passthrough wired (dual-control gated)")
	}
	// Governed Claude Files plane: the inventory + the CRITICAL dual-control, hold-gated
	// point DELETE over the persistent Files store. Wired only when the operator provisioned a
	// workspace key (OLIVARES_CLAUDE_FILES_CONFIG); the dual-control gate reuses the compliance
	// ApprovalGate already wired above. Unwired ⇒ the plane is inert + the RTBF leg is skipped.
	if fileEraser := newFileStoreEraserAdapter(filesCfg, log); fileEraser != nil {
		compOpts = append(compOpts, compliance.WithFileStoreEraser(fileEraser))
		log.Info("files: governed Claude Files plane wired (inventory + dual-control delete + RTBF disclosure)")
	}
	// the coordinator is CONSTRUCTED here (compOpts must carry it into
	// compliance.New below) but its evidence ports — the legal-hold checker over
	// this same compliance module and the WORM archive sink — are late-bound by
	// boot() via bindCryptoShredPorts, once both exist. Until the ports bind, the
	// coordinator is deny-closed: readiness blocks and verification reports
	// explicit unverified gaps, never fabricated success.
	rtbfCoord := newCryptoShredCoordinator(osGetenv, log)
	if rtbfCoord != nil {
		compOpts = append(compOpts, compliance.WithCryptoShredCoordinator(rtbfCoord))
		log.Info("compliance: enterprise RTBF crypto-shred coordinator wired")
	}
	// OSCAL profile/SSP ingestion. The resolver is the commercial enterprise add-on,
	// wired only under -tags enterprise (newOscalProfileResolver returns the real resolver
	// there, nil in the default build). nil ⇒ the ingestion endpoint answers 501 and the
	// OSCAL export keeps include-all — no feature removed from the free product.
	if resolver := newOscalProfileResolver(); resolver != nil {
		compOpts = append(compOpts, compliance.WithProfileResolver(resolver))
		log.Info("compliance: OSCAL profile/SSP ingestion wired (governed, deny-closed; scopes the assessment-results export)")
	}
	// Records-vault: the regulatory retention-floor + compliance-mode governor is the
	// commercial enterprise/wormretention add-on, wired only under -tags enterprise
	// (newRetentionGovernor returns the real governor there, nil in the default build). nil
	// ⇒ no floor is enforced and schedules are freely relaxed/deleted — byte-identical to
	// the free product (no feature removed; the floor only ADDS a deny when wired).
	if governor := newRetentionGovernor(osGetenv, log); governor != nil {
		compOpts = append(compOpts, compliance.WithRetentionGovernor(governor))
		log.Info("compliance: retention governor wired (named regulatory floors + compliance-mode lock; enterprise)")
	}
	// Named-regulation depth: the DORA Register-of-Information generator + major-incident
	// classifier (enterprise/doraregister) and the FedRAMP-adjacent OSCAL POA&M builder
	// (enterprise/oscalingest) are wired only under -tags enterprise (newRegulatoryPackager /
	// newPOAMBuilder return the real ones there, nil in the default build). nil ⇒ the
	// /dora/register and /dora/incidents endpoints answer 501 and the evidence OSCAL export
	// keeps its three models — no feature removed from the free product (no rug-pull).
	if pkg := newRegulatoryPackager(); pkg != nil {
		compOpts = append(compOpts, compliance.WithRegulatoryPackager(pkg))
		log.Info("compliance: regulatory packager wired (DORA Register of Information + major-incident classification; enterprise)")
	}
	if poam := newPOAMBuilder(); poam != nil {
		compOpts = append(compOpts, compliance.WithPOAMBuilder(poam))
		log.Info("compliance: OSCAL POA&M builder wired (FedRAMP-adjacent plan-of-action-and-milestones; enterprise)")
	}
	// ISO/IEC 42001 AIMS certification-readiness: the pack (SoA, AI policy, risk
	// register, impact assessments, lifecycle controls, supplier governance) is the
	// commercial enterprise/iso42001 add-on, wired only under -tags enterprise
	// (newAIMSPackager returns the real packager there, nil in the default build). nil ⇒
	// the /aims/pack endpoints answer 501 and the open catalog/evidence/risk surfaces are
	// unchanged — no feature removed from the free product (no rug-pull).
	if aims := newAIMSPackager(); aims != nil {
		compOpts = append(compOpts, compliance.WithAIMSPackager(aims))
		log.Info("compliance: AIMS packager wired (ISO/IEC 42001 certification-readiness pack; enterprise)")
	}
	// Compliance-depth: the US state AI law packs, sector-overlay packs, CCM and FedRAMP
	// 20x KSIs are the commercial enterprise/compliancedepth add-on, wired only under -tags
	// enterprise (newComplianceDepthPackager returns the real packager there, nil in the default
	// build). nil ⇒ the /depth/* endpoints answer 501 and the open catalog/calendar/evidence
	// surfaces are unchanged — no feature removed from the free product (no rug-pull).
	if depth := newComplianceDepthPackager(); depth != nil {
		compOpts = append(compOpts, compliance.WithComplianceDepth(depth))
		log.Info("compliance: depth packager wired (US state AI laws + sector overlays + CCM + FedRAMP 20x; enterprise)")
	}
	// NIS 2 significant-incident classification: the Art 23 classifier is the
	// commercial enterprise/nis2incident add-on, wired only under -tags enterprise
	// (newNIS2IncidentPackager returns the real packager there when
	// OLIVARES_NIS2INCIDENT_CONFIG is set and valid, nil in the default build). nil ⇒
	// the /nis2/incidents classification endpoints answer 501 and the open nis2
	// catalog/calendar surfaces are unchanged — no feature removed from the free
	// product (no rug-pull).
	if nis2 := newNIS2IncidentPackager(osGetenv, log); nis2 != nil {
		compOpts = append(compOpts, compliance.WithNIS2IncidentPackager(nis2))
		log.Info("compliance: NIS 2 incident packager wired (Art 23 significant-incident classification; enterprise)")
	}
	comp := compliance.New(compOpts...)
	knowledgeOpts = append(knowledgeOpts, knowledge.WithHoldGate(complianceHoldGate{m: comp}))

	// wire the REAL deploy executor (module VII ACTS), the firm IdentityBinder
	// and the drift loop from the operator config (OLIVARES_DEPLOY_EXECUTOR_CONFIG).
	// All deny-closed when absent: no executor => the module keeps unwiredExecutor
	// (apply/retire stay honest 503); no binder => degraded attribution (never faked);
	// no drift tenants => no loop. Operator secrets live in the config file, never the
	// store. The binder/drift handlers are late-bound by boot() after api.New.
	deployExec := newDeployExecutor(deployExecCfg, wifBroker, log)
	if deployExec != nil {
		deployOpts = append(deployOpts, deploy.WithExecutor(deployExec))
	}
	deployBinder := newDeployIdentityBinder(deployExecCfg.Identity.Tenants, log)
	if deployBinder != nil {
		deployOpts = append(deployOpts, deploy.WithIdentityBinder(deployBinder))
	}
	deployDrift := newDeployDriftLoop(deployExecCfg.Drift.Tenants, secs(deployExecCfg.Drift.IntervalSeconds), log)

	// wire the orchestration + voice dispatchers from operator config (modules IV
	// and XVI now ACT). Deny-closed when absent: no config => each module keeps its
	// unwiredDispatcher (an approved fire/open stays declared_not_fired/declared_not_opened)
	// and Start() warns. The orchestration runtime fire route REUSES the same governed
	// executor engine the deploy module acts through (deployEngine(deployExec); nil when
	// no executor is provisioned, so that route then fails closed per fire). The A2A
	// fire route and the voice providers are minted from their own operator-secret config;
	// master keys and BYO auth live in those files, never the store, never a returned ref.
	orchDisp := newOrchestrationDispatcher(orchDispatchCfg, deployEngine(deployExec), log)
	if orchDisp != nil {
		orchOpts = append(orchOpts, orchestration.WithDispatcher(orchDisp))
	}
	if vd := newVoiceDispatcher(voiceDispatchCfg, log); vd != nil {
		voiceOpts = append(voiceOpts, voice.WithDispatcher(vd))
	}
	if strings.TrimSpace(voiceCallCfg.WebhookSecret) != "" {
		if cfg, ok := voiceCallModuleConfig(voiceCallCfg, log); ok {
			voiceOpts = append(voiceOpts, voice.WithCallConfig(cfg), voice.WithTranscriptClassifier(voiceTranscriptClassifier{}))
			if ctrl, attach := newVoiceCallController(voiceDispatchCfg, voiceCallCfg, log); ctrl != nil {
				voiceOpts = append(voiceOpts, voice.WithCallController(ctrl))
				if attach != nil {
					voiceOpts = append(voiceOpts, voice.WithSidebandAttacher(attach))
				}
			}
		}
	}

	// (FIN-08): wire the FinOps pre-flight budget gate into the actuation seams
	// (orchestration fire, voice open) and the model-router (resolve). The SAME finops
	// instance is in the module set below, so the engine's UseData wires its store before
	// any request reaches CheckBudget. Unlike the approval gate / dispatcher this needs
	// NO operator config — FinOps is in-process — so it is ALWAYS wired: an exhausted
	// enforcing budget (action=throttle|block) now DENIES the spend (deny-closed) instead
	// of only emitting the finops_budget_cap finding. The gate is opt-in by nature (no
	// enforcing budget ⇒ never denies) and fails OPEN on a FinOps read error, so wiring it
	// can never take down actuation. See budgetgate.go.
	fin := finops.New()
	orchOpts = append(orchOpts, orchestration.WithBudgetGate(orchBudgetGate{fin: fin, log: log}))
	voiceOpts = append(voiceOpts, voice.WithBudgetGate(voiceBudgetGate{fin: fin, log: log}))
	// late-bind the evals regression-gate budget adapter (evals is built first).
	evBudget.bind(fin)

	// the FinOps→Anthropic-org upstream-cap backstop (defense in depth). OPT-IN and
	// default-OFF: nil unless OLIVARES_CLAUDE_ADMIN_ACTUATOR_CONFIG provisions an Admin
	// credential + allowlist AND sets backstop.enabled. It governs every actuation through
	// the same bridge (adminGate); boot() subscribes it to the bus once that bridge's
	// handler is bound. fin satisfies budgetCapResolver (resolves a capped budget → the
	// offending key/workspace).
	finBackstop := newFinopsBackstop(adminActuatorCfg, fin, bridge, log)

	// the estate kill-switch stop gates, into EVERY actuation seam
	// (orchestration fire, voice open, deploy apply/retire; models execute below
	// once modelsOpts exists). Like the budget gates they are ALWAYS wired
	// (governance is in-process, no operator config) — but with the INVERSE
	// error posture: the module ports fail CLOSED on a gate error, because a
	// stop is positive enforcement and an unreadable stop state must never mean
	// "go" (killswitchgate.go).
	orchOpts = append(orchOpts, orchestration.WithStopGate(orchStopGate{guard: gov}))
	voiceOpts = append(voiceOpts, voice.WithStopGate(voiceStopGate{guard: gov}))
	deployOpts = append(deployOpts, deploy.WithStopGate(deployStopGate{guard: gov}))

	// liveingest (module XXIV): the in-process producer for detective bus events the
	// out-of-process Claude connector cannot Host.Publish (guardrail.observed,
	// the voice probe). Deny-closed: the observed-text half is OFF unless the
	// operator opts in (OLIVARES_LIVEINGEST_INSPECT_OBSERVED_REFS=1); it then publishes
	// already-redacted tool_args references for the security detector chain — never raw
	// content, never widening the connector's capture (docs/SECURITY-HARDENING.md). It is ALSO the
	// publisher of the RUNTIME cost/forensic of governed Claude calls (CLA-15/
	// ANT2-15): the judge adapter (below) and the models routing-execute path hand it
	// the cost lines + forensic findings their MessageResponse carries. Its Host exists
	// only at Init and the publish methods are nil-safe, so binding its (stable) pointer
	// now is in time.
	live := liveingest.New(liveingest.WithObservedRefInspection(osGetenv("OLIVARES_LIVEINGEST_INSPECT_OBSERVED_REFS") == "1"))
	ci.bindRuntimeCostSink(live)
	// a governed A2A scheduled-fire is also a COMMUNICATION fact — make it visible
	// in module IV's graph (an a2a edge) + the SOC feed (an a2a_delegation finding) via
	// the same in-process producer. Late-bound (live's Host exists only at Init) and
	// fail-open; a no-op when no dispatcher is provisioned.
	orchDisp.bindObservationSink(live)

	// (module X): the governed routing-EXECUTION seam and the read-only rate-limit
	// inventory. The executor reuses the SAME inference credential as the judge and
	// publishes its runtime cost/forensic through `live`; with no credential it is nil,
	// so /execute stays deny-closed (503). The rate-limit provider is read-only and
	// degrades to empty-with-reason when no Admin credential is configured.
	modelsOpts := []models.Option{
		models.WithBudgetGate(modelsBudgetGate{fin: fin, log: log}),
		// the kill-switch gate on the execute (spend) path — estate
		// graduation only (routed execution has no agent dimension).
		models.WithStopGate(modelsStopGate{guard: gov}),
		// the source-scope gate on the execute path — drop every model the
		// acting session is out of scope for (model→workspace/agent-group binding),
		// deny-closed, before the budget gate and the executor.
		models.WithScopeGate(modelsScopeGate{resolver: ssResolver, log: log}),
		// the actor-scope resolver the model-access decision needs (workspace +
		// agent-groups of the acting session) to match model-access grants. The grants/
		// groups themselves are models-owned tables the module reads directly; this is the
		// only cross-module dependency of the decision (deny-closed on error).
		models.WithActorScopeResolver(modelsActorScopeResolver{resolver: ssResolver, log: log}),
	}
	if ex := newModelsExecutor(osGetenv, inferenceDoer, live, log); ex != nil {
		modelsOpts = append(modelsOpts, models.WithExecutor(ex))
	}
	if rlp := newModelsRateLimitProvider(osGetenv, log); rlp != nil {
		modelsOpts = append(modelsOpts, models.WithRateLimitProvider(rlp))
	}
	// the platforms reference (surfaces matrix + per-platform lifecycle) is
	// DECLARED data compiled into the claude-api connector — credential-less, so it
	// is wired unconditionally (unlike the rate-limit inventory above, which needs
	// the Admin credential). GET /v1/m/models/platforms is what flipped the web
	// platforms view from its static *.data.ts copy.
	modelsOpts = append(modelsOpts, models.WithPlatformsProvider(newPlatformsProvider()))

	// the policy/identity AUTHORING consoles the web already calls.
	// Closed the truth loop — the seams that were honest nils are now real:
	//   - claudePolicy (B): managed-* validate/dry-run/publish/versions at
	//     /v1/m/claude-policy, PLUS the distribution seam made real (the decided
	//     v1 mechanism: signed artifact in the plane + agent PULL with attestation;
	//     deny-closed — "distributed" only after the signed record committed) and the
	//     OBSERVED-config provider over the attested check-ins, so publish computes
	//     PERMITTED-vs-OBSERVED drift fleet-wide and records it as REAL findings.
	//     Both are store-backed: their data handles are late-bound by boot() (the
	//     knowledgeGuard pattern). A nil policy signing key (e.g. the e2e harness)
	//     keeps the honest seam-pending posture, exactly as before.
	//   - agents (D): managed-agent tool-confirmation at /v1/m/claude-agents, bound to
	//     the approval machine and audited with a redacted fingerprint. Thread
	//     events are served LIVE through a dedicated reader per tenant (the
	//     claude-api request-time pattern), built from the operator's configured
	//     claude-managed-agents sources; no source ⇒ the honest empty stands.
	//   - identity (E): the read-only WIF graph at /v1/m/identity/wif, reconciled against
	//     the live WIF Admin API (org:admin OAuth) when configured, else the declared
	//     federation rules, via wifAdapter (populated by wireRoster).
	// wifAdapter is created earlier (it feeds both the identity console and the WIF
	// credential broker); here it backs the read-only WIF graph console.
	policyDistributor := governance.NewPolicyArtifactDistributor(policySigner)
	policyObserved := governance.NewPolicyObservedStore()
	policyOpts := []governance.PolicyConsoleOption{governance.WithObservedConfig(policyObserved)}
	if policyDistributor != nil {
		policyOpts = append(policyOpts, governance.WithManagedDistributor(policyDistributor))
	}
	claudePolicyConsole := governance.NewPolicyConsole(policyOpts...)
	// the thread-event provider is always wired (non-nil); its per-tenant
	// readers are POPULATED post-store by deferredSecretWiring.openAll, because a
	// reader's config may reference a secret that resolves only once the store
	// exists. Until then (and for a tenant with no usable CMA source) it serves the
	// honest empty.
	claudeThreads := newClaudeThreadEventProvider(srcCfg, log)
	agentsOpts := []governance.AgentsConsoleOption{governance.WithThreadEventProvider(claudeThreads)}
	agentsConsole := governance.NewAgentsConsole(agentsOpts...)
	// the identity console's POSTURE tab (External Keys/CMEK + workspace
	// residency, ANT2-04/06) reads the same read-only Claude Admin credential the
	// rate-limit inventory above uses. The routes are mounted UNCONDITIONALLY — with
	// no credential the provider is nil and they answer available=false with a reason,
	// which is an answer; a 404 is a defect, and that 404 is what came from.
	identityOpts := []governance.IdentityConsoleOption{governance.WithWifGraphProvider(wifAdapter)}
	if pp := newIdentityPostureProvider(osGetenv, log); pp != nil {
		identityOpts = append(identityOpts, governance.WithIdentityPostureProvider(pp))
	}
	identityConsole := governance.NewIdentityConsole(identityOpts...)

	// privileged-session recording. The module is BOTH the api.Options.
	// Recorder (the engine's module-route capture wrapper, wired in boot.go) and
	// governance's RecordingGate (break-glass refuses to activate without an
	// actively recorded session — deny-closed). The optional Claude-backed
	// summarizer rides the same inference credential as the judge (honest 501
	// when unconfigured).
	recOpts := append(ci.recordingOptions(), recording.WithTimelineResolver(&sessionsTimelineAdapter{sessions: sm}))
	recmod := recording.New(recOpts...)
	gov.UseRecordingGate(recmod)

	sec := security.New(secOpts...)
	// XV: the notify module routes findings to output connectors through the
	// connector-dispatcher adapter (notifydispatch.go). Destinations are provisioned by
	// the operator (OLIVARES_NOTIFY_CONFIG, secrets out of the store); with none
	// configured the transport is wired but empty and the module warns once.
	// the dispatcher is constructed with its provisioned destinations but
	// OPENED post-store by deferredSecretWiring.openAll, so a destination's secret
	// references (a Slack webhook URL, a PagerDuty routing key) resolve against the
	// secret store/backends first.
	// the dispatcher shares the SINGLE external-connector trust root
	// (srcCfg.ConnectorTrust) with external source/content plugins — an external
	// output destination is admitted against the same anchors, never a second path.
	notifyDispatcher := newConnectorDispatcher(notifyDestinations, srcCfg.ConnectorTrust, log)
	nm := notify.New(notify.WithDispatcher(notifyDispatcher))

	// XIX: the eventing platform (typed event subscriptions over the bus).
	// Constructed deny-closed — no authorizer, no secret sealer; BOTH are
	// late-bound by boot() (UseAuthorizer needs the composed evaluator,
	// UseSecretSealer needs the data dir). Only the env posture options
	// (loopback endpoints, retention window) resolve here. See eventingwire.go.
	// an estate stop PARKS the tenant's deliveries (the only durable work
	// queue) except the static governance channel — approval.requested and
	// finding.reported keep flowing so the stop never silences the rail its own
	// dual-control re-enable is decided through (killswitchgate.go).
	// the SIEM-sink renderer re-shapes ledger and findings events into a
	// control tower's native dialect (OCSF 1.8/CEF/LEEF) and envelope; the engine
	// still owns the durable transport/retry/DLQ/replay. It is stateless, so it is
	// wired at construction (no late-binding). Without it, SIEM-sink subscriptions
	// are parked deny-closed; generic webhooks are unaffected.
	// the egress destination policy. A malformed one is fatal here rather than
	// a downgrade to "unconstrained" — see loadEventingOptions.
	evtOpts, err := loadEventingOptions(osGetenv, log)
	if err != nil {
		return moduleSet{}, err
	}
	evtm := eventing.New(append(evtOpts,
		eventing.WithDeliveryGate(eventingKillSwitchGate{guard: gov, log: log}),
		eventing.WithSinkRenderer(siemforward.NewRenderer()))...)
	// the ledger forwarder walks the tamper-evident ledger from a per-tenant
	// cursor and hands each sealed record to the eventing engine (IngestAudit), so
	// the audit ledger reaches SIEM control towers over the SAME durable machinery.
	// The leader-gated pump (ledgerforwardpump.go) calls its ForwardDue per tenant.
	siemfwd := siemforward.New(evtm)

	// Module X (models) handle kept so the composition root can reuse it as the
	// model-access gate of the inline inference PEP, exactly as the routing
	// execute path already does.
	mdl := models.New(modelsOpts...)
	var governedEmbedder *governedKnowledgeEmbedder
	if ci.embedder != nil {
		governedEmbedder = newGovernedKnowledgeEmbedder(ci.embedder, mdl, ci.knowledgeStatus, log)
		knowledgeOpts = append(knowledgeOpts, knowledge.WithEmbedder(governedEmbedder))
	}
	voiceMod := voice.New(voiceOpts...)
	bindVoiceWebhookModule(voiceMod)
	if b, ok := newAgentCoreExportBinding(agentCoreCfg, bridge, []governance.AgentCoreExportProvider{mdl, ss}, log); ok {
		gov.UseAgentCoreExport(b)
	}
	// the inline inference PEP's per-tenant governance config + DLP policy. The
	// proxy LISTENER is built post-boot in cmd_serve.go (its own socket, opt-in via
	// OLIVARES_INFERENCE_PROXY_CONFIG); this module is the durable, console-authorable
	// policy the decider reads. It composes nothing (cmd composes).
	ipx := inferenceproxy.New()

	// the knowledge module is held so the deferred document sources can be
	// registered on it (AddSource) after the store resolves their credentials.
	km := knowledge.New(knowledgeOpts...)

	// the tool-pin verifier is constructed ONCE here (nil in the
	// community build) and shared by BOTH the MCP gateway PEP (rug-pull detection
	// at tools/call) and the enterprise reporting tool-pin source, so the report
	// reflects the SAME pins the gateway enforces — not a second, always-empty
	// store. boot() threads it to the gateway via the engine.
	pinVerifier := newToolPinVerifier(osGetenv, log)

	// the reporting module, held so boot() can register the schedule pump
	// (RunDueSchedules per tenant) — the driver that makes the scheduler
	// actually FIRE in the running binary, not just persist. Inert in the
	// community build (SchedulerWired() is false, so newReportSchedulePump is nil).
	rep := reporting.New(
		reporting.WithComplianceSource(reportingComplianceAdapter{comp: comp}),
		reporting.WithScheduler(newReportScheduler()),
		reporting.WithBranding(newReportBranding()),
		reporting.WithCustomTemplates(newReportCustomTemplates()),
		// the enterprise report engine (posture/risk/bundle) wired with
		// REAL data sources — nil in the community build (the /enterprise/*
		// routes then answer 501). This is the call-site the engine
		// previously lacked (it was constructed nowhere, with empty Deps).
		reporting.WithEnterpriseReports(newEnterpriseReportSource(osGetenv, comp, gov, pinVerifier, log)),
	)

	// notify-test workflow steps actuate through the notify module's own
	// evidenced test path (claim-then-send, delivery ledger) — the composition
	// root owns the bridge, modules never import each other. The per-tenant
	// workflow/step caps are operator-tunable (defaults in-module).
	orchOpts = append(orchOpts, orchestration.WithNotifyTester(orchNotifyTester{n: nm}))
	orchOpts = append(orchOpts, orchestration.WithWorkflowLimits(loadWorkflowLimits(osGetenv, log)))
	workflowKernel := newWorkflowKernelAdapter(sm)
	workflowCommunication := newWorkflowCommunicationAdapter(sm)
	orchOpts = append(orchOpts,
		orchestration.WithWorkflowWorkControl(workflowKernel),
		orchestration.WithWorkflowRuntimeControl(workflowKernel),
		orchestration.WithWorkflowMessageControl(workflowCommunication),
		orchestration.WithWorkflowHandoffControl(workflowCommunication),
		orchestration.WithWorkflowAckReader(workflowCommunication),
	)
	var remoteApproval orchestration.ApprovalGate
	if bridge != nil {
		remoteApproval = bridge.orchestrationGate()
	}
	remoteWork, err := newOrchRemoteExecutor(orchDispatchCfg, sm, remoteApproval, log)
	if err != nil {
		return moduleSet{}, fmt.Errorf("wire K5 remote work executor: %w", err)
	}
	protocolBindingReconciler := newProtocolBindingReconcileMux()
	sm.UseProtocolBindingRemoteReconciler(protocolBindingReconciler)
	if remoteWork != nil {
		if err := protocolBindingReconciler.Use(sessions.BindingProtocolA2A, remoteWork); err != nil {
			return moduleSet{}, fmt.Errorf("wire K5 A2A protocol reconcile adapter: %w", err)
		}
		sm.UseProtocolBindingSpecValidator(sessions.BindingProtocolA2A, remoteWork)
		orchOpts = append(orchOpts, orchestration.WithRemoteWorkExecutor(remoteWork))
	}

	// D-06: the dedicated target-binding HMAC key + the effective-dispatcher-
	// config generation. Together they freeze the FULL effect target a human
	// approved (schedule/route AND the operator image/command/URL/skill) so a
	// re-point or a config reload voids a pending approval. Without a key the
	// module blocks every acting step (deny-closed); the generation folds the
	// operator config in so re-pointing a subject to an attacker image is caught.
	if key, ok := loadTargetBindingKey(osGetenv, log); ok {
		orchOpts = append(orchOpts, orchestration.WithTargetBindingKey(key))
	}
	orchOpts = append(orchOpts, orchestration.WithDispatcherGeneration(newDispatcherGeneration(orchDispatchCfg)))

	// the routine-governance policy gate + the AUTHORITATIVE
	// actuation-environment resolver. ALWAYS wired, like the kill-switch and
	// budget gates (governance is in-process, no operator config), and with the
	// kill switch's FAIL-CLOSED posture — an unreadable routine policy denies.
	// Without these two options the five controls decide nothing, which is
	// exactly the state closed. Each has its OWN wireproof test through
	// this composition root (a cadence floor never consults the environment
	// resolver, so one test cannot guard both).
	orchOpts = append(orchOpts, orchestration.WithRoutinePolicyGate(orchRoutinePolicyGate{gov: gov}))
	orchOpts = append(orchOpts, orchestration.WithTargetEnvironmentResolver(newOrchTargetEnvironment(orchDispatchCfg)))

	// constructed outside the slice so the composition root can hand the
	// cadence pump its tenant-scoped scan seam (the eventing-module pattern).
	orch := orchestration.New(orchOpts...)
	all := []api.Module{
		accessmap.New(),
		// the pin verifier's operator surface (enterprise implements
		// ToolPinAdmin; community nil ⇒ the /toolpins routes answer 501).
		capabilities.New(capabilitiesOpts(pinVerifier)...),
		// (gap #12): the Claude Code adoption / productivity read-model + dashboard.
		// Subscribes to the MetricSample bus signal both Claude connectors now emit;
		// open-core, never cost (the authoritative cost surface is FinOps).
		claudeadoption.New(),
		// the source-scoping plane (binding table + write API + the resolver
		// already injected into the models/knowledge scope gates above).
		ss,
		// XIV: the catalog signs approved registry entries with its INDEPENDENT
		// Ed25519 key (provisioned at boot, fail-closed 0600). On by default in the
		// shipped binary; a nil key (e.g. the e2e harness) keeps the unsigned/unpinned
		// honest default. Never the audit key — a separate artifact-signing key.
		catalog.New(catalog.WithSigningKey(catalogSigner)),
		comp,
		// saved console views — named, shareable snapshots of a console
		// view's URL-state (server-side, size-capped params only, audited).
		consoleviews.New(),
		deploy.New(deployOpts...),
		ev,
		evtm,
		fin,
		gov,
		// Authoring consoles (route-only; they reuse gov's policy_revision/approval
		// tables via the shared store and mount the hyphenated REST namespaces the web
		// already calls).
		claudePolicyConsole,
		agentsConsole,
		identityConsole,
		health.New(),
		inventory.New(),
		km,
		// liveingest: the in-process producer of the detective bus events the
		// out-of-process Claude connector cannot Host.Publish (guardrail.observed,
		// the voice probe). Deny-closed: the observed-text half is OFF unless the
		// operator opts in (OLIVARES_LIVEINGEST_INSPECT_OBSERVED_REFS=1); it then publishes
		// already-redacted tool_args references for the security detector chain — never raw
		// content, never widening the connector's capture (docs/SECURITY-HARDENING.md).
		live,
		mdl,
		// inference-proxy governance config + DLP policy (route-only here; the
		// listener is post-boot in cmd_serve.go).
		ipx,
		nm,
		// the observability read-models — per-standard/source
		// ingestion-health fed from the bus, the ledger-correlation trace read-model
		// (trace_id/span_id audit meta), and the MEASURED supply-chain state
		// of this running binary. The ldflags build identity is constructed HERE
		// because main.{version,commit,date} never leave package main otherwise
		// (main.go:28-32).
		observability.New(observability.WithBuildInfo(observability.BuildInfo{
			Version: version, Commit: commit, Date: date,
		})),
		orch,
		// read-only posture/inventory export for control-tower enrichment
		// (Agent 365 / ServiceNow AI Control Tower). Route-only; outbound posture,
		// never identity.
		postureexport.New(),
		recmod,
		redteam.New(redteamOpts...),
		// on-demand PDF/HTML report generation from compliance, audit and FinOps
		// data. The data-source adapters are wired at construction; the enterprise add-on
		// (scheduler, branding, custom templates) is nil in the community build.
		rep,
		sb,
		sec,
		siemfwd,
		sm,
		voiceMod,
	}
	// hand the recorder the namespaces actually mounted so a tenant's
	// recorded-namespace config is validated against reality (a typo would
	// silently un-record a surface).
	known := make([]string, 0, len(all))
	for _, m := range all {
		known = append(known, m.APINamespace())
	}
	recmod.UseKnownNamespaces(known)
	return moduleSet{all: all, gov: gov, approvalBridge: bridge, compliance: comp, deployBinder: deployBinder, deployDrift: deployDrift, knowledgeGuard: knowledgeGuard, vectorIndex: vecAdapter, live: live, wif: wifAdapter, wifBroker: wifBroker, recorder: recmod, accountEraser: accEraser, eventing: evtm, notify: nm, orchestration: orch, siemforward: siemfwd, policyDist: policyDistributor, policyObserved: policyObserved, sessions: sm, protocolBindingReconciler: protocolBindingReconciler, finops: fin, finopsBackstop: finBackstop, models: mdl, inferenceProxy: ipx,
		knowledge:           km,
		knowledgeStatus:     ci.knowledgeStatus,
		knowledgeEmbedder:   governedEmbedder,
		sourceScopeResolver: ssResolver,
		rtbfCoordinator:     rtbfCoord,
		pinVerifier:         pinVerifier,
		identityConsole:     identityConsole,
		reporting:           rep,
		deferredSecrets:     &deferredSecretWiring{content: pendingContent, knowledge: km, notify: notifyDispatcher, claude: claudeThreads}}, nil
}

// loadGovernancePDP builds the IDN-09 external policy engine from the environment.
// OLIVARES_PDP_ENGINE selects cedar|opa (empty/none = native ABAC only). Cedar reads
// its policy from OLIVARES_PDP_CEDAR_FILE; OPA reads OLIVARES_PDP_OPA_URL /
// _OPA_PATH / _OPA_TOKEN. A misconfiguration disables the external PDP (the native
// ABAC engine and RBAC still govern) and logs loudly — it never silently un-governs
// and never crashes the control plane over a policy file.
// loadGovernanceOptions assembles every governance.Option from the environment: the
// optional external PDP (Cedar/OPA deny-overlay) AND the offline-staleness bound
// (ADR-0024 Q1), which are independent — the staleness bound governs the ALWAYS-present
// scoped-grant engine, not the external PDP, so it must not be gated behind
// OLIVARES_PDP_ENGINE.
func loadGovernanceOptions(getenv func(string) string, log *slog.Logger) ([]governance.Option, error) {
	opts := loadGovernancePDP(getenv, log)
	if raw := strings.TrimSpace(getenv("OLIVARES_POLICY_MAX_STALENESS")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("OLIVARES_POLICY_MAX_STALENESS=%q must be a positive Go duration; refusing to start instead of silently disabling offline-staleness enforcement", raw)
		}
		opts = append(opts, governance.WithOfflinePolicyStaleness(d))
		log.Info("governance: offline-staleness enforcement enabled — positive scoped grants expire deny-closed past the bound (forbid/deny stay enforced)", "policy_max_staleness", d.String())
	}
	return opts, nil
}

func loadGovernancePDP(getenv func(string) string, log *slog.Logger) []governance.Option {
	engine := strings.TrimSpace(getenv("OLIVARES_PDP_ENGINE"))
	if engine == "" || strings.EqualFold(engine, "none") {
		return nil
	}
	cfg := governance.PDPConfig{Engine: governance.PDPEngine(strings.ToLower(engine)), Logger: log}
	switch cfg.Engine {
	case governance.PDPCedar:
		if file := strings.TrimSpace(getenv("OLIVARES_PDP_CEDAR_FILE")); file != "" {
			b, err := os.ReadFile(file) //nolint:gosec // operator-provided policy path
			if err != nil {
				log.Error("pdp: cannot read cedar policy file; external PDP disabled (native ABAC still enforced)", "file", file, "err", err)
				return nil
			}
			cfg.CedarPolicy = string(b)
		}
	case governance.PDPOPA:
		cfg.OPABaseURL = getenv("OLIVARES_PDP_OPA_URL")
		cfg.OPADecisionPath = getenv("OLIVARES_PDP_OPA_PATH")
		cfg.OPAToken = getenv("OLIVARES_PDP_OPA_TOKEN")
	}
	pdp, err := governance.NewExternalPDP(cfg)
	if err != nil {
		log.Error("pdp: invalid external PDP config; external PDP disabled (native ABAC still enforced)", "engine", engine, "err", err)
		return nil
	}
	if pdp == nil {
		return nil
	}
	log.Info("pdp: external policy engine enabled", "engine", engine)
	return []governance.Option{governance.WithExternalPDP(pdp)}
}

// complianceHoldGate bridges the knowledge.HoldGate seam to the compliance
// module's exported CheckHold: tenant-wide + class + subject holds
// evaluated in ONE call, the single §4 matching rule. The mapping is
// field-for-field (ids/matter/scope, never content); an error PROPAGATES as-is
// so knowledge renders its 503 deny — it is NEVER swallowed into held=false (a
// hold that cannot be ruled out blocks the destruction, the safe direction).
// Neither module imports the other; the composition root owns the glue.
type complianceHoldGate struct{ m *compliance.Module }

var _ knowledge.HoldGate = complianceHoldGate{}

func (g complianceHoldGate) Check(ctx context.Context, tenant model.TenantID, subjectKind, subjectRef, dataClass string) (bool, []knowledge.HoldRef, error) {
	dec, err := g.m.CheckHold(ctx, tenant, compliance.HoldSubject{Kind: subjectKind, Ref: subjectRef, DataClass: dataClass})
	if err != nil {
		return false, nil, err
	}
	var holds []knowledge.HoldRef
	for _, h := range dec.Holds {
		holds = append(holds, knowledge.HoldRef{ID: h.ID, MatterRef: h.MatterRef, ScopeKind: h.ScopeKind})
	}
	return dec.Held, holds, nil
}

// modelsProviderRetention bridges compliance.ProviderRetention to the models
// reference's read-only export: the maximum forced-retention period
// across Covered families (≥30 days, no ZDR — uplift 2026-06-09), with the
// covered families as provenance. days==0 (no covered family in the reference)
// passes through honestly — the module then annotates a zero floor rather than
// a fabricated one.
type modelsProviderRetention struct{}

var _ compliance.ProviderRetention = modelsProviderRetention{}

func (modelsProviderRetention) MaxForcedRetentionDays(context.Context) (int, string) {
	days, families := models.MaxCoveredRetentionDays()
	source := "models.reference"
	if len(families) > 0 {
		source += ": " + strings.Join(families, ", ")
	}
	return days, source
}

// evalsScorerAdapter bridges the sandbox.Scorer seam to the evals module's public
// ScoreOutputs method, translating between the two modules' own DTOs. It is the
// production form of the adapter the sandbox integration test exercises:
// neither module imports the other; the composition root owns the glue.
type evalsScorerAdapter struct{ ev *evals.Module }

func (a evalsScorerAdapter) Score(ctx context.Context, tenant model.TenantID, req sandbox.ScoreRequest) (sandbox.ScoreVerdict, error) {
	card, err := a.ev.ScoreOutputs(ctx, tenant, evals.ScoreOutputsRequest{
		SuiteRef:    req.SuiteRef,
		SubjectKind: req.SubjectKind,
		SubjectRef:  req.SubjectRef,
		Variant:     req.Variant,
		Actor:       "module:olivares.sandbox",
		Outputs:     req.Outputs,
	})
	if err != nil {
		return sandbox.ScoreVerdict{}, err
	}
	return sandbox.ScoreVerdict{
		Score:    card.Score,
		PassRate: card.PassRate,
		// Passed = every scored case passed and at least one was actually scored; a
		// degraded/all-skipped scorecard is never a pass.
		Passed:  card.Status == "completed" && card.Failed == 0 && (card.Passed+card.Failed) > 0,
		Total:   card.Total,
		PassedN: card.Passed,
		FailedN: card.Failed,
	}, nil
}
