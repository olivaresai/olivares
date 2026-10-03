// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

func mcpGatewayInput() auth.MCPGatewayServerInput {
	return auth.MCPGatewayServerInput{Name: "Example", Transport: "streamable_http", URL: "https://tools.example.test/mcp", CredentialRef: "store:mcp/example"}
}

func TestMCPGatewayStoreDeniesUnsafeConfiguration(t *testing.T) {
	ctx := context.Background()
	tenant := model.TenantID(model.NewID())
	const raw = "DO-NOT-STORE-THIS-CREDENTIAL"
	for name, alter := range map[string]func(*auth.MCPGatewayServerInput){
		"raw credential":         func(in *auth.MCPGatewayServerInput) { in.CredentialRef = raw },
		"deployment environment": func(in *auth.MCPGatewayServerInput) { in.CredentialRef = "env:OPERATOR_KEY" },
		"operator file":          func(in *auth.MCPGatewayServerInput) { in.CredentialRef = "file:/operator/key" },
		"URL credentials":        func(in *auth.MCPGatewayServerInput) { in.URL = "https://alice:" + raw + "@tools.example.test/mcp" },
		"URL query":              func(in *auth.MCPGatewayServerInput) { in.URL += "?token=" + raw },
		"plaintext transport":    func(in *auth.MCPGatewayServerInput) { in.URL = "http://tools.example.test/mcp" },
		"stdio command":          func(in *auth.MCPGatewayServerInput) { in.Transport = "stdio" },
		"enabled at creation":    func(in *auth.MCPGatewayServerInput) { in.Enabled = true },
		"private JWK": func(in *auth.MCPGatewayServerInput) {
			in.Trust.JWKS = []byte(`{"keys":[{"kty":"OKP","d":"` + raw + `"}]}`)
		},
		"invalid address grant": func(in *auth.MCPGatewayServerInput) { in.EgressCIDRs = []string{"anything"} },
		"unknown JWK credential field": func(in *auth.MCPGatewayServerInput) {
			in.Trust.JWKS = []byte(`{"keys":[{"kty":"OKP","password":"` + raw + `"}]}`)
		},
		"empty scope": func(in *auth.MCPGatewayServerInput) { in.AllowedTools = []auth.MCPGatewayToolPolicy{{Name: "read"}} },
		"duplicate tools": func(in *auth.MCPGatewayServerInput) {
			in.AllowedTools = []auth.MCPGatewayToolPolicy{{Name: "read", RequiredScope: "tools:read"}, {Name: "read", RequiredScope: "tools:read"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			svc := auth.NewMCPGatewayStore(testStore(t))
			in := mcpGatewayInput()
			alter(&in)
			_, err := svc.PutServer(ctx, adminActor(), tenant, 0, "", in)
			if !errors.Is(err, auth.ErrMCPGatewayInvalid) {
				t.Fatalf("unsafe config = %v", err)
			}
			if strings.Contains(err.Error(), raw) {
				t.Fatal("refusal echoed credential")
			}
			got, err := svc.Get(ctx, tenant)
			if err != nil || got.Version != 0 || len(got.Servers) != 0 {
				t.Fatalf("denial persisted state: %+v %v", got, err)
			}
		})
	}
}

func TestMCPGatewayStoreConfinementCASAndExplicitEnable(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	svc := auth.NewMCPGatewayStore(st)
	a, b := model.TenantID(model.NewID()), model.TenantID(model.NewID())
	initial, err := svc.Get(ctx, a)
	if err != nil || !initial.SessionTools || initial.Version != 0 {
		t.Fatalf("default: %+v %v", initial, err)
	}
	in := mcpGatewayInput()
	created, err := svc.PutServer(ctx, adminActor(), a, 0, "", in)
	if err != nil || created.Version != 1 || len(created.Servers) != 1 {
		t.Fatalf("create: %+v %v", created, err)
	}
	id := created.Servers[0].ID
	if got, err := svc.Get(ctx, b); err != nil || len(got.Servers) != 0 {
		t.Fatalf("foreign list: %+v %v", got, err)
	}
	if _, err := svc.DeleteServer(ctx, adminActor(), b, 0, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("foreign delete: %v", err)
	}
	if _, err := svc.SetSessionTools(ctx, adminActor(), a, 0, true); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale CAS: %v", err)
	}
	in.Enabled = true
	if _, err := svc.PutServer(ctx, adminActor(), a, 1, id, in); !errors.Is(err, auth.ErrMCPGatewayInvalid) {
		t.Fatalf("untested enable: %v", err)
	}
	probe := auth.MCPGatewayProbe{State: "ok", TestedAt: "2026-09-30T00:00:00Z", Tools: []auth.MCPGatewayTool{{Name: "read", Fingerprint: strings.Repeat("a", 64)}}}
	observed, err := svc.SaveProbe(ctx, adminActor(), a, 1, id, probe)
	if err != nil || observed.Servers[0].Enabled || len(observed.Servers[0].AllowedTools) != 0 {
		t.Fatalf("observation conferred authority: %+v %v", observed, err)
	}
	in.Trust = auth.MCPGatewayTrust{Resource: "https://plane.example.test/mcp", Issuer: "https://issuer.example.test", JWKSURL: "https://issuer.example.test/jwks"}
	in.AllowedTools = []auth.MCPGatewayToolPolicy{{Name: "forged", RequiredScope: "tools:read"}}
	if _, err := svc.PutServer(ctx, adminActor(), a, 2, id, in); !errors.Is(err, auth.ErrMCPGatewayInvalid) {
		t.Fatalf("unobserved tool: %v", err)
	}
	in.AllowedTools[0].Name = "read"
	enabled, err := svc.PutServer(ctx, adminActor(), a, 2, id, in)
	if err != nil || !enabled.Servers[0].Enabled {
		t.Fatalf("explicit enable: %+v %v", enabled, err)
	}
	in.URL = "https://replacement.example.test/mcp"
	if _, err := svc.PutServer(ctx, adminActor(), a, 3, id, in); !errors.Is(err, auth.ErrMCPGatewayInvalid) {
		t.Fatalf("new endpoint inherited test: %v", err)
	}
	in.Enabled = false
	disabled, err := svc.PutServer(ctx, adminActor(), a, 3, id, in)
	if err != nil || disabled.Servers[0].Probe.State != "never_tested" {
		t.Fatalf("new endpoint test reset: %+v %v", disabled, err)
	}
	if _, err := svc.SaveProbe(ctx, adminActor(), a, 2, id, probe); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale probe publication: %v", err)
	}
	on, err := svc.SetSessionTools(ctx, adminActor(), a, 4, true)
	if err != nil || !on.SessionTools {
		t.Fatalf("session switch: %+v %v", on, err)
	}
	reopened, err := auth.NewMCPGatewayStore(st).Get(ctx, a)
	if err != nil || reopened.Version != 5 || !reopened.SessionTools {
		t.Fatalf("reopen: %+v %v", reopened, err)
	}
	deleted, err := svc.DeleteServer(ctx, adminActor(), a, 5, id)
	if err != nil || deleted.Version != 6 || len(deleted.Servers) != 0 {
		t.Fatalf("delete: %+v %v", deleted, err)
	}
	// These are not global observation sources and cannot be selected by generic console CRUD.
	if sources, err := auth.NewSourceStore(st).List(ctx, auth.GlobalSourceScope); err != nil || len(sources) != 0 {
		t.Fatalf("leaked to global roster: %v %v", sources, err)
	}
	var actions []string
	if err := st.AuthView(ctx, func(as store.AuthScope) error {
		return as.Audit().Walk(ctx, 1, func(event model.AuditEvent) error {
			if strings.HasPrefix(event.Action, "mcp_gateway.") {
				actions = append(actions, event.Action)
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if len(actions) != 6 {
		t.Fatalf("atomic change audits: %v", actions)
	}
}

func TestMCPGatewayLocalEnvironmentReservesPrivateRunnerDirectories(t *testing.T) {
	for _, name := range []string{"HOME", "TMPDIR", "TMP", "TEMP"} {
		for _, secretRef := range []bool{false, true} {
			in := auth.MCPGatewayServerInput{Name: "Private cache", Command: "npx"}
			if secretRef {
				in.EnvSecretRefs = map[string]string{name: "store:mcp/cache-home"}
			} else {
				in.Env = map[string]string{name: "/user/project"}
			}
			_, err := auth.NewMCPGatewayStore(testStore(t)).PutServer(t.Context(), adminActor(), model.TenantID(model.NewID()), 0, "", in)
			if !errors.Is(err, auth.ErrMCPGatewayInvalid) {
				t.Fatalf("runner-owned %s accepted (secret ref %v): %v", name, secretRef, err)
			}
		}
	}
}
