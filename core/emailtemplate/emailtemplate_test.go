// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package emailtemplate

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

const (
	// A LITERAL ampersand in the query, not a percent-encoded one: the escaping
	// this fixture exists to prove only happens to characters that are still
	// special when they reach the HTML. %26 would have proved nothing.
	acceptURL = "https://console.example.invalid/invite/accept?token=abc&id=def"
	// The brand accent as a FILL, which the DTCG sources declare and every rendered
	// body must therefore carry. Pinned as a literal ON PURPOSE, and it is the one
	// literal allowed in this tree: if it stops matching, either the brand moved and
	// the emails did not follow, or the emails moved and the brand did not.
	//
	// It is now the SAME hex in both themes, and that is the design rather than a
	// collapse: the orange was split by ROLE, not by theme. `accent` is the fill and
	// stays the brand orange because the ink sits ON it; only the orange used as TEXT
	// on a near-white canvas is deepened. web/tokens/theme.light.tokens.json says so
	// in its own $description.
	brandAccentFill = "#f08000"
	// The deepened orange is the light theme's accent-as-TEXT token, and the email
	// must NOT carry it. Two independent reasons, so this is an assertion and not a
	// coincidence: the email uses the accent only as a button fill with its own ink on
	// top (email/layout.mjs), and the brand manual BRAND-02(d) keeps links out of the
	// accent entirely, so no accent-colored text exists to deepen.
	//
	// This constant used to be asserted PRESENT, on the premise that the button's
	// inline style carried a distinct light accent. That premise died when the orange
	// was split by role, and the assertion is inverted here rather than deleted so the
	// rule stays pinned instead of merely stopping being checked.
	canvasTextAccent = "#b45500"
	// The dark canvas, which proves the light/dark derivation still happens at all.
	// The accent can no longer prove it: it is one hex in both themes now, so an email
	// that had lost its dark-mode block entirely would still carry the accent.
	darkCanvas = "#28282b"
)

func expiry() time.Time {
	return time.Date(2026, 8, 13, 10, 30, 0, 0, time.UTC)
}

// The whole point of the package: what a customer receives is derived from the
// tokens rather than typed next to the sending code.
func TestRenderInviteCarriesTheDerivedBrand(t *testing.T) {
	locales, err := Locales()
	if err != nil {
		t.Fatalf("Locales: %v", err)
	}
	if len(locales) != 7 {
		t.Fatalf("expected the seven shipped locales, got %d: %v", len(locales), locales)
	}

	for _, l := range locales {
		msg, err := RenderInvite(l, Invite{AcceptURL: acceptURL, ExpiresAt: expiry()})
		if err != nil {
			t.Fatalf("RenderInvite(%q): %v", l, err)
		}
		if msg.Subject == "" || msg.Text == "" || msg.HTML == "" {
			t.Fatalf("RenderInvite(%q): empty field in %+v", l, msg)
		}
		if !strings.Contains(msg.HTML, brandAccentFill) {
			t.Errorf("RenderInvite(%q): html does not carry the brand accent fill %s", l, brandAccentFill)
		}
		if !strings.Contains(msg.HTML, darkCanvas) {
			t.Errorf("RenderInvite(%q): html has no dark-scheme block (dark canvas %s absent)", l, darkCanvas)
		}
		if strings.Contains(msg.HTML, canvasTextAccent) {
			t.Errorf("RenderInvite(%q): html carries the canvas-TEXT accent %s; the email uses the accent only as a fill", l, canvasTextAccent)
		}
	}
}

// A body that still contains a marker is a body that ships literal brace text to
// someone being onboarded.
func TestNoMarkerSurvivesRendering(t *testing.T) {
	locales, err := Locales()
	if err != nil {
		t.Fatalf("Locales: %v", err)
	}
	for _, l := range locales {
		msg, err := RenderInvite(l, Invite{AcceptURL: acceptURL, ExpiresAt: expiry()})
		if err != nil {
			t.Fatalf("RenderInvite(%q): %v", l, err)
		}
		for _, body := range []struct{ what, s string }{
			{"subject", msg.Subject}, {"text", msg.Text}, {"html", msg.HTML},
		} {
			if m := markerRE.FindString(body.s); m != "" {
				t.Errorf("RenderInvite(%q): %s still carries %s", l, body.what, m)
			}
		}
	}
}

// The declared placeholder set and what the templates actually contain must agree.
// Go cannot enforce this at compile time the way the generated TypeScript value
// map does for the Worker, so the same guarantee arrives one step later, here.
func TestDeclaredPlaceholdersMatchTheTemplates(t *testing.T) {
	b, err := load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	declared := map[string]bool{}
	for _, p := range b.Placeholders["invite"] {
		declared[p] = true
	}
	if len(declared) == 0 {
		t.Fatal("bundle declares no placeholders for the invite template")
	}

	seen := map[string]bool{}
	for _, l := range b.Locales {
		tpl := b.Templates[l]["invite"]
		for _, body := range []string{tpl.Subject, tpl.Text, tpl.HTML} {
			for _, m := range markerRE.FindAllStringSubmatch(body, -1) {
				seen[m[1]] = true
				if !declared[m[1]] {
					t.Errorf("locale %q: template carries undeclared marker {{%s}}", l, m[1])
				}
			}
		}
	}
	for p := range declared {
		if !seen[p] {
			t.Errorf("placeholder {{%s}} is declared but no template emits it", p)
		}
	}
}

// A value is escaped exactly once. Escaped twice, a customer reads "&amp;amp;".
func TestSubstitutedValuesAreEscapedOnceInHTMLAndNotInText(t *testing.T) {
	msg, err := RenderInvite("en", Invite{AcceptURL: acceptURL, ExpiresAt: expiry()})
	if err != nil {
		t.Fatalf("RenderInvite: %v", err)
	}
	if !strings.Contains(msg.HTML, "token=abc&amp;id=def") {
		t.Error("html: the ampersand in the accept URL is not escaped")
	}
	if strings.Contains(msg.HTML, "&amp;amp;") {
		t.Error("html: a value was escaped twice")
	}
	if !strings.Contains(msg.Text, "token=abc&id=def") {
		t.Error("text: the accept URL should appear verbatim, not HTML-escaped")
	}
}

// One pass, not one per value. With per-value substitution a value that contains
// another marker gets re-scanned and replaced by whatever comes later in the loop
// — one field silently taking another's place.
func TestSubstitutionDoesNotRescanItsOwnOutput(t *testing.T) {
	got, err := substitute(
		"[{{A}}][{{B}}]",
		map[string]string{"A": "{{B}}", "B": "secret"},
		false,
	)
	if err != nil {
		t.Fatalf("substitute: %v", err)
	}
	if got != "[{{B}}][secret]" {
		t.Errorf("value containing a marker was re-scanned: got %q", got)
	}
}

func TestSubstituteRefusesAMarkerWithNoValue(t *testing.T) {
	if _, err := substitute("x {{NOPE}} y", map[string]string{}, false); err == nil {
		t.Fatal("expected an error for a marker with no value")
	}
}

// These URLs are built by the engine, so the guard should never fire in
// production — which is exactly why it is tested rather than assumed.
func TestRenderInviteRejectsUnusableInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   Invite
	}{
		{"javascript scheme", Invite{AcceptURL: "javascript:alert(1)", ExpiresAt: expiry()}},
		{"mailto scheme", Invite{AcceptURL: "mailto:someone@example.invalid", ExpiresAt: expiry()}},
		{"scheme-less", Invite{AcceptURL: "console.example.invalid/accept", ExpiresAt: expiry()}},
		{"no host", Invite{AcceptURL: "https://", ExpiresAt: expiry()}},
		{"zero expiry", Invite{AcceptURL: acceptURL}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := RenderInvite("en", tc.in); err == nil {
				t.Fatalf("expected an error for %s", tc.name)
			}
		})
	}
}

// Deliverability is a property of the rendered output, not of the generator's
// good intentions: no images of any kind, no external resources, no scripts.
// Half of corporate mail blocks remote images by default and Gmail strips both
// data: URIs and inline vector, so a message that depends on any of them is a
// message that arrives blank.
func TestRenderedHTMLPullsNothingFromTheNetwork(t *testing.T) {
	msg, err := RenderInvite("en", Invite{AcceptURL: acceptURL, ExpiresAt: expiry()})
	if err != nil {
		t.Fatalf("RenderInvite: %v", err)
	}
	for _, bad := range []struct {
		what string
		re   *regexp.Regexp
	}{
		{"an image tag", regexp.MustCompile(`(?i)<img\b`)},
		{"an inline vector", regexp.MustCompile(`(?i)<svg\b`)},
		{"an external resource link", regexp.MustCompile(`(?i)<link\b`)},
		{"a script", regexp.MustCompile(`(?i)<script\b`)},
		{"a remote resource", regexp.MustCompile(`(?i)(?:src|background)\s*=\s*["']?https?:`)},
		{"a data URI", regexp.MustCompile(`(?i)["'(]\s*data:`)},
		{"a web font", regexp.MustCompile(`(?i)@font-face|@import`)},
	} {
		if bad.re.MatchString(msg.HTML) {
			t.Errorf("rendered html contains %s", bad.what)
		}
	}
}

// A plain-text body that says "use the button below" is a broken body: there is
// no button. It must carry the address itself.
func TestPlainTextCarriesTheLinkItself(t *testing.T) {
	msg, err := RenderInvite("en", Invite{AcceptURL: acceptURL, ExpiresAt: expiry()})
	if err != nil {
		t.Fatalf("RenderInvite: %v", err)
	}
	if !strings.Contains(msg.Text, acceptURL) {
		t.Error("text body does not contain the accept URL")
	}
	if strings.Contains(msg.Text, "<") || strings.Contains(msg.Text, "&nbsp;") {
		t.Error("text body carries markup")
	}
	if !strings.Contains(msg.Text, "2026-08-13 10:30 UTC") {
		t.Error("text body does not carry the formatted expiry")
	}
}

func TestPickLocale(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Locale
	}{
		{"es", "es"},
		{"es-ES", "es"},
		{"ZH-Hans-CN", "zh"},
		{"ja_JP", "ja"},
		{"fr;q=0.9", "fr"},
		{"", DefaultLocale},
		{"pt-BR", DefaultLocale},
		{"klingon", DefaultLocale},
	} {
		if got := PickLocale(tc.in); got != tc.want {
			t.Errorf("PickLocale(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// An unknown locale must still produce a usable email rather than an error.
func TestUnknownLocaleFallsBackRatherThanFailing(t *testing.T) {
	msg, err := RenderInvite("klingon", Invite{AcceptURL: acceptURL, ExpiresAt: expiry()})
	if err != nil {
		t.Fatalf("RenderInvite with unknown locale: %v", err)
	}
	en, err := RenderInvite(DefaultLocale, Invite{AcceptURL: acceptURL, ExpiresAt: expiry()})
	if err != nil {
		t.Fatalf("RenderInvite(en): %v", err)
	}
	if msg.Subject != en.Subject {
		t.Errorf("unknown locale did not fall back to English: %q vs %q", msg.Subject, en.Subject)
	}
}

// An email is forwarded and archived. The body carries the link and its expiry
// and nothing else that identifies anyone.
func TestBodyCarriesNoIdentifierBeyondTheLink(t *testing.T) {
	msg, err := RenderInvite("en", Invite{AcceptURL: acceptURL, ExpiresAt: expiry()})
	if err != nil {
		t.Fatalf("RenderInvite: %v", err)
	}
	// Looking for an at-sign was the first version of this test and it was wrong:
	// the stylesheet's own `@media (prefers-color-scheme: dark)` matched it. What
	// matters is an ADDRESS, so that is what is looked for. The struct has no field
	// that could carry one, and this asserts the shape stays that way.
	addressRE := regexp.MustCompile(`[\w.+-]+@[\w-]+\.[\w.-]+`)
	for _, body := range []struct{ what, s string }{
		{"subject", msg.Subject}, {"text", msg.Text}, {"html", msg.HTML},
	} {
		if m := addressRE.FindString(body.s); m != "" {
			t.Errorf("%s carries what looks like an email address: %q", body.what, m)
		}
	}
}
