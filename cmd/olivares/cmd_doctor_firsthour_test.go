// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/olivaresai/olivares/cmd/olivares/exitcode"
)

func TestDoctorFirstHourChecksDoNotFailAHealthyInstall(t *testing.T) {
	o, deps := doctorFixture(t)
	deps.lookPath = func(string) (string, error) { return "", errNotFound{} }
	deps.getenv = func(string) string { return "" }

	report, code, err := runDoctor(context.Background(), o, deps)
	if err != nil {
		t.Fatal(err)
	}
	if code != exitcode.OK || report.Overall != "healthy" {
		t.Fatalf("doctor = code %d overall %q checks=%+v", code, report.Overall, report.Checks)
	}
	agent := doctorCheckByName(report, "first-hour-coding-agent")
	if agent.Status != "unknown" || agent.Required {
		t.Errorf("coding-agent = %+v, want optional unknown", agent)
	}
	pep := doctorCheckByName(report, "first-hour-hook-pep")
	if pep.Status != "unknown" || pep.Required {
		t.Errorf("hook-pep = %+v, want optional unknown", pep)
	}
	next := doctorCheckByName(report, "first-hour-next-step")
	if next.Status != "pass" {
		t.Errorf("next-step status = %q, want pass", next.Status)
	}
	if !strings.Contains(next.Detail, "olivares agent tool detect") {
		t.Errorf("next-step does not name detect: %q", next.Detail)
	}
}

func TestDoctorFirstHourNextStepWhenAgentPresentAndPEPMissing(t *testing.T) {
	o, deps := doctorFixture(t)
	deps.lookPath = func(name string) (string, error) {
		if name == "claude" {
			return "/usr/bin/claude", nil
		}
		return "", errNotFound{}
	}
	deps.getenv = func(string) string { return "" }
	report, code, err := runDoctor(context.Background(), o, deps)
	if err != nil {
		t.Fatal(err)
	}
	if code != exitcode.OK {
		t.Fatalf("doctor code = %d, want 0; checks=%+v", code, report.Checks)
	}
	if got := doctorCheckByName(report, "first-hour-coding-agent"); got.Status != "pass" || got.Detail != "official CLI on PATH: claude" {
		t.Errorf("coding-agent = %+v", got)
	}
	next := doctorCheckByName(report, "first-hour-next-step")
	if !strings.Contains(next.Detail, "OLIVARES_HOOK_PEP_CONFIG") {
		t.Errorf("next-step does not name hook PEP: %q", next.Detail)
	}
}

func TestDoctorFirstHourHookPEPSetNeverPrintsTheValue(t *testing.T) {
	o, deps := doctorFixture(t)
	secret := "http://127.0.0.1:8447/?token=never-print-this-pep"
	deps.lookPath = func(name string) (string, error) {
		if name == "claude" {
			return "/usr/bin/claude", nil
		}
		return "", errNotFound{}
	}
	deps.getenv = func(key string) string {
		if key == "OLIVARES_HOOK_PEP_URL" {
			return secret
		}
		return ""
	}
	report, code, err := runDoctor(context.Background(), o, deps)
	if err != nil {
		t.Fatal(err)
	}
	if code != exitcode.OK {
		t.Fatalf("doctor code = %d", code)
	}
	pep := doctorCheckByName(report, "first-hour-hook-pep")
	if pep.Status != "pass" || pep.Detail != "OLIVARES_HOOK_PEP_URL is set" {
		t.Errorf("hook-pep = %+v", pep)
	}
	body, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte(secret)) || bytes.Contains(body, []byte("never-print-this-pep")) {
		t.Fatalf("doctor JSON disclosed the hook PEP URL:\n%s", body)
	}
	next := doctorCheckByName(report, "first-hour-next-step")
	if !strings.Contains(next.Detail, "GET /v1/audit?action=hook.tool") {
		t.Errorf("next-step does not name evidence: %q", next.Detail)
	}
}

type errNotFound struct{}

func (errNotFound) Error() string { return "not found" }
