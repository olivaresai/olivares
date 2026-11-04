// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"testing"

	"github.com/olivaresai/olivares/connectors/modelprovider"
	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/driverfacts"
)

// The console names and offers tools from GET /v1/m/sessions/tools, and its tests read
// the engine's answer from one fixture. These tests hold the route to the driver table
// and the fixture to the route, so neither can drift from driverfacts.
const toolsFixture = "../../web/src/features/agentops/engine-tools.fixture.json"

func toolsAnswer(t *testing.T) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	New().handleListTools(rec, httptest.NewRequest(http.MethodGet, "/v1/m/sessions/tools", nil), api.ModuleContext{})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body)
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

func TestToolsReadIsPublishedWithRunRead(t *testing.T) {
	t.Parallel()
	doc := api.ModuleOpenAPIDocument([]api.Module{New()})
	paths, _ := doc["paths"].(map[string]any)
	item, _ := paths["/v1/m/sessions/tools"].(map[string]any)
	op, ok := item["get"].(map[string]any)
	if !ok {
		t.Fatalf("the beta document does not publish GET /v1/m/sessions/tools")
	}
	if got := op["x-required-permission"]; got != string(permRunRead) {
		t.Fatalf("x-required-permission = %v, want %s", got, permRunRead)
	}
}

func TestToolsReadIsTheDriverTable(t *testing.T) {
	t.Parallel()
	tools, _ := toolsAnswer(t)["tools"].([]any)
	all := driverfacts.All()
	if len(tools) != len(all) {
		t.Fatalf("%d tools listed, the driver table has %d", len(tools), len(all))
	}
	for i, f := range all {
		row, _ := tools[i].(map[string]any)
		if row["key"] != f.Key || row["name"] != f.Name || row["name_stem"] != f.NameStem() || row["session"] != f.Session ||
			row["vendor"] != f.Vendor || row["monogram"] != f.Monogram || row["color"] != f.Color {
			t.Fatalf("row %d = %v, want %s %q stem %q session %v vendor %q mark %q %q", i, row, f.Key, f.Name, f.NameStem(), f.Session, f.Vendor, f.Monogram, f.Color)
		}
		bindings, _ := row["bindings"].([]any)
		if len(bindings) != len(f.Bindings) {
			t.Fatalf("%s: %d bindings listed, the table has %d", f.Key, len(bindings), len(f.Bindings))
		}
		for j, b := range f.Bindings {
			got, _ := bindings[j].(map[string]any)
			if got["kind"] != b.Kind || got["egress"] != b.Egress {
				t.Fatalf("%s binding %d = %v, want %s %s", f.Key, j, got, b.Kind, b.Egress)
			}
		}
	}
}

func TestToolsConsoleFixtureIsTheEngineAnswer(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(toolsFixture)
	if err != nil {
		t.Fatalf("read the console fixture: %v", err)
	}
	var fixture map[string]any
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode %s: %v", toolsFixture, err)
	}
	if got := toolsAnswer(t); !reflect.DeepEqual(got, fixture) {
		want, _ := json.MarshalIndent(got, "", "  ")
		t.Fatalf("%s is not the engine's answer; write this:\n%s", toolsFixture, want)
	}
}

// themeBackgrounds reads the backgrounds a mark sits on (canvas, surface, raised,
// muted) of every console theme from the console's own token file.
func themeBackgrounds(t *testing.T) []string {
	t.Helper()
	raw, err := os.ReadFile("../../web/src/styles/tokens.css")
	if err != nil {
		t.Fatalf("read the console tokens: %v", err)
	}
	m := regexp.MustCompile(`--(?:canvas|surface|raised|muted): (#[0-9a-fA-F]{6});`).FindAllStringSubmatch(string(raw), -1)
	if len(m) < 8 {
		t.Fatalf("found %d theme backgrounds, want the light and dark themes' four each", len(m))
	}
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v[1])
	}
	return out
}

// luminance is the WCAG relative luminance of a #rrggbb color.
func luminance(hex string) float64 {
	var l float64
	for i, w := range []float64{0.2126, 0.7152, 0.0722} {
		v, _ := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		c := float64(v) / 255
		if c <= 0.04045 {
			c /= 12.92
		} else {
			c = math.Pow((c+0.055)/1.055, 2.4)
		}
		l += w * c
	}
	return l
}

func contrast(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	return (math.Max(la, lb) + 0.05) / (math.Min(la, lb) + 0.05)
}

// Every tool and preset has one mark: one or two capitals and a color that keeps the
// 3:1 non-text contrast against every background of both console themes and under its
// white letters.
func TestCatalogIdentityContrast(t *testing.T) {
	t.Parallel()
	backgrounds := append(themeBackgrounds(t), "#ffffff")
	mark := regexp.MustCompile(`^[A-Z]{1,2}$`)
	color := regexp.MustCompile(`^#[0-9A-F]{6}$`)
	type identity struct{ who, monogram, color string }
	var all []identity
	for _, f := range driverfacts.All() {
		all = append(all, identity{f.Key, f.Monogram, f.Color})
		if f.Vendor == "" {
			t.Errorf("tool %s has no vendor", f.Key)
		}
	}
	for _, p := range modelprovider.Presets() {
		all = append(all, identity{p.Ref, p.Monogram, p.Color})
	}
	for _, id := range all {
		if !mark.MatchString(id.monogram) || !color.MatchString(id.color) {
			t.Errorf("%s: mark %q color %q, want one or two capitals and #RRGGBB", id.who, id.monogram, id.color)
			continue
		}
		for _, bg := range backgrounds {
			if c := contrast(id.color, bg); c < 3 {
				t.Errorf("%s: %s on %s is %.2f:1, want 3:1", id.who, id.color, bg, c)
			}
		}
	}
}
