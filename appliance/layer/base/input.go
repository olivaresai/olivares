// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package base

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/olivaresai/olivares/appliance/answers"
	"github.com/olivaresai/olivares/appliance/answers/carriers"
)

// Answers holds the validated public settings first boot verifies and applies. The answers
// module normalizes them; this package reads its canonical plan and never re-validates.
type Answers struct {
	Hostname          string
	Timezone          string
	SSHAuthorizedKeys []string
	StorageProfile    string
	PublicConsoleURL  string
}

// Input is one validated answers document with the digest first boot compares on restart.
type Input struct {
	Source  string // the delivery's carrier and reference, never its content
	Digest  string
	Answers Answers
}

const digestDomain = "olivares-appliance-firstboot/input/v1\n"

// NewInput validates document with the answers module. The digest covers the canonical plan
// without its advisory source label (schema version and normalized public answers; the schema
// accepts no secret) and, when one carrier was selected explicitly, its name.
func NewInput(source, selected string, document []byte) (Input, error) {
	plan, err := answers.Build(bytes.NewReader(document))
	if err != nil {
		var invalid *answers.ValidationError
		if errors.As(err, &invalid) {
			return Input{}, Refuse("the answers were refused at " + invalid.Error())
		}
		return Input{}, Refuse("the answers could not be read")
	}
	canonical, err := plan.JSON()
	if err != nil {
		return Input{}, Refuse("the answers could not be encoded")
	}
	var view struct {
		Answers struct {
			Host struct {
				Hostname string `json:"hostname"`
				Time     struct {
					Timezone string `json:"timezone"`
				} `json:"time"`
				SSHAuthorizedKeys []string `json:"ssh_authorized_keys"`
			} `json:"host"`
			Product struct {
				StorageProfile   string `json:"storage_profile"`
				PublicConsoleURL string `json:"public_console_url"`
			} `json:"product"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(canonical, &view); err != nil {
		return Input{}, Refuse("the answers plan could not be decoded")
	}
	form, err := carriers.Comparable(document)
	if err != nil {
		return Input{}, Refuse("the answers could not be encoded")
	}
	sum := sha256.Sum256([]byte(digestDomain + selected + "\n" + string(form)))
	host, product := view.Answers.Host, view.Answers.Product
	return Input{
		Source: source,
		Digest: hex.EncodeToString(sum[:]),
		Answers: Answers{
			Hostname:          host.Hostname,
			Timezone:          host.Time.Timezone,
			SSHAuthorizedKeys: host.SSHAuthorizedKeys,
			StorageProfile:    product.StorageProfile,
			PublicConsoleURL:  product.PublicConsoleURL,
		},
	}, nil
}
