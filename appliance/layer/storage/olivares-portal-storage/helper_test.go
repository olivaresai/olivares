// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/storage"
)

// peerKernel attests one connection's peer: account, unit, and the controlling terminal's
// device number (0 for none).
type peerKernel struct {
	uid     uint32
	account string
	unit    string
	ttyNr   int
}

func (k peerKernel) PeerCred(int) (int, uint32, error) { return 4242, k.uid, nil }
func (k peerKernel) PeerPidfd(int) (int, error)        { return 9, nil }
func (k peerKernel) PidOf(int) (int, error)            { return 4242, nil }
func (k peerKernel) Cgroup(int) (string, error) {
	return "0::/system.slice/" + k.unit + "\n", nil
}
func (k peerKernel) Stat(int) (string, error) {
	return "4242 (client) S 1 4242 4242 " + strconv.Itoa(k.ttyNr) + " -1 4194560 0 0 0 0", nil
}
func (k peerKernel) AccountOf(uint32) (string, error) { return k.account, nil }
func (k peerKernel) Alive(int) error                  { return nil }
func (k peerKernel) Close(int)                        {}

var portal = peerKernel{uid: 998, account: "olivares-portal", unit: "olivares-portal.service"}

func TestStorageHelper_AnswersTheInventoryToTheAttestedConsoleOnly(t *testing.T) {
	inventory := storage.Inventory{Schema: storage.Schema, BootID: "3f2b9c1e-8a7d-4e6f-b5c4-d3e2f1a0b9c8", Disks: []storage.Disk{},
		LVM: storage.LVM{State: storage.LVMRead}}
	reads := 0
	h := helper(func(context.Context) (storage.Inventory, error) { reads++; return inventory, nil })
	serve := func(k peerKernel, args []string, document string) (int, helperschema.Response) {
		var out, log bytes.Buffer
		code := invocationServe(h, k, args, document, &out, &log)
		var response helperschema.Response
		if out.Len() > 0 {
			if err := json.Unmarshal(out.Bytes(), &response); err != nil {
				t.Fatalf("answer %q: %v", out.String(), err)
			}
		}
		return code, response
	}

	code, response := serve(portal, nil, `{"op":"inventory"}`)
	if code != 0 || response.Result != helperschema.ResultAnswered || reads != 1 {
		t.Fatalf("exit %d answer %+v reads %d", code, response, reads)
	}
	var got storage.Inventory
	if err := json.Unmarshal(response.Bundle, &got); err != nil || !reflect.DeepEqual(got, inventory) {
		t.Fatalf("inventory %+v %v", got, err)
	}

	for name, tc := range map[string]struct {
		k        peerKernel
		args     []string
		document string
		exit     int
	}{
		"an SSH root session":        {peerKernel{uid: 0, account: "root", unit: "sshd.service"}, nil, `{"op":"inventory"}`, 1},
		"the tty1 repair console":    {peerKernel{uid: 0, account: "root", unit: "olivares-repair-console.service", ttyNr: 4<<8 | 1}, nil, `{"op":"inventory"}`, 1},
		"the network guard":          {peerKernel{uid: 997, account: "olivares-net-guard", unit: "olivares-net-guard.service"}, nil, `{"op":"inventory"}`, 1},
		"a device argument":          {portal, []string{"--device", "/dev/sda"}, `{"op":"inventory"}`, 2},
		"a device in the document":   {portal, nil, `{"op":"inventory","device":"/dev/sda"}`, 1},
		"a mutation in the document": {portal, nil, `{"op":"format"}`, 1},
	} {
		t.Run(name, func(t *testing.T) {
			before := reads
			code, _ := serve(tc.k, tc.args, tc.document)
			if code != tc.exit || reads != before {
				t.Fatalf("exit %d, want %d; reads %d", code, tc.exit, reads-before)
			}
		})
	}

	failing := helper(func(context.Context) (storage.Inventory, error) {
		return storage.Inventory{}, errors.New("no system bus")
	})
	var out, log bytes.Buffer
	if code := invocationServe(failing, portal, nil, `{"op":"inventory"}`, &out, &log); code != 2 || !strings.Contains(out.String(), `"failed"`) {
		t.Fatalf("a failed read exited %d with %q", code, out.String())
	}
}
