// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package firewall_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/firewall"
	"github.com/olivaresai/olivares/appliance/layer/firewall/policy"
	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

// candidateJSON is a valid candidate policy document.
func candidateJSON(t *testing.T) string {
	t.Helper()
	data, err := policy.Canonical(managed())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRequest_IsClosedAndTakesNoPath(t *testing.T) {
	id := operationID(1)
	candidate := candidateJSON(t)
	for _, doc := range []string{
		`{"op":"status"}`,
		`{"op":"apply","operation_id":"` + id + `","candidate":` + candidate + `}`,
		`{"op":"apply","operation_id":"` + id + `","candidate":` + candidate + `,"app":"proxy","revert_after_s":600}`,
		`{"op":"confirm","operation_id":"` + id + `"}`,
		`{"op":"revert","operation_id":"` + id + `"}`,
	} {
		var r firewall.Request
		if err := helperschema.Decode(strings.NewReader(doc), &r); err != nil {
			t.Errorf("%s: %v", doc, err)
		}
	}
	for name, doc := range map[string]string{
		"a path":                         `{"op":"apply","operation_id":"` + id + `","candidate":` + candidate + `,"path":"/etc/nftables.conf"}`,
		"a rule":                         `{"op":"apply","operation_id":"` + id + `","candidate":` + strings.Replace(candidate, `"apps":[]`, `"apps":[],"rules":["accept"]`, 1) + `}`,
		"product ports":                  `{"op":"apply","operation_id":"` + id + `","candidate":` + strings.Replace(candidate, `"apps":[]`, `"apps":[],"product_ports":["1/tcp"]`, 1) + `}`,
		"an unknown op":                  `{"op":"flush"}`,
		"a folded key":                   `{"OP":"status"}`,
		"a read with an operation":       `{"op":"status","operation_id":"` + id + `"}`,
		"an act without its operation":   `{"op":"confirm"}`,
		"an apply without a candidate":   `{"op":"apply","operation_id":"` + id + `"}`,
		"a confirm with a candidate":     `{"op":"confirm","operation_id":"` + id + `","candidate":` + candidate + `}`,
		"a window below a minute":        `{"op":"apply","operation_id":"` + id + `","candidate":` + candidate + `,"revert_after_s":30}`,
		"a window above ten minutes":     `{"op":"apply","operation_id":"` + id + `","candidate":` + candidate + `,"revert_after_s":601}`,
		"an application that is no slug": `{"op":"apply","operation_id":"` + id + `","candidate":` + candidate + `,"app":"../x"}`,
		"a candidate outside the schema": `{"op":"apply","operation_id":"` + id + `","candidate":` + strings.Replace(candidate, `"ssh_port":22`, `"ssh_port":0`, 1) + `}`,
		"an upper-case operation id":     `{"op":"revert","operation_id":"` + strings.ToUpper(id) + `"}`,
		"a document followed by another": `{"op":"status"}{"op":"status"}`,
	} {
		var r firewall.Request
		err := helperschema.Decode(strings.NewReader(doc), &r)
		var input *helperschema.InputError
		if !errors.As(err, &input) {
			t.Errorf("%s: %v, want an input refusal", name, err)
		}
	}
}

func TestRules_AdmitTheConsoleAndTheRepairConsoleOnly(t *testing.T) {
	rules := firewall.Rules()
	peer := func(i helperschema.Invoker) helperschema.Peer {
		uid := uint32(1000)
		if i.Account == "root" {
			uid = 0
		}
		return helperschema.Peer{UID: uid, Account: i.Account, Unit: i.Unit, TTY: i.TTY, Attested: true}
	}
	for _, c := range []struct {
		op      string
		invoker helperschema.Invoker
		admit   bool
	}{
		{"status", helperschema.Portal, true},
		{"status", helperschema.RepairConsole, true},
		{"status", helperschema.NetGuard, false},
		{"apply", helperschema.Portal, true},
		{"apply", helperschema.RepairConsole, false},
		{"confirm", helperschema.Portal, true},
		{"confirm", helperschema.RepairConsole, false},
		{"revert", helperschema.Portal, true},
		{"revert", helperschema.RepairConsole, true},
		{"revert", helperschema.NetGuard, false},
	} {
		err := helperschema.Admit(peer(c.invoker), rules, c.op)
		if (err == nil) != c.admit {
			t.Errorf("%s by %s: %v, want admitted %v", c.op, c.invoker.Name, err, c.admit)
		}
	}
	unattested := peer(helperschema.Portal)
	unattested.Attested = false
	for _, op := range []string{"apply", "confirm", "revert"} {
		var refusal *helperschema.Refusal
		if err := helperschema.Admit(unattested, rules, op); !errors.As(err, &refusal) || refusal.Code != helperschema.CodeNoConnectionIdentity {
			t.Errorf("%s without a proven connection identity: %v", op, err)
		}
	}
	if err := helperschema.Admit(peer(helperschema.Portal), rules, "flush"); err == nil {
		t.Error("an op outside the closed set was admitted")
	}
}

func TestAnswer_StatusReadsAndActsReachTheOwner(t *testing.T) {
	ctx := context.Background()
	o := newOwner(t)
	install(t, o, managed())
	status := firewall.Answer(ctx, o.Owner, &firewall.Request{Op: "status"})
	if status.Result != helperschema.ResultAnswered {
		t.Fatalf("status: %+v", status)
	}
	var read firewall.Status
	if err := json.Unmarshal(status.Bundle, &read); err != nil || read.ConfirmedDigest != policy.Digest(managed()) || read.Windows == nil {
		t.Errorf("the status answer is %s (%v)", status.Bundle, err)
	}
	candidate := managed()
	candidate.Portal.Enabled = false
	id := operationID(2)
	applied := firewall.Answer(ctx, o.Owner, &firewall.Request{Op: "apply", OperationID: id, Candidate: &candidate})
	var w firewall.Window
	if applied.Result != helperschema.ResultPerformed || json.Unmarshal(applied.Bundle, &w) != nil || w.State != firewall.WindowPending {
		t.Fatalf("apply: %+v", applied)
	}
	// A second apply while that window is open is the owner's closed refusal, with no effect.
	other := firewall.Answer(ctx, o.Owner, &firewall.Request{Op: "apply", OperationID: operationID(3), Candidate: &candidate})
	if other.Result != helperschema.ResultRefused || other.Code != firewall.CodeTargetLocked {
		t.Errorf("a second window: %+v", other)
	}
	reverted := firewall.Answer(ctx, o.Owner, &firewall.Request{Op: "revert", OperationID: id})
	if reverted.Result != helperschema.ResultPerformed || json.Unmarshal(reverted.Bundle, &w) != nil || w.State != firewall.WindowReverted {
		t.Errorf("revert: %+v", reverted)
	}
	if o.kernel.digest(t) != policy.Digest(managed()) {
		t.Error("the revert did not load the confirmed policy")
	}
}

func TestKernel_ReadsTheTableNftReports(t *testing.T) {
	// The fixed argument vectors: the ruleset travels on standard input, never as a path.
	if !slices.Equal(firewall.LoadArgs, []string{"-f", "-"}) || !slices.Equal(firewall.ListArgs, []string{"-j", "list", "table", "inet", "olivares"}) {
		t.Errorf("nft runs with %q and %q", firewall.LoadArgs, firewall.ListArgs)
	}
	digest := "sha256:" + strings.Repeat("ab", 32)
	listed := `{"nftables": [{"metainfo": {"version": "1.1.3", "json_schema_version": 1}},
 {"table": {"family": "inet", "name": "olivares", "handle": 7, "comment": "olivares-firewall-policy ` + digest + `"}},
 {"chain": {"family": "inet", "table": "olivares", "name": "input", "handle": 1, "type": "filter", "hook": "input", "prio": 0, "policy": "drop"}},
 {"chain": {"family": "inet", "table": "olivares", "name": "forward", "handle": 2, "type": "filter", "hook": "forward", "prio": 0, "policy": "drop"}},
 {"rule": {"family": "inet", "table": "olivares", "chain": "input", "handle": 3, "expr": [{"match": {"op": "in", "left": {"ct": {"key": "state"}}, "right": ["established", "related"]}}, {"accept": null}]}},
 {"rule": {"family": "inet", "table": "olivares", "chain": "input", "handle": 4, "expr": [{"drop": null}]}},
 {"rule": {"family": "inet", "table": "olivares", "chain": "forward", "handle": 5, "expr": [{"accept": null}]}}]}`
	got, err := firewall.ParseTable([]byte(listed))
	want := firewall.Table{Comment: "olivares-firewall-policy " + digest, InputPolicy: "drop", ForwardPolicy: "drop", InputRules: 2, ForwardRules: 1}
	if err != nil || got != want {
		t.Fatalf("the listed table reads %+v (%v), want %+v", got, err, want)
	}
	for name, doc := range map[string]string{
		"no table":                  `{"nftables": [{"metainfo": {"version": "1.1.3"}}]}`,
		"another table":             strings.ReplaceAll(listed, `"olivares"`, `"filter"`),
		"no forward chain":          strings.Replace(listed, `"name": "forward"`, `"name": "fwd"`, 1),
		"a hook on the wrong chain": strings.Replace(listed, `"hook": "input"`, `"hook": "output"`, 1),
		"not a listing":             `{"table": {}}`,
		"trailing data":             listed + `{}`,
	} {
		if _, err := firewall.ParseTable([]byte(doc)); err == nil {
			t.Errorf("%s: read as a table", name)
		}
	}
}

func TestHost_SSHPortIsTheOnePortSshdReports(t *testing.T) {
	ctx := context.Background()
	answer := func(out string, err error) (func(context.Context, string, ...string) ([]byte, error), *[]string) {
		var calls []string
		return func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, strings.Join(append([]string{name}, args...), " "))
			return []byte(out), err
		}, &calls
	}
	run, calls := answer("port 2222\naddressfamily any\nlistenaddress [::]:2222\nlistenaddress 0.0.0.0:2222\n", nil)
	if port, err := firewall.SSHPort(ctx, run); err != nil || port != 2222 {
		t.Errorf("sshd reports port 2222; read %d (%v)", port, err)
	}
	if !slices.Equal(*calls, []string{"/usr/sbin/sshd -T"}) {
		t.Errorf("ran %q", *calls)
	}
	for name, out := range map[string]string{
		"two ports":  "port 22\nport 2222\n",
		"no port":    "addressfamily any\n",
		"port zero":  "port 0\n",
		"not a port": "port 22;accept\n",
		"a big port": "port 65536\n",
	} {
		run, _ := answer(out, nil)
		if port, err := firewall.SSHPort(ctx, run); err == nil {
			t.Errorf("%s: read port %d", name, port)
		}
	}
	run, _ = answer("port 22\n", errors.New("exit status 255"))
	if _, err := firewall.SSHPort(ctx, run); err == nil {
		t.Error("a failed sshd -T gave a port")
	}
}

func TestHost_DHCPv6ClientInterfacesAreThePhysicalLinks(t *testing.T) {
	dir := t.TempDir()
	for _, link := range []struct {
		name   string
		device bool
	}{{"eth1", true}, {"eth0", true}, {"lo", false}, {"veth3a", false}, {"podman0", false}, {"bad name", true}} {
		if err := os.MkdirAll(filepath.Join(dir, link.name), 0o755); err != nil {
			t.Fatal(err)
		}
		if link.device {
			if err := os.WriteFile(filepath.Join(dir, link.name, "device"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := firewall.ClientInterfaces(dir)
	if err != nil || !slices.Equal(got, []string{"eth0", "eth1"}) {
		t.Errorf("the physical links are %q (%v), want eth0 and eth1", got, err)
	}
	for i := 0; i < policy.MaxInterfaces; i++ {
		name := "en" + string(rune('a'+i))
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name, "device"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := firewall.ClientInterfaces(dir); err == nil {
		t.Errorf("more physical links than the policy bounds were read: %q", got)
	}
}

// A candidate the console builds from a selection without interfaces still travels to the
// helper: the seam refuses a null anywhere, so every list encodes as a list.
func TestCandidate_TravelsToTheHelperWithoutANull(t *testing.T) {
	confirmed := firewall.Initial(firewall.Selection{}, 22, nil)
	for name, d := range map[string]policy.Document{
		"initial":   confirmed,
		"candidate": firewall.Candidate(confirmed, firewall.Selection{Listen: policy.ListenLocal}),
	} {
		data, err := json.Marshal(firewall.Request{Op: firewall.OpApply, OperationID: operationID(5), Candidate: &d})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "null") {
			t.Errorf("%s encodes a null: %s", name, data)
		}
		var r firewall.Request
		if err := helperschema.Decode(strings.NewReader(string(data)), &r); err != nil {
			t.Errorf("%s: the helper refuses it: %v", name, err)
		}
	}
}
