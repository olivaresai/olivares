// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package carriers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/olivaresai/olivares/appliance/answers/carriers"
)

func TestCarriers_TheAdvisorySourceLabelDoesNotMakeAConflict(t *testing.T) {
	doc := readFixture(t, "answers.json")
	credential := allCarriers(t, bytes.Replace(doc, []byte(`"source": "nocloud"`), []byte(`"source": "systemd-credential"`), 1))[2]
	file := allCarriers(t, bytes.Replace(doc, []byte(`"source": "nocloud"`), []byte(`"source": "file"`), 1))[3]
	got, err := carriers.Resolve(context.Background(), "", credential, file)
	if err != nil {
		t.Fatalf("documents that differ only in their advisory source label were refused: %v", err)
	}
	if got.Carrier != carriers.Credential {
		t.Fatalf("the record must name the carrier that delivered the answers, got %s", got)
	}
	renamed := allCarriers(t, bytes.Replace(doc, []byte(`"hostname": "olivares.example.test"`), []byte(`"hostname": "renamed.example.test"`), 1))[3]
	if _, err := carriers.Resolve(context.Background(), "", credential, renamed); err == nil {
		t.Fatal("documents with different answers were combined")
	}
}

func TestCarriers_TheNoCloudSeedSetsTheFullHostname(t *testing.T) {
	var answers struct {
		Host struct {
			Hostname string `json:"hostname"`
		} `json:"host"`
	}
	if err := json.Unmarshal(readFixture(t, "answers.json"), &answers); err != nil {
		t.Fatal(err)
	}
	body, ok := bytes.CutPrefix(readFixture(t, "nocloud/user-data"), []byte("#cloud-config\n"))
	if !ok {
		t.Fatal("user-data is not cloud-config")
	}
	var seed struct {
		FQDN                   string `json:"fqdn"`
		PreferFQDNOverHostname bool   `json:"prefer_fqdn_over_hostname"`
	}
	if err := json.Unmarshal(body, &seed); err != nil {
		t.Fatal(err)
	}
	// host.hostname is the complete kernel hostname, so cloud-init must set the FQDN as it.
	if seed.FQDN != answers.Host.Hostname || !seed.PreferFQDNOverHostname {
		t.Fatalf("the seed sets fqdn %q (prefer %v), the answers declare %q", seed.FQDN, seed.PreferFQDNOverHostname, answers.Host.Hostname)
	}
}
