// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package portal

import (
	"encoding/json"
	"html/template"
	"net/http"
	"net/netip"

	"github.com/olivaresai/olivares/appliance/layer/portal/auth"
)

// ConsoleName is the descriptive label of the appliance's management console.
const ConsoleName = "Appliance Console"

// Fact is one status value. Its zero value is unmeasured, and it renders as
// "unmeasured" rather than as a guess.
type Fact struct{ value string }

// Measured returns a fact with an observed value. An empty value stays unmeasured.
func Measured(value string) Fact { return Fact{value: value} }

// String returns the measured value, or "unmeasured".
func (f Fact) String() string {
	if f.value == "" {
		return "unmeasured"
	}
	return f.value
}

// Status is what the console shows. It is built once from measurements and rendered
// without further I/O, so a page never waits on the product or the network.
type Status struct {
	FirstBoot Fact
	Product   Fact
	Channel   Fact
	Custody   Custody
	Firewall  FirewallMeasurement
	Selection Selection
	Listen    Decision
	// SignIn is the sign-in mode in force, which every page states. Its zero value is the
	// refusing mode, so a page that nobody told which door is open says that none is.
	SignIn auth.State
	// Firewalls is the console's firewall read the Firewall page renders; nil renders it unread.
	Firewalls FirewallReads
}

// Snapshot measures the firewall through p and decides the listener. First-boot state,
// product reachability and channel freshness have no reader in this version and stay
// unmeasured. A nil probe is the refusing default. The sign-in mode is decided by the mode
// selector, not here, and the caller states it on the snapshot it serves.
func Snapshot(s Selection, c Custody, p FirewallProbe) Status {
	if p == nil {
		p = NoFirewallProbe{}
	}
	firewall := p.MeasureFirewall()
	return Status{
		Custody:   c,
		Firewall:  firewall,
		Selection: s,
		Listen:    Decide(s, c, firewall),
	}
}

// statusView is what any caller may read: state words, never configuration detail.
type statusView struct {
	Console         string     `json:"console"`
	FirstBoot       string     `json:"first_boot"`
	Fingerprint     string     `json:"certificate_fingerprint_sha256"`
	Firewall        string     `json:"firewall"`
	Product         string     `json:"product"`
	Channel         string     `json:"channel"`
	Listen          Mode       `json:"listen"`
	SignIn          string     `json:"sign_in"`
	SignInStatement string     `json:"sign_in_statement"`
	Local           *localView `json:"local,omitempty"`
}

// localView is the detail a caller on this host also reads.
type localView struct {
	FirewallPolicy       string   `json:"firewall_policy"`
	ManagementInterfaces []string `json:"management_interfaces,omitempty"`
	RemoteWaitsFor       []string `json:"remote_waits_for,omitempty"`
}

func (s Status) view(local bool) statusView {
	v := statusView{
		Console:         ConsoleName,
		FirstBoot:       s.FirstBoot.String(),
		Fingerprint:     Measured(s.Custody.CertificateSHA256).String(),
		Firewall:        s.Firewall.state(),
		Product:         s.Product.String(),
		Channel:         s.Channel.String(),
		Listen:          s.Listen.Mode,
		SignIn:          s.SignIn.Mode.String(),
		SignInStatement: s.SignIn.Statement(),
	}
	if !local {
		return v
	}
	v.Local = &localView{
		FirewallPolicy:       s.Firewall.Policy.String(),
		ManagementInterfaces: s.Selection.ManagementInterfaces,
		RemoteWaitsFor:       s.Listen.Reasons,
	}
	return v
}

var page = template.Must(template.New("status").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>{{.Console}}</title>
</head>
<body>
<h1>{{.Console}}</h1>
<p>Read-only status of this appliance. Nothing on this page changes the appliance.</p>
<p>{{.SignInStatement}}</p>
<dl>
<dt>First boot</dt><dd>{{.FirstBoot}}</dd>
<dt>Certificate fingerprint (SHA-256)</dt><dd>{{.Fingerprint}}</dd>
<dt>Firewall</dt><dd>{{.Firewall}}</dd>
<dt>Product</dt><dd>{{.Product}}</dd>
<dt>Update channel</dt><dd>{{.Channel}}</dd>
<dt>Remote access</dt><dd>{{.Listen}}</dd>
<dt>Sign-in</dt><dd>{{.SignIn}}</dd>
</dl>
{{with .Local}}<h2>Shown on this host only</h2>
<dl>
<dt>Firewall policy</dt><dd>{{.FirewallPolicy}}</dd>
<dt>Management interfaces</dt><dd>{{range $i, $name := .ManagementInterfaces}}{{if $i}}, {{end}}{{$name}}{{else}}none selected{{end}}</dd>
<dt>Remote access waits for</dt><dd>{{range $i, $reason := .RemoteWaitsFor}}{{if $i}}; {{end}}{{$reason}}{{else}}nothing{{end}}</dd>
</dl>
{{end}}</body>
</html>
`))

// NewHandler serves GET / as HTML and GET /status as JSON, the Network page and, to a caller
// on this host, the Firewall page, and nothing else. A caller on a loopback address also reads
// the firewall policy, the selected interfaces and what remote access waits for; any other
// caller reads state words only. Every page states the sign-in mode, to every caller: an
// operator who cannot sign in has to be told which door is open, and the mode is a state word,
// not configuration detail.
func NewHandler(s Status) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /network", serveNetwork(s))
	mux.HandleFunc("/firewall", serveFirewall(s))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = page.Execute(w, s.view(loopbackPeer(r.RemoteAddr)))
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		body, err := json.MarshalIndent(s.view(loopbackPeer(r.RemoteAddr)), "", "  ")
		if err != nil {
			http.Error(w, "status unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(append(body, '\n'))
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("Referrer-Policy", "no-referrer")
		mux.ServeHTTP(w, r)
	})
}

// loopbackPeer reports whether a connection comes from this host. An address that does
// not parse is treated as remote.
func loopbackPeer(remoteAddr string) bool {
	peer, err := netip.ParseAddrPort(remoteAddr)
	return err == nil && peer.Addr().Unmap().IsLoopback()
}
