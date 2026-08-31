// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/modules/eventing"
)

// eventingwire_test.go pins the secret sealer's custody posture: AES-GCM
// round-trip, tenant-bound AAD (a ciphertext never opens for another tenant),
// fail-closed key loading (env shape, file permissions), version agility, and
// the env-driven construction options.

func TestEventingSealerRoundTripAndTenantBinding(t *testing.T) {
	dir := t.TempDir()
	sealer, err := newEventingSealer(dir, func(string) string { return "" })
	if err != nil {
		t.Fatalf("newEventingSealer: %v", err)
	}
	ctx := context.Background()
	tenantA := model.TenantID("11111111-1111-1111-1111-111111111111")
	tenantB := model.TenantID("22222222-2222-2222-2222-222222222222")
	secret := []byte("olvw_super_secret_value")

	sealed, err := sealer.Seal(ctx, tenantA, secret)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !strings.HasPrefix(sealed, "v1:") {
		t.Fatalf("sealed form must be versioned, got %q", sealed[:8])
	}
	if strings.Contains(sealed, string(secret)) {
		t.Fatal("sealed form must not embed the cleartext")
	}
	got, err := sealer.Open(ctx, tenantA, sealed)
	if err != nil || string(got) != string(secret) {
		t.Fatalf("Open round-trip = %q, %v", got, err)
	}
	// The AAD binds the tenant: the SAME ciphertext must not open elsewhere.
	if _, err := sealer.Open(ctx, tenantB, sealed); err == nil {
		t.Fatal("a ciphertext sealed for tenant A must not open for tenant B")
	}
	// Tampering fails closed.
	tampered := sealed[:len(sealed)-2] + "AA"
	if _, err := sealer.Open(ctx, tenantA, tampered); err == nil {
		t.Fatal("a tampered ciphertext must not open")
	}
	// An unknown version prefix fails closed (agility, not laxity).
	if _, err := sealer.Open(ctx, tenantA, "v9:"+sealed[3:]); err == nil {
		t.Fatal("an unknown sealed-secret version must not open")
	}
}

// The per-node key file persists across restarts (a second sealer built from
// the same data dir opens what the first sealed) and is minted 0600.
func TestEventingSealerKeyFilePersistence(t *testing.T) {
	dir := t.TempDir()
	none := func(string) string { return "" }
	s1, err := newEventingSealer(dir, none)
	if err != nil {
		t.Fatal(err)
	}
	tenant := model.TenantID("11111111-1111-1111-1111-111111111111")
	sealed, err := s1.Seal(context.Background(), tenant, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, eventingSecretKeyFile))
	if err != nil {
		t.Fatalf("key file not minted: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file perms = %v, want 0600", fi.Mode().Perm())
	}
	s2, err := newEventingSealer(dir, none)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s2.Open(context.Background(), tenant, sealed); err != nil || string(got) != "x" {
		t.Fatalf("restart must open prior seals: %q, %v", got, err)
	}
}

// An over-permissive key file is refused (the secure package's posture), and a
// malformed env key is an error — never a silent downgrade.
func TestEventingSealerFailsClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventingSecretKeyFile)
	key := base64.StdEncoding.EncodeToString(make([]byte, 32)) + "\n"
	if err := os.WriteFile(path, []byte(key), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := newEventingSealer(dir, func(string) string { return "" }); err == nil {
		t.Fatal("a group/world-readable key file must be refused")
	}

	for _, bad := range []string{"notbase64!!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		env := func(k string) string {
			if k == eventingSecretKeyEnv {
				return bad
			}
			return ""
		}
		if _, err := newEventingSealer(t.TempDir(), env); err == nil {
			t.Fatalf("malformed env key %q must be refused", bad)
		}
	}

	// A valid env key wins over the file path (the HA shared-key posture) and
	// two nodes with the same env key open each other's seals.
	shared := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))
	env := func(k string) string {
		if k == eventingSecretKeyEnv {
			return shared
		}
		return ""
	}
	n1, err := newEventingSealer(t.TempDir(), env)
	if err != nil {
		t.Fatal(err)
	}
	n2, err := newEventingSealer(t.TempDir(), env)
	if err != nil {
		t.Fatal(err)
	}
	tenant := model.TenantID("11111111-1111-1111-1111-111111111111")
	sealed, err := n1.Seal(context.Background(), tenant, []byte("ha"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := n2.Open(context.Background(), tenant, sealed); err != nil || string(got) != "ha" {
		t.Fatalf("HA peers must open each other's seals: %q, %v", got, err)
	}
}

func TestLoadEventingOptions(t *testing.T) {
	log := discardLog()
	env := map[string]string{}
	getenv := func(k string) string { return env[k] }

	mustLoad := func(t *testing.T) []eventing.Option {
		t.Helper()
		opts, err := loadEventingOptions(getenv, log)
		if err != nil {
			t.Fatalf("loadEventingOptions: %v", err)
		}
		return opts
	}

	if opts := mustLoad(t); len(opts) != 0 {
		t.Fatalf("clean env must yield no overrides, got %d", len(opts))
	}
	env[eventingAllowLoopbackEnv] = "1"
	env[eventingRetentionEnv] = "48h"
	if opts := mustLoad(t); len(opts) != 2 {
		t.Fatalf("loopback+retention must yield 2 options, got %d", len(opts))
	}
	// An invalid retention keeps the default (option not emitted) — a typo
	// never silently changes the replay window.
	env[eventingRetentionEnv] = "garbage"
	if opts := mustLoad(t); len(opts) != 1 {
		t.Fatalf("invalid retention must be dropped, got %d options", len(opts))
	}

	// An egress policy the operator DID configure but which cannot be read is fatal,
	// not a downgrade to "no policy". The asymmetry with the retention case above is
	// deliberate and is the whole point: an unparseable retention falls back to a
	// SAFE default, while an unparseable destination policy would fall back to
	// permitting every host — the opposite of what the operator asked for, with a
	// green boot to say so.
	env[envEventingEgressPolicy] = filepath.Join(t.TempDir(), "does-not-exist.json")
	if _, err := loadEventingOptions(getenv, log); err == nil {
		t.Fatal("an unreadable egress policy must refuse to boot, not boot unconstrained")
	}
}

func TestLoadEventingEgressPolicy(t *testing.T) {
	write := func(t *testing.T, body string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "egress.json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	getenvFor := func(path string) func(string) string {
		return func(k string) string {
			if k == envEventingEgressPolicy {
				return path
			}
			return ""
		}
	}
	tenant := model.NewTenantID()

	// Unset: no policy object at all, which the module reads as ABSENT and permits.
	if p, err := loadEventingEgressPolicy(func(string) string { return "" }); p != nil || err != nil {
		t.Fatalf("unset must yield no policy: %v %v", p, err)
	}

	// A default policy applies to a tenant with no entry of its own.
	p, err := loadEventingEgressPolicy(getenvFor(write(t,
		`{"default":{"allow":[{"host":"soc.example.com"}]}}`)))
	if err != nil {
		t.Fatal(err)
	}
	pol, err := p.EgressPolicy(context.Background(), tenant)
	if err != nil || !pol.InForce || len(pol.Allow) != 1 || pol.Allow[0].Host != "soc.example.com" {
		t.Fatalf("default policy not applied: %+v %v", pol, err)
	}

	// A tenant entry REPLACES the default rather than extending it: inheriting would
	// silently widen a policy an operator wrote to be exact.
	p, err = loadEventingEgressPolicy(getenvFor(write(t, `{
		"default":{"allow":[{"host":"soc.example.com"}]},
		"tenants":{"`+tenant.String()+`":{"allow":[{"host":"only.example.com"}]}}}`)))
	if err != nil {
		t.Fatal(err)
	}
	pol, err = p.EgressPolicy(context.Background(), tenant)
	if err != nil {
		t.Fatal(err)
	}
	if len(pol.Allow) != 1 || pol.Allow[0].Host != "only.example.com" {
		t.Fatalf("a tenant entry must replace the default, got %+v", pol.Allow)
	}

	// An authored empty allow-list is a deny-all and is honored as one.
	p, err = loadEventingEgressPolicy(getenvFor(write(t, `{"default":{"allow":[]}}`)))
	if err != nil {
		t.Fatal(err)
	}
	pol, _ = p.EgressPolicy(context.Background(), tenant)
	if !pol.InForce || len(pol.Allow) != 0 {
		t.Fatalf("an authored empty allow-list must stay in force and empty: %+v", pol)
	}

	for _, bad := range []string{
		`{}`, // constrains nothing: the operator asked for a control and would get none
		`{"default":{"allow":[{"host":"soc.example.com","cidr":"203.0.113.0/24"}]}}`,
		`{"default":{"allow":[{"cidr":"not-a-cidr"}]}}`,
		`{"tenants":{"not-a-tenant-id":{"allow":[{"host":"a.example.com"}]}}}`,
	} {
		if _, err := loadEventingEgressPolicy(getenvFor(write(t, bad))); err == nil {
			t.Errorf("accepted an invalid policy file: %s", bad)
		}
	}
}
