// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/spf13/cobra"
)

func TestLoginTrustedProxiesResolution(t *testing.T) {
	tests := []struct {
		name    string
		options loginProxyOptions
		env     string
		wantErr bool
	}{
		{name: "default"},
		{name: "environment", env: "10.0.0.0/8"},
		{name: "invalid_environment", env: "invalid", wantErr: true},
		{name: "flag_over_environment", options: loginProxyOptions{value: "10.0.0.0/8", set: true}, env: "invalid"},
		{name: "explicit_empty", options: loginProxyOptions{set: true}, env: "invalid"},
		{name: "invalid_flag", options: loginProxyOptions{value: "invalid", set: true}, env: "10.0.0.0/8", wantErr: true},
		{name: "value_without_presence", options: loginProxyOptions{value: "10.0.0.0/8"}, env: "invalid", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			trust, err := tt.options.resolve(func(key string) string {
				if key != envLoginTrustedProxies {
					t.Fatalf("unexpected environment lookup %q", key)
				}
				return tt.env
			})
			if (err != nil) != tt.wantErr || (err == nil && trust == nil) {
				t.Fatalf("resolve = %v, want error=%t and a set on success", err, tt.wantErr)
			}
		})
	}
	resolved := &auth.TrustedLoginProxies{}
	opts := loginProxyOptions{resolved: resolved}
	got, err := opts.resolve(func(string) string {
		t.Fatal("a resolved set read configuration again")
		return ""
	})
	if err != nil || got != resolved {
		t.Fatalf("resolved set was replaced: %v", err)
	}
}

func TestLoginTrustedProxiesCommandPrecedence(t *testing.T) {
	t.Setenv(envLoginTrustedProxies, "invalid-environment-cidr")
	constructors := []struct {
		name string
		make func() *cobra.Command
		args []string
	}{
		{name: "serve", make: newServeCmd},
		{name: "quickstart", make: newQuickstartCmd},
		{name: "governed_rag", make: newQuickstartGovernedRAGCmd, args: []string{"--bucket=fixture", "--credential-ref=store:fixture"}},
	}
	for _, command := range constructors {
		for _, flag := range []string{"omitted", "", "10.0.0.0/8", "invalid-flag-cidr"} {
			t.Run(command.name+"/"+flag, func(t *testing.T) {
				cmd := command.make()
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
				dir := filepath.Join(t.TempDir(), "not-created")
				args := append([]string{}, command.args...)
				// An invalid public URL stops valid proxy configuration before boot.
				args = append(args, "--data-dir="+dir, "--public-url=not-a-url")
				if flag != "omitted" {
					args = append(args, "--login-trusted-proxies="+flag)
				}
				cmd.SetArgs(args)
				err := cmd.Execute()
				if err == nil {
					t.Fatal("invalid startup input was accepted")
				}
				want := "public-url"
				if flag == "omitted" || flag == "invalid-flag-cidr" {
					want = "login-trusted-proxies"
				}
				if !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), "unknown flag") {
					t.Fatalf("startup error = %v, want %s validation", err, want)
				}
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatalf("invalid configuration left a data directory: %v", err)
				}
			})
		}
	}
}

func TestLoginTrustedProxiesDirectBootRejectsBeforeWrites(t *testing.T) {
	if isolateBootCaller(t) {
		return
	}
	t.Setenv(envLoginTrustedProxies, "10.0.0.0/8,,2001:db8::/32")
	dir := filepath.Join(t.TempDir(), "not-created")
	eng, err := boot(context.Background(), bootConfig{DataDir: dir, Engine: "sqlite"})
	if eng != nil {
		_ = eng.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "login-trusted-proxies") {
		t.Fatalf("boot did not refuse invalid proxy configuration: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("refused boot left a data directory: %v", err)
	}
}

func TestLoginTrustedProxiesDirectBootExplicitEmpty(t *testing.T) {
	if isolateBootCaller(t) {
		return
	}
	t.Setenv(envLoginTrustedProxies, "invalid-environment-cidr")
	eng, err := boot(context.Background(), bootConfig{
		DataDir: t.TempDir(), Engine: "sqlite", DSN: ":memory:", Version: "test",
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), NoIngest: true,
		LoginTrustedProxies: &auth.TrustedLoginProxies{},
	})
	if err != nil {
		t.Fatalf("explicit empty trust did not override the invalid environment: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
}

func TestLoginTrustedProxiesGovernedRAGCarriesResolvedSet(t *testing.T) {
	resolved := &auth.TrustedLoginProxies{}
	opts := newQuickstartGovernedRAGOptions()
	opts.loginProxies = loginProxyOptions{value: "ignored", set: true, resolved: resolved}
	serve := governedRAGServeOptions(opts)
	got, err := serve.loginProxies.resolve(func(string) string {
		t.Fatal("governed-RAG start reread the environment")
		return ""
	})
	if err != nil || got != resolved {
		t.Fatalf("governed-RAG lost the resolved proxy set: %v", err)
	}
}

func TestLoginTrustedProxiesStrictRegistry(t *testing.T) {
	clearOlivaresEnv(t)
	t.Setenv(envLoginTrustedProxies, "10.0.0.0/8")
	if mode := configEnvKeyMode(envLoginTrustedProxies); mode != configKeyExact {
		t.Fatalf("trusted proxy key mode = %v, want exact", mode)
	}
	if out, err := executeConfigCommand("effective", "--strict"); err != nil || !strings.Contains(out, envLoginTrustedProxies+"=10.0.0.0/8") {
		t.Fatalf("strict configuration rejected or omitted the key: %v; output=%s", err, out)
	}
}
