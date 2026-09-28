// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package storage_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/storage"
)

func TestHelper_InventoryIsAReadForTheConsoleAlone(t *testing.T) {
	rules := storage.HelperRules()
	if len(rules) != 1 || rules[0].Subcommand != storage.SubcommandInventory || rules[0].Mutating ||
		!reflect.DeepEqual(rules[0].Invokers, []helperschema.Invoker{helperschema.Portal}) {
		t.Fatalf("rules %+v: one read, for the Appliance Console alone", rules)
	}
	portal := helperschema.Peer{UID: 998, Account: "olivares-portal", Unit: "olivares-portal.service", Attested: true}
	if err := helperschema.Admit(portal, rules, storage.SubcommandInventory); err != nil {
		t.Fatalf("the console refused: %v", err)
	}
	unattested := portal
	unattested.Attested = false
	for _, refused := range []helperschema.Peer{
		unattested,
		{UID: 0, Account: "root", Unit: "sshd.service", Attested: true},
		{UID: 997, Account: "olivares-net-guard", Unit: "olivares-net-guard.service", Attested: true},
	} {
		if helperschema.Admit(refused, rules, storage.SubcommandInventory) == nil {
			t.Fatalf("%+v admitted", refused)
		}
	}

	request := &storage.InventoryRequest{}
	if err := helperschema.Decode(strings.NewReader(`{"op":"inventory"}`), request); err != nil || request.Operation() != "" {
		t.Fatalf("the inventory document: %v, operation %q", err, request.Operation())
	}
	for _, document := range []string{`{"op":"format"}`, `{"op":"inventory","device":"/dev/sda"}`, `{"op":"inventory","path":"/"}`,
		`{"op":"inventory","operation_id":"00112233445566778899aabbccddeeff"}`, `{"Op":"inventory"}`, `{}`} {
		if helperschema.Decode(strings.NewReader(document), &storage.InventoryRequest{}) == nil {
			t.Errorf("accepted %s", document)
		}
	}
}

func TestHelper_AnswerCarriesTheBoundedInventoryDocument(t *testing.T) {
	inv := read(t, &fakeBus{objects: fixture(t, "virtio-blank-serial.json")},
		&fakeKernel{boot: bootA, firstMiB: map[string]string{"/dev/vda": firstMiBOfVda}})
	answer := storage.Answer(inv, nil)
	if answer.Result != helperschema.ResultAnswered || len(answer.Bundle) == 0 || len(answer.Bundle) > storage.MaxDocumentBytes {
		t.Fatalf("answer %+v", answer)
	}
	var sent helperschema.Request
	reader := storage.HelperReader(func(_ context.Context, name string, request helperschema.Request) (helperschema.Response, error) {
		if name != storage.HelperName {
			t.Fatalf("called helper %q", name)
		}
		sent = request
		return answer, nil
	})
	got, err := reader(context.Background())
	if err != nil || !reflect.DeepEqual(got, inv) {
		t.Fatalf("round trip: %v\n %+v\nwant\n %+v", err, got, inv)
	}
	if sent == nil || sent.Subcommand() != storage.SubcommandInventory || sent.Validate() != nil {
		t.Fatalf("sent %+v", sent)
	}

	if failed := storage.Answer(storage.Inventory{}, errors.New("no bus")); failed.Result != helperschema.ResultFailed || len(failed.Bundle) != 0 {
		t.Fatalf("a failed read answered %+v", failed)
	}
	huge := inv
	for len(huge.Disks) < 2000 {
		huge.Disks = append(huge.Disks, inv.Disks...)
	}
	if over := storage.Answer(huge, nil); over.Result != helperschema.ResultFailed || len(over.Bundle) != 0 {
		t.Fatalf("an inventory over its bound was answered: %s %d bytes", over.Result, len(over.Bundle))
	}

	for name, response := range map[string]helperschema.Response{
		"refused":       {Result: helperschema.ResultRefused, Code: helperschema.CodeNotAdmitted},
		"no document":   {Result: helperschema.ResultAnswered},
		"unknown field": {Result: helperschema.ResultAnswered, Bundle: json.RawMessage(`{"schema":"storage-inventory/v1","boot_id":"x","disks":[],"lvm":{"state":"read"},"extra":1}`)},
		"wrong schema":  {Result: helperschema.ResultAnswered, Bundle: json.RawMessage(`{"schema":"storage-inventory/v0","boot_id":"x","disks":[],"lvm":{"state":"read"}}`)},
	} {
		reader := storage.HelperReader(func(context.Context, string, helperschema.Request) (helperschema.Response, error) {
			return response, nil
		})
		if _, err := reader(context.Background()); err == nil {
			t.Errorf("%s: read as an inventory", name)
		}
	}
	lost := storage.HelperReader(func(context.Context, string, helperschema.Request) (helperschema.Response, error) {
		return helperschema.Response{}, errors.New("503 consumer_unavailable")
	})
	if _, err := lost(context.Background()); err == nil {
		t.Error("an absent helper read as an inventory")
	}
}

func TestHelper_Tty1RepairConsoleIsRefusedTheStorageRead(t *testing.T) {
	console := helperschema.Peer{UID: 0, Account: "root", Unit: "olivares-repair-console.service", TTY: "/dev/tty1", Attested: true}
	err := helperschema.Admit(console, storage.HelperRules(), storage.SubcommandInventory)
	var refusal *helperschema.Refusal
	if !errors.As(err, &refusal) || refusal.Code != helperschema.CodeNotAdmitted {
		t.Fatalf("the tty1 repair console asked for the storage read: %v, want not_admitted", err)
	}
}
