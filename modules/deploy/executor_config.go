// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/core/store"
)

const executorSetting = "deploy_executor"

// ExecutorConfig is the saved local-Docker setup. Credentials remain references
// into the tenant's existing sealed secret store. Other backends keep their
// published operator configuration until their product setup is qualified.
type ExecutorConfig struct {
	SocketPath    string `json:"socket_path"`
	CredentialRef string `json:"credential_ref"`
}

func (c *ExecutorConfig) validate() error {
	if c.SocketPath == "" {
		c.SocketPath = "/var/run/docker.sock"
	}
	// Product input cannot turn the engine into an arbitrary Unix-socket client.
	if c.SocketPath != "/var/run/docker.sock" && c.SocketPath != "/run/docker.sock" {
		return errors.New("socket_path must name the local Docker daemon (/var/run/docker.sock or /run/docker.sock)")
	}
	ref, ok := secret.ParseReference(c.CredentialRef)
	if !ok || ref.Scheme != secret.SchemeStore || auth.ValidateSecretName(ref.Locator) != "" || containsInlineCredential(ref.Locator) {
		return errors.New("credential_ref must reference a tenant secret (store:<name>)")
	}
	c.CredentialRef = "store:" + ref.Locator
	return nil
}

// ExecutorSetup reuses the composition root's governed executor and secret store.
// Build does no infrastructure mutation. Test checks the credential and daemon
// connection read-only; it must never plan, create, start, stop or remove a unit.
type ExecutorSetup interface {
	Build(context.Context, model.TenantID, ExecutorConfig) (Executor, error)
	Test(context.Context, model.TenantID, ExecutorConfig) error
}

// WithExecutorSetup enables saved configuration. An explicit operator-file
// override remains authoritative even when it wires no backend.
func WithExecutorSetup(setup ExecutorSetup, operatorOverride bool) Option {
	return func(m *Module) { m.executorSetup, m.executorOverride = setup, operatorOverride }
}

func savedExecutor(ctx context.Context, sc store.Scope) (ExecutorConfig, bool, error) {
	org, err := sc.Org(ctx)
	if err != nil {
		return ExecutorConfig{}, false, err
	}
	raw, exists := org.Settings[executorSetting]
	if !exists {
		return ExecutorConfig{}, false, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return ExecutorConfig{}, false, errors.New("invalid saved executor configuration")
	}
	var cfg ExecutorConfig
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return ExecutorConfig{}, false, errors.New("invalid saved executor configuration")
	}
	if err := cfg.validate(); err != nil {
		return ExecutorConfig{}, false, err
	}
	return cfg, true, nil
}

func (m *Module) readExecutorConfig(ctx context.Context, mc api.ModuleContext) (ExecutorConfig, bool, error) {
	var cfg ExecutorConfig
	var exists bool
	err := mc.Data.View(ctx, func(sc store.Scope) error {
		var err error
		cfg, exists, err = savedExecutor(ctx, sc)
		return err
	})
	return cfg, exists, err
}

// executorFor takes one immutable configuration snapshot per lifecycle request.
// Its fingerprint binds new approvals to that setup; legacy approvals keep their
// published hash when the operator executor is effective.
func (m *Module) executorFor(ctx context.Context, mc api.ModuleContext) (Executor, string, error) {
	if m.executorOverride || m.executorSetup == nil {
		return m.exec, "", nil
	}
	// Saved setup is tenant-wide. Confined callers keep their existing operator
	// executor behavior without gaining access to parent organization settings.
	if _, confined := mc.Principal.ConfinedWorkspaceIn(mc.Tenant); confined {
		return m.exec, "", nil
	}
	cfg, exists, err := m.readExecutorConfig(ctx, mc)
	if err != nil {
		return nil, "", err
	}
	if !exists {
		return m.exec, "", nil
	}
	exec, err := m.executorSetup.Build(ctx, mc.Tenant, cfg)
	if err != nil {
		return nil, "", err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, "", err
	}
	return exec, hashHex(string(raw)), nil
}

func executorSpecHash(specHash, binding string) string {
	if binding == "" {
		return specHash
	}
	return specHash + ":executor:" + binding
}

func (m *Module) handleGetExecutorConfig(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	cfg, _, err := m.readExecutorConfig(r.Context(), mc)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		ExecutorConfig
		OperatorOverride bool `json:"operator_override"`
	}{cfg, m.executorOverride})
}

func (m *Module) refuseExecutorOverride(w http.ResponseWriter) bool {
	if !m.executorOverride {
		return false
	}
	writeJSON(w, http.StatusConflict, errorBody("OLIVARES_DEPLOY_EXECUTOR_CONFIG overrides saved setup; remove the override and restart before configuring or testing saved setup"))
	return true
}

func (m *Module) handlePutExecutorConfig(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if !requireStepUp(w, r, mc) || m.refuseExecutorOverride(w) {
		return
	}
	var cfg ExecutorConfig
	if !decodeJSON(w, r, &cfg) {
		return
	}
	if err := cfg.validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
		return
	}
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		org, err := sc.Org(r.Context())
		if err != nil {
			return err
		}
		if org.Settings == nil {
			org.Settings = map[string]any{}
		}
		org.Settings[executorSetting] = map[string]any{"socket_path": cfg.SocketPath, "credential_ref": cfg.CredentialRef}
		if _, err := sc.SetOrgSettings(r.Context(), org.Settings); err != nil {
			return err
		}
		return auditEvent(r.Context(), sc, mc, "deploy.executor.configure", "org", model.ID(mc.Tenant), map[string]any{"socket_path": cfg.SocketPath, "credential_ref": cfg.CredentialRef})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	m.handleGetExecutorConfig(w, r, mc)
}

func (m *Module) handleTestExecutor(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.refuseExecutorOverride(w) {
		return
	}
	cfg, exists, err := m.readExecutorConfig(r.Context(), mc)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if !exists || m.executorSetup == nil {
		execUnavailable(w, errNoExecutor)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	testErr := m.executorSetup.Test(ctx, mc.Tenant, cfg)
	// Seal only the outcome, never daemon response bodies or credential material.
	err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		return auditEvent(r.Context(), sc, mc, "deploy.executor.test", "org", model.ID(mc.Tenant), map[string]any{"connected": testErr == nil})
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if testErr != nil {
		execUnavailable(w, testErr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"connected": true, "runtime": "docker"})
}
