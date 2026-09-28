// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/helpers/invocation"
	"github.com/olivaresai/olivares/appliance/layer/services"
)

// kernel describes one connection's peer: its account, its unit and its terminal.
type kernel struct {
	uid     uint32
	account string
	unit    string
	ttyNr   string
	pidfd   bool
}

func (k kernel) PeerCred(int) (int, uint32, error) { return 4242, k.uid, nil }
func (k kernel) PeerPidfd(int) (int, error) {
	if !k.pidfd {
		return 0, errors.New("protocol not available")
	}
	return 9, nil
}
func (k kernel) PidOf(int) (int, error)     { return 4242, nil }
func (k kernel) Cgroup(int) (string, error) { return "0::/system.slice/" + k.unit + "\n", nil }
func (k kernel) Stat(int) (string, error) {
	return "4242 (peer) S 1 4242 4242 " + k.ttyNr + " -1 0", nil
}
func (k kernel) AccountOf(uint32) (string, error) { return k.account, nil }
func (k kernel) Alive(int) error                  { return nil }
func (k kernel) Close(int)                        {}

// manager is a service manager with one loaded unit that records every act asked of it.
type manager struct{ acts []string }

func (m *manager) ListUnitsByPatterns(context.Context, []string, []string) ([]services.Unit, error) {
	return []services.Unit{{Name: "olivares.service", LoadState: "loaded", ActiveState: "active", SubState: "running"}}, nil
}
func (m *manager) GetUnitFileState(context.Context, string) (string, error) { return "enabled", nil }
func (m *manager) Dependents(context.Context, string) ([]string, error)     { return nil, nil }
func (m *manager) Subscribe(context.Context) (<-chan services.JobRemoved, func(), error) {
	m.acts = append(m.acts, "Subscribe")
	return make(chan services.JobRemoved), func() {}, nil
}
func (m *manager) QueueJob(_ context.Context, method, _, _ string) (string, error) {
	m.acts = append(m.acts, method)
	return "/org/freedesktop/systemd1/job/1", nil
}
func (m *manager) EnableUnitFiles(context.Context, []string, bool, bool) (int, error) {
	m.acts = append(m.acts, "EnableUnitFiles")
	return 0, nil
}
func (m *manager) DisableUnitFiles(context.Context, []string, bool) (int, error) {
	m.acts = append(m.acts, "DisableUnitFiles")
	return 0, nil
}

var id = strings.Repeat("cd", 16)

func TestUnitsHelper_ActsWaitForTheServiceActAndReadsAnswer(t *testing.T) {
	if actAdopted {
		t.Fatal("the service act is adopted in source: its product consumer must be composed and measured first")
	}
	m := &manager{}
	dial := func() (services.Bus, func(), error) { return m, func() {}, nil }
	journalRuns := 0
	journal := func(context.Context, []string, int64) ([]byte, error) {
		journalRuns++
		return []byte(`{"__REALTIME_TIMESTAMP":"1790000000000000","PRIORITY":"6","MESSAGE":"started"}`), nil
	}
	serve := func(h invocation.Helper, k invocation.Kernel, args []string, doc string) (int, helperschema.Response, string) {
		var out, log bytes.Buffer
		code := invocation.Serve(context.Background(), h, k, args, 0, strings.NewReader(doc), &out, &log)
		var resp helperschema.Response
		_ = json.Unmarshal(out.Bytes(), &resp)
		return code, resp, out.String()
	}
	portal := kernel{uid: 990, account: "olivares-portal", unit: "olivares-portal.service", ttyNr: "0", pidfd: true}
	h := helper(dial, journal)

	code, resp, _ := serve(h, portal, nil, `{"op":"list"}`)
	if code != invocation.ExitDone || resp.Result != helperschema.ResultAnswered || !strings.Contains(string(resp.Bundle), "olivares.service") {
		t.Fatalf("list: exit %d %+v", code, resp)
	}
	code, resp, _ = serve(h, portal, nil, `{"op":"status","unit":"olivares.service"}`)
	if code != invocation.ExitDone || resp.Result != helperschema.ResultAnswered || !strings.Contains(string(resp.Bundle), `"class":"managed"`) {
		t.Fatalf("status: exit %d %+v", code, resp)
	}
	peer := helperschema.Peer{UID: 990, Account: "olivares-portal", Unit: "olivares-portal.service", Attested: true}
	for _, doc := range []string{
		`{"op":"logs","unit":"olivares.service","operation_id":"` + id + `"}`,
		`{"op":"stop","unit":"olivares.service","operation_id":"` + id + `"}`,
		`{"op":"enable","unit":"olivares.service","operation_id":"` + id + `"}`,
	} {
		// The seam refuses every act before its guest proof of the peer's pidfd.
		if code, resp, _ := serve(h, portal, nil, doc); code != invocation.ExitRefused || resp.Code != helperschema.CodePidfdUnproven {
			t.Errorf("%s: exit %d %+v", doc, code, resp)
		}
		// Past that proof, the helper still refuses every act until the service act is adopted.
		req := &services.Request{}
		if err := helperschema.Decode(strings.NewReader(doc), req); err != nil {
			t.Fatal(err)
		}
		if resp := h.Perform(context.Background(), peer, req); resp.Result != helperschema.ResultRefused || resp.Code != services.CodeActNotAdopted {
			t.Errorf("%s performed before adoption: %+v", doc, resp)
		}
	}
	if journalRuns != 0 || len(m.acts) != 0 {
		t.Fatalf("a refused act reached the journal reader (%d) or the service manager (%v)", journalRuns, m.acts)
	}
	// With the adoption switched, the same acts reach the executor (a fake manager here).
	adopted := helperWith(true, dial, journal)
	if resp := adopted.Perform(context.Background(), peer, &services.Request{Op: services.OpLogs, Unit: "olivares.service", OperationID: id}); resp.Result != helperschema.ResultPerformed || journalRuns != 1 {
		t.Errorf("an adopted logs act answered %+v after %d journal runs", resp, journalRuns)
	}
	if resp := adopted.Perform(context.Background(), peer, &services.Request{Op: services.OpEnable, Unit: "olivares.service", OperationID: id}); resp.Result != helperschema.ResultPerformed || !slices.Equal(m.acts, []string{"EnableUnitFiles"}) {
		t.Errorf("an adopted enable answered %+v after %v", resp, m.acts)
	}
	// No argument and no path: an argument is a usage failure, and --help states the contract.
	if code, _, _ := serve(h, portal, []string{"--unit", "/etc/passwd"}, `{"op":"list"}`); code != invocation.ExitFailure {
		t.Errorf("an argument: exit %d", code)
	}
	if code, _, out := serve(h, portal, []string{"--help"}, ""); code != invocation.ExitDone || !strings.Contains(out, "no path") {
		t.Errorf("--help: exit %d %q", code, out)
	}
	// An invoker outside the table is refused, whatever the document says.
	stranger := kernel{uid: 1000, account: "operator", unit: "run-u12.service", ttyNr: "0", pidfd: true}
	if code, resp, _ := serve(h, stranger, nil, `{"op":"list"}`); code != invocation.ExitRefused || resp.Code != helperschema.CodeNotAdmitted {
		t.Errorf("a stranger: exit %d %+v", code, resp)
	}
}
