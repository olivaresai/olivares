// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package helperclient

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/firewall"
	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/services"
	"github.com/olivaresai/olivares/appliance/layer/storage"
)

func TestHelperClient_ReachesTheModuleHelpersAndRefusesAnUnknownName(t *testing.T) {
	me := uint32(os.Getuid())
	ctx := context.Background()
	dir := shortDir(t)
	units := listen(t, dir, helperschema.HelperUnits, `{"result": "answered", "bundle": {"units": [], "truncated": false}}`+"\n")
	inventory := listen(t, dir, helperschema.HelperStorage, `{"result": "answered", "bundle": {"schema": "x"}}`+"\n")
	status := listen(t, dir, helperschema.HelperFirewall, `{"result": "answered", "bundle": {"confirmed_digest": "", "confirmed": null, "windows": []}}`+"\n")
	client := Client{Dir: dir, Owner: me}

	for name, c := range map[string]struct {
		helper  string
		request helperschema.Request
		sent    map[string]any
		got     func() []string
	}{
		"the units helper's list": {helperschema.HelperUnits, &services.Request{Op: services.OpList},
			map[string]any{"op": "list"}, units.documents},
		"the storage helper's inventory": {helperschema.HelperStorage, &storage.InventoryRequest{Op: storage.SubcommandInventory},
			map[string]any{"op": "inventory"}, inventory.documents},
		"the firewall helper's status": {helperschema.HelperFirewall, &firewall.Request{Op: firewall.OpStatus},
			map[string]any{"op": "status"}, status.documents},
	} {
		t.Run(name, func(t *testing.T) {
			response, err := client.Call(ctx, c.helper, c.request)
			if err != nil || response.Result != helperschema.ResultAnswered || len(response.Bundle) == 0 {
				t.Fatalf("%s: %+v %v", c.helper, response, err)
			}
			documents := c.got()
			if len(documents) != 1 {
				t.Fatalf("the helper received %d documents", len(documents))
			}
			var sent map[string]any
			if err := json.Unmarshal([]byte(documents[0]), &sent); err != nil || len(sent) != len(c.sent) || sent["op"] != c.sent["op"] {
				t.Fatalf("the helper received %q: the document and nothing about the caller", documents[0])
			}
		})
	}

	// A name that is no helper of the seam is refused before any socket is looked at, even when a
	// socket of that name answers in the directory.
	stray := listen(t, dir, "nftables", `{"result": "answered"}`)
	for _, name := range []string{"nftables", "../units", "units.sock", "Units", "storage/..", "Firewall", "firewall.sock", ""} {
		response, err := client.Call(ctx, name, &services.Request{Op: services.OpList})
		if StatusOf(err) != http.StatusUnprocessableEntity || response.Result != "" {
			t.Errorf("%q: %+v %v (status %d), want 422 before any socket", name, response, err, StatusOf(err))
		}
	}
	if len(units.documents()) != 1 || len(inventory.documents()) != 1 || len(status.documents()) != 1 || len(stray.documents()) != 0 {
		t.Fatalf("a refused name reached a socket: units %d, storage %d, firewall %d, nftables %d",
			len(units.documents()), len(inventory.documents()), len(status.documents()), len(stray.documents()))
	}

	// An invalid document for a module helper asks nothing either.
	if _, err := client.Call(ctx, helperschema.HelperUnits, &services.Request{Op: "mask", Unit: "olivares.service"}); StatusOf(err) != http.StatusUnprocessableEntity {
		t.Errorf("an invalid units document: %v, want 422", err)
	}
	if len(units.documents()) != 1 {
		t.Fatalf("an invalid document reached the units helper")
	}
}
