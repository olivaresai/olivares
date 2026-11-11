// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A value under a credential-looking name is refused on save, whatever the case
// of the name and wherever it sits, and the refusal points to a secret reference.
func TestMCPGatewayRefusesCredentialNamesWithASecretReference(t *testing.T) {
	const private = "private-value"
	for _, name := range []string{"key", "Sig", "SIGNATURE", "auth", "Bearer", "pass", "PWD", "credential", "x-amz-signature"} {
		envName := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		for form, in := range map[string]auth.MCPGatewayServerInput{
			"argument":      {Name: "Local", Command: "npx", Args: []string{"--" + name + "=" + private}},
			"argument pair": {Name: "Local", Command: "npx", Args: []string{"--" + name, private}},
			"env name":      {Name: "Local", Command: "npx", Env: map[string]string{envName: private}},
			"env value":     {Name: "Local", Command: "npx", Env: map[string]string{"UPSTREAM": "https://u.test/x?" + name + "=" + private}},
			"command":       {Name: "Local", Command: envName + "=" + private + " npx"},
		} {
			t.Run(name+"/"+form, func(t *testing.T) {
				svc := auth.NewMCPGatewayStore(testStore(t))
				_, err := svc.PutServer(t.Context(), adminActor(), model.TenantID(model.NewID()), 0, "", in)
				if !errors.Is(err, auth.ErrMCPGatewayInvalid) || strings.Contains(err.Error(), private) || !strings.Contains(err.Error(), "secret reference") {
					t.Fatalf("credential under %q saved or refused without pointing to a secret reference: %v", name, err)
				}
			})
		}
	}
	t.Run("command with an environment prefix", func(t *testing.T) {
		svc := auth.NewMCPGatewayStore(testStore(t))
		in := auth.MCPGatewayServerInput{Name: "Local", Command: "FOO=" + private + " npx"}
		_, err := svc.PutServer(t.Context(), adminActor(), model.TenantID(model.NewID()), 0, "", in)
		if !errors.Is(err, auth.ErrMCPGatewayInvalid) || strings.Contains(err.Error(), private) {
			t.Fatalf("environment prefix in the command saved: %v", err)
		}
	})
	t.Run("words that only contain a credential word stay public", func(t *testing.T) {
		svc := auth.NewMCPGatewayStore(testStore(t))
		in := auth.MCPGatewayServerInput{Name: "Local", Command: "npx", Args: []string{"--author=alice", "--keyword", "x", "--key-file", "/etc/k.pem", "--auth"},
			Env: map[string]string{"MONKEY": "1", "PASSAGE": "2"}}
		if _, err := svc.PutServer(t.Context(), adminActor(), model.TenantID(model.NewID()), 0, "", in); err != nil {
			t.Fatalf("public names refused: %v", err)
		}
	})
}

// The ledger entry of a save names the server, never a value it holds.
func TestMCPGatewaySaveAuditNamesNoValue(t *testing.T) {
	st := testStore(t)
	svc := auth.NewMCPGatewayStore(st)
	tenant := model.TenantID(model.NewID())
	in := auth.MCPGatewayServerInput{Name: "Local", Command: "npx", Args: []string{"-y", "args-public-value"},
		Env: map[string]string{"MODE": "env-public-value"}, EnvSecretRefs: map[string]string{"API_KEY": "store:mcp/audit-ref"}}
	out, err := svc.PutServer(t.Context(), adminActor(), tenant, 0, "", in)
	if err != nil {
		t.Fatal(err)
	}
	in.Env["MODE"] = "env-second-value"
	if _, err := svc.PutServer(t.Context(), adminActor(), tenant, out.Version, out.Servers[0].ID, in); err != nil {
		t.Fatal(err)
	}
	saves := 0
	if err := st.AuthView(t.Context(), func(as store.AuthScope) error {
		walker, ok := as.Audit().(store.CanonicalWalker)
		if !ok {
			return errors.New("ledger lacks canonical reader")
		}
		return walker.WalkCanonical(t.Context(), 1, func(event model.AuditEvent, canonical string, _ []byte) error {
			if !strings.HasPrefix(event.Action, "mcp_gateway.server.") {
				return nil
			}
			saves++
			for _, value := range []string{"npx", "args-public-value", "env-public-value", "env-second-value", "audit-ref"} {
				if strings.Contains(canonical, value) {
					t.Errorf("%s audit names the value %q: %s", event.Action, value, canonical)
				}
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if saves != 2 {
		t.Fatalf("saves audited = %d, want 2", saves)
	}
}
