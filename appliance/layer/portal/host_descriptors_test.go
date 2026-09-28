// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package portal

import (
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/hostops"
	"github.com/olivaresai/olivares/appliance/layer/services"
)

func TestHostCatalog_RegistersTheServicesTasksForEverySurface(t *testing.T) {
	catalog, err := hostops.NewCatalog(hostDescriptors())
	if err != nil {
		t.Fatalf("the host catalog is refused: %v", err)
	}
	if got, want := len(catalog.Descriptors()), 1+len(services.Ops()); got != want {
		t.Fatalf("the host catalog has %d tasks, want %d", got, want)
	}
	for _, surface := range []string{"web", "cli", "tui"} {
		if _, err := catalog.Describe("host", "status", surface); err != nil {
			t.Errorf("%s: host status: %v", surface, err)
		}
		for _, op := range services.Ops() {
			d, err := catalog.Describe(services.Module, op, surface)
			if err != nil || d.Category != "Services" {
				t.Errorf("%s: service %s: %+v %v", surface, op, d, err)
			}
		}
	}
}
