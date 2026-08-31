// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package secret_test

import (
	"context"
	"errors"
	"testing"

	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/sdk"
)

// staticHandler resolves any locator to a fixed value, or errors.
type staticHandler struct {
	val []byte
	err error
}

func (h staticHandler) Resolve(_ context.Context, _ string) ([]byte, error) {
	if h.err != nil {
		return nil, h.err
	}
	return h.val, nil
}

// echoHandler returns the locator itself (to assert the locator passed through).
type echoHandler struct{}

func (echoHandler) Resolve(_ context.Context, locator string) ([]byte, error) {
	return []byte("resolved:" + locator), nil
}

func TestParseReference(t *testing.T) {
	cases := []struct {
		in     string
		scheme string
		loc    string
		ok     bool
	}{
		{"store:gdrive/token", "store", "gdrive/token", true},
		{"db:gdrive/token", "store", "gdrive/token", true}, // db folds to store
		{"env:VAULT_TOKEN", "env", "VAULT_TOKEN", true},
		{"file:/run/secrets/tok", "file", "/run/secrets/tok", true},
		{"vault:secret/data/x#token", "vault", "secret/data/x#token", true},
		{"aws-secretsmanager:prod/gdrive", "aws-secretsmanager", "prod/gdrive", true},
		{"db://svc:AUDIT-password@db.internal/app", "", "", false},
		{"https://vault.example:8200", "", "", false}, // a URL is not a secret reference
		{"plain-token-value", "", "", false},
		{"unknownscheme:foo", "", "", false}, // unknown scheme = literal
		{"env:", "", "", false},              // empty locator
		{"", "", "", false},
	}
	for _, c := range cases {
		ref, ok := secret.ParseReference(c.in)
		if ok != c.ok {
			t.Errorf("ParseReference(%q) ok = %v, want %v", c.in, ok, c.ok)
			continue
		}
		if ok && (ref.Scheme != c.scheme || ref.Locator != c.loc) {
			t.Errorf("ParseReference(%q) = {%q,%q}, want {%q,%q}", c.in, ref.Scheme, ref.Locator, c.scheme, c.loc)
		}
	}
}

func descWith(secretKeys ...string) sdk.Descriptor {
	d := sdk.Descriptor{}
	for _, k := range secretKeys {
		d.ConfigFields = append(d.ConfigFields, sdk.ConfigField{Key: k, Secret: true})
	}
	// A non-secret field, to prove non-secret literals pass through.
	d.ConfigFields = append(d.ConfigFields, sdk.ConfigField{Key: "base_url"})
	return d
}

func TestResolveSubstitutesReferences(t *testing.T) {
	r := secret.NewResolver(map[string]secret.Handler{
		secret.SchemeEnv:   staticHandler{val: []byte("ENVVAL")},
		secret.SchemeStore: echoHandler{},
	})
	in := sdk.Config{Settings: map[string]string{
		"token":    "env:GDRIVE_TOKEN",
		"api_key":  "store:gdrive/key",
		"base_url": "https://drive.example",
	}}
	out, err := r.Resolve(context.Background(), descWith("token", "api_key"), in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if out.Settings["token"] != "ENVVAL" {
		t.Errorf("token = %q, want ENVVAL", out.Settings["token"])
	}
	if out.Settings["api_key"] != "resolved:gdrive/key" {
		t.Errorf("api_key = %q", out.Settings["api_key"])
	}
	if out.Settings["base_url"] != "https://drive.example" {
		t.Errorf("base_url should pass through, got %q", out.Settings["base_url"])
	}
	// The operator's input map must not be mutated.
	if in.Settings["token"] != "env:GDRIVE_TOKEN" {
		t.Errorf("input mutated: %q", in.Settings["token"])
	}
}

func TestResolveStrictRejectsInlineSecret(t *testing.T) {
	r := secret.NewResolver(map[string]secret.Handler{secret.SchemeEnv: staticHandler{val: []byte("x")}})
	in := sdk.Config{Settings: map[string]string{"token": "sk-ant-raw-literal-secret"}}
	_, err := r.Resolve(context.Background(), descWith("token"), in)
	var inline secret.ErrInlineSecret
	if !errors.As(err, &inline) {
		t.Fatalf("want ErrInlineSecret, got %v", err)
	}
	if inline.Field != "token" {
		t.Errorf("offending field = %q, want token", inline.Field)
	}
	// The error must never embed the secret value.
	if got := inline.Error(); contains(got, "sk-ant-raw-literal-secret") {
		t.Errorf("error leaked the secret value: %q", got)
	}
}

func TestResolveStrictDisabledPassesLiteral(t *testing.T) {
	r := secret.NewResolver(nil, secret.WithStrict(false))
	in := sdk.Config{Settings: map[string]string{"token": "raw-literal"}}
	out, err := r.Resolve(context.Background(), descWith("token"), in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if out.Settings["token"] != "raw-literal" {
		t.Errorf("token = %q", out.Settings["token"])
	}
}

func TestResolveUnwiredSchemeFailsClosed(t *testing.T) {
	// `store:` is a recognized scheme but no handler is wired (the collector case).
	r := secret.NewResolver(map[string]secret.Handler{secret.SchemeEnv: staticHandler{val: []byte("x")}})
	in := sdk.Config{Settings: map[string]string{"token": "store:gdrive/token"}}
	_, err := r.Resolve(context.Background(), descWith("token"), in)
	if err == nil {
		t.Fatal("want fail-closed error for unwired store scheme, got nil")
	}
}

func TestResolveHandlerErrorPropagates(t *testing.T) {
	r := secret.NewResolver(map[string]secret.Handler{
		secret.SchemeVault: staticHandler{err: errors.New("vault: 403 permission denied")},
	})
	in := sdk.Config{Settings: map[string]string{"token": "vault:secret/data/x#token"}}
	_, err := r.Resolve(context.Background(), descWith("token"), in)
	if err == nil {
		t.Fatal("want handler error, got nil")
	}
}

func TestResolveNoDescriptorSkipsStrict(t *testing.T) {
	// External plugin: no descriptor, so the strict no-inline check is skipped, but
	// references still resolve.
	r := secret.NewResolver(map[string]secret.Handler{secret.SchemeEnv: staticHandler{val: []byte("V")}})
	in := sdk.Config{Settings: map[string]string{"token": "env:X", "other": "raw-literal"}}
	out, err := r.Resolve(context.Background(), sdk.Descriptor{}, in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if out.Settings["token"] != "V" || out.Settings["other"] != "raw-literal" {
		t.Errorf("out = %v", out.Settings)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestCheckNoInlineSecrets(t *testing.T) {
	desc := sdk.Descriptor{ConfigFields: []sdk.ConfigField{
		{Key: "token", Secret: true},
		{Key: "base_url"},
	}}

	// A reference in the secret field, a literal in a non-secret field: OK.
	if err := secret.CheckNoInlineSecrets(desc, sdk.Config{Settings: map[string]string{
		"token": "store:vault/token", "base_url": "https://v",
	}}); err != nil {
		t.Errorf("references + non-secret literal should pass: %v", err)
	}

	// A literal in the declared-secret field: rejected as ErrInlineSecret.
	err := secret.CheckNoInlineSecrets(desc, sdk.Config{Settings: map[string]string{
		"token": "hvs.literalsecretvalue", "base_url": "https://v",
	}})
	var ise secret.ErrInlineSecret
	if !errors.As(err, &ise) || ise.Field != "token" {
		t.Errorf("inline secret in a declared-secret field = %v, want ErrInlineSecret{token}", err)
	}

	// No declared secret fields (e.g. an unknown/plugin descriptor): no-op.
	if err := secret.CheckNoInlineSecrets(sdk.Descriptor{}, sdk.Config{Settings: map[string]string{
		"token": "anything",
	}}); err != nil {
		t.Errorf("zero descriptor should be a no-op: %v", err)
	}
}
