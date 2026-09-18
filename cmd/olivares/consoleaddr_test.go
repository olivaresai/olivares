// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/webaddr"
)

// THE FLAG HAS TO EXIST ON ALL THREE COMMANDS. Cobra's local flags are per
// command and are NOT inherited, so `quickstart governed-rag` needs its own — a
// fact a reader is entitled to assume is wrong, which is why it is asserted
// rather than argued.
func TestPublicURLFlagIsOnEveryCommandThatServesAConsole(t *testing.T) {
	t.Parallel()
	serve := newServeCmd()
	quickstart := newQuickstartCmd()
	governedRAG := quickstart.Commands()
	check := func(name string, f *pflag.Flag) {
		t.Helper()
		if f == nil {
			t.Fatalf("%s has no --public-url flag", name)
		}
		if f.DefValue != "" {
			t.Errorf("%s --public-url default = %q, want empty (declaring nothing keeps today's behavior)", name, f.DefValue)
		}
		for _, must := range []string{"OLIVARES_PUBLIC_URL", "restart"} {
			if !strings.Contains(f.Usage, must) {
				t.Errorf("%s --public-url help does not mention %q: %s", name, must, f.Usage)
			}
		}
	}
	check("serve", serve.Flags().Lookup("public-url"))
	check("quickstart", quickstart.Flags().Lookup("public-url"))
	var found bool
	for _, sub := range governedRAG {
		if sub.Name() == "governed-rag" {
			found = true
			check("quickstart governed-rag", sub.Flags().Lookup("public-url"))
		}
	}
	if !found {
		t.Fatal("quickstart has no governed-rag subcommand")
	}
}

// A DECLARED ADDRESS IS WHAT THE PANEL PRINTS, and nothing about the bind leaks
// into it: the two are independent by design, which is the entire point of a
// deployment behind a reverse proxy.
func TestADeclaredAddressIsWhatThePanelPrints(t *testing.T) {
	t.Parallel()
	declared, err := webaddr.Parse("--public-url", "https://olivares.example.com")
	if err != nil {
		t.Fatal(err)
	}
	got := resolveConsoleAddress(declared, ":8443", false).withPlan(webAuthnPlan{Source: "per-request"})
	if got.URL() != "https://olivares.example.com" {
		t.Fatalf("panel address = %q, want the declared one", got.URL())
	}
	if !got.Declared {
		t.Error("a declared address was not reported as declared")
	}
	// Nothing to explain: a real host name over https, and the wildcard bind is no
	// longer the address, so its paragraph must not fire.
	if got.Advice != "" {
		t.Errorf("a usable declared address printed advice:\n%s", got.Advice)
	}
}

// A SCHEME THAT DISAGREES WITH THE TRANSPORT IS NAMED, NEVER REFUSED. Refusing it
// would break the reverse-proxy deployment this whole change exists to serve, and
// the text describes the backend protocol without claiming a proxy is there.
func TestASchemeTransportMismatchIsNamedAndNotRefused(t *testing.T) {
	t.Parallel()
	cases := []struct {
		declared string
		insecure bool
		warn     bool
	}{
		{"https://olivares.example.com", false, false},
		{"http://olivares.example.com", true, false},
		{"http://olivares.example.com", false, true},
		{"https://olivares.example.com", true, true},
	}
	for _, c := range cases {
		addr, err := webaddr.Parse("--public-url", c.declared)
		if err != nil {
			t.Fatalf("Parse(%q) was REFUSED; every row here must be accepted: %v", c.declared, err)
		}
		got := resolveConsoleAddress(addr, "127.0.0.1:8443", c.insecure).withPlan(webAuthnPlan{Source: "per-request"})
		mentions := strings.Contains(got.Advice, "this engine is serving")
		if mentions != c.warn {
			t.Errorf("%q with insecure=%v: transport paragraph present = %v, want %v\n%s",
				c.declared, c.insecure, mentions, c.warn, got.Advice)
		}
		if got.URL() != addr.Origin {
			t.Errorf("%q: panel address = %q, want the declared one", c.declared, got.URL())
		}
		// AND IT MUST NOT NAME A TOPOLOGY. "A TLS-terminating proxy" is the wrong
		// description in the https-declared / plaintext-backend direction — that is
		// a proxy ADDING TLS, not terminating it — and clause 8 allows describing
		// the backend protocol without asserting what is in front of the engine.
		if strings.Contains(got.Advice, "TLS-terminating") {
			t.Errorf("%q with insecure=%v: the paragraph asserts a proxy topology this process cannot see:\n%s",
				c.declared, c.insecure, got.Advice)
		}
	}
	// The non-firing direction: plain http off the local host is refused by the
	// BROWSER, not by us, and it gets the secure-context paragraph rather than the
	// proxy one.
	addr, err := webaddr.Parse("--public-url", "http://olivares.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if a := resolveConsoleAddress(addr, "127.0.0.1:8443", true).withPlan(webAuthnPlan{Source: "per-request"}).Advice; !strings.Contains(a, "secure context") {
		t.Errorf("plain http off-host did not get the secure-context paragraph:\n%s", a)
	}
}

// THE ADVICE HAS TO LAND WHERE THE PRODUCT'S OWN CAPTURE COMMANDS CAN SEE IT.
//
// The documented way to read this banner is a RANGE — install-service.sh and
// seven tutorial pages all publish
// `sed -n '/FIRST-BOOT SETUP/,/========================/p'` — so advice printed
// after the closing marker is outside every capture command the product ships.
// And sixty documented `grep -A<n>` commands count lines from the header down to
// `Token:`, so the advice must come AFTER Token or it pushes the token out of
// their window.
//
// Both halves are asserted, and the second is asserted by comparing against the
// same banner with no advice, so it measures the distance rather than a constant
// somebody can update to match a regression.
func TestAdviceIsInsideTheDocumentedCaptureRange(t *testing.T) {
	// A FRESH ENGINE PER RENDER, and it is not fussiness: the setup token is
	// single-use, so a second announceSetup on the same engine takes the "setup
	// still pending" branch and prints a banner with no Token line at all. The
	// first draft of this test compared against that and measured nothing.
	withAdvice := renderFirstBootBanner(t, newAnnounceTestEngine(t), declaredConsoleAddress(t, "https://127.0.0.1:8443", false))
	if !strings.Contains(withAdvice, "Passkeys will not work at that address") {
		t.Fatalf("the fixture produced no advice, so this test asserts nothing:\n%s", withAdvice)
	}
	header := strings.Index(withAdvice, "=== FIRST-BOOT SETUP ===")
	token := strings.Index(withAdvice, "  Token:")
	advice := strings.Index(withAdvice, "Passkeys will not work")
	closing := strings.Index(withAdvice, "========================\n\n")
	switch {
	case header < 0 || token < 0 || closing < 0:
		t.Fatalf("banner is not the shape every documented capture command assumes:\n%s", withAdvice)
	case advice < token:
		t.Error("the advice is printed BEFORE the Token line, which pushes the token out of every documented grep -A window")
	case advice > closing:
		t.Error("the advice is printed after the closing marker, which is outside every documented sed range")
	}
	// The header-to-Token distance is what sixty documented commands measure, so
	// it is compared against a banner built with nothing to say rather than against
	// a number.
	quiet := renderFirstBootBanner(t, newAnnounceTestEngine(t), declaredConsoleAddress(t, "https://olivares.example.com", false))
	if a := linesBetween(quiet, "=== FIRST-BOOT SETUP ===", "  Token:"); a != linesBetween(withAdvice, "=== FIRST-BOOT SETUP ===", "  Token:") {
		t.Errorf("the advice changed the header-to-Token distance: %d without advice, %d with",
			a, linesBetween(withAdvice, "=== FIRST-BOOT SETUP ===", "  Token:"))
	}
	// And a banner with nothing to say prints exactly what it printed before.
	if strings.Contains(quiet, "Passkeys will not work") || strings.Contains(quiet, "bound to EVERY interface") {
		t.Errorf("a usable address still printed advice:\n%s", quiet)
	}
}

func renderFirstBootBanner(t *testing.T, eng *engine, addr consoleAddress) string {
	t.Helper()
	var out bytes.Buffer
	if err := announceSetup(context.Background(), &out, eng, addr, false); err != nil {
		t.Fatalf("announceSetup: %v", err)
	}
	return out.String()
}

func linesBetween(s, from, to string) int {
	lines := strings.Split(s, "\n")
	start := -1
	for i, l := range lines {
		switch {
		case start < 0 && strings.Contains(l, from):
			start = i
		case start >= 0 && strings.HasPrefix(l, to):
			return i - start
		}
	}
	return -1
}

// A WILDCARD BIND IS NAMED AS A BIND, AND THE PANEL LISTS WHAT IT ANSWERS AT.
//
// The earlier version of this row required the sentence "the URL above is the one
// that works on this machine" and nothing else. That sentence was true and it was
// the whole defect: the reader is on a server over SSH, and the single address the
// panel offered is the one address their browser cannot open. D18 replaced it with
// the list, so the row now measures the list — and still measures that nothing
// claims reachability, which this process cannot observe.
func TestAWildcardBindIsNamedAndNeverPromisesRemoteReach(t *testing.T) {
	t.Parallel()
	for _, listen := range []string{":8443", "0.0.0.0:8443", "[::]:8443"} {
		got := resolveConsoleAddress(webaddr.Address{}, listen, false).withPlan(webAuthnPlan{Source: "per-request"})
		if got.URL() != "https://localhost:8443" {
			t.Errorf("%q: panel address = %q, want an openable same-machine address", listen, got.URL())
		}
		if !strings.Contains(got.Advice, "EVERY interface of this host") {
			t.Errorf("%q: the bind was printed as if it were an address:\n%s", listen, got.Advice)
		}
		if !strings.Contains(got.Advice, "the console answers") {
			t.Errorf("%q: the advice does not offer the addresses the bind answers at:\n%s", listen, got.Advice)
		}
		// Loopback is always in the list, and it is LAST: whoever reads this in a
		// terminal on the machine can use it, and whoever reads it over SSH wants
		// the routable ones first.
		addrs := got.Reachable
		if len(addrs) == 0 {
			t.Fatalf("%q: no address was enumerated at all", listen)
		}
		if last := addrs[len(addrs)-1]; last.Origin != "https://127.0.0.1:8443" {
			t.Errorf("%q: last enumerated address = %q, want loopback last", listen, last.Origin)
		}
		for i, a := range addrs[:len(addrs)-1] {
			if a.IsLoopback() {
				t.Errorf("%q: loopback address %q at position %d, want it only last", listen, a.Origin, i)
			}
		}
		// NOTHING in the paragraph may promise reach. A firewall, a route and a NAT
		// are all invisible to this process.
		for _, forbidden := range []string{"reachable from", "you can reach", "is reachable"} {
			if strings.Contains(got.Advice, forbidden) {
				t.Errorf("%q: the advice claims reachability (%q):\n%s", listen, forbidden, got.Advice)
			}
		}
	}
	// Non-firing: a concrete bind is not called a wildcard and enumerates nothing.
	concrete := resolveConsoleAddress(webaddr.Address{}, "panel.example.com:8443", false).withPlan(webAuthnPlan{Source: "per-request"})
	if strings.Contains(concrete.Advice, "EVERY interface") {
		t.Errorf("a concrete bind was called a wildcard:\n%s", concrete.Advice)
	}
	if len(concrete.Reachable) != 0 {
		t.Errorf("a concrete bind enumerated %d addresses; there is nothing to enumerate", len(concrete.Reachable))
	}
}

// THE CONTAINER SENTENCE IS A DIFFERENT REMEDY, NOT A HEDGE. Inside a container
// the enumerated addresses are the container's, and the useful address is the
// published port on a host this process cannot see — so the paragraph must send
// the reader to the host, and must say where the setup token is, because it is
// printed to a log they are not looking at.
func TestContainerAdviceSendsTheReaderToThePublishedPort(t *testing.T) {
	t.Parallel()
	base := resolveConsoleAddress(webaddr.Address{}, ":8443", false)
	base.Container = true
	got := base.withPlan(webAuthnPlan{Source: "per-request"}).Advice
	for _, want := range []string{"INSIDE this container", "published port", "OLIVARES_PUBLIC_URL", "first-boot"} {
		if !strings.Contains(got, want) {
			t.Errorf("the container advice does not mention %q:\n%s", want, got)
		}
	}
	// The mapped port named is the one this process listens on, taken from the
	// bind rather than assumed.
	if !strings.Contains(got, "mapped to 8443") {
		t.Errorf("the container advice does not name the container-side port:\n%s", got)
	}
	odd := resolveConsoleAddress(webaddr.Address{}, ":19443", false)
	odd.Container = true
	if a := odd.withPlan(webAuthnPlan{Source: "per-request"}).Advice; !strings.Contains(a, "mapped to 19443") {
		t.Errorf("a non-default port was not carried into the container advice:\n%s", a)
	}
}

// WHEN ENUMERATION ANSWERS NOTHING, THE PANEL SAYS SO. An empty list printed as a
// list is a panel that looks broken; the fallback names the one address it is
// sure of and states what it could not find out.
func TestWildcardAdviceWithoutAnyEnumeratedAddress(t *testing.T) {
	t.Parallel()
	addr := resolveConsoleAddress(webaddr.Address{}, ":8443", false)
	addr.Reachable = nil
	got := addr.withPlan(webAuthnPlan{Source: "per-request"}).Advice
	if !strings.Contains(got, "could not enumerate") {
		t.Errorf("the fallback does not state what went unanswered:\n%s", got)
	}
	if !strings.Contains(got, "https://localhost:8443") {
		t.Errorf("the fallback does not offer the one address it is sure of:\n%s", got)
	}
}

// AN INVALID EXPLICIT PAIR IS REFUSED BEFORE ANYTHING MUTABLE HAPPENS ON THE
// HOST. The observable is the data directory: boot() creates it, mints three
// private keys inside it and opens a store. If the refusal came later, the
// operator would have to clean that up before they could retry.
func TestAnInvalidExplicitPinIsRefusedBeforeTheDataDirectoryExists(t *testing.T) {
	t.Setenv("OLIVARES_WEBAUTHN_RPID", "10.1.2.3")
	t.Setenv("OLIVARES_WEBAUTHN_ORIGINS", "https://10.1.2.3:8443")
	dataDir := filepath.Join(t.TempDir(), "install")
	eng, err := boot(context.Background(), bootConfig{
		DataDir: dataDir, Engine: "sqlite", Version: "test", Logger: slog.Default(),
	})
	if err == nil {
		_ = eng.Close()
		t.Fatal("boot accepted a relying party no browser will ever complete a ceremony against")
	}
	if !strings.Contains(err.Error(), "OLIVARES_WEBAUTHN_") {
		t.Errorf("the refusal does not name the keys to edit: %v", err)
	}
	if strings.Contains(err.Error(), "10.1.2.3") {
		t.Errorf("the refusal echoes a configured value: %v", err)
	}
	if _, statErr := os.Stat(dataDir); !os.IsNotExist(statErr) {
		t.Errorf("boot created %s before refusing the configuration (stat err = %v)", dataDir, statErr)
	}
}

// The positive control for the test above: with a VALID pin, boot gets past that
// point and builds the installation. Without this, deleting the whole check would
// leave the negative test green.
func TestAValidExplicitPinBootsAndReachesTheAPI(t *testing.T) {
	t.Setenv("OLIVARES_WEBAUTHN_RPID", "panel.example.com")
	t.Setenv("OLIVARES_WEBAUTHN_ORIGINS", "https://panel.example.com:8443")
	dataDir := filepath.Join(t.TempDir(), "install")
	eng, err := boot(context.Background(), bootConfig{
		DataDir: dataDir, Engine: "sqlite", Version: "test", Logger: slog.Default(),
	})
	if err != nil {
		t.Fatalf("boot refused a usable explicit relying party: %v", err)
	}
	t.Cleanup(func() { _ = eng.Close() })
	if _, statErr := os.Stat(dataDir); statErr != nil {
		t.Fatalf("boot did not build the installation: %v", statErr)
	}
}

// THE PANEL DESCRIBES THE RESOLVED PLAN, NOT THE ADDRESS ALONE.
//
// Both rows below printed "https://localhost:8443, which is a name a browser will
// complete a ceremony against" before this correction, and in both the sentence
// was false — measured by an independent review, not deduced:
//
//   - a DECLARED unusable address makes every ceremony leg answer 503, so the
//     localhost the panel offered is refused by the engine too;
//   - a PINNED relying party is verified against its configured origins, and
//     localhost is not among them unless the operator put it there.
//
// The third row is the one case where the offer is real: with per-request
// derivation the relying party follows the address the browser used, so changing
// the address changes the outcome.
func TestThePasskeyParagraphFollowsTheResolvedPlan(t *testing.T) {
	t.Parallel()
	at := func(t *testing.T, raw string) webaddr.Address {
		t.Helper()
		a, err := webaddr.Parse("--public-url", raw)
		if err != nil {
			t.Fatalf("Parse(%q): %v", raw, err)
		}
		return a
	}
	pinned := webAuthnPlan{
		RP:     auth.WebAuthnRP{ID: "panel.example.com", Origins: []string{"https://panel.example.com"}},
		Source: "OLIVARES_WEBAUTHN_RPID",
	}

	t.Run("a declared unusable address offers no localhost ceremony", func(t *testing.T) {
		got := resolveConsoleAddress(at(t, "https://127.0.0.1:8443"), "127.0.0.1:8443", false).
			withPlan(webAuthnPlan{Unusable: true, Source: "OLIVARES_PUBLIC_URL"})
		// The assertion is about an OFFER, not the word: naming localhost as a name
		// the verifier accepts is a true remedy (declaring it makes the plan
		// usable). Printing a localhost URL to open is the false promise.
		for _, offer := range []string{"https://localhost", "http://localhost"} {
			if strings.Contains(got.Advice, offer) {
				t.Errorf("printed %s to open while the engine refuses every leg with 503:\n%s", offer, got.Advice)
			}
		}
		if !strings.Contains(got.Advice, "503") {
			t.Errorf("did not say what the engine actually does:\n%s", got.Advice)
		}
		if strings.Contains(got.Advice, "will complete") {
			t.Errorf("promised a completion:\n%s", got.Advice)
		}
	})

	t.Run("a pinned relying party offers no localhost and names the pinned origins", func(t *testing.T) {
		got := resolveConsoleAddress(webaddr.Address{}, "127.0.0.1:8443", false).withPlan(pinned)
		if strings.Contains(got.Advice, "localhost") {
			t.Errorf("offered localhost while the verifier's origins exclude it:\n%s", got.Advice)
		}
		if !strings.Contains(got.Advice, "OLIVARES_WEBAUTHN_ORIGINS") {
			t.Errorf("did not point at the configuration that decides:\n%s", got.Advice)
		}
	})

	t.Run("a pinned relying party reached AT a pinned origin says nothing", func(t *testing.T) {
		// The non-firing direction for the row above: when the printed address IS
		// one of the pinned origins there is nothing to warn about, and a paragraph
		// would be noise on a correct deployment.
		got := resolveConsoleAddress(at(t, "https://panel.example.com"), ":8443", false).withPlan(pinned)
		if got.Advice != "" {
			t.Errorf("warned about a correctly pinned deployment:\n%s", got.Advice)
		}
	})

	t.Run("per-request derivation is the one plan where localhost is a real offer", func(t *testing.T) {
		got := resolveConsoleAddress(webaddr.Address{}, "127.0.0.1:8477", false).
			withPlan(webAuthnPlan{Source: "per-request"})
		if !strings.Contains(got.Advice, "https://localhost:8477") {
			t.Errorf("withheld the alternative that does work, on the SERVED port:\n%s", got.Advice)
		}
		if !strings.Contains(got.Advice, "derived from the name") {
			t.Errorf("offered localhost without saying why it works here:\n%s", got.Advice)
		}
	})
}

// C7. THE THREE REASONS A HOST CANNOT BE A RELYING PARTY ARE NOT INTERCHANGEABLE.
//
// The panel gave one sentence — "a single-label host name is not one this build's
// verifier accepts" — and then told the operator to use a dotted host name. For a
// dotted-but-invalid name both halves are false: it already has a dot, this
// build's verifier accepts the string, and what refuses it is the strict-domain
// rule a browser applies. An operator told their only defect is a missing dot
// adds a dot and fails again.
func TestTheRefusalNamesTheRealDefect(t *testing.T) {
	t.Parallel()
	at := func(raw string) webaddr.Address {
		a, err := webaddr.Parse("--public-url", raw)
		if err != nil {
			t.Fatalf("Parse(%q): %v", raw, err)
		}
		return a
	}
	cases := []struct {
		raw     string
		want    string
		mustNot []string
	}{
		{"https://10.0.0.7:8443", "a browser will not run a passkey ceremony at an IP address", nil},
		{"https://olivares", "a single-label host name is one this build's verifier refuses", nil},
		// The rows the review named: dotted, accepted by the verifier, refused by
		// the strict-domain rule. They must NOT be told the defect is a missing dot.
		{"https://my_host.example.com", "not a valid domain", []string{"single-label", "Use a\ndotted host name"}},
		{"https://-lead.example", "not a valid domain", []string{"single-label", "Use a\ndotted host name"}},
		{"https://trail-.example", "not a valid domain", []string{"single-label"}},
	}
	for _, c := range cases {
		addr := at(c.raw)
		for _, plan := range []webAuthnPlan{
			{Source: "per-request"},
			{Unusable: true, Source: "--public-url"},
		} {
			got := resolveConsoleAddress(addr, "127.0.0.1:8443", false).withPlan(plan).Advice
			if !strings.Contains(got, c.want) {
				t.Errorf("%q (unusable=%v): advice does not name the real defect %q:\n%s",
					c.raw, plan.Unusable, c.want, got)
			}
			for _, bad := range c.mustNot {
				if strings.Contains(got, bad) {
					t.Errorf("%q (unusable=%v): advice still says %q:\n%s", c.raw, plan.Unusable, bad, got)
				}
			}
		}
	}
}
