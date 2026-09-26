// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package carriers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/olivaresai/olivares/appliance/answers"
)

// Carrier names. Each is also a provenance label the answers schema accepts.
const (
	NoCloud    = "nocloud"
	GuestInfo  = "guestinfo"
	Credential = "systemd-credential"
	File       = "file"
)

// Locations of the carriers on an installed appliance.
const (
	// NoCloudPath is where the seed's cloud-config write_files entry puts the document, so
	// cloud-init, the host-settings owner, is also the one that emits the answers.
	NoCloudPath = "/etc/olivares-appliance/carriers/nocloud.json"
	// FilePath is written by an operator or by the local assistant.
	FilePath = "/etc/olivares-appliance/answers.json"
	// SelectionPath optionally names the one carrier to read when several are present.
	SelectionPath = "/etc/olivares-appliance/carrier"
	// CredentialName is the credential the first-boot unit imports with ImportCredential=.
	CredentialName = "olivares.appliance.answers"
	// OVFProperty is the OVF environment property holding the base64 document.
	OVFProperty = "olivares-appliance-answers"
	// GuestInfoKey is the guestinfo variable that holds the OVF environment.
	GuestInfoKey = "guestinfo.ovfEnv"
)

// Delivery is one document as a carrier received it.
type Delivery struct {
	Carrier  string
	Ref      string // where it was read: a path, a credential name or a guestinfo key
	Document []byte // handed to the answers module only; never logged or recorded
}

// String names the delivery for status records and logs: the carrier and its reference.
func (d Delivery) String() string { return d.Carrier + ":" + d.Ref }

// Carrier reads at most one document. A carrier that holds nothing reports false.
type Carrier interface {
	Name() string
	Read(ctx context.Context) (Delivery, bool, error)
}

// ErrNoCarrier reports that no carrier holds a document yet.
var ErrNoCarrier = errors.New("no answers carrier is present")

// ConflictError names the carriers whose documents differ. It never includes their content.
type ConflictError struct{ Carriers []string }

func (e *ConflictError) Error() string {
	return "answers carriers disagree: " + strings.Join(e.Carriers, ", ") +
		"; name the one to use in " + SelectionPath
}

// InvalidError reports a document the answers module refused, by carrier and schema path.
type InvalidError struct {
	Delivery string
	Err      error
}

func (e *InvalidError) Error() string { return e.Delivery + ": " + e.Err.Error() }
func (e *InvalidError) Unwrap() error { return e.Err }

// Comparable returns a document's canonical plan without its source label. The label is
// advisory: it states where the author meant the document to travel, carriers deliver it
// unchanged, and the first-boot record names the carrier that actually delivered it.
// Carriers are compared, and the restart digest is computed, on this form. Refusals are the
// answers module's, unchanged.
func Comparable(document []byte) ([]byte, error) {
	plan, err := answers.Build(bytes.NewReader(document))
	if err != nil {
		return nil, err
	}
	canonical, err := plan.JSON()
	if err != nil {
		return nil, err
	}
	var fields map[string]any
	if err := json.Unmarshal(canonical, &fields); err != nil {
		return nil, err
	}
	if doc, ok := fields["answers"].(map[string]any); ok {
		delete(doc, "source")
	}
	return json.Marshal(fields)
}

// Resolve reads every carrier, or only the selected one, and returns the document they
// deliver. Present carriers are compared in their Comparable form, so formatting and the
// advisory source label do not matter and any difference in an answer does: it refuses
// rather than combining them.
func Resolve(ctx context.Context, selected string, all ...Carrier) (Delivery, error) {
	var found []Delivery
	var plans [][]byte
	for _, c := range all {
		if selected != "" && c.Name() != selected {
			continue
		}
		d, ok, err := c.Read(ctx)
		if err != nil {
			return Delivery{}, err
		}
		if !ok {
			continue
		}
		form, err := Comparable(d.Document)
		if err != nil {
			return Delivery{}, &InvalidError{Delivery: d.String(), Err: err}
		}
		found = append(found, d)
		plans = append(plans, form)
	}
	if len(found) == 0 {
		if selected != "" {
			return Delivery{}, fmt.Errorf("%w: the selected carrier %s holds no document", ErrNoCarrier, selected)
		}
		return Delivery{}, ErrNoCarrier
	}
	for i := 1; i < len(found); i++ {
		if !bytes.Equal(plans[i], plans[0]) {
			names := make([]string, 0, len(found))
			for _, d := range found {
				names = append(names, d.Carrier)
			}
			sort.Strings(names)
			return Delivery{}, &ConflictError{Carriers: names}
		}
	}
	return found[0], nil
}

// Selection reads the optional explicit carrier choice. An absent file selects none.
func Selection(path string) (string, error) {
	data, ok, err := ReadProtected(path, 64)
	if err != nil || !ok {
		return "", err
	}
	switch name := strings.TrimSpace(string(data)); name {
	case NoCloud, GuestInfo, Credential, File:
		return name, nil
	}
	return "", &InputError{Ref: path, Reason: "names no known carrier"}
}

// Installed returns the carriers of an installed appliance. credentialsDir is the unit's
// $CREDENTIALS_DIRECTORY, empty when it received no credential.
func Installed(credentialsDir string, run Runner) []Carrier {
	return []Carrier{
		CredentialCarrier{Dir: credentialsDir},
		GuestInfoCarrier{Run: run},
		FileCarrier{Label: NoCloud, Path: NoCloudPath},
		FileCarrier{Label: File, Path: FilePath},
	}
}
