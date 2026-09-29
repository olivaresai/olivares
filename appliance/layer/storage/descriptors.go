// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package storage

import "github.com/olivaresai/olivares/appliance/layer/hostops"

// Descriptors returns the storage module's task descriptors: the inventory read. It has no
// input, no confirmation and no act, and web, CLI and TUI render the same definition.
func Descriptors() []hostops.Descriptor {
	return []hostops.Descriptor{{
		ID:       "storage.list",
		Module:   "storage",
		Verb:     "list",
		Title:    "Storage inventory",
		Category: "Storage",
		InputSchema: hostops.InputSchema{Type: "object", Properties: map[string]hostops.InputField{},
			Required: []string{}},
		Preconditions: []string{"Local admission or an authorized web session", "The storage helper answers on its socket"},
		Consequence: "Read disks, partitions, filesystems, swap and LVM, the system disk, each consumer and the operations the host " +
			"reports; no host setting changes. An LVM metadata backup or a snapshot is not a data backup.",
		Confirmation:      "none",
		Modes:             []string{"product-up", "product-down-repair", "unavailable"},
		EquivalentCommand: "olivares-appliance storage list --plan",
		OutputSchema:      "hostop-v1.schema.json",
		Surfaces:          []string{"web", "cli", "tui"},
	}}
}
