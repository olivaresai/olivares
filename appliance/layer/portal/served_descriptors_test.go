// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package portal

import (
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/firewall"
	"github.com/olivaresai/olivares/appliance/layer/hostops"
	"github.com/olivaresai/olivares/appliance/layer/services"
	"github.com/olivaresai/olivares/appliance/layer/storage"
)

// The local socket serves one catalog for every composed module: the host status, each
// services task, the storage inventory and each firewall task, described to the CLI and the TUI
// alike.
func TestServedCatalog_DescribesEveryComposedModulesTasks(t *testing.T) {
	catalog, err := hostops.NewCatalog(servedDescriptors())
	if err != nil {
		t.Fatalf("the served catalog is refused: %v", err)
	}
	if got, want := len(catalog.Descriptors()), 1+len(services.Ops())+len(storage.Descriptors())+len(firewall.Descriptors()); got != want {
		t.Fatalf("the served catalog has %d tasks, want %d", got, want)
	}
	for _, surface := range []string{"cli", "tui"} {
		if _, err := catalog.Describe("host", "status", surface); err != nil {
			t.Errorf("%s: host status: %v", surface, err)
		}
		for _, op := range services.Ops() {
			if _, err := catalog.Describe(services.Module, op, surface); err != nil {
				t.Errorf("%s: service %s: %v", surface, op, err)
			}
		}
		if _, err := catalog.Describe("storage", "list", surface); err != nil {
			t.Errorf("%s: storage list: %v", surface, err)
		}
		for _, verb := range firewall.Verbs() {
			if _, err := catalog.Describe(firewall.Module, verb, surface); err != nil {
				t.Errorf("%s: firewall %s: %v", surface, verb, err)
			}
		}
	}
}
