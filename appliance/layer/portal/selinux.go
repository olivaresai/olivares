// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package portal

import (
	"errors"
	"os"
	"strings"
)

// PortalKeyType is the SELinux type of the operator's key file on an appliance that enforces
// its policy: the service manager reads the key to deliver the credential, and the console's
// own domain may only measure it.
const PortalKeyType = "olivares_portal_key_t"

// selinuxEnforce is selinuxfs's enforce switch. SELinux is enabled wherever it exists, whether it
// reads 1 (enforcing) or 0 (permissive), and the key's label is judged in both. Tests replace it.
var selinuxEnforce = "/sys/fs/selinux/enforce"

// measureKeyLabel returns why the operator's key fails its SELinux label, or "" when it holds or
// the host has no SELinux. A key copied into the operator's directory takes the directory's type
// until it is relabeled, and the console's own domain may read that type; only a key of
// PortalKeyType is readable by the service manager alone. SELinux counts as absent only when
// the switch does not exist: a switch that cannot be measured is judged as enabled, so the check
// never switches itself off. The reason is fixed and names neither the path nor the label.
func measureKeyLabel(key string) string {
	if _, err := os.Stat(selinuxEnforce); errors.Is(err, os.ErrNotExist) {
		return ""
	}
	label, err := keyLabel(key)
	if err != nil {
		return "key's SELinux label cannot be read"
	}
	// user:role:type, then the level, which may itself hold colons.
	if fields := strings.SplitN(label, ":", 4); len(fields) < 3 || fields[2] != PortalKeyType {
		return "key is not labeled " + PortalKeyType
	}
	return ""
}
