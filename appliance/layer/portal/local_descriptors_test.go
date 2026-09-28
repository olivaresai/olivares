// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package portal

import (
	"testing"

	"github.com/olivaresai/olivares/appliance/layer/hostops"
)

func TestLocalDescriptors_RegisterTheHostStatusAndTheStorageInventory(t *testing.T) {
	catalog, err := hostops.NewCatalog(localDescriptors())
	if err != nil {
		t.Fatalf("the local catalog is refused: %v", err)
	}
	for _, task := range [][2]string{{"host", "status"}, {"storage", "list"}} {
		for _, surface := range []string{"cli", "tui"} {
			if _, err := catalog.Describe(task[0], task[1], surface); err != nil {
				t.Errorf("%s %s is not described to the %s: %v", task[0], task[1], surface, err)
			}
		}
	}
}
