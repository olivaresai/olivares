// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// Exercise the real auth-partition transactions; the test sealer only supplies
// deterministic scope binding, as in auth's SecretStore tests.
type rotationTestSealer struct{}

func (rotationTestSealer) Seal(_ context.Context, tenant model.TenantID, value []byte) (string, error) {
	return tenant.String() + ":" + string(value), nil
}
func (rotationTestSealer) Open(_ context.Context, tenant model.TenantID, sealed string) ([]byte, error) {
	value, ok := strings.CutPrefix(sealed, tenant.String()+":")
	if !ok {
		return nil, errors.New("wrong fixture scope")
	}
	return []byte(value), nil
}

type rotationStoreVault struct{ secrets *auth.SecretStore }

func (v rotationStoreVault) Seal(ctx context.Context, actor auth.Principal, tenant model.TenantID, name string, value []byte) (string, error) {
	_, err := v.secrets.Put(ctx, actor, tenant, name, string(value), "provider fixture")
	return name, err
}
func (v rotationStoreVault) Open(ctx context.Context, tenant model.TenantID, name string) ([]byte, error) {
	return v.secrets.Resolve(ctx, tenant, name)
}
func (v rotationStoreVault) Revoke(ctx context.Context, actor auth.Principal, tenant model.TenantID, name string) error {
	return v.secrets.Delete(ctx, actor, tenant, name)
}

func TestProviderRecordRotationWithStoreVault(t *testing.T) {
	m, st, tenant, _ := newRuntimeHarness(t)
	vault := rotationStoreVault{auth.NewSecretStore(st, rotationTestSealer{})}
	WithProviderSecretVault(vault)(m)
	rec := mustCreateRecord(t, m, tenant, anthropicInput("store-backed rotation"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	key := "f1-store-backed-rotated-value"
	after, err := m.PatchProviderRecord(ctx, tenant, rec.Ref, ProviderRecordPatch{APIKey: &key, Actor: testActor()})
	if err != nil {
		t.Fatalf("rotation through the real vault transactions failed: %v", err)
	}
	if after.Ref != rec.Ref || after.SecretRef == rec.SecretRef || after.ProbeState != ProbeNever {
		t.Fatal("rotation must preserve the provider reference and publish an untested new locator")
	}
	opened, err := vault.Open(ctx, tenant, after.SecretRef)
	if err != nil || string(opened) != key {
		t.Fatal("published locator does not resolve the rotated credential")
	}
	if _, err := vault.Open(ctx, tenant, rec.SecretRef); !errors.Is(err, auth.ErrSecretNotFound) {
		t.Fatal("superseded locator was not destroyed")
	}
	values, err := vault.secrets.List(ctx, tenant)
	if err != nil || len(values) != 1 {
		t.Fatal("rotation left unowned vault entries")
	}
}

func TestProviderRecordFailedRotationKeepsPublishedCredential(t *testing.T) {
	for _, invalid := range []string{"duplicate name", "bad endpoint"} {
		t.Run(invalid, func(t *testing.T) {
			m, _, tenant, vault, _ := providerHarness(t)
			rec := mustCreateRecord(t, m, tenant, anthropicInput("original"))
			mustCreateRecord(t, m, tenant, anthropicInput("occupied"))
			key, name, endpoint := "f1-unpublished-new-credential", "occupied", "http://invalid.fixture"
			patch := ProviderRecordPatch{APIKey: &key, Actor: testActor()}
			if invalid == "duplicate name" {
				patch.DisplayName = &name
			} else {
				patch.BaseURL = &endpoint
			}
			if _, err := m.PatchProviderRecord(context.Background(), tenant, rec.Ref, patch); err == nil {
				t.Fatal("invalid patch was accepted")
			}
			current, err := m.GetProviderRecord(context.Background(), tenant, rec.Ref)
			if err != nil || current.SecretRef != rec.SecretRef || current.KeyHint != rec.KeyHint {
				t.Fatal("failed publication changed the provider row")
			}
			opened, err := vault.Open(context.Background(), tenant, current.SecretRef)
			if err != nil || string(opened) != testProviderKey || vault.count() != 2 {
				t.Fatal("failed publication changed the live key or retained an unpublished value")
			}
		})
	}
}

type rotationHookVault struct {
	*fakeVault
	afterSeal func(context.Context) error
}

func (v *rotationHookVault) Seal(ctx context.Context, actor auth.Principal, tenant model.TenantID, name string, value []byte) (string, error) {
	locator, err := v.fakeVault.Seal(ctx, actor, tenant, name, value)
	if err == nil && v.afterSeal != nil {
		hook := v.afterSeal
		v.afterSeal = nil
		err = hook(ctx)
	}
	return locator, err
}

func (v *rotationHookVault) Revoke(ctx context.Context, actor auth.Principal, tenant model.TenantID, locator string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return v.fakeVault.Revoke(ctx, actor, tenant, locator)
}

func TestProviderRecordCanceledRotationWithdrawsPreparedValue(t *testing.T) {
	m, _, tenant, base, _ := providerHarness(t)
	vault := &rotationHookVault{fakeVault: base}
	WithProviderSecretVault(vault)(m)
	rec := mustCreateRecord(t, m, tenant, anthropicInput("canceled rotation"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	vault.afterSeal = func(context.Context) error { cancel(); return nil }
	key := "f1-canceled-new-credential"
	if _, err := m.PatchProviderRecord(ctx, tenant, rec.Ref, ProviderRecordPatch{APIKey: &key, Actor: testActor()}); err == nil {
		t.Fatal("canceled publication succeeded")
	}
	current, err := m.GetProviderRecord(context.Background(), tenant, rec.Ref)
	if err != nil || current.SecretRef != rec.SecretRef || vault.count() != 1 {
		t.Fatal("canceled publication changed the record or retained a prepared value")
	}
}

// Interleave the competing write after sealing but before publication. There is
// no scheduling guess: this is the boundary on which the old implementation
// either deadlocked or overwrote another operation's published credential.
func TestProviderRecordRotationRejectsConcurrentChanges(t *testing.T) {
	for _, change := range []string{"revoke", "rotate"} {
		t.Run(change, func(t *testing.T) {
			m, _, tenant, base, _ := providerHarness(t)
			vault := &rotationHookVault{fakeVault: base}
			WithProviderSecretVault(vault)(m)
			rec := mustCreateRecord(t, m, tenant, anthropicInput("concurrent rotation"))
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			winner := "f1-winner-credential"
			vault.afterSeal = func(ctx context.Context) error {
				if change == "revoke" {
					_, err := m.RevokeProviderRecord(ctx, testActor(), tenant, rec.Ref)
					return err
				}
				_, err := m.PatchProviderRecord(ctx, tenant, rec.Ref, ProviderRecordPatch{APIKey: &winner, Actor: testActor()})
				return err
			}
			loser := "f1-stale-credential"
			_, err := m.PatchProviderRecord(ctx, tenant, rec.Ref, ProviderRecordPatch{APIKey: &loser, Actor: testActor()})
			current, readErr := m.GetProviderRecord(ctx, tenant, rec.Ref)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if change == "revoke" {
				if !errors.Is(err, ErrProviderRecordRevoked) || current.State != ProviderRecordRevoked || vault.count() != 0 {
					t.Fatal("rotation revived a revoked provider or retained an unpublished value")
				}
			} else {
				opened, openErr := vault.Open(ctx, tenant, current.SecretRef)
				if !errors.Is(err, ErrProviderRecordChanged) || openErr != nil || string(opened) != winner || vault.count() != 1 {
					t.Fatal("stale rotation replaced the winner or retained an unpublished value")
				}
			}
		})
	}
}

type rotationHookProbe struct{ probe func(context.Context) error }

func (p rotationHookProbe) Probe(ctx context.Context, _ ProviderProbeRequest) (ProviderProbeResult, error) {
	return ProviderProbeResult{Models: []string{"old-key-model"}, Detail: "old credential accepted"}, p.probe(ctx)
}

func TestProviderRecordProbeCannotPublishAcrossRotation(t *testing.T) {
	m, _, tenant, _, _ := providerHarness(t)
	rec := mustCreateRecord(t, m, tenant, anthropicInput("probe race"))
	WithProviderProbe(rotationHookProbe{probe: func(ctx context.Context) error {
		key := "f1-new-untested-credential"
		_, err := m.PatchProviderRecord(ctx, tenant, rec.Ref, ProviderRecordPatch{APIKey: &key, Actor: testActor()})
		return err
	}})(m)
	if _, err := m.TestProviderRecord(context.Background(), tenant, rec.Ref); !errors.Is(err, ErrProviderRecordChanged) {
		t.Fatal("old probe verdict was accepted after rotation")
	}
	current, err := m.GetProviderRecord(context.Background(), tenant, rec.Ref)
	if err != nil || current.ProbeState != ProbeNever || len(current.Models) != 0 || current.ProbedAt != "" {
		t.Fatal("new credential inherited the old credential's verdict")
	}
}

func TestProviderRecordEndpointChangeClearsMeasuredVerdict(t *testing.T) {
	m, _, tenant, _, _ := providerHarness(t)
	rec := mustCreateRecord(t, m, tenant, anthropicInput("endpoint change"))
	if _, err := m.TestProviderRecord(context.Background(), tenant, rec.Ref); err != nil {
		t.Fatal(err)
	}
	endpoint := "https://different.fixture"
	after, err := m.PatchProviderRecord(context.Background(), tenant, rec.Ref, ProviderRecordPatch{BaseURL: &endpoint})
	if err != nil || after.ProbeState != ProbeNever || len(after.Models) != 0 || after.ProbedAt != "" {
		t.Fatal("changed endpoint inherited the previous endpoint's measured verdict")
	}
}

func TestBoundRecordLaunchAfterRotationUsesPublishedLocator(t *testing.T) {
	m, tenant, runner, host, _, rec, profile := boundHarness(t)
	key := "f1-bound-rotated-credential"
	if _, err := m.PatchProviderRecord(context.Background(), tenant, rec.Ref,
		ProviderRecordPatch{APIKey: &key, Actor: testActor()}); err != nil {
		t.Fatal(err)
	}
	if _, err := launchBound(m, tenant, profile); err != nil {
		t.Fatal(err)
	}
	value, ok := envValue(runner.lastSpec(), "ANTHROPIC_API_KEY")
	if !ok || value != key {
		t.Fatal("stable profile binding did not inject the newly published credential")
	}
	host.mu.Lock()
	defer host.mu.Unlock()
	if host.calls != 0 {
		t.Fatal("rotated provider launch consulted the host fallback")
	}
}
