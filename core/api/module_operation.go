// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// ModuleOperationErrorBody exposes only the closed admission answer.
func ModuleOperationErrorBody(err error) (int, map[string]any) {
	switch {
	case errors.Is(err, auth.ErrUnauthenticated):
		return http.StatusUnauthorized, ErrorBody("unauthenticated", "authentication required")
	case errors.Is(err, ErrModuleOperationAdmission):
		return http.StatusForbidden, ErrorBody("forbidden", "module operation admission required")
	case errors.Is(err, ErrModuleOperationDisabled):
		return http.StatusNotFound, ErrorBody("module_not_enabled", "the requested module is not enabled on this node")
	case errors.Is(err, ErrRecordingConsentRequired):
		return http.StatusForbidden, ErrorBody("recording_consent_required", "recording consent required")
	case errors.Is(err, errRecordingUnavailable):
		return http.StatusServiceUnavailable, ErrorBody("recording_unavailable", "session recording unavailable; privileged surfaces are deny-closed without recording")
	default:
		status, body, _ := StoreErrorBody(err)
		return status, body
	}
}

var (
	ErrModuleOperationAdmission = errors.New("module operation admission required")
	ErrModuleOperationDisabled  = errors.New("module operation is not enabled")
)

// moduleOperationScope retains engine-issued authority. Public context fields
// and a different module's route witness cannot establish an operation scope.
type moduleOperationScope struct {
	server     *Server
	principal  auth.Principal
	tenant     model.TenantID
	call       *RecordedCall
	httpActive atomic.Bool
}

// The public AMR slice must not alias retained engine or recording authority.
func copyOperationPrincipal(p auth.Principal) auth.Principal {
	p.AMR = slices.Clone(p.AMR)
	return p
}

// ModuleOperationContext reconstructs current authenticated authority from an
// opaque credential reference for an in-process consumer. It issues no token.
// Callers must still enter the target module's supported operation.
func (s *Server) ModuleOperationContext(ctx context.Context, ref auth.PrincipalRef, tenant model.TenantID, namespace string) (context.Context, ModuleContext, error) {
	decisionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	p, err := s.authr.ResolvePrincipalScope(decisionCtx, ref, tenant)
	if err != nil {
		return ctx, ModuleContext{}, err
	}
	mc := ModuleContext{Principal: p, Tenant: tenant, operation: &moduleOperationScope{server: s, principal: p, tenant: tenant}}
	for _, m := range s.modules {
		if m.APINamespace() == namespace {
			return mc.ForModule(ctx, m)
		}
	}
	return ctx, ModuleContext{}, ErrModuleOperationDisabled
}

// ForModule binds an existing authenticated context to the target module's
// actual store and availability. It does not authorize execution. Confinement
// follows the engine-issued principal, including calls without an HTTP request.
func (mc ModuleContext) ForModule(ctx context.Context, target Module) (context.Context, ModuleContext, error) {
	if _, ok := mc.Principal.Ref(); !ok {
		return ctx, ModuleContext{}, auth.ErrUnauthenticated
	}
	if mc.operation == nil || mc.operation.server == nil || mc.Tenant != mc.operation.tenant {
		return ctx, ModuleContext{}, ErrModuleOperationAdmission
	}
	if mc.operation.call != nil && !mc.operation.httpActive.Load() {
		return ctx, ModuleContext{}, ErrModuleOperationAdmission
	}
	s := mc.operation.server
	enabled := false
	for _, m := range s.modules {
		if target != nil && reflect.TypeOf(target).Comparable() && m == target {
			enabled = true
			break
		}
	}
	if !enabled {
		return ctx, ModuleContext{}, ErrModuleOperationDisabled
	}
	mc.Principal = mc.operation.principal
	if mc.operation.call == nil {
		// An HTTP scope is already authenticated for this synchronous request,
		// including existing global API tokens. Detached scopes must reconstruct
		// their current authority again rather than retain cached memberships.
		ref, ok := mc.operation.principal.Ref()
		if !ok {
			return ctx, ModuleContext{}, auth.ErrUnauthenticated
		}
		decisionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		p, err := s.authr.ResolvePrincipalScope(decisionCtx, ref, mc.Tenant)
		if err != nil {
			return ctx, ModuleContext{}, err
		}
		mc.Principal = p
	}
	mc.Principal = copyOperationPrincipal(mc.Principal)
	mc.Data, mc.Standing, mc.Admission = NewScopedData(s.st, mc.Tenant), s.standing, s.Admits
	mc.Resource, mc.Authorization = auth.ResourceAttrs{}, auth.RouteAuthorizationWitness{}
	return withModuleRequestBoundary(ctx, mc.Tenant, mc.Principal), mc, nil
}

// admitsOperationEntry preserves the collection door before any target read.
// The matching live HTTP wrapper already asked it; another module's permission
// never stands in for this entry decision.
func (mc ModuleContext) admitsOperationEntry(ctx context.Context, perm auth.Permission) bool {
	if mc.operation == nil {
		return false
	}
	prior := mc.operation.call
	if prior != nil && mc.operation.httpActive.Load() && prior.Permission == perm && strings.HasPrefix(string(perm), prior.Namespace+":") {
		return true
	}
	return mc.Admits(ctx, perm, auth.ResourceFor(perm))
}

// AdmitOperation asks the existing collection admission seam before target reads and
// retains the existing recording gate for a nested operation. The route wrapper
// already records a direct HTTP call; it must not reserve a second frame for it.
// Complete must be deferred immediately and called with the observed outcome.
func (mc ModuleContext) AdmitOperation(ctx context.Context, call RecordedCall) (ModuleContext, func(RecordedResult), error) {
	if !mc.admitsOperationEntry(ctx, call.Permission) {
		return ModuleContext{}, nil, ErrModuleOperationAdmission
	}
	mc.Resource, mc.Authorization = auth.ResourceAttrs{}, auth.RouteAuthorizationWitness{}
	call.Principal, call.Tenant = mc.Principal, mc.Tenant
	prior := mc.operation.call
	if prior != nil && prior.Namespace == call.Namespace && prior.Method == call.Method && prior.Pattern == call.Pattern && prior.Permission == call.Permission && maps.Equal(prior.Params, call.Params) {
		return mc, func(RecordedResult) {}, nil
	}
	s := mc.operation.server
	if s.recorder == nil {
		return mc, func(RecordedResult) {}, nil
	}
	// Recording owns a tenant-global ledger, as in the HTTP wrapper. Only the
	// module's data access is workspace-confined; do not confine the recorder.
	recorderCtx := context.WithValue(ctx, ctxKeyModuleBoundary, nil)
	dec, err := s.recorder.Gate(recorderCtx, call)
	if err != nil {
		if errors.Is(err, ErrRecordingConsentRequired) {
			return ModuleContext{}, nil, ErrRecordingConsentRequired
		}
		return ModuleContext{}, nil, errRecordingUnavailable
	}
	if !dec.Record {
		return mc, func(RecordedResult) {}, nil
	}
	if dec.Session.IsZero() {
		return ModuleContext{}, nil, errRecordingUnavailable
	}
	mc.RecordingSession = dec.Session
	start := time.Now()
	return mc, func(result RecordedResult) {
		result.DurationMS = time.Since(start).Milliseconds()
		if err := s.recorder.Record(context.WithoutCancel(recorderCtx), call, dec, result); err != nil {
			s.log.Error("api: module operation recording gap", "namespace", call.Namespace, "session", dec.Session)
		}
	}, nil
}
