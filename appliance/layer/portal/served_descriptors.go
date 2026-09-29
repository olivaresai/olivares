// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package portal

import (
	"github.com/olivaresai/olivares/appliance/layer/firewall"
	"github.com/olivaresai/olivares/appliance/layer/hostops"
	"github.com/olivaresai/olivares/appliance/layer/services"
	"github.com/olivaresai/olivares/appliance/layer/storage"
)

// servedDescriptors is the task catalog the portal's local socket serves: the host status and
// every composed module's tasks. Each module keeps its own list and test (hostDescriptors for
// the services module, localDescriptors for the storage module, firewall.Descriptors for the
// firewall module); this is their union.
func servedDescriptors() []hostops.Descriptor {
	ds := []hostops.Descriptor{hostops.StatusDescriptor()}
	ds = append(ds, services.Descriptors()...)
	ds = append(ds, storage.Descriptors()...)
	return append(ds, firewall.Descriptors()...)
}
