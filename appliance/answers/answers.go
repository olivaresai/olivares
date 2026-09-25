// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package answers validates appliance answers and describes future installation work.
// It has no host, credential, network or product effect adapters.
package answers

import (
	"encoding/json"
	"errors"
	"io"
)

type document struct {
	SchemaVersion string  `json:"schema_version"`
	Source        string  `json:"source"`
	Host          host    `json:"host"`
	Product       product `json:"product"`
}

type host struct {
	Owner             string        `json:"owner"`
	Hostname          string        `json:"hostname"`
	Network           network       `json:"network"`
	Time              clockSettings `json:"time"`
	SSHAuthorizedKeys []string      `json:"ssh_authorized_keys"`
}

type network struct {
	Mode string `json:"mode"`
}
type clockSettings struct {
	Timezone string   `json:"timezone"`
	Servers  []string `json:"servers"`
}
type product struct {
	StorageProfile   string `json:"storage_profile"`
	PublicConsoleURL string `json:"public_console_url"`
	UpdateChannel    string `json:"update_channel"`
	NodeRole         string `json:"node_role"`
}

type operation struct {
	Owner  string `json:"owner"`
	Action string `json:"action"`
	State  string `json:"state"`
}

type planDocument struct {
	SchemaVersion        string      `json:"schema_version"`
	State                string      `json:"state"`
	Executable           bool        `json:"executable"`
	Answers              document    `json:"answers"`
	Operations           []operation `json:"operations"`
	PendingPrerequisites []string    `json:"pending_prerequisites"`
}

// Plan is validated, non-executable installation intent. Its zero value is invalid.
// Only Build can construct one; no apply interface is provided.
type Plan struct{ value *planDocument }

// JSON returns a stable, newline-terminated representation containing public settings.
func (p Plan) JSON() ([]byte, error) {
	if p.value == nil {
		return nil, errors.New("no validated plan")
	}
	b, err := json.MarshalIndent(p.value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Build reads one answers document and returns a plan without observing or changing a host.
func Build(r io.Reader) (Plan, error) {
	d, err := readDocument(r)
	if err != nil {
		return Plan{}, err
	}
	if err := validate(&d); err != nil {
		return Plan{}, err
	}
	return Plan{value: &planDocument{
		SchemaVersion: "appliance-plan/v1", State: "planned", Answers: d,
		Operations: []operation{
			{Owner: "cloud-init", Action: "wait-for-completion", State: "pending"},
			{Owner: "cloud-init", Action: "verify-host-settings", State: "pending"},
			{Owner: "olivares", Action: "generate-product-config", State: "pending"},
			{Owner: "olivares", Action: "initialize-storage", State: "pending"},
		},
		PendingPrerequisites: []string{
			"host-and-product-adapters-unimplemented", "instance-identity-unverified",
			"firewall-policy-unverified", "protected-setup-delivery-and-expiry-unimplemented",
			"service-start-and-health-unverified",
		},
	}}, nil
}
