// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

// Package emailtemplate renders the engine's outgoing emails from templates
// generated out of the product's design tokens.
//
// THE GAP THIS FILLS. core/api declares InviteSender and defines no body for the
// message it sends. The interface has existed since and the engine has never
// said what an invitation looks like, so whoever wires a mailer invents the email
// — subject, wording, and whatever brand they happen to reach for. That is not a
// brand problem, it is the absence of one, and it is the only one of the four
// outgoing-mail surfaces that could not be fixed by deleting hand-written HTML,
// because there was no HTML to delete.
//
// WHERE THE CONTENT COMES FROM. Nothing here decides how the email looks. The
// layout, the palette, the type stack and the copy in seven languages are
// resolved once at build time by email/build.mjs, out of the same DTCG sources
// in web/tokens/ that generate the console's stylesheet, and land in
// templates.generated.json. This package escapes a value and substitutes a
// marker. It cannot drift from the console's brand because it holds no opinion
// about it.
//
// The caller supplies the transport. The engine never holds a mailer (that is
// the composition root's job, and the reason InviteSender is an interface), so
// this package returns a Message and sends nothing.
package emailtemplate

import (
	"embed"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

//go:embed templates.generated.json
var bundleFS embed.FS

// Locale is one of the seven languages the product ships.
type Locale string

// DefaultLocale is used for an empty or unrecognized tag. An invitation in the
// wrong language still gets the recipient into their account; no invitation does
// not.
const DefaultLocale Locale = "en"

// Message is a rendered email, ready for any transport. Both bodies are always
// present: a corporate client with HTML disabled must be able to use the link.
type Message struct {
	Subject string
	Text    string
	HTML    string
}

// Invite is the data the invitation template substitutes.
//
// Deliberately narrow. An email is forwarded and archived, so it carries the one
// link the recipient needs and its expiry, and no account identifiers, no tenant
// internals and not even the recipient's own address — the To: header already
// carries that, and repeating it in the body only widens what a forward leaks.
type Invite struct {
	// AcceptURL is the single-use redemption link. Must be http or https.
	AcceptURL string
	// ExpiresAt is when the token stops working. Rendered in UTC as
	// "2006-01-02 15:04 UTC": unambiguous in every locale, which a localized
	// numeric date is not — 03/04 is two different days on two continents.
	ExpiresAt time.Time
}

type template struct {
	Subject string `json:"subject"`
	Text    string `json:"text"`
	HTML    string `json:"html"`
}

type bundle struct {
	Locales      []Locale                       `json:"locales"`
	Placeholders map[string][]string            `json:"placeholders"`
	Templates    map[Locale]map[string]template `json:"templates"`
}

var (
	loadOnce sync.Once
	loaded   bundle
	loadErr  error
)

func load() (bundle, error) {
	loadOnce.Do(func() {
		data, err := bundleFS.ReadFile("templates.generated.json")
		if err != nil {
			loadErr = fmt.Errorf("emailtemplate: reading embedded bundle: %w", err)
			return
		}
		if err := json.Unmarshal(data, &loaded); err != nil {
			loadErr = fmt.Errorf("emailtemplate: parsing embedded bundle: %w", err)
		}
	})
	return loaded, loadErr
}

// Locales returns the shipped locales, in the order the bundle declares them.
func Locales() ([]Locale, error) {
	b, err := load()
	if err != nil {
		return nil, err
	}
	out := make([]Locale, len(b.Locales))
	copy(out, b.Locales)
	return out, nil
}

// PickLocale narrows a BCP-47 tag to a shipped locale, falling back to English.
//
// It accepts what callers actually hold — "es", "es-ES", "ZH-Hans-CN" — and never
// fails. Only the primary subtag is matched: the product ships one variant per
// language, so region matching would be a distinction with nothing behind it.
func PickLocale(tag string) Locale {
	b, err := load()
	if err != nil {
		return DefaultLocale
	}
	primary := strings.ToLower(strings.TrimSpace(tag))
	if i := strings.IndexAny(primary, "-_;, \t"); i >= 0 {
		primary = primary[:i]
	}
	for _, l := range b.Locales {
		if Locale(primary) == l {
			return l
		}
	}
	return DefaultLocale
}

var markerRE = regexp.MustCompile(`\{\{([A-Z_]+)\}\}`)

// substitute replaces every marker in one pass.
//
// One pass over the body, never one pass per value. Substituting values in a loop
// lets a value that itself contains a marker be re-scanned by a later iteration,
// which turns two ordinary lines of code into a way to leak one field into
// another's place. A single pass never looks at what it just wrote.
func substitute(body string, values map[string]string, escape bool) (string, error) {
	var missing []string
	out := markerRE.ReplaceAllStringFunc(body, func(whole string) string {
		name := whole[2 : len(whole)-2]
		v, ok := values[name]
		if !ok {
			missing = append(missing, name)
			return whole
		}
		if escape {
			return html.EscapeString(v)
		}
		return v
	})
	if len(missing) > 0 {
		// Unreachable while the template and its declared placeholders come from
		// the same build; kept because the alternative is mailing the literal
		// text "{{ACCEPT_URL}}" to someone being onboarded.
		return "", fmt.Errorf("emailtemplate: no value for marker(s) %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// requireHTTPURL rejects anything that is not an ordinary web address.
//
// The accept URL is built by the engine and never arrives from a request, so this
// should never fire — which is why it is checked rather than assumed. A
// javascript: URL in an href is live in a handful of mail clients.
func requireHTTPURL(field, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("emailtemplate: %s is not a URL: %w", field, err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("emailtemplate: %s has non-HTTP scheme %q", field, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("emailtemplate: %s has no host", field)
	}
	return nil
}

// ExpiryFormat is how an expiry instant is written into an email body.
const ExpiryFormat = "2006-01-02 15:04 UTC"

// RenderInvite renders the onboarding invitation in the given locale.
//
// An unrecognized locale falls back to English rather than failing: the caller is
// delivering a credential, and refusing to render because a language tag was odd
// would strand the person being onboarded.
func RenderInvite(locale Locale, in Invite) (Message, error) {
	b, err := load()
	if err != nil {
		return Message{}, err
	}
	if err := requireHTTPURL("AcceptURL", in.AcceptURL); err != nil {
		return Message{}, err
	}
	if in.ExpiresAt.IsZero() {
		return Message{}, fmt.Errorf("emailtemplate: Invite.ExpiresAt is zero")
	}

	byName, ok := b.Templates[locale]
	if !ok {
		byName = b.Templates[DefaultLocale]
	}
	tpl, ok := byName["invite"]
	if !ok {
		return Message{}, fmt.Errorf("emailtemplate: bundle has no invite template for %q", locale)
	}

	values := map[string]string{
		"ACCEPT_URL": in.AcceptURL,
		"EXPIRES_AT": in.ExpiresAt.UTC().Format(ExpiryFormat),
	}

	text, err := substitute(tpl.Text, values, false)
	if err != nil {
		return Message{}, err
	}
	htmlBody, err := substitute(tpl.HTML, values, true)
	if err != nil {
		return Message{}, err
	}
	return Message{Subject: tpl.Subject, Text: text, HTML: htmlBody}, nil
}
