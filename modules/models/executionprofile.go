// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package models

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// D01-C2A deliberately recognizes one narrow operation and protocol. These values
// describe a future guarded dispatch; C2A never invokes a chat executor or transport.
const (
	ExecutionActionTextGenerate       = "text.generate"
	ExecutionProtocolChatTextV1       = "chat-completions.text.v1"
	ExecutionAdapterModelProviderChat = "olivares.modelprovider.chat-text"
	ExecutionAdapterVersion1          = "1"
)

// ErrExecutionProfileUnavailable is intentionally opaque. A missing, foreign or
// removed profile has the same result so a tenant cannot enumerate another tenant's
// operator configuration.
var ErrExecutionProfileUnavailable = errors.New("models: execution profile unavailable")

// ExecutionProfile is the non-secret, immutable snapshot the models module needs to
// bind a routing decision. CredentialRef and resolved credential values remain in the
// composition root; CredentialAudience is public authority metadata, not a secret.
type ExecutionProfile struct {
	Tenant             model.TenantID
	Ref                string
	Revision           string
	Action             string
	Protocol           string
	AdapterID          string
	AdapterVersion     string
	ProviderRef        string
	ModelRef           string
	Endpoint           string
	Surface            string
	InferenceGeo       string
	CredentialAudience string
	AuthScheme         string
	TransportKey       string
	AllowHTTP          bool
	MaxRequestBytes    int
	MaxResponseBytes   int
	Timeout            time.Duration
}

// ExecutionProfileResolver performs an immutable tenant-scoped lookup. It performs
// no credential resolution or network I/O. The composition root supplies the registry.
type ExecutionProfileResolver interface {
	ResolveExecutionProfile(context.Context, model.TenantID, string, string) (ExecutionProfile, error)
}

type unavailableExecutionProfileResolver struct{}

func (unavailableExecutionProfileResolver) ResolveExecutionProfile(context.Context, model.TenantID, string, string) (ExecutionProfile, error) {
	return ExecutionProfile{}, ErrExecutionProfileUnavailable
}

type executionProfileHTTPError struct {
	status  int
	code    string
	message string
}

func profilePinInvalid() *executionProfileHTTPError {
	return &executionProfileHTTPError{
		status: http.StatusBadRequest, code: "execution_profile_pin_required",
		message: "execution_profile_ref and execution_profile_revision must be supplied together",
	}
}

func profileRevisionInvalid() *executionProfileHTTPError {
	return &executionProfileHTTPError{
		status: http.StatusBadRequest, code: "execution_profile_revision_invalid",
		message: "execution_profile_revision must be sha256 followed by 64 lowercase hexadecimal characters",
	}
}

func profileUnavailable() *executionProfileHTTPError {
	return &executionProfileHTTPError{
		status: http.StatusServiceUnavailable, code: "execution_profile_unavailable",
		message: "the pinned execution profile is unavailable",
	}
}

func profileBindingMismatch() *executionProfileHTTPError {
	return &executionProfileHTTPError{
		status: http.StatusUnprocessableEntity, code: "profile_binding_mismatch",
		message: "the routing policy or resolved target does not match the pinned execution profile",
	}
}

func unsupportedProfileProtocol() *executionProfileHTTPError {
	return &executionProfileHTTPError{
		status: http.StatusUnprocessableEntity, code: "unsupported_protocol",
		message: "the pinned execution profile uses an unsupported protocol",
	}
}

func unsupportedExecutionOperation() *executionProfileHTTPError {
	return &executionProfileHTTPError{
		status: http.StatusUnprocessableEntity, code: "unsupported_operation",
		message: "the requested execution operation is unsupported",
	}
}

func executionProfileRequired() *executionProfileHTTPError {
	return &executionProfileHTTPError{
		status: http.StatusUnprocessableEntity, code: "execution_profile_required",
		message: "operation requires a pinned execution profile",
	}
}

func chatExecutionUnavailable() *executionProfileHTTPError {
	return &executionProfileHTTPError{
		status: http.StatusServiceUnavailable, code: "chat_execution_unavailable",
		message: "the pinned Chat execution path is not available in this build",
	}
}

func executionProfileErrorBody(e *executionProfileHTTPError) map[string]any {
	return map[string]any{"error": map[string]string{"code": e.code, "message": e.message}}
}

func writeExecutionProfileError(w http.ResponseWriter, e *executionProfileHTTPError) {
	writeJSON(w, e.status, executionProfileErrorBody(e))
}

func (s routingSpec) hasExecutionProfile() bool {
	return strings.TrimSpace(s.ExecutionProfileRef) != "" && strings.TrimSpace(s.ExecutionProfileRevision) != ""
}

func (s routingSpec) hasPartialExecutionProfile() bool {
	return (strings.TrimSpace(s.ExecutionProfileRef) == "") != (strings.TrimSpace(s.ExecutionProfileRevision) == "")
}

// resolveExecutionProfile validates the policy-to-profile binding without resolving
// a credential. The registry has already content-verified the profile revision; this
// method fixes the exact operation, protocol, target, endpoint and effective surface.
func (m *Module) resolveExecutionProfile(ctx context.Context, tenant model.TenantID, spec routingSpec) (ExecutionProfile, *executionProfileHTTPError) {
	if spec.hasPartialExecutionProfile() {
		return ExecutionProfile{}, profilePinInvalid()
	}
	if !spec.hasExecutionProfile() {
		return ExecutionProfile{}, nil
	}
	if !validExecutionProfileRevision(spec.ExecutionProfileRevision) {
		return ExecutionProfile{}, profileRevisionInvalid()
	}
	p, err := m.executionProfiles.ResolveExecutionProfile(
		ctx, tenant, strings.TrimSpace(spec.ExecutionProfileRef), strings.TrimSpace(spec.ExecutionProfileRevision),
	)
	if err != nil || p.Tenant != tenant || p.Ref != strings.TrimSpace(spec.ExecutionProfileRef) ||
		p.Revision != strings.TrimSpace(spec.ExecutionProfileRevision) {
		return ExecutionProfile{}, profileUnavailable()
	}
	if p.Action != ExecutionActionTextGenerate {
		return ExecutionProfile{}, unsupportedExecutionOperation()
	}
	if p.Protocol != ExecutionProtocolChatTextV1 || p.AdapterID != ExecutionAdapterModelProviderChat ||
		p.AdapterVersion != ExecutionAdapterVersion1 {
		return ExecutionProfile{}, unsupportedProfileProtocol()
	}
	if p.ProviderRef == "" || p.ModelRef == "" || p.Endpoint == "" || p.Surface == "" ||
		p.InferenceGeo == "" || p.CredentialAudience == "" || p.TransportKey == "" ||
		p.MaxRequestBytes <= 0 || p.MaxResponseBytes <= 0 || p.Timeout <= 0 {
		return ExecutionProfile{}, profileUnavailable()
	}
	if p.AuthScheme != "bearer" && p.AuthScheme != "none" {
		return ExecutionProfile{}, profileUnavailable()
	}
	if strings.TrimSpace(spec.PinnedModel) != p.ModelRef ||
		(spec.GatewayEndpoint != "" && spec.GatewayEndpoint != p.Endpoint) {
		return ExecutionProfile{}, profileBindingMismatch()
	}
	return p, nil
}

func validExecutionProfileRevision(revision string) bool {
	const prefix = "sha256:"
	if len(revision) != len(prefix)+64 || !strings.HasPrefix(revision, prefix) {
		return false
	}
	for _, c := range revision[len(prefix):] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func executionProfileMatchesTarget(p ExecutionProfile, target *targetDTO) bool {
	return target != nil && target.ProviderRef == p.ProviderRef && target.ModelRef == p.ModelRef &&
		(!target.ViaGateway || target.Endpoint == p.Endpoint)
}
