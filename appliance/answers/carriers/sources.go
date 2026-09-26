// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package carriers

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"path/filepath"
	"strings"
)

// FileCarrier reads the document from a protected file: the local file, or the file
// cloud-init writes from a NoCloud seed.
type FileCarrier struct {
	Label string // NoCloud or File
	Path  string
}

// Name returns the carrier's label.
func (c FileCarrier) Name() string { return c.Label }

// Read returns the file's bytes unchanged.
func (c FileCarrier) Read(context.Context) (Delivery, bool, error) {
	data, ok, err := ReadProtected(c.Path, MaxDocumentBytes)
	if err != nil || !ok {
		return Delivery{}, ok, err
	}
	return Delivery{Carrier: c.Label, Ref: c.Path, Document: data}, true, nil
}

// CredentialCarrier reads the document systemd hands the unit as a credential.
type CredentialCarrier struct {
	// Dir is the unit's $CREDENTIALS_DIRECTORY; empty when it received no credential.
	Dir string
}

// Name returns the systemd credential carrier's label.
func (CredentialCarrier) Name() string { return Credential }

// Read returns the credential's bytes unchanged.
func (c CredentialCarrier) Read(context.Context) (Delivery, bool, error) {
	if c.Dir == "" {
		return Delivery{}, false, nil
	}
	data, ok, err := ReadProtected(filepath.Join(c.Dir, CredentialName), MaxDocumentBytes)
	if err != nil || !ok {
		return Delivery{}, ok, err
	}
	return Delivery{Carrier: Credential, Ref: CredentialName, Document: data}, true, nil
}

// GuestInfoCarrier reads the OVF environment VMware exposes as guestinfo.ovfEnv and
// decodes the base64 document in its olivares-appliance-answers property.
type GuestInfoCarrier struct{ Run Runner }

// Name returns the guestinfo carrier's label.
func (GuestInfoCarrier) Name() string { return GuestInfo }

// maxEnvironmentBytes bounds the OVF environment; it also carries other properties.
const maxEnvironmentBytes = 1 << 20

// Read returns the decoded property. Without VMware tools, or outside VMware, the
// carrier is absent.
func (c GuestInfoCarrier) Read(ctx context.Context) (Delivery, bool, error) {
	env, ok := c.environment(ctx)
	if !ok {
		return Delivery{}, false, nil
	}
	ref := GuestInfoKey + "#" + OVFProperty
	if len(env) > maxEnvironmentBytes {
		return Delivery{}, false, &InputError{Ref: ref, Reason: "the OVF environment is larger than 1 MiB"}
	}
	var parsed struct {
		Properties []struct {
			Key   string `xml:"key,attr"`
			Value string `xml:"value,attr"`
		} `xml:"PropertySection>Property"`
	}
	if err := xml.Unmarshal(env, &parsed); err != nil {
		return Delivery{}, false, &InputError{Ref: ref, Reason: "the OVF environment is not well-formed XML"}
	}
	var encoded []string
	for _, p := range parsed.Properties {
		if p.Key == OVFProperty {
			encoded = append(encoded, p.Value)
		}
	}
	switch {
	case len(encoded) == 0:
		return Delivery{}, false, nil
	case len(encoded) > 1:
		return Delivery{}, false, &InputError{Ref: ref, Reason: "the property appears more than once"}
	}
	data, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(encoded[0]), ""))
	if err != nil {
		return Delivery{}, false, &InputError{Ref: ref, Reason: "the property is not standard base64"}
	}
	if len(data) > MaxDocumentBytes {
		return Delivery{}, false, &InputError{Ref: ref, Reason: "the decoded document is larger than 64 KiB"}
	}
	return Delivery{Carrier: GuestInfo, Ref: ref, Document: data}, true, nil
}

// environment asks the VMware tools for the OVF environment the way cloud-init does:
// vmware-rpctool first, then vmtoolsd --cmd. The arguments name the key only; the value
// arrives on standard output and never enters an argument vector.
func (c GuestInfoCarrier) environment(ctx context.Context) ([]byte, bool) {
	query := "info-get " + GuestInfoKey
	if out, err := c.Run(ctx, "vmware-rpctool", query); err == nil && len(out) > 0 {
		return out, true
	}
	if out, err := c.Run(ctx, "vmtoolsd", "--cmd", query); err == nil && len(out) > 0 {
		return out, true
	}
	return nil, false
}
