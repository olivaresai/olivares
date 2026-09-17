// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/olivaresai/olivares/core/model"
)

// Launch readiness — the LOCAL requirements of one profile under one selection.
//
// This is a PURE, dated observation and nothing else. It answers "what can this
// node see about the requirements of launching THIS profile over THIS transport
// and isolation", and it deliberately does not answer "will the launch
// succeed": that verdict belongs to the POST, which revalidates everything here
// and then does the things this read refuses to do.
//
// ⛔ WHAT IT NEVER DOES, because doing any of them would make a GET an act:
// no Runner.Launch, no Mint of any kind, no LaunchGate.Authorize, no StopGate,
// no Claim, no HITL, no PEP provisioning, no OpenSession, no execution of a
// provider program — not even `--version` — and no reading of a login file
// inside a profile's homes. Readiness never enables, repairs or activates the
// K3 communication posture to complete itself; it samples what composition
// already bound and says so.
//
// ⛔ AND IT IS NOT A BOOLEAN. `operable` on the profile DTO is preserved exactly
// as it was and keeps meaning what it computes — enabled here — while this read
// is the source of the requirements panel. Four states are kept apart on
// purpose, because collapsing them is the measured defect this exists to fix:
//
//   - ready         — the included LOCAL requirement was checked and is met. It
//     is not authentication and it is not authorization.
//   - not_configured — a KNOWN absence or incompatibility of configuration.
//   - unsupported   — a combination or environment this runtime does not run.
//   - unknown       — the observation could not be made. It is never rewritten
//     as "absent": an unexaminable home is not a missing home, and a Runner with
//     no inspection seam is not a Runner that cannot launch.
//   - not_applicable — the requirement does not belong to this combination. It
//     never substitutes for unknown.

// ReadinessState is the closed state vocabulary above.
type ReadinessState string

// The five readiness states.
const (
	ReadinessReady         ReadinessState = "ready"
	ReadinessNotConfigured ReadinessState = "not_configured"
	ReadinessUnsupported   ReadinessState = "unsupported"
	ReadinessUnknown       ReadinessState = "unknown"
	ReadinessNotApplicable ReadinessState = "not_applicable"
)

// ReadinessCheck is the closed set of requirement dimensions. It is a LIST and
// not a map on purpose: the panel keeps every dimension visible even when one
// of them is a dominant cause, so a known blocker never hides an unknown.
type ReadinessCheck string

// The nine requirement dimensions, in the order they are reported.
const (
	CheckProfile            ReadinessCheck = "profile"
	CheckLocalEnvironment   ReadinessCheck = "local_environment"
	CheckDriver             ReadinessCheck = "driver"
	CheckRunner             ReadinessCheck = "runner"
	CheckProgram            ReadinessCheck = "program"
	CheckHomes              ReadinessCheck = "homes"
	CheckAuthSource         ReadinessCheck = "auth_source"
	CheckCredentialSource   ReadinessCheck = "credential_source"
	CheckRuntimeCredentials ReadinessCheck = "runtime_credentials"
)

// readinessCheckOrder is the reported order. Reporting is complete and ordered
// so two observations of the same node are comparable.
var readinessCheckOrder = [...]ReadinessCheck{
	CheckProfile, CheckLocalEnvironment, CheckDriver, CheckRunner, CheckProgram,
	CheckHomes, CheckAuthSource, CheckCredentialSource, CheckRuntimeCredentials,
}

// The closed CAUSE codes. They are identifiers, never sentences, and never
// carry a path, an argv, an environment value or a raw error: the console
// renders localized copy and a static documentation link from the code alone.
const (
	// profile
	codeProfileActive   = "profile_active"
	codeProfileDisabled = "profile_disabled"
	codeProfileRetired  = "profile_retired"

	// local_environment
	codeEnvironmentLocal       = "environment_local"
	codeOtherEnvironment       = "other_environment"
	codeEnvironmentUnavailable = "environment_unavailable"

	// driver
	codeDriverOperable       = "driver_operable"
	codeDriverNotRegistered  = "driver_not_registered"
	codeTransportUnsupported = "transport_unsupported"

	// runner
	codeRunnerReady                 = "runner_ready"
	codeRunnerNotConfigured         = "runner_not_configured"
	codeIsolationUnsupported        = "isolation_unsupported"
	codeRunnerInspectionUnavailable = "runner_inspection_unavailable"

	// program
	codeProgramPresent       = "program_present"
	codeProgramMissing       = "program_missing"
	codeProgramNotExecutable = "program_not_executable"

	// homes
	codeHomesResolved   = "homes_resolved"
	codeHomeUnavailable = "home_unavailable"
	codeHomeChanged     = "home_changed"

	// auth_source
	codeAuthSourceAccountHome                  = "auth_source_account_home"
	codeAuthSourceManagedInjection             = "auth_source_managed_injection"
	codeAuthSourceLegacyClaude                 = "auth_source_legacy_claude"
	codeAuthSourceRequired                     = "auth_source_required"
	codeManagedInjectionNotAppliedForTransport = "managed_injection_not_applied_for_transport"

	// credential_source
	codeClaudeCredentialSourceConfigured    = "claude_credential_source_configured"
	codeClaudeCredentialSourceNotConfigured = "claude_credential_source_not_configured"
	codeProviderCredentialAdapterConfigured = "provider_credential_adapter_configured"
	codeProviderAdapterNotConfigured        = "provider_credential_adapter_not_configured"
	codeCredentialSourceNotInjected         = "credential_source_not_injected"

	// runtime_credentials
	codeRuntimeCredentialsWired        = "runtime_credentials_wired"
	codeRuntimeCredentialsNotRequested = "runtime_credentials_not_requested"
	codeRuntimeCredentialWiringPartial = "runtime_credential_wiring_incomplete"
	codeRuntimeReadinessUnavailable    = "runtime_readiness_unavailable"

	// shared
	codeInspectionUnavailable       = "inspection_unavailable"
	codeNotCheckedInThisEnvironment = "not_checked_in_this_environment"
)

// The closed REMEDIATION identifiers. A remediation is a stable identifier the
// console maps to localized copy and a documentation link; it is never a
// message built from the filesystem and never names a path.
const (
	remediationReviewProfile             = "review_profile"
	remediationSelectLocalProfile        = "select_local_profile"
	remediationConfigureEnvironment      = "configure_execution_environment"
	remediationConfigureDriver           = "configure_provider_driver"
	remediationConfigureRunner           = "configure_session_runner"
	remediationConfigureProgram          = "configure_provider_program"
	remediationReviewProfileHomes        = "review_profile_homes"
	remediationAuthorizeAuthSource       = "authorize_auth_source"
	remediationConfigureClaudeCredential = "configure_claude_credential_source"
	remediationConfigureProviderAdapter  = "configure_provider_credential_adapter"
	remediationReviewRuntimeCredentials  = "review_runtime_credential_composition"
	remediationSelectSupportedSelection  = "select_supported_selection"
	remediationRetryInspection           = "retry_inspection"
)

// The closed protocol/io/input vocabulary of transport_capabilities. It
// describes the SHAPE of the channel this selection would open, so the console
// can stop offering a bridged turn console for a lifecycle-only transport.
const (
	protocolClaudeStreamJSON    = "claude_stream_json"
	protocolClaudeRemoteControl = "claude_remote_control"
	protocolCodexAppServer      = "codex_app_server"
	protocolGrokACP             = "grok_acp"
	protocolOpenCodeACP         = "opencode_acp"
	protocolUnknown             = "unknown"

	ioBidirectional = "bidirectional"
	ioLifecycleOnly = "lifecycle_only"
	ioUnknown       = "unknown"

	inputLine        = "line"
	inputText        = "text"
	inputUnavailable = "unavailable"
	inputUnknown     = "unknown"
)

// The two statements that are ALWAYS unknown here and always present. They do
// not participate in configuration_state: their unknown is an expected check of
// a future act, not a permanent reason to refuse to ask for one.
const (
	codeNotObservedForThisLaunch = "not_observed_for_this_launch"
	codeEvaluatedOnSubmit        = "evaluated_on_submit"
)

// remainingCheck identifiers: what this read deliberately did NOT decide,
// because deciding it needs the effects the POST is allowed to have.
const (
	remainingWorkspaceAndTemplate = "workspace_and_template_validation"
	remainingLaunchAuthorization  = "current_launch_authorization"
	remainingClaimAdmission       = "claim_admission"
	remainingCredentialMint       = "credential_mint_when_required"
	remainingProcessStart         = "process_start"
	remainingProviderAuth         = "provider_authentication_and_protocol"
)

// launchRemainingChecks is fixed and complete: the console shows the applicable
// ones, and a reader that sees the whole list cannot mistake a ready
// configuration for an authorized launch.
func launchRemainingChecks() []string {
	return []string{
		remainingWorkspaceAndTemplate, remainingLaunchAuthorization, remainingClaimAdmission,
		remainingCredentialMint, remainingProcessStart, remainingProviderAuth,
	}
}

// readinessCodeVocabulary is the CLOSED cause vocabulary as a value, so the
// published OpenAPI enum can be compared against it instead of against a list
// somebody retyped. The contract battery walks this file's own constants and
// fails if one of them is missing here, which is the only way a cause added
// tomorrow cannot quietly become an unconstrained string in the contract.
func readinessCodeVocabulary() []string {
	return []string{
		codeProfileActive, codeProfileDisabled, codeProfileRetired,
		codeEnvironmentLocal, codeOtherEnvironment, codeEnvironmentUnavailable,
		codeDriverOperable, codeDriverNotRegistered, codeTransportUnsupported,
		codeRunnerReady, codeRunnerNotConfigured, codeIsolationUnsupported, codeRunnerInspectionUnavailable,
		codeProgramPresent, codeProgramMissing, codeProgramNotExecutable,
		codeHomesResolved, codeHomeUnavailable, codeHomeChanged,
		codeAuthSourceAccountHome, codeAuthSourceManagedInjection, codeAuthSourceLegacyClaude,
		codeAuthSourceRequired, codeManagedInjectionNotAppliedForTransport,
		codeClaudeCredentialSourceConfigured, codeClaudeCredentialSourceNotConfigured,
		codeProviderCredentialAdapterConfigured, codeProviderAdapterNotConfigured,
		codeCredentialSourceNotInjected,
		codeRuntimeCredentialsWired, codeRuntimeCredentialsNotRequested,
		codeRuntimeCredentialWiringPartial, codeRuntimeReadinessUnavailable,
		codeInspectionUnavailable, codeNotCheckedInThisEnvironment,
	}
}

// readinessRemediationVocabulary is the closed remedy vocabulary, same rule.
func readinessRemediationVocabulary() []string {
	return []string{
		remediationReviewProfile, remediationSelectLocalProfile, remediationConfigureEnvironment,
		remediationConfigureDriver, remediationConfigureRunner, remediationConfigureProgram,
		remediationReviewProfileHomes, remediationAuthorizeAuthSource, remediationConfigureClaudeCredential,
		remediationConfigureProviderAdapter, remediationReviewRuntimeCredentials,
		remediationSelectSupportedSelection, remediationRetryInspection,
	}
}

// readinessStateVocabulary and readinessCheckVocabulary complete the published
// closure: the five states one DIMENSION can carry, and the nine dimensions.
func readinessStateVocabulary() []string {
	return []string{
		string(ReadinessReady), string(ReadinessNotConfigured), string(ReadinessUnsupported),
		string(ReadinessUnknown), string(ReadinessNotApplicable),
	}
}

// readinessAggregateStateVocabulary is the strictly SMALLER set configuration_state
// can hold, and the difference is not cosmetic: `not_applicable` is a statement
// about ONE requirement not belonging to a combination, and there is no such
// thing as a whole configuration that does not apply. aggregateReadiness cannot
// produce it — it starts at ready and only ever moves up in rank, and
// not_applicable ranks with ready — so publishing five values invited a client
// to handle a state this API never emits.
func readinessAggregateStateVocabulary() []string {
	return []string{
		string(ReadinessReady), string(ReadinessNotConfigured),
		string(ReadinessUnsupported), string(ReadinessUnknown),
	}
}

func readinessCheckVocabulary() []string {
	out := make([]string, 0, len(readinessCheckOrder))
	for _, c := range readinessCheckOrder {
		out = append(out, string(c))
	}
	return out
}

// LaunchReadinessCheck is one requirement dimension's dated verdict.
type LaunchReadinessCheck struct {
	Check       ReadinessCheck `json:"check"`
	State       ReadinessState `json:"state"`
	Code        string         `json:"code"`
	Remediation string         `json:"remediation,omitempty"`
}

// LaunchReadinessSelection is the closed query: which transport and which
// isolation the console is asking about. It is not a launch request and it
// cannot override the profile's driver, homes, environment or auth source.
type LaunchReadinessSelection struct {
	Transport string `json:"transport"`
	Isolation string `json:"isolation"`
}

// LaunchTransportCapabilities describes the channel shape of this selection.
// unknown is a real answer: a registered driver that publishes no transport
// metadata is not assumed to speak another provider's protocol.
type LaunchTransportCapabilities struct {
	Protocol string `json:"protocol"`
	IO       string `json:"io"`
	Input    string `json:"input"`
}

// LaunchReadinessStatement is a dimension this read does NOT decide.
type LaunchReadinessStatement struct {
	State ReadinessState `json:"state"`
	Code  string         `json:"code"`
	// RequiredPermission names the permission the future act needs. It is
	// informative: holding it is not checked here and this read never grants it.
	//
	// ⛔ `omitempty` IS LOAD-BEARING, DO NOT "TIDY" IT AWAY. This one struct serves
	// both statements, and only launch_authorization has a permission to name:
	// there is no permission that would make a provider authenticated. The
	// published schema says so — provider_authentication is a CLOSED object
	// without this property, launch_authorization REQUIRES it with the single
	// value sessions:run:write — so emitting an empty string here would put a
	// field on provider_authentication that its own contract forbids.
	RequiredPermission string `json:"required_permission,omitempty"`
}

// SessionLaunchReadiness is the complete observation. Every field is a
// reference, a closed identifier or a timestamp — never a path, an argument, an
// environment value, a credential or a raw error string.
type SessionLaunchReadiness struct {
	ProfileRef     string `json:"profile_ref"`
	ProfileVersion int64  `json:"profile_version"`
	Driver         string `json:"driver"`
	ProfileState   string `json:"profile_state"`
	EnvironmentRef string `json:"environment_ref"`
	// EvaluatedEnvironmentRef is the environment that answered. Empty when this
	// node has no persistent execution-environment identity at all.
	EvaluatedEnvironmentRef string                      `json:"evaluated_environment_ref,omitempty"`
	ObservedAt              string                      `json:"observed_at"`
	Selection               LaunchReadinessSelection    `json:"selection"`
	ConfigurationState      ReadinessState              `json:"configuration_state"`
	Checks                  []LaunchReadinessCheck      `json:"checks"`
	TransportCapabilities   LaunchTransportCapabilities `json:"transport_capabilities"`
	ProviderAuthentication  LaunchReadinessStatement    `json:"provider_authentication"`
	LaunchAuthorization     LaunchReadinessStatement    `json:"launch_authorization"`
	RemainingChecks         []string                    `json:"remaining_checks"`
}

// codeProfileChanged is the ratified stable identifier of this route's 409
// (contract §4). It is a CODE and not a sentence: a console that has to detect
// the conflict by matching prose is a console that breaks when the prose is
// improved or translated, and the measured defect was exactly that — the body
// carried only `error.message`, so the agreed identifier never reached the
// client.
const codeProfileChanged = "profile_changed"

// launchReadinessErr WRAPS a runErr and adds this route's stable code.
//
// ⛔ IT IS SCOPED TO THIS ROUTE ON PURPOSE. Every other run error keeps the
// envelope it has always had: widening `errorBody` or `writeRunErr` would
// re-shape the error contract of every session route at once, which is a
// separate decision with its own blast radius. This type is the whole mechanism,
// and only this route's writer serializes the code.
//
// It WRAPS rather than replaces so the status survives either way: a serializer
// that has never heard of this type still finds the *runErr through Unwrap and
// answers 409 with the ordinary envelope, instead of degrading a conflict into a
// 500 because the error stopped being the shape it recognized.
type launchReadinessErr struct {
	code string
	err  *runErr
}

func (e *launchReadinessErr) Error() string { return e.err.Error() }
func (e *launchReadinessErr) Unwrap() error { return e.err }

// errReadinessProfileChanged is the 409 of an incoherent observation: the
// profile moved under the filesystem checks, so the result describes a mixture
// of two states and is discarded rather than published.
var errReadinessProfileChanged = &launchReadinessErr{
	code: codeProfileChanged,
	err: &runErr{
		http.StatusConflict,
		"the provider profile changed during this inspection; the observation was discarded, read it again",
	},
}

// errReadinessUnavailable is the 503 of a module with no store: no DTO is
// produced, because a DTO here would certify requirements nobody looked at.
var errReadinessUnavailable = &runErr{
	http.StatusServiceUnavailable,
	"the provider profile store is not available on this node; launch requirements were not inspected",
}

// EvaluateLaunchReadiness observes the LOCAL launch requirements of one profile
// under one selection. It is the whole service behind the GET, exported so the
// composition root and the acceptance batteries drive the same code the route
// does.
//
// The order is load-bearing (§5.7): the profile is read and its version fixed,
// the environment identity is resolved, the filesystem is examined WITHOUT a
// database transaction held open, and the profile is read AGAIN. A version that
// moved in between means the checks straddle two profiles, and a straddled
// observation is refused with 409 instead of published as one.
func (m *Module) EvaluateLaunchReadiness(
	ctx context.Context,
	tenant model.TenantID,
	ref string,
	sel LaunchReadinessSelection,
) (SessionLaunchReadiness, error) {
	if m.data == nil {
		return SessionLaunchReadiness{}, errReadinessUnavailable
	}
	before, err := m.GetProfile(ctx, tenant, ref)
	if err != nil {
		return SessionLaunchReadiness{}, err
	}
	out := m.observeLaunchReadiness(ctx, before, sel)
	after, err := m.GetProfile(ctx, tenant, ref)
	if err != nil {
		return SessionLaunchReadiness{}, err
	}
	if after.Version != before.Version {
		return SessionLaunchReadiness{}, errReadinessProfileChanged
	}
	return out, nil
}

// observeLaunchReadiness is the pure evaluation over ONE profile snapshot.
func (m *Module) observeLaunchReadiness(
	ctx context.Context,
	prof ProviderProfile,
	sel LaunchReadinessSelection,
) SessionLaunchReadiness {
	env := m.rt.environmentRef
	local := env != "" && prof.EnvironmentRef == env
	// A dimension that depends on THIS node's filesystem, registry or wiring is
	// not examinable for a profile another environment administers, and it is not
	// examinable at all on a node with no environment identity. It stays unknown
	// with a code that says which, rather than being reported as an absence.
	examinable := local

	runner, program := m.runnerAndProgramChecks(ctx, prof, sel, examinable)
	checks := []LaunchReadinessCheck{
		profileStateCheck(prof),
		localEnvironmentCheck(prof, env),
		m.driverCheck(prof, sel, examinable),
		runner,
		program,
		m.homesCheck(prof, examinable),
		authSourceCheck(prof, sel),
		m.credentialSourceCheck(prof, sel, examinable),
		m.runtimeCredentialsCheck(ctx, examinable),
	}
	// The panel is COMPLETE by construction — the literal above is the whole set —
	// and the contract battery compares it against readinessCheckOrder, so a
	// dimension that stopped being emitted fails a test rather than shrinking a
	// console's panel in production.

	return SessionLaunchReadiness{
		ProfileRef: prof.Ref, ProfileVersion: prof.Version, Driver: prof.Driver,
		ProfileState: prof.State, EnvironmentRef: prof.EnvironmentRef,
		EvaluatedEnvironmentRef: env,
		ObservedAt:              model.NewTimestamp(m.now()).String(),
		Selection:               sel,
		ConfigurationState:      aggregateReadiness(checks),
		Checks:                  checks,
		TransportCapabilities:   m.transportCapabilities(prof.Driver, sel.Transport),
		ProviderAuthentication: LaunchReadinessStatement{
			State: ReadinessUnknown, Code: codeNotObservedForThisLaunch,
		},
		LaunchAuthorization: LaunchReadinessStatement{
			State: ReadinessUnknown, Code: codeEvaluatedOnSubmit,
			RequiredPermission: string(permRunWrite),
		},
		RemainingChecks: launchRemainingChecks(),
	}
}

// verdict labels one dimension's state, cause and remedy with the dimension it
// belongs to, so every check function returns a complete, self-describing row
// and the assembled list keeps one fixed order.
func verdict(check ReadinessCheck, state ReadinessState, code, remediation string) LaunchReadinessCheck {
	return LaunchReadinessCheck{Check: check, State: state, Code: code, Remediation: remediation}
}

// aggregateReadiness is the EXPLICIT precedence of §4: a known unsupported
// combination first, then a known missing configuration, then uncertainty, and
// ready only when every applicable requirement is ready. not_applicable is
// neutral — it is the absence of a requirement, not the satisfaction of one.
func aggregateReadiness(checks []LaunchReadinessCheck) ReadinessState {
	worst := ReadinessReady
	rank := func(s ReadinessState) int {
		switch s {
		case ReadinessUnsupported:
			return 3
		case ReadinessNotConfigured:
			return 2
		case ReadinessUnknown:
			return 1
		default:
			return 0
		}
	}
	for _, c := range checks {
		if rank(c.State) > rank(worst) {
			worst = c.State
		}
	}
	return worst
}

// --- the nine dimensions ------------------------------------------------------

// profileStateCheck reuses the profile lifecycle exactly as snapshotForLaunch
// reads it: active launches, disabled refuses reversibly, retired is final.
func profileStateCheck(prof ProviderProfile) LaunchReadinessCheck {
	switch prof.State {
	case ProfileActive:
		return verdict(CheckProfile, ReadinessReady, codeProfileActive, "")
	case ProfileDisabled:
		return verdict(CheckProfile, ReadinessNotConfigured, codeProfileDisabled, remediationReviewProfile)
	default:
		return verdict(CheckProfile, ReadinessNotConfigured, codeProfileRetired, remediationReviewProfile)
	}
}

// localEnvironmentCheck answers only about locality. A foreign profile is
// unsupported HERE — this node does not run it and does not forward to the node
// that does — and a node with no environment identity keeps profiled launches
// deny-closed, which is a known configuration absence rather than a mystery.
func localEnvironmentCheck(prof ProviderProfile, env string) LaunchReadinessCheck {
	switch {
	case env == "":
		return verdict(CheckLocalEnvironment, ReadinessNotConfigured, codeEnvironmentUnavailable, remediationConfigureEnvironment)
	case prof.EnvironmentRef != env:
		return verdict(CheckLocalEnvironment, ReadinessUnsupported, codeOtherEnvironment, remediationSelectLocalProfile)
	default:
		return verdict(CheckLocalEnvironment, ReadinessReady, codeEnvironmentLocal, "")
	}
}

// driverCheck reports whether this runtime OPERATES the profile's driver under
// the requested transport. It reuses driverOperable — the same predicate the
// launch refuses on — so a change there cannot leave this read behind.
//
// The transport half is decided BEFORE locality, deliberately: remote-control
// on a non-Claude driver is refused by resolveLaunchProfileInto on every node,
// so calling it unknown for a foreign profile would hide a fact this read knows.
func (m *Module) driverCheck(
	prof ProviderProfile,
	sel LaunchReadinessSelection,
	examinable bool,
) LaunchReadinessCheck {
	if Transport(sel.Transport) == TransportRemoteControl && prof.Driver != providerDriverClaude {
		return verdict(CheckDriver, ReadinessUnsupported, codeTransportUnsupported, remediationSelectSupportedSelection)
	}
	if !examinable {
		return verdict(CheckDriver, ReadinessUnknown, codeNotCheckedInThisEnvironment, "")
	}
	if m.driverOperable(prof.Driver) {
		return verdict(CheckDriver, ReadinessReady, codeDriverOperable, "")
	}
	return verdict(CheckDriver, ReadinessNotConfigured, codeDriverNotRegistered, remediationConfigureDriver)
}

// runnerAndProgramChecks are produced together because the program verdict is
// the Runner's answer: only the Runner knows how it resolves an executable, and
// a Runner with no inspection seam therefore leaves BOTH dimensions unknown
// rather than having this module guess with exec.LookPath on its behalf.
//
// ⛔ The seam is OPTIONAL by construction. Runner keeps exactly one required
// method; an external Runner that never heard of this read keeps launching, and
// the console can still ask for a launch under a declared "check incomplete".
// Nothing here turns that unknown into ready.
func (m *Module) runnerAndProgramChecks(
	ctx context.Context,
	prof ProviderProfile,
	sel LaunchReadinessSelection,
	examinable bool,
) (LaunchReadinessCheck, LaunchReadinessCheck) {
	runner := LaunchReadinessCheck{Check: CheckRunner}
	program := LaunchReadinessCheck{Check: CheckProgram}
	notChecked := func() (LaunchReadinessCheck, LaunchReadinessCheck) {
		runner.State, runner.Code = ReadinessUnknown, codeNotCheckedInThisEnvironment
		program.State, program.Code = ReadinessUnknown, codeNotCheckedInThisEnvironment
		return runner, program
	}
	if !examinable {
		return notChecked()
	}
	if _, unwired := m.rt.runner.(unwiredRunner); unwired {
		runner.State, runner.Code = ReadinessNotConfigured, codeRunnerNotConfigured
		runner.Remediation = remediationConfigureRunner
		// No Runner means nobody resolves a program: saying "missing" would blame
		// the operator's binary for a wiring absence.
		program.State, program.Code = ReadinessUnknown, codeNotCheckedInThisEnvironment
		return runner, program
	}
	inspector, ok := m.rt.runner.(RunnerInspector)
	if !ok {
		runner.State, runner.Code = ReadinessUnknown, codeRunnerInspectionUnavailable
		runner.Remediation = remediationRetryInspection
		program.State, program.Code = ReadinessUnknown, codeInspectionUnavailable
		program.Remediation = remediationRetryInspection
		return runner, program
	}
	obs, err := inspector.InspectLaunch(ctx, RunnerInspection{
		Program: m.readinessProgram(prof.Driver), Isolation: Isolation(sel.Isolation),
	})
	if err != nil {
		// A Runner that could not answer is not a Runner that refused. The raw
		// error never leaves this function.
		runner.State, runner.Code = ReadinessUnknown, codeInspectionUnavailable
		runner.Remediation = remediationRetryInspection
		program.State, program.Code = ReadinessUnknown, codeInspectionUnavailable
		program.Remediation = remediationRetryInspection
		return runner, program
	}
	switch obs.Isolation {
	case RunnerIsolationSupported:
		runner.State, runner.Code = ReadinessReady, codeRunnerReady
	case RunnerIsolationUnsupported:
		runner.State, runner.Code = ReadinessUnsupported, codeIsolationUnsupported
		runner.Remediation = remediationSelectSupportedSelection
	default:
		runner.State, runner.Code = ReadinessUnknown, codeInspectionUnavailable
		runner.Remediation = remediationRetryInspection
	}
	switch obs.Program {
	case RunnerProgramExecutable:
		// Found and executable BY METADATA. Not the official build, not a
		// compatible version, not a working CLI: the handshake decides those and it
		// is in remaining_checks.
		program.State, program.Code = ReadinessReady, codeProgramPresent
	case RunnerProgramMissing:
		program.State, program.Code = ReadinessNotConfigured, codeProgramMissing
		program.Remediation = remediationConfigureProgram
	case RunnerProgramNotExecutable:
		program.State, program.Code = ReadinessNotConfigured, codeProgramNotExecutable
		program.Remediation = remediationConfigureProgram
	default:
		program.State, program.Code = ReadinessUnknown, codeInspectionUnavailable
		program.Remediation = remediationRetryInspection
	}
	return runner, program
}

// readinessProgram is the EFFECTIVE executable of a launch under this driver,
// taken from the same two places buildLaunchSpec takes it from: m.rt.program for
// the historical Claude path, driverProgram for a registered driver. It is never
// re-read from an environment variable — after boot those are the operator's
// remedy, not an independent source of truth — and never from a console catalog.
func (m *Module) readinessProgram(driver string) string {
	if d, ok := m.driverFor(driver); ok {
		return m.driverProgram(d)
	}
	return m.rt.program
}

// homesCheck re-runs the launch's OWN home revalidation (revalidateHome), then
// classifies the refusal into a closed code. The ready verdict is exactly
// revalidateHome's, so this read cannot drift from the launch; the second pass
// only decides WHICH cause to name, and defaults to unknown when it cannot tell.
//
// The homes are examined as DIRECTORIES at their registered canonical location.
// Their contents are never listed and no file inside them is ever opened: a
// login lives in there, and a valid home is not an authenticated account.
func (m *Module) homesCheck(prof ProviderProfile, examinable bool) LaunchReadinessCheck {
	if !examinable {
		return verdict(CheckHomes, ReadinessUnknown, codeNotCheckedInThisEnvironment, "")
	}
	configErr := revalidateHome("config_home", prof.ConfigHome)
	userErr := revalidateHome("user_home", prof.UserHome)
	if configErr == nil && userErr == nil {
		return verdict(CheckHomes, ReadinessReady, codeHomesResolved, "")
	}
	stored := prof.ConfigHome
	if configErr == nil {
		stored = prof.UserHome
	}
	switch classifyHome(stored) {
	case codeHomeUnavailable:
		return verdict(CheckHomes, ReadinessNotConfigured, codeHomeUnavailable, remediationReviewProfileHomes)
	case codeHomeChanged:
		return verdict(CheckHomes, ReadinessNotConfigured, codeHomeChanged, remediationReviewProfileHomes)
	default:
		return verdict(CheckHomes, ReadinessUnknown, codeInspectionUnavailable, remediationRetryInspection)
	}
}

// classifyHome names the cause of a home that no longer revalidates, using the
// same primitives canonicalHome uses. It returns a closed code and NEVER the
// path or the raw error.
func classifyHome(stored string) string {
	if strings.TrimSpace(stored) == "" {
		return codeHomeUnavailable
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(stored))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return codeHomeUnavailable
		}
		return codeInspectionUnavailable
	}
	st, err := os.Stat(resolved)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return codeHomeUnavailable
		}
		return codeInspectionUnavailable
	}
	if !st.IsDir() {
		return codeHomeUnavailable
	}
	if resolved != stored {
		// The alias now points somewhere else. Launching into it would be launching
		// into an identity nobody registered, which is why the launch refuses too.
		return codeHomeChanged
	}
	// The home looks fine now and the launch check refused a moment ago: a race,
	// not a diagnosis. Uncertainty is reported as uncertainty.
	return codeInspectionUnavailable
}

// authSourceCheck reuses requireAuthSourceForDriver — the same deny-closed
// predicate the launch applies — and then applies the ratified precision for
// the one combination where the authorized source is NOT the one that acts.
//
// ⛔ CLAUDE remote-control DOES NOT MINT in this code (maybeMint returns nothing
// for a transport that is not stream-json). A profile that authorizes
// managed_injection and is asked about remote-control therefore gets `unknown`
// with managed_injection_not_applied_for_transport: the selection is authorized
// and the injection it names will not be applied, so this read certifies neither
// managed injection nor an authenticated account. The launch is UNCHANGED — no
// new refusal, no added fallback to the account home — and the discrepancy is
// published instead of hidden.
func authSourceCheck(prof ProviderProfile, sel LaunchReadinessSelection) LaunchReadinessCheck {
	if err := requireAuthSourceForDriver(prof.Driver, prof.AuthSource); err != nil {
		return verdict(CheckAuthSource, ReadinessNotConfigured, codeAuthSourceRequired, remediationAuthorizeAuthSource)
	}
	if managedInjectionNotApplied(prof.Driver, prof.AuthSource, sel.Transport) {
		return verdict(CheckAuthSource, ReadinessUnknown, codeManagedInjectionNotAppliedForTransport, "")
	}
	switch prof.AuthSource {
	case AuthSourceAccountHome:
		return verdict(CheckAuthSource, ReadinessReady, codeAuthSourceAccountHome, "")
	case AuthSourceManagedInjection:
		return verdict(CheckAuthSource, ReadinessReady, codeAuthSourceManagedInjection, "")
	default:
		return verdict(CheckAuthSource, ReadinessReady, codeAuthSourceLegacyClaude, "")
	}
}

// managedInjectionNotApplied is the single named condition of the precision
// above, in one place so the auth_source and credential_source halves cannot
// disagree about it.
func managedInjectionNotApplied(driver, source, transport string) bool {
	return driver == providerDriverClaude &&
		source == AuthSourceManagedInjection &&
		Transport(transport) != TransportStreamJSON
}

// credentialSourceCheck reports the WIRING of the credential this selection
// would need — never a mint, never a token file read, never a call to WIF or
// SPIRE. A wired source is ready for wiring and nothing else: the mint itself
// stays in remaining_checks.
//
// The four cases mirror mintLaunchAuthority exactly, including its refusal to
// let one provider's bearer serve another's managed launch.
func (m *Module) credentialSourceCheck(
	prof ProviderProfile,
	sel LaunchReadinessSelection,
	examinable bool,
) LaunchReadinessCheck {
	if err := requireAuthSourceForDriver(prof.Driver, prof.AuthSource); err != nil {
		// Which credential path applies is not yet decided, so this is uncertainty
		// about a requirement, not the absence of one: not_applicable would claim
		// the requirement does not exist.
		return verdict(CheckCredentialSource, ReadinessUnknown, codeAuthSourceRequired, remediationAuthorizeAuthSource)
	}
	if prof.AuthSource == AuthSourceAccountHome {
		// Nothing is injected: the authorized account home IS the credential, and
		// demanding a global WIF on top of it is the conflation §5.1 forbids.
		return verdict(CheckCredentialSource, ReadinessNotApplicable, codeCredentialSourceNotInjected, "")
	}
	if prof.Driver == providerDriverClaude {
		if Transport(sel.Transport) != TransportStreamJSON {
			// Lifecycle-only: this launch injects no inference bearer at all.
			if managedInjectionNotApplied(prof.Driver, prof.AuthSource, sel.Transport) {
				return verdict(CheckCredentialSource, ReadinessNotApplicable, codeManagedInjectionNotAppliedForTransport, "")
			}
			return verdict(CheckCredentialSource, ReadinessNotApplicable, codeCredentialSourceNotInjected, "")
		}
		if !examinable {
			return verdict(CheckCredentialSource, ReadinessUnknown, codeNotCheckedInThisEnvironment, "")
		}
		if _, deny := m.rt.creds.(denyCredentialSource); deny {
			return verdict(CheckCredentialSource, ReadinessNotConfigured, codeClaudeCredentialSourceNotConfigured, remediationConfigureClaudeCredential)
		}
		return verdict(CheckCredentialSource, ReadinessReady, codeClaudeCredentialSourceConfigured, "")
	}
	if !examinable {
		return verdict(CheckCredentialSource, ReadinessUnknown, codeNotCheckedInThisEnvironment, "")
	}
	if src, ok := m.rt.providerCreds[prof.Driver]; !ok || src == nil {
		return verdict(CheckCredentialSource, ReadinessNotConfigured, codeProviderAdapterNotConfigured, remediationConfigureProviderAdapter)
	}
	return verdict(CheckCredentialSource, ReadinessReady, codeProviderCredentialAdapterConfigured, "")
}

// runtimeCredentialsCheck reports the EFFECTIVE work/K3 dependency, which can
// deny a launch on its own even when the inference credential exists
// (ensureRuntimeCredentialWiring / ensureRuntimeCredentialReadiness). Two
// issuers are required, not one flag.
//
// ⛔ IT NEVER ACTIVATES OR REPAIRS ANYTHING. EvaluateCommunicationReadiness
// samples the bound witnesses and, by its own contract, never enables runtime
// credentials as a side effect. A standalone module that does not ask for the
// dual posture reports not_applicable: its absence is not a universal defect.
func (m *Module) runtimeCredentialsCheck(ctx context.Context, examinable bool) LaunchReadinessCheck {
	// Locality comes FIRST here and not, as it does for auth_source, after the
	// applicability question — because whether the dual posture is requested at
	// all is a fact about THIS node's composition. Answering not_applicable for a
	// profile another environment administers would be this node stating that the
	// OTHER node does not ask for runtime credentials, which it cannot know.
	if !examinable {
		return verdict(CheckRuntimeCredentials, ReadinessUnknown, codeNotCheckedInThisEnvironment, "")
	}
	if !m.rt.communicationCredentialsEnabled {
		return verdict(CheckRuntimeCredentials, ReadinessNotApplicable, codeRuntimeCredentialsNotRequested, "")
	}
	if err := m.ensureRuntimeCredentialWiring(); err != nil {
		return verdict(CheckRuntimeCredentials, ReadinessNotConfigured, codeRuntimeCredentialWiringPartial, remediationReviewRuntimeCredentials)
	}
	if !m.communicationReadinessComposed() {
		// Wiring is complete and no dynamic witness was bound, which is exactly the
		// state the launch path treats as satisfied.
		return verdict(CheckRuntimeCredentials, ReadinessReady, codeRuntimeCredentialsWired, "")
	}
	readiness, err := m.EvaluateCommunicationReadiness(ctx)
	if err != nil || len(readiness.Unavailable) > 0 {
		return verdict(CheckRuntimeCredentials, ReadinessUnknown, codeRuntimeReadinessUnavailable, remediationRetryInspection)
	}
	if !readiness.Effective {
		return verdict(CheckRuntimeCredentials, ReadinessNotConfigured, codeRuntimeCredentialWiringPartial, remediationReviewRuntimeCredentials)
	}
	return verdict(CheckRuntimeCredentials, ReadinessReady, codeRuntimeCredentialsWired, "")
}

// transportCapabilities describes the channel this selection would open. It is
// resolved from the REAL seam of the driver that would run — the historical
// Claude path by name, a registered driver through its optional transport
// profile — and answers unknown for a registered driver that publishes none.
// Nothing here infers a protocol from a driver key's resemblance to another's.
func (m *Module) transportCapabilities(driver, transport string) LaunchTransportCapabilities {
	unknown := LaunchTransportCapabilities{Protocol: protocolUnknown, IO: ioUnknown, Input: inputUnknown}
	if driver == providerDriverClaude {
		switch Transport(transport) {
		case TransportStreamJSON:
			return LaunchTransportCapabilities{
				Protocol: protocolClaudeStreamJSON, IO: ioBidirectional, Input: inputLine,
			}
		case TransportRemoteControl:
			// Lifecycle only: the child's I/O is relayed by the provider, so there is
			// no bridged turn console to offer and no line to send.
			return LaunchTransportCapabilities{
				Protocol: protocolClaudeRemoteControl, IO: ioLifecycleOnly, Input: inputUnavailable,
			}
		default:
			return unknown
		}
	}
	if Transport(transport) != TransportStreamJSON {
		// remote-control is a Claude Code transport; no registered driver has it.
		return unknown
	}
	d, ok := m.driverFor(driver)
	if !ok {
		return unknown
	}
	profile, ok := d.(ProviderDriverTransport)
	if !ok {
		// A registered driver with no transport metadata. Its own contract is the
		// only source, and it published none.
		return unknown
	}
	return normalizeTransportProfile(profile.TransportProfile())
}

// normalizeTransportProfile keeps the published vocabulary closed even if a
// driver outside this package answers with something else.
func normalizeTransportProfile(p DriverTransportProfile) LaunchTransportCapabilities {
	out := LaunchTransportCapabilities{Protocol: protocolUnknown, IO: ioUnknown, Input: inputUnknown}
	switch p.Protocol {
	case protocolClaudeStreamJSON, protocolClaudeRemoteControl, protocolCodexAppServer, protocolGrokACP, protocolOpenCodeACP:
		out.Protocol = p.Protocol
	}
	switch p.IO {
	case ioBidirectional, ioLifecycleOnly:
		out.IO = p.IO
	}
	switch p.Input {
	case inputLine, inputText, inputUnavailable:
		out.Input = p.Input
	}
	return out
}
