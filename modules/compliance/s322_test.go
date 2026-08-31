// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package compliance

import (
	"strings"
	"testing"
)

func s322WatchlistItem(id string) (WatchlistItem, bool) {
	for _, item := range regulatoryWatchlist {
		if item.ID == id {
			return item, true
		}
	}
	return WatchlistItem{}, false
}

func TestOmnibusAdoptedPendingOJ(t *testing.T) {
	for _, id := range []string{
		"eu_ai_act.high_risk_annex3_omnibus",
		"eu_ai_act.high_risk_annex1_omnibus",
		"eu_ai_act.art50_marking_grace_ends",
		"eu_ai_act.new_art5_ncii_csam_compliance",
	} {
		ms, ok := milestoneByID[id]
		if !ok {
			t.Fatalf("milestone %s missing", id)
		}
		if ms.Status != MilestoneAdoptedPendingOJ {
			t.Errorf("%s status = %q, want %q", id, ms.Status, MilestoneAdoptedPendingOJ)
		}
	}

	adopted, ok := milestoneByID["eu_ai_act.omnibus_adopted"]
	if !ok {
		t.Fatal("eu_ai_act.omnibus_adopted missing")
	}
	if adopted.Status != MilestonePassed {
		t.Errorf("eu_ai_act.omnibus_adopted status = %q, want %q", adopted.Status, MilestonePassed)
	}
	if !strings.Contains(adopted.Effect, "EP") || !strings.Contains(adopted.Effect, "Council") {
		t.Errorf("eu_ai_act.omnibus_adopted effect must mention both EP and Council; got %q", adopted.Effect)
	}

	for _, ms := range regulatoryCalendar {
		if !strings.Contains(ms.Regime, "Digital Omnibus") {
			continue
		}
		if ms.Status == MilestoneInForce || ms.Status == MilestoneAppliesFrom {
			t.Errorf("%s must not be in_force/applies_from before an OJ source; got %q", ms.ID, ms.Status)
		}
		if ms.Status != MilestoneAdoptedPendingOJ && ms.Status != MilestonePassed {
			t.Errorf("%s status = %q, want adopted_pending_oj or passed", ms.ID, ms.Status)
		}
	}

	for _, id := range []string{"eu_ai_act.high_risk_annex3_original", "eu_ai_act.high_risk_annex1_original"} {
		ms, ok := milestoneByID[id]
		if !ok {
			t.Fatalf("milestone %s missing", id)
		}
		if ms.Status != MilestoneAppliesFrom {
			t.Errorf("%s must remain applies_from until OJ publication; got %q", id, ms.Status)
		}
	}

	item, ok := s322WatchlistItem("digital_omnibus_ai")
	if !ok {
		t.Fatal("digital_omnibus_ai watchlist item missing")
	}
	if item.Status != "adopted_pending_oj" {
		t.Errorf("digital_omnibus_ai status = %q, want adopted_pending_oj", item.Status)
	}
	if !strings.Contains(item.Note, "OJ publication") || !strings.Contains(strings.ToLower(item.Note), "flip") {
		t.Errorf("digital_omnibus_ai note must state the OJ publication flip criterion; got %q", item.Note)
	}
	if !strings.Contains(calendarDateDisclaimer, "provisional_agreement") || !strings.Contains(calendarDateDisclaimer, "adopted_pending_oj") {
		t.Errorf("calendar disclaimer must mention both provisional_agreement and adopted_pending_oj; got %q", calendarDateDisclaimer)
	}
}

func TestAICMv11Pin(t *testing.T) {
	fw, ok := frameworkByID["csa_aicm"]
	if !ok {
		t.Fatal("csa_aicm missing from catalog")
	}
	if fw.Pin.PublishedOn != "2026-06-22" || fw.Pin.Status != PinGuidance {
		t.Errorf("csa_aicm pin = {published %q status %q}, want 2026-06-22 / guidance", fw.Pin.PublishedOn, fw.Pin.Status)
	}
	if !strings.Contains(fw.Pin.Document, "v1.1") || !strings.Contains(fw.Pin.Document, "247") {
		t.Errorf("csa_aicm pin document must contain v1.1 and 247; got %q", fw.Pin.Document)
	}
	for _, want := range []string{"247", "STAR for AI", "Level 1", "never a certification"} {
		if !strings.Contains(fw.Disclaimer, want) {
			t.Errorf("csa_aicm disclaimer must contain %q; got %q", want, fw.Disclaimer)
		}
	}
	if len(fw.Controls) != 18 {
		t.Fatalf("csa_aicm has %d controls, want 18", len(fw.Controls))
	}
}

func TestS322WatchlistItems(t *testing.T) {
	for _, id := range []string{"iso_27090", "owasp_aivss"} {
		item, ok := s322WatchlistItem(id)
		if !ok {
			t.Fatalf("%s watchlist item missing", id)
		}
		if !strings.HasPrefix(item.Source.URL, "https://") {
			t.Errorf("%s source URL must be https; got %q", id, item.Source.URL)
		}
		if item.VerifiedOn != "2026-07-03" {
			t.Errorf("%s verified_on = %q, want 2026-07-03", id, item.VerifiedOn)
		}
	}

	for _, id := range []string{"nistir_8596", "nist_cosais_overlays"} {
		item, ok := s322WatchlistItem(id)
		if !ok {
			t.Fatalf("%s watchlist item missing", id)
		}
		if item.VerifiedOn != "2026-07-03" {
			t.Errorf("%s verified_on = %q, want 2026-07-03", id, item.VerifiedOn)
		}
	}
}
