// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package localclient

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/portal/localframe"
	"github.com/olivaresai/olivares/appliance/layer/portal/localsession"
)

type transport struct {
	records []localframe.Record
	err     error
	writes  []localframe.Record
	closed  bool
}

func (t *transport) Next() (localframe.Record, error) {
	if len(t.records) == 0 {
		return localframe.Record{}, t.err
	}
	r := t.records[0]
	t.records = t.records[1:]
	return r, nil
}
func (t *transport) Write(r localframe.Record) error { t.writes = append(t.writes, r); return nil }
func (t *transport) Close() error                    { t.closed = true; return nil }

func TestLocalClient_RefusedWithForeignCredentialsIsProtocolMismatch(t *testing.T) {
	wire := &transport{err: &localframe.Fault{Reason: "credentials_wrong_account"}}
	_, err := connect(wire)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "local_protocol_mismatch" || failure.Sent || len(wire.writes) != 0 || !wire.closed {
		t.Fatalf("failure=%#v writes=%d closed=%v", failure, len(wire.writes), wire.closed)
	}
}
func TestLocalClient_RefusedAuthenticatesBeforeAnyRequest(t *testing.T) {
	for _, code := range []string{"local_peer_refused", "local_capacity", "pidfd_unproven", "passcred_unset"} {
		wire := &transport{records: []localframe.Record{{Type: "refused", Code: code}}}
		_, err := connect(wire)
		var refused *Refused
		if !errors.As(err, &refused) || refused.Code != code || len(wire.writes) != 0 || !wire.closed {
			t.Fatalf("code=%s err=%v", code, err)
		}
	}
}
func TestLocalClient_CredentialFailureAfterOperationSentReportsUnverifiedAndNeverResends(t *testing.T) {
	wire := &transport{records: []localframe.Record{{Type: "ready", Version: 1}}, err: &localframe.Fault{Reason: "credentials_wrong_account"}}
	client, err := connect(wire)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = client.Operation("12121212121212121212121212121212", "cli")
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "response_unverified" || !failure.Sent || len(wire.writes) != 1 || !wire.closed {
		t.Fatalf("failure=%#v writes=%d", failure, len(wire.writes))
	}
	_, _, _ = client.Operation("12121212121212121212121212121212", "cli")
	if len(wire.writes) != 1 {
		t.Fatal("request resent on failed connection")
	}
}
func TestLocalClient_RejectsRefusedAfterReadyAndMismatchedResponse(t *testing.T) {
	for _, reply := range []localframe.Record{{Type: "refused", Code: "pidfd_unproven"}, {Type: "response", Op: "task.plan", Status: 200}} {
		wire := &transport{records: []localframe.Record{{Type: "ready", Version: 1}, reply}}
		client, err := connect(wire)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := client.Operation("12121212121212121212121212121212", "cli"); err == nil || !wire.closed {
			t.Fatal("unexpected reply accepted")
		}
	}
}

func TestLocalClient_ModuleReadSendsOneClosedRequestAndDecodesItsAnswer(t *testing.T) {
	answer := `{"query":"services.list","generation":3,"read_at":"2026-09-27T19:00:00Z","offset":0,"next":0,"total":1,"truncated":false,` +
		`"units":[{"name":"sshd.service","class":"lockout-risk","load_state":"loaded","active_state":"active","sub_state":"running"}]}`
	wire := &transport{records: []localframe.Record{{Type: "ready", Version: 1}, {Type: "response", Op: "module.read", Status: 200, Body: json.RawMessage(answer)}}}
	client, err := connect(wire)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Read(localsession.QueryServicesList, "", 0, "cli")
	if err != nil || got.Query != localsession.QueryServicesList || got.Generation != 3 || len(got.Units) != 1 || got.Units[0].Name != "sshd.service" {
		t.Fatalf("read: %+v %v", got, err)
	}
	if len(wire.writes) != 1 || wire.writes[0].Type != "request" || wire.writes[0].Op != "module.read" ||
		string(wire.writes[0].Body) != `{"query":"services.list","surface":"cli"}` {
		t.Fatalf("sent %#v", wire.writes)
	}

	wire = &transport{records: []localframe.Record{{Type: "ready", Version: 1}, {Type: "response", Op: "module.read", Status: 200, Body: json.RawMessage(answer)}}}
	client, _ = connect(wire)
	if _, err := client.Read(localsession.QueryServicesStatus, "sshd.service", 0, "tui"); err == nil || !wire.closed {
		t.Fatalf("an answer to another query was accepted: %v", err)
	}
	if string(wire.writes[0].Body) != `{"query":"services.status","unit":"sshd.service","surface":"tui"}` {
		t.Fatalf("sent %s", wire.writes[0].Body)
	}

	wire = &transport{records: []localframe.Record{{Type: "ready", Version: 1}, {Type: "error", Op: "module.read", Status: 503, Code: "consumer_unavailable"}}}
	client, _ = connect(wire)
	_, err = client.Read(localsession.QueryStorageInventory, "", 40, "cli")
	var refused *Refused
	if !errors.As(err, &refused) || refused.Code != "consumer_unavailable" || string(wire.writes[0].Body) != `{"query":"storage.inventory","offset":40,"surface":"cli"}` {
		t.Fatalf("a refused read: %v, sent %s", err, wire.writes[0].Body)
	}
}

func TestLocalClient_FirewallStatusIsVerifiedByItsHead(t *testing.T) {
	head := `"firewall":{"policy_digest":"sha256:` + strings.Repeat("1f", 32) + `","input_policy":"drop","confirmed_digest":"sha256:` + strings.Repeat("1f", 32) + `"}`
	answer := `{"query":"firewall.status","generation":2,"read_at":"2026-09-27T19:00:00Z","offset":0,"next":0,"total":1,"truncated":false,` + head +
		`,"firewall_rows":[{"kind":"port","port":"22/tcp","interfaces":["*"]}]}`
	wire := &transport{records: []localframe.Record{{Type: "ready", Version: 1}, {Type: "response", Op: "module.read", Status: 200, Body: json.RawMessage(answer)}}}
	client, err := connect(wire)
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Read(localsession.QueryFirewallStatus, "", 0, "cli")
	if err != nil || got.Firewall == nil || got.Firewall.InputPolicy != "drop" || len(got.FirewallRows) != 1 || got.FirewallRows[0].Port != "22/tcp" {
		t.Fatalf("read: %+v %v", got, err)
	}
	if len(wire.writes) != 1 || string(wire.writes[0].Body) != `{"query":"firewall.status","surface":"cli"}` {
		t.Fatalf("sent %#v", wire.writes)
	}

	// A firewall.status answer without its head, and another query's answer with one, are
	// unverified and end the session.
	for name, c := range map[string]struct{ query, answer string }{
		"a firewall.status answer without its head": {localsession.QueryFirewallStatus,
			`{"query":"firewall.status","generation":2,"read_at":"2026-09-27T19:00:00Z","offset":0,"next":0,"total":0,"truncated":false}`},
		"a services.list answer with the firewall's head": {localsession.QueryServicesList,
			`{"query":"services.list","generation":2,"read_at":"2026-09-27T19:00:00Z","offset":0,"next":0,"total":0,"truncated":false,` + head + `}`},
		"a storage.inventory answer with firewall rows": {localsession.QueryStorageInventory,
			`{"query":"storage.inventory","generation":2,"read_at":"2026-09-27T19:00:00Z","offset":0,"next":0,"total":1,"truncated":false,"firewall_rows":[{"kind":"port","port":"22/tcp","interfaces":["*"]}]}`},
	} {
		wire := &transport{records: []localframe.Record{{Type: "ready", Version: 1}, {Type: "response", Op: "module.read", Status: 200, Body: json.RawMessage(c.answer)}}}
		client, _ := connect(wire)
		var failure *Failure
		if _, err := client.Read(c.query, "", 0, "cli"); !errors.As(err, &failure) || failure.Code != "response_unverified" || !wire.closed {
			t.Errorf("%s: %v, want response_unverified and a closed session", name, err)
		}
	}
}
