// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bufio"
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func scriptedPrompter(answers []string) (*prompter, *bytes.Buffer) {
	out := &bytes.Buffer{}
	in := strings.Join(answers, "\n") + "\n"
	return &prompter{in: bufio.NewReader(strings.NewReader(in)), out: out}, out
}

func dummyCmd() *cobra.Command {
	c := &cobra.Command{}
	c.SetContext(context.Background())
	return c
}

// TestSetupInteractivePostgres scripts the whole postgres-prod flow (no live
// provisioning) and proves the wizard externalizes the password into a secret file
// and references it from the env file — never inlining it.
func TestSetupInteractivePostgres(t *testing.T) {
	t.Parallel()
	answers := []string{
		"3",          // profile: postgres-prod
		"",           // host (localhost)
		"",           // port (5432)
		"",           // database (olivares)
		"",           // sslmode (verify-full)
		"",           // app role (olivares_app)
		"apppw",      // app password
		"",           // owner role (olivares_owner)
		"ownerpw",    // owner password
		"",           // admin role (olivares_admin; required by postgres-prod)
		"adminpw",    // admin password
		"",           // provision now? (no)
		"",           // data dir
		"",           // listen
		"",           // grpc listen
		"/tls.crt",   // operator TLS certificate
		"/tls.key",   // operator TLS private key
		"/ca.crt",    // collector mTLS CA
		"/audit.key", // external audit signing key
		"",           // residency? (no)
	}
	p, _ := scriptedPrompter(answers)
	plan, err := buildPlanInteractive(dummyCmd(), p, "/tmp/olivares-secrets")
	if err != nil {
		t.Fatalf("buildPlanInteractive: %v", err)
	}
	if p.err != nil {
		t.Fatalf("prompter error: %v", p.err)
	}
	if plan.Profile != profilePostgresPro || plan.Engine != "postgres" {
		t.Fatalf("profile/engine = %q/%q", plan.Profile, plan.Engine)
	}
	if !strings.HasPrefix(plan.DSNArg, "file:") || !strings.HasPrefix(plan.OwnerDSNArg, "file:") {
		t.Fatalf("DSNs not externalized: dsn=%q owner=%q", plan.DSNArg, plan.OwnerDSNArg)
	}
	if len(plan.Secrets) != 3 {
		t.Fatalf("expected 3 secret files (app + owner + admin), got %d", len(plan.Secrets))
	}
	// The password lives in the secret file, NOT in the rendered env file.
	var sawAppPw bool
	for _, s := range plan.Secrets {
		if strings.Contains(s.Content, "apppw") {
			sawAppPw = true
		}
	}
	if !sawAppPw {
		t.Error("the app password did not reach a secret file")
	}
	if plan.AdminDSNArg == "" || plan.TLSCert != "/tls.crt" || plan.GRPCClientCA != "/ca.crt" || plan.AuditSigningKeyFile != "/audit.key" {
		t.Fatalf("postgres-prod posture not resolved: %+v", plan)
	}
	rendered := plan.renderEnvFile()
	if strings.Contains(rendered, "apppw") || strings.Contains(rendered, "ownerpw") {
		t.Errorf("the env file leaked a password:\n%s", rendered)
	}
	if err := plan.validate(); err != nil {
		t.Fatalf("scripted plan is invalid: %v", err)
	}
}

// TestSetupInteractiveEval proves the minimal eval flow yields a loopback SQLite plan.
func TestSetupInteractiveEval(t *testing.T) {
	t.Parallel()
	p, _ := scriptedPrompter([]string{"1", ""}) // profile eval, default data dir
	plan, err := buildPlanInteractive(dummyCmd(), p, t.TempDir())
	if err != nil {
		t.Fatalf("buildPlanInteractive: %v", err)
	}
	if plan.Profile != profileEval || plan.Engine != "sqlite" {
		t.Fatalf("eval plan = %q/%q", plan.Profile, plan.Engine)
	}
	if !hostIsLoopback(plan.Listen) {
		t.Errorf("eval listen %q is not loopback", plan.Listen)
	}
	if err := plan.validate(); err != nil {
		t.Fatalf("eval plan invalid: %v", err)
	}
}

func TestSetupInteractiveK8sDefaultsDualStackBinds(t *testing.T) {
	t.Parallel()
	p, _ := scriptedPrompter([]string{
		"4",                    // profile: k8s
		"",                     // Postgres? (yes)
		"file:/secrets/db.dsn", // mounted app DSN
		"",                     // owner DSN
		"",                     // admin DSN
		"",                     // HTTP bind
		"",                     // gRPC bind
		"",                     // residency? (no)
	})
	plan, err := buildPlanInteractive(dummyCmd(), p, t.TempDir())
	if err != nil {
		t.Fatalf("buildPlanInteractive: %v", err)
	}
	if p.err != nil {
		t.Fatalf("prompter error: %v", p.err)
	}
	if plan.Listen != ":8443" || plan.GRPCListen != ":8444" {
		t.Fatalf("k8s binds = %q/%q, want :8443/:8444", plan.Listen, plan.GRPCListen)
	}
	if err := plan.validate(); err != nil {
		t.Fatalf("k8s plan invalid: %v", err)
	}
}

// TestPrompterEOFRequired proves a required answer with exhausted stdin records a
// clear error instead of looping forever.
func TestPrompterEOFRequired(t *testing.T) {
	t.Parallel()
	p := &prompter{in: bufio.NewReader(strings.NewReader("")), out: &bytes.Buffer{}}
	_ = p.askRequired("database password")
	if p.err == nil {
		t.Fatal("askRequired on empty stdin should set p.err")
	}
}

// TestPrompterDefaults proves Enter selects the default for ask/askBool/askChoice.
func TestPrompterDefaults(t *testing.T) {
	t.Parallel()
	p, _ := scriptedPrompter([]string{"", "", ""})
	if got := p.ask("x", "def"); got != "def" {
		t.Errorf("ask default = %q", got)
	}
	if got := p.askBool("y", true); got != true {
		t.Errorf("askBool default = %v", got)
	}
	if got := p.askChoice("z", []string{"a", "b", "c"}, 1); got != "b" {
		t.Errorf("askChoice default = %q", got)
	}
}
