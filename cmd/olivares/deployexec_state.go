// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/runtime/executor"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/modules/deploy"
)

// deployExecutorSetup is late-bound to the existing sealed store before serving.
// No secret is read at setup/save time or retained in module configuration.
type deployExecutorSetup struct {
	secrets *auth.SecretStore
	log     *slog.Logger
}

func (s *deployExecutorSetup) credentialSource(tenant model.TenantID, cfg deploy.ExecutorConfig) executor.CredentialSource {
	return executor.MintFunc(func(ctx context.Context, req executor.MintRequest) (executor.Credential, error) {
		ref, ok := secret.ParseReference(cfg.CredentialRef)
		if !ok || ref.Scheme != secret.SchemeStore || auth.ValidateSecretName(ref.Locator) != "" || s.secrets == nil {
			return executor.Credential{}, errors.New("executor: tenant credential reference is unavailable")
		}
		raw, err := s.secrets.Resolve(ctx, tenant, ref.Locator)
		if err != nil {
			return executor.Credential{}, errors.New("executor: tenant credential reference could not be resolved")
		}
		token := strings.TrimSpace(string(raw))
		clear(raw)
		if token == "" {
			return executor.Credential{}, errors.New("executor: tenant credential is empty")
		}
		digest := sha256.Sum256([]byte(token))
		return executor.Credential{ID: "store-token:" + req.Environment + ":" + req.Mode.String() + ":" + hex.EncodeToString(digest[:])[:16], Token: token, NotAfter: time.Now().Add(time.Minute), Scheme: "store-token"}, nil
	})
}

func (s *deployExecutorSetup) Build(_ context.Context, tenant model.TenantID, cfg deploy.ExecutorConfig) (deploy.Executor, error) {
	backend := executor.NewDockerBackend(executor.DockerConfig{SocketPath: cfg.SocketPath, Tenant: tenant.String(), DisableKeepAlives: true})
	return &deployExecutor{e: executor.New(executor.WithBackend(backend, "docker"), executor.WithCredentialSource(s.credentialSource(tenant, cfg)), executor.WithLogger(s.log))}, nil
}

func (s *deployExecutorSetup) Test(ctx context.Context, tenant model.TenantID, cfg deploy.ExecutorConfig) error {
	// Keep the existing credential gate even though local Docker authenticates at
	// the OS socket boundary and receives no bearer token on the wire.
	if _, err := s.credentialSource(tenant, cfg).Mint(ctx, executor.MintRequest{Runtime: "docker", Mode: executor.ModeRead}); err != nil {
		return err
	}
	backend := executor.NewDockerBackend(executor.DockerConfig{SocketPath: cfg.SocketPath, Timeout: 5 * time.Second, DisableKeepAlives: true})
	return backend.TestConnection(ctx)
}
