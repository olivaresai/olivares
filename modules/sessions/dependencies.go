// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	obstrace "github.com/olivaresai/olivares/core/observability/trace"
)

// Dependencies is the engine's single sessions dependency set. The engine owns
// its assembly before runtime Init, including the recovery ports needed before
// leadership acquisition. Ports are read-only after their first consumer starts;
// Start checks required ports without replacing or mutating this record.
// Optional ports retain their existing OFF, refusal, or default behavior.
type Dependencies struct {
	// Data retains ordinary tenant service/standing guards. RecoveryData is the
	// distinct custody-only, residency-guarded handle used during promotion;
	// runtime mint/renew must never inherit its service-suspension bypass.
	Data                             api.ModuleData
	Standing                         auth.StandingReader
	RecoveryData                     api.ModuleData
	WorkspaceSnapshotAuthority       func(context.Context, model.TenantID, auth.Principal, string, model.ID) (string, error)
	WorkIdentity                     WorkIdentityResolver
	WorkContent                      WorkContentGuard
	WorkEventSink                    WorkEventSink
	WorkAuthorizer                   WorkAuthorizer
	OrchestrationScopes              OrchestrationWorkScopeSource
	WorkOutboxAuthority              WorkOutboxClaimPolicy
	ProtocolBindingReconciler        ProtocolBindingRemoteReconciler
	ProtocolLocalResourceResolver    ProtocolLocalResourceResolver
	ProviderSources                  ProviderSourceResolver
	ToolLogin                        ToolLoginStatus
	ProfileLogin                     ProfileLoginStatus
	CommunicationSealer              CommunicationContentSealer
	CommunicationAuthority           *CommunicationRequestAuthority
	CommunicationDirectoryResolver   DirectorySnapshotResolver
	CommunicationAudienceAttestor    PublicationAudienceAttestor
	CommunicationGrantClosure        ChannelGrantSubjectClosureResolver
	CommunicationReadAuthorizer      CoreEntityReadAuthorizer
	CommunicationOperationAuthorizer CoreEntityOperationAuthorizer
	CommunicationGuardData           communicationGuardReconciliationData
	CommunicationStoreReadiness      CommunicationStoreReadinessWitness
	CommunicationPumpReadiness       CommunicationPumpReadinessWitness
	ManagedStopAuthority             *ManagedStopAuthority
	CursorKeyring                    *CursorTokenKeys
	Runner                           Runner
	SessionMCP                       SessionMCPLaunchSource
	GitRead                          SessionGitReadSource
	GitReadDataDir                   string
	Creds                            CredentialSource
	LaunchGate                       LaunchGate
	StopGate                         StopGate
	Recorder                         Recorder
	Classifier                       Classifier
	CostSink                         SessionCostSink
	ListPricer                       ListPricer
	WorkSessionCreds                 WorkSessionCredentialSource
	CommunicationSessionCreds        CommunicationSessionCredentialSource
	// The enabled bit distinguishes an intentional K3 OFF posture from a lost
	// issuer on a configured deployment; the latter remains deny-closed.
	CommunicationCredentialsEnabled   bool
	RecoveryWorkSessionCreds          WorkSessionCredentialSource
	RecoveryCommunicationSessionCreds CommunicationSessionCredentialSource
	ProgramResolver                   func(driver string) string
	ApprovalGate                      ProviderApprovalGate
	ProviderVault                     ProviderSecretVault
	ProviderProbe                     ProviderProbe
	HostTools                         HostToolObserver
	// SessionAccessCheck terminates runs whose current owner loses access.
	SessionAccessCheck        func(context.Context, model.TenantID, string) (auth.SessionScope, string, error)
	Tracer                    *obstrace.Provider
	ApprovalRecoveryTenants   func(context.Context) ([]model.TenantID, error)
	QueuedCredentialCapture   func(context.Context, auth.QueuedCredential) (auth.QueuedCredential, error)
	ProviderApprovalPolicy    ProviderApprovalPolicy
	ProviderApprovalPrincipal func(context.Context, model.TenantID, string) (auth.Principal, string, error)
	QueuedLaunchAuthorization func(context.Context, model.TenantID, auth.QueuedCredential, string, model.ID) (auth.Principal, error)
}

// normalize canonicalizes absent interface ports, including typed nils. It does
// not inspect an adapter, call it, or expose any dependency value.
func (d *Dependencies) normalize() {
	fields := reflect.ValueOf(d).Elem()
	for i := 0; i < fields.NumField(); i++ {
		f := fields.Field(i)
		if f.Kind() == reflect.Interface && !communicationPortBound(f.Interface()) {
			f.SetZero()
		}
	}

	if d.Runner == nil {
		d.Runner = unwiredRunner{}
	}
	if d.Creds == nil {
		d.Creds = denyCredentialSource{}
	}
	if d.LaunchGate == nil {
		d.LaunchGate = allowLaunchGate{}
	}
	if d.StopGate == nil {
		d.StopGate = allowStopGate{}
	}
	if d.Recorder == nil {
		d.Recorder = noopRecorder{}
	}
	if d.ApprovalGate == nil {
		d.ApprovalGate = denyProviderApprovalGate{}
	}
}

func (m *Module) checkDependencies() error {
	if !m.engineDependencies {
		return nil
	}
	var missing []string
	for _, check := range m.dependencyChecks() {
		if !communicationPortBound(check.port) && check.required {
			missing = append(missing, check.name+" ("+check.effect+")")
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("sessions: missing required dependencies: %s", strings.Join(missing, "; "))
	}
	return nil
}

type dependencyCheck struct {
	name     string
	port     any
	effect   string
	required bool
}

func configuredDependency(port any) any {
	switch port.(type) {
	case unwiredRunner, denyCredentialSource, allowLaunchGate, allowStopGate, noopRecorder, denyProviderApprovalGate:
		return nil
	default:
		return port
	}
}

func (m *Module) dependencyChecks() []dependencyCheck {
	d := m.Dependencies
	authority := d.CommunicationAuthority
	if !authority.bound() {
		authority = nil
	}
	cursor := d.CursorKeyring
	if !m.CommunicationCursorTokenKeyringBound() {
		cursor = nil
	}
	checks := []dependencyCheck{
		{"Data", d.Data, "persistent sessions and work", true},
		{"Standing", d.Standing, "account-fenced writes", true},
		{"RecoveryData", d.RecoveryData, "residency-guarded credential recovery", true},
		{"WorkspaceSnapshotAuthority", d.WorkspaceSnapshotAuthority, "authorized workspace snapshots", true},
		{"WorkIdentity", d.WorkIdentity, "owner-bearing work commands", true},
		{"WorkContent", d.WorkContent, "durable content inspection", true},
		{"WorkEventSink", d.WorkEventSink, "work event publication", true},
		{"WorkAuthorizer", d.WorkAuthorizer, "shared work authorization", true},
		{"OrchestrationScopes", d.OrchestrationScopes, "orchestration work scope", true},
		{"WorkOutboxAuthority", d.WorkOutboxAuthority, "outbox claim authority", true},
		{"ProtocolBindingReconciler", d.ProtocolBindingReconciler, "authenticated remote protocol reconciliation", false},
		{"ProtocolLocalResourceResolver", d.ProtocolLocalResourceResolver, "local protocol resource authorization", true},
		{"ProviderSources", d.ProviderSources, "provider source bindings", true},
		{"ToolLogin", d.ToolLogin, "tool login resolution", true},
		{"ProfileLogin", d.ProfileLogin, "profile login checks", true},
		{"CommunicationSealer", d.CommunicationSealer, "communication payload sealing", false},
		{"CommunicationAuthority", authority, "authenticated communication authority", true},
		{"CommunicationDirectoryResolver", d.CommunicationDirectoryResolver, "communication audience resolution", false},
		{"CommunicationAudienceAttestor", d.CommunicationAudienceAttestor, "publication audience attestation", false},
		{"CommunicationGrantClosure", d.CommunicationGrantClosure, "communication subject closure", false},
		{"CommunicationReadAuthorizer", d.CommunicationReadAuthorizer, "external communication read authorization", false},
		{"CommunicationOperationAuthorizer", d.CommunicationOperationAuthorizer, "external communication write authorization", false},
		{"CommunicationGuardData", d.CommunicationGuardData, "communication guard repair", false},
		{"CommunicationStoreReadiness", d.CommunicationStoreReadiness, "communication store readiness", false},
		{"CommunicationPumpReadiness", d.CommunicationPumpReadiness, "communication effects", false},
		{"ManagedStopAuthority", d.ManagedStopAuthority, "fenced managed Stop", true},
		{"CursorKeyring", cursor, "signed communication cursors", false},
		{"Runner", configuredDependency(d.Runner), "session process launches", false},
		{"SessionMCP", d.SessionMCP, "session MCP configuration", true},
		{"GitRead", d.GitRead, "session repository credentials", false},
		{"Creds", configuredDependency(d.Creds), "legacy inference credential minting", false},
		{"LaunchGate", configuredDependency(d.LaunchGate), "governed session admission", true},
		{"StopGate", configuredDependency(d.StopGate), "emergency-stop admission", true},
		{"Recorder", configuredDependency(d.Recorder), "governed I/O evidence", true},
		{"Classifier", d.Classifier, "governed read classification", false},
		{"CostSink", d.CostSink, "session spend attribution", true},
		{"ListPricer", d.ListPricer, "token-only usage pricing", true},
		{"WorkSessionCreds", d.WorkSessionCreds, "session work credentials", true},
		{"CommunicationSessionCreds", d.CommunicationSessionCreds, "communication session credentials", true},
		{"RecoveryWorkSessionCreds", d.RecoveryWorkSessionCreds, "work credential withdrawal during recovery", true},
		{"RecoveryCommunicationSessionCreds", d.RecoveryCommunicationSessionCreds, "communication credential withdrawal during recovery", true},
		{"ProgramResolver", d.ProgramResolver, "managed executable override", false},
		{"ApprovalGate", configuredDependency(d.ApprovalGate), "provider approvals", true},
		{"ProviderVault", d.ProviderVault, "provider credential custody", false},
		{"ProviderProbe", d.ProviderProbe, "provider connection probes", false},
		{"HostTools", d.HostTools, "host tool observation", false},
		{"SessionAccessCheck", d.SessionAccessCheck, "owner access termination", true},
		{"Tracer", d.Tracer, "session traces", false},
		{"ApprovalRecoveryTenants", d.ApprovalRecoveryTenants, "cross-tenant waiting-launch recovery", false},
		{"QueuedCredentialCapture", d.QueuedCredentialCapture, "queued-launch credential capture", true},
		{"ProviderApprovalPolicy", d.ProviderApprovalPolicy, "provider approval policy", true},
		{"ProviderApprovalPrincipal", d.ProviderApprovalPrincipal, "provider approval identity", true},
		{"QueuedLaunchAuthorization", d.QueuedLaunchAuthorization, "queued-launch authorization", true},
	}
	if d.ManagedStopAuthority != nil {
		checks = append(checks,
			dependencyCheck{"ManagedStopAuthority.resolver", d.ManagedStopAuthority.resolver, "managed Stop identity", true},
			dependencyCheck{"ManagedStopAuthority.authorizer", d.ManagedStopAuthority.authorizer, "managed Stop authorization", true},
			dependencyCheck{"ManagedStopAuthority.elector", d.ManagedStopAuthority.elector, "managed Stop fencing", true})
	}
	return checks
}

func (m *Module) reportOptionalDependencies() {
	if !m.engineDependencies || m.log == nil {
		return
	}
	var missing []string
	for _, check := range m.dependencyChecks() {
		if !check.required && !communicationPortBound(check.port) {
			missing = append(missing, check.name+" ("+check.effect+")")
		}
	}
	if len(missing) > 0 {
		m.log.Info("sessions: optional dependencies unavailable", "disabled", missing)
	}
}
