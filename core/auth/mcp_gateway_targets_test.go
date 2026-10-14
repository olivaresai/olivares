// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// A server's targets are nodes of the skills assignment tree. An omitted list
// keeps them, so a client that does not know them never widens a server; an
// explicit empty list clears them. Each change is in the ledger.
func TestMCPGatewayServerTargets(t *testing.T) {
	id := model.NewID().String()
	for name, payload := range map[string]string{
		"unknown kind": `{"name":"Local","command":"npx","targets":[{"target_kind":"tenant","target_id":"` + id + `"}]}`,
		"not an id":    `{"name":"Local","command":"npx","targets":[{"target_kind":"workspace","target_id":"sales"}]}`,
		"zero id":      `{"name":"Local","command":"npx","targets":[{"target_kind":"agent","target_id":"00000000-0000-0000-0000-000000000000"}]}`,
		"duplicate":    `{"name":"Local","command":"npx","targets":[{"target_kind":"agent","target_id":"` + id + `"},{"target_kind":"agent","target_id":"` + id + `"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			svc := auth.NewMCPGatewayStore(testStore(t))
			var in auth.MCPGatewayServerInput
			if err := json.Unmarshal([]byte(payload), &in); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.PutServer(t.Context(), adminActor(), model.TenantID(model.NewID()), 0, "", in); !errors.Is(err, auth.ErrMCPGatewayInvalid) {
				t.Fatalf("invalid targets accepted: %v", err)
			}
		})
	}

	st := testStore(t)
	svc := auth.NewMCPGatewayStore(st)
	tenant := model.TenantID(model.NewID())
	workspace := auth.MCPGatewayTarget{Kind: "workspace", ID: model.NewID().String()}
	agent := auth.MCPGatewayTarget{Kind: "agent", ID: model.NewID().String()}
	in := auth.MCPGatewayServerInput{Name: "Filesystem", Command: "npx", Targets: []auth.MCPGatewayTarget{workspace}}
	out, err := svc.PutServer(t.Context(), adminActor(), tenant, 0, "", in)
	if err != nil {
		t.Fatal(err)
	}
	serverID := out.Servers[0].ID
	targets := func(want ...auth.MCPGatewayTarget) {
		t.Helper()
		got, err := svc.Get(t.Context(), tenant)
		if err != nil || len(got.Servers) != 1 || !reflect.DeepEqual(got.Servers[0].Targets, append([]auth.MCPGatewayTarget(nil), want...)) {
			t.Fatalf("stored targets = %+v (%v), want %+v", got.Servers, err, want)
		}
		out = got
	}
	targets(workspace)

	in.Targets = nil // an older form's write
	if out, err = svc.PutServer(t.Context(), adminActor(), tenant, out.Version, serverID, in); err != nil {
		t.Fatal(err)
	}
	targets(workspace)
	in.Targets = []auth.MCPGatewayTarget{workspace, agent}
	if out, err = svc.PutServer(t.Context(), adminActor(), tenant, out.Version, serverID, in); err != nil {
		t.Fatal(err)
	}
	targets(workspace, agent)
	in.Targets = []auth.MCPGatewayTarget{}
	if out, err = svc.PutServer(t.Context(), adminActor(), tenant, out.Version, serverID, in); err != nil {
		t.Fatal(err)
	}
	targets()

	// The ledger names each assignment change and only those.
	var recorded []string
	if err := st.AuthView(t.Context(), func(as store.AuthScope) error {
		walker, ok := as.Audit().(store.CanonicalWalker)
		if !ok {
			return errors.New("the ledger lacks a canonical reader")
		}
		return walker.WalkCanonical(t.Context(), 1, func(event model.AuditEvent, canonical string, _ []byte) error {
			if !strings.HasPrefix(event.Action, "mcp_gateway.server.") {
				return nil
			}
			var meta struct {
				Targets *[]auth.MCPGatewayTarget `json:"targets"`
			}
			if err := json.Unmarshal([]byte(canonical), &meta); err != nil {
				return err
			}
			entry := event.Action + " -"
			if meta.Targets != nil {
				kinds := []string{}
				for _, target := range *meta.Targets {
					kinds = append(kinds, target.Kind)
				}
				entry = event.Action + " [" + strings.Join(kinds, ",") + "]"
			}
			recorded = append(recorded, entry)
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"mcp_gateway.server.create [workspace]", "mcp_gateway.server.update -", "mcp_gateway.server.update [workspace,agent]", "mcp_gateway.server.update []"}
	if !reflect.DeepEqual(recorded, want) {
		t.Fatalf("ledger = %q, want %q", recorded, want)
	}
}

// A pin names an installed catalog tool by program, version and executable
// digest. An omitted pin keeps the stored one; another digest withdraws the
// last test, since it names other code.
func TestMCPGatewayServerPin(t *testing.T) {
	sum := strings.Repeat("a", 64)
	for name, payload := range map[string]string{
		"remote":       `{"name":"Remote","url":"https://example.test/mcp","transport":"streamable_http","pin":{"program":"rea","version":"1.0.0","sha256":"` + sum + `"}}`,
		"no digest":    `{"name":"Local","command":"/opt/rea","pin":{"program":"rea","version":"1.0.0"}}`,
		"upper digest": `{"name":"Local","command":"/opt/rea","pin":{"program":"rea","version":"1.0.0","sha256":"` + strings.ToUpper(sum) + `"}}`,
		"program path": `{"name":"Local","command":"/opt/rea","pin":{"program":"../rea","version":"1.0.0","sha256":"` + sum + `"}}`,
		"version dots": `{"name":"Local","command":"/opt/rea","pin":{"program":"rea","version":"1..0","sha256":"` + sum + `"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			svc := auth.NewMCPGatewayStore(testStore(t))
			var in auth.MCPGatewayServerInput
			if err := json.Unmarshal([]byte(payload), &in); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.PutServer(t.Context(), adminActor(), model.TenantID(model.NewID()), 0, "", in); !errors.Is(err, auth.ErrMCPGatewayInvalid) {
				t.Fatalf("invalid pin accepted: %v", err)
			}
		})
	}
	svc := auth.NewMCPGatewayStore(testStore(t))
	tenant := model.TenantID(model.NewID())
	pin := &auth.MCPGatewayPin{Program: "rea", Version: "1.0.0", SHA256: sum}
	in := auth.MCPGatewayServerInput{Name: "REA", Command: "/opt/rea", Pin: pin}
	out, err := svc.PutServer(t.Context(), adminActor(), tenant, 0, "", in)
	if err != nil || out.Servers[0].Transport != "stdio" {
		t.Fatalf("a pinned command was not stored as a local server: %v %+v", err, out.Servers)
	}
	id := out.Servers[0].ID
	tested := func() {
		t.Helper()
		if out, err = svc.SaveProbe(t.Context(), adminActor(), tenant, out.Version, id, auth.MCPGatewayProbe{State: "ok", Tools: []auth.MCPGatewayTool{{Name: "search", Fingerprint: sum}}}); err != nil {
			t.Fatal(err)
		}
	}
	tested()
	in.Pin = nil // an older form's write
	if out, err = svc.PutServer(t.Context(), adminActor(), tenant, out.Version, id, in); err != nil {
		t.Fatal(err)
	}
	if got := out.Servers[0]; got.Pin == nil || *got.Pin != *pin || got.Probe.State != "ok" {
		t.Fatalf("an omitted pin changed the server: %+v", got)
	}
	in.Pin = &auth.MCPGatewayPin{Program: "rea", Version: "1.0.0", SHA256: strings.Repeat("b", 64)}
	if out, err = svc.PutServer(t.Context(), adminActor(), tenant, out.Version, id, in); err != nil {
		t.Fatal(err)
	}
	if got := out.Servers[0]; got.Pin.SHA256 != in.Pin.SHA256 || got.Probe.State != "never_tested" {
		t.Fatalf("another pinned digest kept the last test: %+v", got)
	}
	// Another command is other code: it does not inherit the pin.
	in.Pin, in.Command = nil, "/opt/other"
	if out, err = svc.PutServer(t.Context(), adminActor(), tenant, out.Version, id, in); err != nil {
		t.Fatal(err)
	}
	if got := out.Servers[0]; got.Pin != nil || got.Command != "/opt/other" {
		t.Fatalf("a new command kept the old pin: %+v", got)
	}
}
