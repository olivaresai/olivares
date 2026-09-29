// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package firewall

import "github.com/olivaresai/olivares/appliance/layer/firewall/policy"

// Selection is the console's selection as first boot published it: portal.enabled,
// portal.listen and host.management_interfaces. An omitted portal.enabled is false and an
// omitted portal.listen is local.
type Selection struct {
	Enabled              bool
	Listen               string
	ManagementInterfaces []string
}

// Initial is the first confirmed policy: SSH on the port sshd reports, the product's ports, the
// console's row as the selection states it, link-local DHCPv6 replies on the client interfaces
// and no application row.
func Initial(sel Selection, sshPort int, dhcpv6 []string) policy.Document {
	return policy.Document{
		SchemaVersion:          policy.SchemaVersion,
		SSHPort:                sshPort,
		Portal:                 portal(sel),
		DHCPv6ClientInterfaces: append([]string{}, dhcpv6...),
		Apps:                   []policy.AppRow{},
	}
}

// Candidate is the policy a general change applies: the confirmed policy with the console's row
// following the selection now published. The applications' rows and every other row are kept.
// Every list is a list, never nil, because the helper refuses a null.
func Candidate(confirmed policy.Document, sel Selection) policy.Document {
	confirmed.Portal = portal(sel)
	confirmed.Apps = append([]policy.AppRow{}, confirmed.Apps...)
	return confirmed
}

func portal(sel Selection) policy.Portal {
	listen := sel.Listen
	if listen == "" {
		listen = policy.ListenLocal
	}
	return policy.Portal{Enabled: sel.Enabled, Listen: listen, ManagementInterfaces: append([]string{}, sel.ManagementInterfaces...)}
}
