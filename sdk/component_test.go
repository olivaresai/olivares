// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package sdk_test

import (
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

func TestConfigGetters(t *testing.T) {
	c := sdk.Config{Settings: map[string]string{
		"endpoint": "https://x",
		"workers":  "4",
		"enabled":  "true",
		"interval": "30s",
		"bad_int":  "nope",
	}}

	if c.Get("endpoint") != "https://x" {
		t.Error("Get(endpoint) wrong")
	}
	if _, ok := c.Lookup("missing"); ok {
		t.Error("Lookup(missing) should be false")
	}
	if c.GetInt("workers", 1) != 4 {
		t.Error("GetInt(workers) should parse to 4")
	}
	if c.GetInt("bad_int", 7) != 7 {
		t.Error("GetInt on unparseable should fall back to default")
	}
	if c.GetInt("missing", 9) != 9 {
		t.Error("GetInt on missing should fall back to default")
	}
	if !c.GetBool("enabled", false) {
		t.Error("GetBool(enabled) should be true")
	}
	if c.GetBool("missing", true) != true {
		t.Error("GetBool on missing should fall back to default")
	}
	if c.GetDuration("interval", time.Second) != 30*time.Second {
		t.Error("GetDuration(interval) should parse to 30s")
	}
	if c.GetDuration("missing", 5*time.Second) != 5*time.Second {
		t.Error("GetDuration on missing should fall back to default")
	}
}

func TestComponentTypeValid(t *testing.T) {
	for _, ct := range []sdk.ComponentType{sdk.TypeSource, sdk.TypeOutput, sdk.TypeContentSource, sdk.TypeModule} {
		if !ct.Valid() {
			t.Errorf("%q should be a valid component type", ct)
		}
	}
	if sdk.ComponentType("plugin").Valid() {
		t.Error("unknown component type should be invalid")
	}
}
