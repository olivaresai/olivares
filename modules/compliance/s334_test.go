// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"strings"
	"testing"
)

func TestS334PCIDSSVoiceDepthCatalog(t *testing.T) {
	fw, ok := frameworkByID["pci_dss_401_ai"]
	if !ok {
		t.Fatal("pci_dss_401_ai framework must exist")
	}
	if len(catalog) != 26 {
		t.Fatalf("catalog size = %d, want 26", len(catalog))
	}

	controls := s334ControlsByID(fw)
	for _, id := range []string{
		"req_3_3_1_sad_voice",
		"req_4_2_1_voice_transmission",
		"req_6_4_3_payment_scripts",
		"req_11_6_1_payment_tamper",
		"req_12_3_1_targeted_risk",
		"req_6_ai_dev",
		"req_7_ai_access",
		"req_10_ai_logging",
		"req_11_ai_testing",
		"req_12_ai_risk",
		"req_3_ai_data",
	} {
		if _, ok := controls[id]; !ok {
			t.Fatalf("pci_dss_401_ai missing control %s", id)
		}
	}
}

func TestS334PCIDSSControlsHonestMappingShape(t *testing.T) {
	fw := frameworkByID["pci_dss_401_ai"]
	known := make(map[CapabilityKey]bool, len(capabilityCatalog))
	for _, cap := range capabilityCatalog {
		known[cap.Key] = true
	}

	for _, ctrl := range fw.Controls {
		if ctrl.ID == "" {
			t.Fatal("control has empty ID")
		}
		if ctrl.Title == "" {
			t.Fatalf("control %s has empty Title", ctrl.ID)
		}
		if ctrl.Requirement == "" {
			t.Fatalf("control %s has empty Requirement", ctrl.ID)
		}
		if ctrl.Criterion == "" {
			t.Fatalf("control %s has empty Criterion", ctrl.ID)
		}
		for _, key := range ctrl.Capabilities {
			if !known[key] {
				t.Fatalf("control %s references unknown capability %q", ctrl.ID, key)
			}
		}
		if len(ctrl.Capabilities) == 0 {
			if ctrl.Note == "" {
				t.Fatalf("control %s is an explicit gap but has no Note", ctrl.ID)
			}
			lowerCriterion := strings.ToLower(ctrl.Criterion)
			if strings.Contains(lowerCriterion, "capability") ||
				strings.Contains(lowerCriterion, "evidence") {
				t.Fatalf("control %s gap Criterion reads like claimed evidence: %q",
					ctrl.ID, ctrl.Criterion)
			}
		}
	}
}

func TestS334PCIDSSDisclaimerAndPin(t *testing.T) {
	fw := frameworkByID["pci_dss_401_ai"]
	if fw.Pin.VerifiedOn != "2026-07-05" {
		t.Fatalf("pci_dss_401_ai pin verified_on = %q, want 2026-07-05",
			fw.Pin.VerifiedOn)
	}
	if !strings.Contains(fw.Disclaimer, "Protecting Telephone-Based Payment Card Data") {
		t.Fatalf("disclaimer must mention the telephone-payment supplement: %q",
			fw.Disclaimer)
	}
	if !strings.Contains(fw.Disclaimer, "no AI-agent-specific guidance") {
		t.Fatalf("disclaimer must mention absence of AI-agent-specific PCI SSC guidance: %q",
			fw.Disclaimer)
	}
}

func TestS334VoiceCapabilitiesExist(t *testing.T) {
	known := make(map[CapabilityKey]bool, len(capabilityCatalog))
	for _, cap := range capabilityCatalog {
		known[cap.Key] = true
	}
	for _, key := range []CapabilityKey{
		"voice_call_governance",
		"voice_transcript_dlp",
	} {
		if !known[key] {
			t.Fatalf("capability %q missing from catalog", key)
		}
	}
}

func TestS334VoiceTransmissionIsExplicitGap(t *testing.T) {
	ctrl := s334ControlsByID(frameworkByID["pci_dss_401_ai"])["req_4_2_1_voice_transmission"]
	if ctrl.Capabilities != nil {
		t.Fatalf("req_4_2_1_voice_transmission capabilities = %#v, want nil",
			ctrl.Capabilities)
	}
	if ctrl.Note == "" {
		t.Fatal("req_4_2_1_voice_transmission must carry a Note")
	}
}

func TestS334PCIDSSMilestones(t *testing.T) {
	controls := s334ControlsByID(frameworkByID["pci_dss_401_ai"])
	for _, id := range []string{
		"req_3_3_1_sad_voice",
		"req_4_2_1_voice_transmission",
	} {
		if got := controls[id].MilestoneIDs; len(got) != 0 {
			t.Fatalf("%s milestones = %#v, want none", id, got)
		}
	}
	for _, id := range []string{
		"req_6_4_3_payment_scripts",
		"req_11_6_1_payment_tamper",
		"req_12_3_1_targeted_risk",
	} {
		got := controls[id].MilestoneIDs
		if len(got) != 1 || got[0] != "pci_dss_401.future_dated" {
			t.Fatalf("%s milestones = %#v, want pci_dss_401.future_dated",
				id, got)
		}
	}
}

func s334ControlsByID(fw Framework) map[string]Control {
	out := make(map[string]Control, len(fw.Controls))
	for _, ctrl := range fw.Controls {
		out[ctrl.ID] = ctrl
	}
	return out
}
