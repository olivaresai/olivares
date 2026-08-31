// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"strings"
	"testing"
)

func validPostgresProdPlan() installPlan {
	return installPlan{
		Profile: profilePostgresPro, Engine: "postgres",
		DSNArg:              "file:/etc/olivares/secrets/app.dsn",
		OwnerDSNArg:         "file:/etc/olivares/secrets/owner.dsn",
		AdminDSNArg:         "file:/etc/olivares/secrets/admin.dsn",
		Listen:              "127.0.0.1:8443",
		GRPCListen:          "127.0.0.1:8444",
		TLSCert:             "/etc/olivares/tls.crt",
		TLSKey:              "/etc/olivares/tls.key",
		GRPCClientCA:        "/etc/olivares/collector-ca.crt",
		AuditSigningKeyFile: "/etc/olivares/audit-signing.key",
	}
}

func TestInstallPlanValidate(t *testing.T) {
	t.Parallel()
	base := func() installPlan {
		return installPlan{Profile: profileSingleNode, Listen: "127.0.0.1:8443", GRPCListen: "127.0.0.1:8444", Engine: "sqlite"}
	}
	cases := []struct {
		name    string
		mutate  func(p *installPlan)
		wantErr bool
	}{
		{"ok sqlite", func(*installPlan) {}, false},
		{"ok postgres production", func(p *installPlan) { *p = validPostgresProdPlan() }, false},
		{"postgres needs dsn", func(p *installPlan) { p.Engine = "postgres" }, true},
		{"unknown profile", func(p *installPlan) { p.Profile = "wat" }, true},
		{"bad listen", func(p *installPlan) { p.Listen = "not-a-hostport" }, true},
		{"bad engine", func(p *installPlan) { p.Engine = "mysql" }, true},
		{"tls cert without key", func(p *installPlan) { p.TLSCert = "/x.crt" }, true},
		{"tls both", func(p *installPlan) { p.TLSCert = "/x.crt"; p.TLSKey = "/x.key" }, false},
		{"bad region", func(p *installPlan) { p.Region = "Europe!" }, true},
		{"known regions need home", func(p *installPlan) { p.KnownRegions = []string{"eu", "us"} }, true},
		{"ok residency", func(p *installPlan) { p.Region = "eu"; p.KnownRegions = []string{"eu", "us"} }, false},
		{"bad checkpoint", func(p *installPlan) { p.CheckpointInterval = "soon" }, true},
		{"insecure non-loopback", func(p *installPlan) { p.Listen = "0.0.0.0:8443"; p.Insecure = true }, true},
		{"insecure non-loopback grpc", func(p *installPlan) { p.Insecure = true; p.GRPCListen = "0.0.0.0:8444" }, true},
		{"insecure production loopback", func(p *installPlan) { p.Insecure = true }, true},
		{"insecure eval loopback ok", func(p *installPlan) { p.Profile = profileEval; p.Insecure = true }, false},
		{"privileged db on sqlite", func(p *installPlan) { p.AllowPrivilegedDB = true }, true},
		{"all-interfaces bind ok", func(p *installPlan) { p.Listen = ":8443" }, false},
		{"inline password URL rejected", func(p *installPlan) {
			p.Profile = profileK8s
			p.Engine = "postgres"
			p.DSNArg = "postgres://olivares_app:SECRET@db/olivares"
		}, true},
		{"inline password keyword rejected", func(p *installPlan) {
			p.Profile = profileK8s
			p.Engine = "postgres"
			p.DSNArg = "host=db user=olivares_app password=SECRET dbname=olivares"
		}, true},
		{"passwordless postgres URL ok", func(p *installPlan) {
			p.Profile = profileK8s
			p.Engine = "postgres"
			p.DSNArg = "postgres://olivares_app@db/olivares?sslmode=verify-full"
		}, false},
		{"file ref owner/admin dsn ok", func(p *installPlan) {
			p.Profile = profileK8s
			p.Engine = "postgres"
			p.DSNArg = "file:/etc/olivares/secrets/db.dsn"
			p.OwnerDSNArg = "file:/etc/olivares/secrets/owner.dsn"
			p.AdminDSNArg = "file:/etc/olivares/secrets/admin.dsn"
		}, false},
		{"inline password owner dsn rejected", func(p *installPlan) {
			p.Profile = profileK8s
			p.Engine = "postgres"
			p.DSNArg = "file:/etc/olivares/secrets/db.dsn"
			p.OwnerDSNArg = "postgres://olivares_owner:SECRET@db/olivares"
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := base()
			c.mutate(&p)
			err := p.validate()
			if (err != nil) != c.wantErr {
				t.Fatalf("validate()=%v, wantErr=%v", err, c.wantErr)
			}
		})
	}
}

func TestPostgresProdProfileInvariants(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*installPlan)
		want   string
	}{
		{"sqlite rejected", func(p *installPlan) { p.Engine = "sqlite" }, "requires engine postgres"},
		{"app DSN required", func(p *installPlan) { p.DSNArg = "" }, "application-role --dsn"},
		{"owner DSN required", func(p *installPlan) { p.OwnerDSNArg = "" }, "--owner-dsn"},
		{"app owner split required", func(p *installPlan) { p.OwnerDSNArg = p.DSNArg }, "distinct application and owner"},
		{"admin DSN required", func(p *installPlan) { p.AdminDSNArg = "" }, "--admin-dsn"},
		{"admin split required", func(p *installPlan) { p.AdminDSNArg = p.OwnerDSNArg }, "distinct application, owner, and admin"},
		{"TLS required", func(p *installPlan) { p.TLSCert = ""; p.TLSKey = "" }, "--tls-cert and --tls-key"},
		{"collector mTLS required", func(p *installPlan) { p.GRPCClientCA = "" }, "--grpc-client-ca"},
		{"external audit key required", func(p *installPlan) { p.AuditSigningKeyFile = "" }, "--audit-signing-key-file"},
		{"insecure forbidden", func(p *installPlan) { p.Insecure = true }, "--insecure is forbidden"},
		{"privileged app role forbidden", func(p *installPlan) { p.AllowPrivilegedDB = true }, "forbids --allow-privileged-db-role"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := validPostgresProdPlan()
			tt.mutate(&p)
			if err := p.validate(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("validate() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestConfigGeneratePostgresProdIsTyped(t *testing.T) {
	if out, err := executeConfigCommand("generate", "--profile", profilePostgresPro); err == nil {
		t.Fatalf("postgres-prod without production inputs succeeded:\n%s", out)
	}
	if out, err := executeConfigCommand("generate", "--profile", profilePostgresPro, "--engine", "sqlite"); err == nil {
		t.Fatalf("postgres-prod explicitly backed by SQLite succeeded:\n%s", out)
	}

	out, err := executeConfigCommand(
		"generate", "--profile", profilePostgresPro,
		"--dsn", "file:/etc/olivares/secrets/app.dsn",
		"--owner-dsn", "file:/etc/olivares/secrets/owner.dsn",
		"--admin-dsn", "file:/etc/olivares/secrets/admin.dsn",
		"--tls-cert", "/etc/olivares/tls.crt",
		"--tls-key", "/etc/olivares/tls.key",
		"--grpc-client-ca", "/etc/olivares/collector-ca.crt",
		"--audit-signing-key-file", "/etc/olivares/audit-signing.key",
	)
	if err != nil {
		t.Fatalf("valid postgres-prod config failed: %v\n%s", err, out)
	}
	for _, want := range []string{
		"Resolved posture: postgres-prod",
		"OLIVARES_KEY_CUSTODY=byok",
		"OLIVARES_AUDIT_SIGNING_KEY_FILE=/etc/olivares/audit-signing.key",
		"OLIVARES_DB_MAX_CONNS=20",
		"--engine=postgres",
		"--owner-dsn=file:/etc/olivares/secrets/owner.dsn",
		"--admin-dsn=file:/etc/olivares/secrets/admin.dsn",
		"--tls-cert=/etc/olivares/tls.crt --tls-key=/etc/olivares/tls.key",
		"--grpc-client-ca=/etc/olivares/collector-ca.crt",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated postgres-prod config missing %q:\n%s", want, out)
		}
	}
}

func TestServeFlagsPostgres(t *testing.T) {
	t.Parallel()
	p := validPostgresProdPlan()
	p.Listen = "0.0.0.0:8443"
	p.GRPCListen = "0.0.0.0:8444"
	p.Region = "eu"
	p.KnownRegions = []string{"us", "eu"}
	got := strings.Join(p.serveFlags(), " ")
	for _, want := range []string{
		"--engine=postgres",
		"--dsn=file:/etc/olivares/secrets/app.dsn",
		"--owner-dsn=file:/etc/olivares/secrets/owner.dsn",
		"--admin-dsn=file:/etc/olivares/secrets/admin.dsn",
		"--listen=0.0.0.0:8443", "--grpc-listen=0.0.0.0:8444",
		"--tls-cert=/etc/olivares/tls.crt --tls-key=/etc/olivares/tls.key",
		"--grpc-client-ca=/etc/olivares/collector-ca.crt",
		"--region=eu", "--known-regions=eu,us", // sorted + home folded in
	} {
		if !strings.Contains(got, want) {
			t.Errorf("serveFlags missing %q in: %s", want, got)
		}
	}
}

func TestRenderEnvFileHasNoSecret(t *testing.T) {
	t.Parallel()
	p := validPostgresProdPlan()
	p.MaxConns = 20
	p.Secrets = []envSecretFile{{Path: "/etc/olivares/secrets/app.dsn", Content: "postgres://olivares_app:SUPERSECRET@db/olivares"}}
	out := p.renderEnvFile()
	if strings.Contains(out, "SUPERSECRET") {
		t.Fatal("the env file leaked the database password")
	}
	if !strings.Contains(out, "--dsn=file:/etc/olivares/secrets/app.dsn") {
		t.Errorf("env file missing the file: DSN reference:\n%s", out)
	}
	if !strings.Contains(out, "OLIVARES_DB_MAX_CONNS=20") {
		t.Errorf("env file missing the max-conns knob:\n%s", out)
	}
	if !strings.Contains(out, "OLIVARES_EXTRA_ARGS=") {
		t.Errorf("env file missing OLIVARES_EXTRA_ARGS:\n%s", out)
	}
	if !strings.Contains(out, "OLIVARES_KEY_CUSTODY=byok") || !strings.Contains(out, "OLIVARES_AUDIT_SIGNING_KEY_FILE=/etc/olivares/audit-signing.key") {
		t.Errorf("env file missing resolved external audit-key custody:\n%s", out)
	}
}

func TestRenderK8s(t *testing.T) {
	t.Parallel()
	p := installPlan{Profile: profileK8s, Engine: "postgres", DSNArg: "file:/secrets/db.dsn", Listen: "0.0.0.0:8443", GRPCListen: "0.0.0.0:8444"}
	out := p.render()
	if !strings.Contains(out, "extraArgs:") {
		t.Errorf("k8s render missing extraArgs:\n%s", out)
	}
	if !strings.Contains(out, "--dsn=file:/secrets/db.dsn") {
		t.Errorf("k8s render missing the mounted DSN ref:\n%s", out)
	}
}

func TestSortedKnownRegions(t *testing.T) {
	t.Parallel()
	p := installPlan{Region: "eu", KnownRegions: []string{"us", "ap"}}
	got := strings.Join(p.sortedKnownRegions(), ",")
	if got != "ap,eu,us" {
		t.Errorf("sortedKnownRegions = %q, want ap,eu,us (home folded in, sorted)", got)
	}
}
