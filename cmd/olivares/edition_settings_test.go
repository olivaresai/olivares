// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/olivaresai/olivares/core/api"
)

// edition_settings_test.go: an edition keeps its administrator choices in the deployment
// settings record under its own keys. The record is a typed document, so every other writer
// rewrites it whole: a key it does not declare is lost the next time anything else is saved.

const editionTestKey = "sample-alpha"

var editionTestValue = json.RawMessage(`{"enabled":true,"config":{"mode":"sample"}}`)

func TestEditionSettingPersistsAndSurvivesEveryOtherWriter(t *testing.T) {
	settings := settingsOnBareStore(t)
	ctx := t.Context()

	if got, err := settings.EditionSettings(ctx); err != nil || len(got) != 0 {
		t.Fatalf("fresh deployment holds edition settings: %v err=%v", got, err)
	}
	if err := settings.SaveEditionSetting(ctx, operator(t), editionTestKey, editionTestValue, map[string]any{"enabled": true}); err != nil {
		t.Fatal(err)
	}
	// Each of these rewrites the whole document through the typed struct.
	if err := settings.writeModules(ctx, operator(t), "test.modules", []string{"eventing"}); err != nil {
		t.Fatal(err)
	}
	if _, err := settings.SaveSessionIdleSettings(ctx, operator(t), api.SessionIdleSettings{IdleStopMinutes: 6}); err != nil {
		t.Fatal(err)
	}
	if err := settings.SaveActivation(ctx, operator(t), activationWith(reportingActive), nil); err != nil {
		t.Fatal(err)
	}

	restarted := newProductSettings(settings.st, settings.dataDir)
	got, err := restarted.EditionSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got[editionTestKey], editionTestValue) {
		t.Fatalf("edition setting after other writers and a restart = %s; want %s", got[editionTestKey], editionTestValue)
	}
}

func TestEditionSettingDeleteRemovesOnlyThatKey(t *testing.T) {
	settings := settingsOnBareStore(t)
	ctx := t.Context()
	other := json.RawMessage(`{"enabled":false}`)
	for key, value := range map[string]json.RawMessage{editionTestKey: editionTestValue, "sample-beta": other} {
		if err := settings.SaveEditionSetting(ctx, operator(t), key, value, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := settings.DeleteEditionSetting(ctx, operator(t), editionTestKey, nil); err != nil {
		t.Fatal(err)
	}
	got, err := settings.EditionSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, kept := got[editionTestKey]; kept || !bytes.Equal(got["sample-beta"], other) {
		t.Fatalf("after deleting %s: %v", editionTestKey, got)
	}
	// Deleting what is not stored is not an error: the administrator's intent already holds.
	if err := settings.DeleteEditionSetting(ctx, operator(t), editionTestKey, nil); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func TestEditionSettingRefusesWhatCannotBeStored(t *testing.T) {
	settings := settingsOnBareStore(t)
	huge := json.RawMessage(`"` + strings.Repeat("a", editionSettingMaxBytes) + `"`)
	for name, tc := range map[string]struct {
		key   string
		value json.RawMessage
	}{
		"empty key":    {"", editionTestValue},
		"not JSON":     {editionTestKey, json.RawMessage(`{"enabled":`)},
		"empty value":  {editionTestKey, nil},
		"over the cap": {editionTestKey, huge},
		"path-like":    {"../x", editionTestValue},
	} {
		if err := settings.SaveEditionSetting(t.Context(), operator(t), tc.key, tc.value, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if got, err := settings.EditionSettings(t.Context()); err != nil || len(got) != 0 {
		t.Fatalf("a refused save left %v err=%v", got, err)
	}
}

// The audit event commits with the record: a save whose evidence is dropped changes nothing.
func TestEditionSettingIsRefusedWhenItsAuditEvidenceIsDropped(t *testing.T) {
	settings := settingsOnBareStore(t)
	dropping := newProductSettings(idleSettingsAuditFailureStore{Store: settings.st, drop: true}, settings.dataDir)
	if err := dropping.SaveEditionSetting(t.Context(), operator(t), editionTestKey, editionTestValue, nil); err == nil {
		t.Fatal("saved without audit evidence")
	}
	if got, err := settings.EditionSettings(t.Context()); err != nil || len(got) != 0 {
		t.Fatalf("an unaudited save left %v err=%v", got, err)
	}
}

// Two administrators saving different keys at the same moment both keep their change.
func TestEditionSettingConcurrentSavesOfDifferentKeysBothLand(t *testing.T) {
	settings := settingsOnBareStore(t)
	keys := []string{"sample-alpha", "sample-beta", "sample-gamma", "sample-delta"}
	var wg sync.WaitGroup
	errs := make([]error, len(keys))
	for i, key := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = settings.SaveEditionSetting(t.Context(), operator(t), key, editionTestValue, nil)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("%s: %v", keys[i], err)
		}
	}
	got, err := settings.EditionSettings(t.Context())
	if err != nil || len(got) != len(keys) {
		t.Fatalf("saved %v; want every key of %v (err=%v)", got, keys, err)
	}
}

// A stale editor on another node cannot overwrite a newer choice for the same key.
func TestEditionSettingCompareAndSwapUsesTheCommittedVersion(t *testing.T) {
	p := settingsOnBareStore(t)
	ctx := t.Context()
	key := "sample-setting"
	original := json.RawMessage(`{"enabled":false}`)
	if err := p.CompareAndSwapEditionSetting(ctx, operator(t), key, nil, original, nil); err != nil {
		t.Fatal(err)
	}
	other := newProductSettings(p.st, p.dataDir)
	updated := json.RawMessage(`{"enabled":true}`)
	if err := other.CompareAndSwapEditionSetting(ctx, operator(t), key, original, updated, nil); err != nil {
		t.Fatal(err)
	}
	if err := p.CompareAndSwapEditionSetting(ctx, operator(t), key, original, original, nil); err == nil {
		t.Fatal("stale editor overwrote the committed setting")
	}
	got, err := p.EditionSettings(ctx)
	if err != nil || !bytes.Equal(got[key], updated) {
		t.Fatalf("stored=%s err=%v", got[key], err)
	}
}
