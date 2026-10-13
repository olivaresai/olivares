// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/modules/sessions"
)

func TestBootBindsExecutionEnvironmentBeforeInitialPromotion(t *testing.T) {
	t.Setenv(envExecutionEnvironmentID, "")
	prepareCompositionTestBoot(t)
	ctx := context.Background()
	dir := t.TempDir()
	cfg := bootConfig{DataDir: dir, Engine: "sqlite", Version: "test", Logger: legacyNamingTestLog()}
	seed, err := boot(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if seed != nil {
			_ = seed.Close()
		}
	})
	var tenant model.TenantID
	if err := seed.store.System(ctx, func(sys store.SystemScope) error {
		org, err := sys.CreateOrg(ctx, model.Org{Name: "Legacy accounts", Slug: "legacy-accounts", Status: model.StatusActive})
		tenant = org.TenantID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	createProfile := func(m *sessions.Module) sessions.ProviderProfile {
		t.Helper()
		profile, err := m.CreateProfile(ctx, tenant, sessions.CreateProfileInput{
			Driver: "claude", ConfigHome: t.TempDir(), UserHome: t.TempDir(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if profile.AccountName != "" {
			t.Fatalf("seed profile already named %q", profile.AccountName)
		}
		return profile
	}
	profile := createProfile(seed.sessionsMod)
	environment := seed.sessionsMod.ExecutionEnvironmentRef()
	if environment == "" {
		t.Fatal("seed boot did not persist a local execution environment")
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	seed = nil

	// Exercise the real startup phases, holding the later service/source binding
	// back until the first promotion has adopted the persisted profile.
	prepareCompositionTestBoot(t)
	cfg.ServeMode = true
	b := &bootState{cfg: cfg}
	t.Cleanup(func() {
		b.unwind()
		if b.st != nil {
			_ = b.st.Close()
		}
	})
	var promote func(context.Context) error
	var environmentAtRegistration string
	b.cfg.pdpPromotionRegistered = func(fn func(context.Context) error) {
		promote = fn
		environmentAtRegistration = b.set.sessions.ExecutionEnvironmentRef()
	}
	for _, phase := range []func(context.Context) error{
		b.configure, b.loadSigningCustody, b.buildRuntime, b.openStore, b.bindIdentityAndLeadership,
	} {
		if err := phase(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if environmentAtRegistration != environment {
		t.Fatalf("execution environment before initial promotion = %q, want persisted %q", environmentAtRegistration, environment)
	}
	waitForName := func(ref, want string) {
		t.Helper()
		waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			got, err := b.set.sessions.GetProfile(waitCtx, tenant, ref)
			if err != nil {
				t.Fatal(err)
			}
			if got.AccountName == want {
				return
			}
			select {
			case <-waitCtx.Done():
				t.Fatalf("promoted profile name = %q, want %q", got.AccountName, want)
			case <-tick.C:
			}
		}
	}
	waitForName(profile.Ref, "claude")

	// A later binding phase must reuse the identity, even if the file changes.
	if err := os.WriteFile(filepath.Join(dir, executionEnvironmentFile), []byte("other-environment\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := b.bindServices(ctx); err != nil {
		t.Fatal(err)
	}
	if err := b.bindSources(ctx); err != nil {
		t.Fatal(err)
	}
	resolver := b.set.sessionDependencies.ProviderSources.(*providerSourceResolver)
	if got := b.set.sessions.ExecutionEnvironmentRef(); got != environment || resolver.env != environment || b.sourceReconcilerSvc.environmentRef != environment {
		t.Fatalf("source binding changed the boot identity: sessions=%q resolver=%q reconciler=%q, want %q", got, resolver.env, b.sourceReconcilerSvc.environmentRef, environment)
	}

	// The same registered callback still adopts profiles at a later promotion.
	second := createProfile(b.set.sessions)
	if err := promote(ctx); err != nil {
		t.Fatal(err)
	}
	waitForName(second.Ref, "claude-b")
}

func TestBootRejectsMalformedExecutionEnvironmentBeforePromotion(t *testing.T) {
	prepareCompositionTestBoot(t)
	t.Setenv(envExecutionEnvironmentID, "bad:environment")
	registered := false
	eng, err := boot(context.Background(), bootConfig{
		DataDir: t.TempDir(), Engine: "sqlite", Version: "test", ServeMode: true, Logger: legacyNamingTestLog(),
		pdpPromotionRegistered: func(func(context.Context) error) { registered = true },
	})
	if eng != nil {
		_ = eng.Close()
		t.Fatal("boot accepted a malformed explicit execution environment")
	}
	if err == nil || !strings.Contains(err.Error(), envExecutionEnvironmentID) {
		t.Fatalf("boot did not report the malformed explicit execution environment: %v", err)
	}
	if registered {
		t.Fatal("boot reached leadership before rejecting the malformed execution environment")
	}
}
