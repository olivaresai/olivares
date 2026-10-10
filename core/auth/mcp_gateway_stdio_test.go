// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

func TestMCPGatewayLocalServerDefaultsAndTestFence(t *testing.T) {
	svc := auth.NewMCPGatewayStore(testStore(t))
	tenant := model.TenantID(model.NewID())
	var in auth.MCPGatewayServerInput
	if err := json.Unmarshal([]byte(`{"name":"Filesystem","command":"npx","args":["-y","@modelcontextprotocol/server-filesystem","."]}`), &in); err != nil {
		t.Fatal(err)
	}
	out, err := svc.PutServer(t.Context(), adminActor(), tenant, 0, "", in)
	if err != nil {
		t.Fatalf("name and command should register a disabled server: %v", err)
	}
	row := out.Servers[0]
	if row.Transport != "stdio" || row.Enabled || row.Probe.State != "never_tested" {
		t.Fatalf("unexpected defaults: %+v", row)
	}
	in = row.MCPGatewayServerInput
	in.Enabled = true
	if _, err := svc.PutServer(t.Context(), adminActor(), tenant, out.Version, row.ID, in); !errors.Is(err, auth.ErrMCPGatewayInvalid) {
		t.Fatalf("untested server enabled: %v", err)
	}
	out, err = svc.SaveProbe(t.Context(), adminActor(), tenant, out.Version, row.ID, auth.MCPGatewayProbe{State: "ok", Tools: []auth.MCPGatewayTool{{Name: "read_file", Fingerprint: strings.Repeat("a", 64)}}})
	if err != nil {
		t.Fatal(err)
	}
	in.AllowedTools = []auth.MCPGatewayToolPolicy{{Name: "read_file", RequiredScope: "tools:read"}}
	out, err = svc.PutServer(t.Context(), adminActor(), tenant, out.Version, row.ID, in)
	if err != nil || !out.Servers[0].Enabled {
		t.Fatalf("tested local server needs no OAuth trust configuration: %v", err)
	}
	var changed auth.MCPGatewayServerInput
	_ = json.Unmarshal([]byte(`{"name":"Filesystem","command":"uvx","args":["other"],"enabled":true}`), &changed)
	if _, err := svc.PutServer(t.Context(), adminActor(), tenant, out.Version, row.ID, changed); !errors.Is(err, auth.ErrMCPGatewayInvalid) {
		t.Fatalf("changed executable inherited test: %v", err)
	}
	changed.Enabled = false
	out, err = svc.PutServer(t.Context(), adminActor(), tenant, out.Version, row.ID, changed)
	if err != nil || out.Servers[0].Probe.State != "never_tested" {
		t.Fatalf("command change did not reset probe: %v", err)
	}
}

func TestMCPGatewayLocalServerEnvironmentIsReferenceOnly(t *testing.T) {
	for name, payload := range map[string]string{
		"argv credential":    `{"name":"Local","command":"npx","args":["--token=private-value"]}`,
		"literal secret env": `{"name":"Local","command":"npx","env":{"API_KEY":"private-value"}}`,
		"global namespace":   `{"name":"Local","command":"npx","env_secret_refs":{"API_KEY":"store:provider/key"}}`,
		"overlapping env":    `{"name":"Local","command":"npx","env":{"MODE":"test"},"env_secret_refs":{"MODE":"store:mcp/key"}}`,
		"mixed transport":    `{"name":"Local","command":"npx","url":"https://example.test/mcp"}`,
	} {
		t.Run(name, func(t *testing.T) {
			svc := auth.NewMCPGatewayStore(testStore(t))
			var in auth.MCPGatewayServerInput
			_ = json.Unmarshal([]byte(payload), &in)
			_, err := svc.PutServer(t.Context(), adminActor(), model.TenantID(model.NewID()), 0, "", in)
			if !errors.Is(err, auth.ErrMCPGatewayInvalid) || strings.Contains(err.Error(), "private-value") {
				t.Fatalf("unsafe config accepted or echoed: %v", err)
			}
		})
	}
	svc := auth.NewMCPGatewayStore(testStore(t))
	tenant := model.TenantID(model.NewID())
	in := auth.MCPGatewayServerInput{Name: "Local", Command: "npx", Env: map[string]string{"MODE": "test"}, EnvSecretRefs: map[string]string{"API_KEY": "store:mcp/key"}}
	out, err := svc.PutServer(t.Context(), adminActor(), tenant, 0, "", in)
	if err != nil {
		t.Fatal(err)
	}
	out, err = svc.SaveProbe(t.Context(), adminActor(), tenant, out.Version, out.Servers[0].ID, auth.MCPGatewayProbe{State: "ok", Tools: []auth.MCPGatewayTool{{Name: "read", Fingerprint: strings.Repeat("b", 64), ReadOnly: true}, {Name: "write", Fingerprint: strings.Repeat("c", 64)}}})
	if err != nil {
		t.Fatal(err)
	}
	in.Enabled = true
	out, err = svc.PutServer(t.Context(), adminActor(), tenant, out.Version, out.Servers[0].ID, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Servers[0].AllowedTools) != 2 || !out.Servers[0].AllowedTools[0].Destructive || !out.Servers[0].AllowedTools[1].Destructive {
		t.Fatalf("incorrect default authority: %+v", out.Servers[0].AllowedTools)
	}
	if out.Servers[0].ProposedAllow == nil || len(*out.Servers[0].ProposedAllow) != 1 || (*out.Servers[0].ProposedAllow)[0] != "read" {
		t.Fatal("declared read-only tool was not proposed separately from Ask authority")
	}
	in.EnvSecretRefs["API_KEY"] = "store:mcp/replacement"
	if _, err := svc.PutServer(t.Context(), adminActor(), tenant, out.Version, out.Servers[0].ID, in); !errors.Is(err, auth.ErrMCPGatewayInvalid) {
		t.Fatalf("changed secret reference inherited test: %v", err)
	}
	in.Enabled = false
	out, err = svc.PutServer(t.Context(), adminActor(), tenant, out.Version, out.Servers[0].ID, in)
	if err != nil || out.Servers[0].ProposedAllow == nil || len(*out.Servers[0].ProposedAllow) != 0 {
		t.Fatal("changed connection retained a stale read-only proposal")
	}
	out, err = svc.Get(t.Context(), tenant)
	if err != nil || out.Servers[0].ProposedAllow == nil || *out.Servers[0].ProposedAllow == nil || len(*out.Servers[0].ProposedAllow) != 0 {
		t.Fatal("GET did not carry an empty proposal after invalidating the catalogue")
	}
}

// A command server's egress profile is stored exactly as the proxy admits it,
// belongs to command servers only, and moving it withdraws the last test.
func TestMCPGatewayLocalServerEgressHosts(t *testing.T) {
	for name, payload := range map[string]string{
		"url form":       `{"name":"Local","command":"npx","egress_hosts":["https://registry.npmjs.org"]}`,
		"path":           `{"name":"Local","command":"npx","egress_hosts":["registry.npmjs.org/x"]}`,
		"wildcard":       `{"name":"Local","command":"npx","egress_hosts":["*.npmjs.org"]}`,
		"upper case":     `{"name":"Local","command":"npx","egress_hosts":["Registry.npmjs.org"]}`,
		"default port":   `{"name":"Local","command":"npx","egress_hosts":["registry.npmjs.org:443"]}`,
		"credentials":    `{"name":"Local","command":"npx","egress_hosts":["user@registry.npmjs.org"]}`,
		"duplicate":      `{"name":"Local","command":"npx","egress_hosts":["pypi.org","pypi.org"]}`,
		"empty":          `{"name":"Local","command":"npx","egress_hosts":[""]}`,
		"trailing colon": `{"name":"Local","command":"npx","egress_hosts":["registry.npmjs.org:"]}`,
		"trailing dot":   `{"name":"Local","command":"npx","egress_hosts":["registry.npmjs.org."]}`,
		"port zero":      `{"name":"Local","command":"npx","egress_hosts":["registry.npmjs.org:0"]}`,
		"port range":     `{"name":"Local","command":"npx","egress_hosts":["registry.npmjs.org:99999"]}`,
		"localhost":      `{"name":"Local","command":"npx","egress_hosts":["localhost:8080"]}`,
		"local name":     `{"name":"Local","command":"npx","egress_hosts":["engine.localhost"]}`,
		"loopback":       `{"name":"Local","command":"npx","egress_hosts":["127.0.0.1:8080"]}`,
		"loopback v6":    `{"name":"Local","command":"npx","egress_hosts":["[::1]"]}`,
		"private":        `{"name":"Local","command":"npx","egress_hosts":["10.0.0.5"]}`,
		"metadata":       `{"name":"Local","command":"npx","egress_hosts":["169.254.169.254"]}`,
		"remote server":  `{"name":"Remote","url":"https://example.test/mcp","egress_hosts":["example.test"]}`,
		"too many hosts": `{"name":"Local","command":"npx","egress_hosts":["a.test","b.test","c.test","d.test","e.test","f.test","g.test","h.test","i.test","j.test","k.test","l.test","m.test","n.test","o.test","p.test","q.test"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			svc := auth.NewMCPGatewayStore(testStore(t))
			var in auth.MCPGatewayServerInput
			if err := json.Unmarshal([]byte(payload), &in); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.PutServer(t.Context(), adminActor(), model.TenantID(model.NewID()), 0, "", in); !errors.Is(err, auth.ErrMCPGatewayInvalid) {
				t.Fatalf("invalid egress hosts accepted: %v", err)
			}
		})
	}
	svc := auth.NewMCPGatewayStore(testStore(t))
	tenant := model.TenantID(model.NewID())
	in := auth.MCPGatewayServerInput{Name: "Filesystem", Command: "npx", EgressHosts: []string{"registry.npmjs.org", "mirror.test:8443"}}
	out, err := svc.PutServer(t.Context(), adminActor(), tenant, 0, "", in)
	if err != nil {
		t.Fatal(err)
	}
	row := out.Servers[0]
	if got, _ := svc.Get(t.Context(), tenant); len(got.Servers) != 1 || strings.Join(got.Servers[0].EgressHosts, ",") != "registry.npmjs.org,mirror.test:8443" {
		t.Fatalf("egress hosts not stored as sent: %+v", got.Servers)
	}
	out, err = svc.SaveProbe(t.Context(), adminActor(), tenant, out.Version, row.ID, auth.MCPGatewayProbe{State: "ok", Tools: []auth.MCPGatewayTool{{Name: "read_file", Fingerprint: strings.Repeat("a", 64)}}})
	if err != nil {
		t.Fatal(err)
	}
	in.EgressHosts = []string{"registry.npmjs.org"}
	out, err = svc.PutServer(t.Context(), adminActor(), tenant, out.Version, row.ID, in)
	if err != nil || out.Servers[0].Probe.State != "never_tested" {
		t.Fatalf("an egress change kept the test of another network: %v %+v", err, out.Servers)
	}
}
