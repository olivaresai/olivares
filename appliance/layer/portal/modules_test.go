// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package portal

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/firewall"
	"github.com/olivaresai/olivares/appliance/layer/firewall/policy"
	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/portal/localsession"
	"github.com/olivaresai/olivares/appliance/layer/storage"
)

func TestModuleReads_KeepTheLastCompletedAnswerOfEachModuleHelper(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 9, 27, 19, 0, 0, 0, time.UTC)
	reads := newModuleReads(func() time.Time { return clock })
	if _, _, ok := reads.Units(); ok {
		t.Fatal("the units were read before any helper answered")
	}
	if _, _, ok := reads.Storage(); ok {
		t.Fatal("the storage was read before any helper answered")
	}

	var asked []string
	answers := map[string]helperschema.Response{}
	var failing error
	call := func(_ context.Context, name string, request helperschema.Request) (helperschema.Response, error) {
		document, _ := json.Marshal(request)
		asked = append(asked, name+" "+string(document))
		return answers[name], failing
	}
	inventory := storage.Inventory{Schema: storage.Schema, BootID: strings.Repeat("ab", 16), LVM: storage.LVM{State: storage.LVMRead},
		Disks: []storage.Disk{{Device: "/dev/vda", Size: 1 << 34, Consumers: []storage.Consumer{}, Partitions: []storage.Partition{}, Operations: []storage.Operation{}}}}
	document, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	answers[helperschema.HelperUnits] = helperschema.Response{Result: helperschema.ResultAnswered,
		Bundle: json.RawMessage(`{"units":[{"name":"sshd.service","load_state":"loaded","active_state":"active","sub_state":"running","class":"lockout-risk"}],"truncated":false}`)}
	answers[helperschema.HelperStorage] = helperschema.Response{Result: helperschema.ResultAnswered, Bundle: document}

	reads.refresh(ctx, call)
	units, stamp, ok := reads.Units()
	if !ok || len(units.Units) != 1 || units.Units[0].Name != "sshd.service" || stamp.Generation != 1 || !stamp.ReadAt.Equal(clock) {
		t.Fatalf("units %+v %+v %v", units, stamp, ok)
	}
	disks, diskStamp, ok := reads.Storage()
	if !ok || !reflect.DeepEqual(disks, inventory) || diskStamp.Generation != 1 || !diskStamp.ReadAt.Equal(clock) {
		t.Fatalf("storage %+v %+v %v", disks, diskStamp, ok)
	}

	// An answer that is refused, failed, lost or not the helper's closed document keeps the last
	// completed one and its stamp.
	clock = clock.Add(time.Minute)
	for name, bad := range map[string]helperschema.Response{
		"refused":           {Result: helperschema.ResultRefused, Code: helperschema.CodeNotAdmitted},
		"failed":            {Result: helperschema.ResultFailed, Code: helperschema.CodeEffectFailed},
		"performed":         {Result: helperschema.ResultPerformed},
		"an unknown member": {Result: helperschema.ResultAnswered, Bundle: json.RawMessage(`{"units":[],"truncated":false,"schema":"x","extra":1}`)},
		"another schema":    {Result: helperschema.ResultAnswered, Bundle: json.RawMessage(`{"schema":"other"}`)},
		"no document":       {Result: helperschema.ResultAnswered},
	} {
		answers[helperschema.HelperUnits], answers[helperschema.HelperStorage] = bad, bad
		reads.refresh(ctx, call)
		if got, gotStamp, _ := reads.Units(); !reflect.DeepEqual(got, units) || gotStamp != stamp {
			t.Errorf("%s replaced the units: %+v %+v", name, got, gotStamp)
		}
		if got, gotStamp, _ := reads.Storage(); !reflect.DeepEqual(got, disks) || gotStamp != diskStamp {
			t.Errorf("%s replaced the storage: %+v %+v", name, got, gotStamp)
		}
	}
	failing = errors.New("consumer_unavailable")
	reads.refresh(ctx, call)
	if _, gotStamp, _ := reads.Units(); gotStamp != stamp {
		t.Errorf("a call error replaced the units")
	}
	failing = nil

	// A newer completed answer replaces the last one with the next generation.
	answers[helperschema.HelperUnits] = helperschema.Response{Result: helperschema.ResultAnswered, Bundle: json.RawMessage(`{"units":[],"truncated":true}`)}
	reads.refresh(ctx, call)
	if got, gotStamp, ok := reads.Units(); !ok || len(got.Units) != 0 || !got.Truncated || gotStamp.Generation != 2 || !gotStamp.ReadAt.Equal(clock) {
		t.Fatalf("a newer answer: %+v %+v", got, gotStamp)
	}

	// Every document the reader sent was one of the three reads, never an act.
	if len(asked) != 3*9 {
		t.Fatalf("the reader sent %d documents in 9 rounds", len(asked))
	}
	for _, sent := range asked {
		if sent != `units {"op":"list"}` && sent != `storage {"op":"inventory"}` && sent != `firewall {"op":"status"}` {
			t.Errorf("the reader sent %s", sent)
		}
	}
}

func TestModuleReads_ARoundWaitsForTheSharedLifecycleLock(t *testing.T) {
	ctx := context.Background()
	reads := newModuleReads(time.Now)
	calls := 0
	call := func(context.Context, string, helperschema.Request) (helperschema.Response, error) {
		calls++
		return helperschema.Response{Result: helperschema.ResultFailed}, nil
	}
	if reads.round(ctx, call, func() (func(), error) { return nil, errors.New("held exclusively") }) || calls != 0 {
		t.Fatalf("a round asked %d helpers without the shared lifecycle lock", calls)
	}
	released := 0
	if !reads.round(ctx, call, func() (func(), error) { return func() { released++ }, nil }) || calls != 3 || released != 1 {
		t.Fatalf("a round with the lock: calls %d, released %d", calls, released)
	}

	// start runs a round at once and one per tick, until it is stopped.
	calls = 0
	ticks := make(chan time.Time)
	stop := reads.start(call, ticks, func() (func(), error) { return func() {}, nil })
	ticks <- time.Now()
	stop()
	if calls != 6 {
		t.Fatalf("two rounds asked %d helpers, want 6", calls)
	}
}

// managedPolicy is a confirmed policy: SSH, the product's ports and the console on eth0.
func managedPolicy() policy.Document {
	return policy.Document{SchemaVersion: policy.SchemaVersion, SSHPort: 22, DHCPv6ClientInterfaces: []string{"eth0"}, Apps: []policy.AppRow{},
		Portal: policy.Portal{Enabled: true, Listen: policy.ListenManagement, ManagementInterfaces: []string{"eth0"}}}
}

func TestModuleReads_KeepTheLastCompletedFirewallStatusWithItsMeasurement(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 9, 27, 19, 0, 0, 0, time.UTC)
	reads := newModuleReads(func() time.Time { return clock })
	confirmed := managedPolicy()
	digest := policy.Digest(confirmed)
	measured := firewall.Measurement{SchemaVersion: firewall.MeasurementSchema, BootID: "8d8a1f0c-54c0-4b3e-9d6a-2a1f3b4c5d6e",
		MeasuredAt: "2026-09-27T18:59:00Z", PolicyDigest: digest, InputPolicy: "drop", Rows: policy.Rows(confirmed)}
	reason := ""
	measures := 0
	reads.measure = func() (firewall.Measurement, string) {
		measures++
		if reason != "" {
			return firewall.Measurement{}, reason
		}
		return measured, ""
	}
	if _, _, ok := reads.Firewall(); ok {
		t.Fatal("the firewall was read before its helper answered")
	}

	candidate := managedPolicy()
	candidate.Portal.Enabled = false
	id := strings.Repeat("cd", 16)
	status := firewall.Status{ConfirmedDigest: digest, Confirmed: &confirmed, Windows: []firewall.Window{{SchemaVersion: firewall.WindowSchema,
		OperationID: id, BootID: measured.BootID, DeadlineNS: 120e9, CandidateDigest: policy.Digest(candidate), PreviousDigest: digest,
		Candidate: candidate, State: firewall.WindowPending}}}
	bundle, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	var asked []string
	answer := helperschema.Response{Result: helperschema.ResultAnswered, Bundle: bundle}
	call := func(_ context.Context, name string, request helperschema.Request) (helperschema.Response, error) {
		document, _ := json.Marshal(request)
		asked = append(asked, name+" "+string(document))
		if name != helperschema.HelperFirewall {
			return helperschema.Response{Result: helperschema.ResultFailed}, nil
		}
		return answer, nil
	}

	reads.refresh(ctx, call)
	read, stamp, ok := reads.Firewall()
	wantHead := localsession.FirewallHead{PolicyDigest: digest, BootID: measured.BootID, MeasuredAt: measured.MeasuredAt, InputPolicy: "drop", ConfirmedDigest: digest}
	if !ok || read.Head != wantHead || stamp.Generation != 1 || !stamp.ReadAt.Equal(clock) {
		t.Fatalf("the firewall read is %+v %+v %v, want the head %+v", read, stamp, ok, wantHead)
	}
	var wantRows []localsession.FirewallRow
	for _, row := range measured.Rows {
		wantRows = append(wantRows, localsession.FirewallRow{Kind: localsession.FirewallPort, Port: row.Port, Interfaces: row.Interfaces})
	}
	wantRows = append(wantRows, localsession.FirewallRow{Kind: localsession.FirewallWindow, OperationID: id, State: firewall.WindowPending,
		CandidateDigest: policy.Digest(candidate)})
	if len(measured.Rows) < 3 || !reflect.DeepEqual(read.Rows, wantRows) {
		t.Fatalf("the firewall rows are %+v, want each measured port, then the window: %+v", read.Rows, wantRows)
	}

	// An answer that is refused, failed or not the owner's closed status keeps the last completed read,
	// its measurement and its stamp, although the measurement changed meanwhile.
	clock = clock.Add(time.Minute)
	reason = "the measurement is of another boot"
	confirmedJSON, err := json.Marshal(confirmed)
	if err != nil {
		t.Fatal(err)
	}
	for name, bad := range map[string]helperschema.Response{
		"refused":           {Result: helperschema.ResultRefused, Code: helperschema.CodeNotAdmitted},
		"failed":            {Result: helperschema.ResultFailed, Code: helperschema.CodeEffectFailed},
		"performed":         {Result: helperschema.ResultPerformed, Bundle: bundle},
		"no document":       {Result: helperschema.ResultAnswered},
		"an unknown member": {Result: helperschema.ResultAnswered, Bundle: json.RawMessage(`{"confirmed_digest":"","confirmed":null,"windows":[],"rules":[]}`)},
		"no windows":        {Result: helperschema.ResultAnswered, Bundle: json.RawMessage(`{"confirmed_digest":"","confirmed":null}`)},
		"a digest that is not the confirmed policy's": {Result: helperschema.ResultAnswered,
			Bundle: json.RawMessage(`{"confirmed_digest":"sha256:` + strings.Repeat("0", 64) + `","confirmed":` + string(confirmedJSON) + `,"windows":[]}`)},
		"a digest without a confirmed policy": {Result: helperschema.ResultAnswered,
			Bundle: json.RawMessage(`{"confirmed_digest":"` + digest + `","confirmed":null,"windows":[]}`)},
		"a window of no state of the owner": {Result: helperschema.ResultAnswered,
			Bundle: []byte(strings.Replace(string(bundle), `"state":"pending"`, `"state":"applied"`, 1))},
		"a second document": {Result: helperschema.ResultAnswered, Bundle: append(append([]byte{}, bundle...), bundle...)},
	} {
		answer = bad
		reads.refresh(ctx, call)
		if got, gotStamp, ok := reads.Firewall(); !ok || !reflect.DeepEqual(got, read) || gotStamp != stamp {
			t.Errorf("%s replaced the firewall read: %+v %+v", name, got, gotStamp)
		}
	}

	// A newer completed answer, with the firewall unmeasured and nothing confirmed, is the next
	// generation: why it is unmeasured, no port and no window.
	answer = helperschema.Response{Result: helperschema.ResultAnswered, Bundle: json.RawMessage(`{"confirmed_digest":"","confirmed":null,"windows":[]}`)}
	reads.refresh(ctx, call)
	read, stamp, ok = reads.Firewall()
	if !ok || read.Head != (localsession.FirewallHead{Unmeasured: reason}) || len(read.Rows) != 0 || stamp.Generation != 2 || !stamp.ReadAt.Equal(clock) {
		t.Fatalf("a newer answer: %+v %+v %v", read, stamp, ok)
	}
	if measures == 0 {
		t.Fatal("the measurement was never read")
	}

	// The firewall helper was asked for its status and nothing else.
	for _, sent := range asked {
		if strings.HasPrefix(sent, "firewall ") && sent != `firewall {"op":"status"}` {
			t.Errorf("the reader sent %s", sent)
		}
	}
}
