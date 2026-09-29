// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package portal

import (
	"github.com/olivaresai/olivares/appliance/layer/hostops"
	"github.com/olivaresai/olivares/appliance/layer/services"
)

// hostDescriptors is the host task catalog the local API serves to the CLI and the TUI: the
// host status task and the Services tasks.
func hostDescriptors() []hostops.Descriptor {
	return append([]hostops.Descriptor{hostops.StatusDescriptor()}, services.Descriptors()...)
}
