// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package firewall

import (
	"html/template"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/appliance/layer/firewall/policy"
)

// Snapshot is what the Firewall page renders: the last measurement read and the last status the
// owner answered. The page performs no read of its own.
type Snapshot struct {
	// SignInStatement is the sign-in mode statement every page shows.
	SignInStatement string
	// ChangesCode is the closed code that says why acts are not available on this page;
	// act_not_adopted when empty.
	ChangesCode string
	// Measurement is the last measurement read, when Reason is "".
	Measurement Measurement
	// Reason is why the firewall is unmeasured, or "".
	Reason string
	// Windows are the windows of the last status answer.
	Windows []Window
}

type pageRow struct{ Port, Where, Note string }

type pageWindow struct{ OperationID, Candidate, State, Reason string }

type pageAct struct{ Verb, Command string }

type pageView struct {
	SignInStatement, ChangesCode string
	Measured                     bool
	Reason                       string
	Digest, BootID, At, Input    string
	Rows                         []pageRow
	Open, Closed                 []pageWindow
	Acts                         []pageAct
}

var firewallPage = template.Must(template.New("firewall").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Firewall</title>
</head>
<body>
<h1>Firewall</h1>
<p>{{.SignInStatement}}</p>
<p>Every change is one operation with its own act authorization, loaded at once and kept only when confirmed before its deadline; without confirmation it reverts, and a reboot loads the confirmed policy. Removing the row that carries your session reverts unless confirmed.</p>
{{if .Measured}}<p>Measured policy {{.Digest}} in boot {{.BootID}} at {{.At}}; input {{.Input}} for what no row admits.</p>
<table>
<thead><tr><th>Port</th><th>Answers on</th><th>Note</th></tr></thead>
<tbody>
{{range .Rows}}<tr><td>{{.Port}}</td><td>{{.Where}}</td><td>{{.Note}}</td></tr>
{{end}}</tbody>
</table>
<p>Always kept: established and related replies, loopback, and IPv6 neighbor and router discovery; invalid packets are dropped, and forwarding admits replies only.</p>
{{else}}<p>The firewall is unmeasured: {{.Reason}}. The console stays on loopback until the owner publishes a measurement of this boot.</p>
{{end}}{{range .Open}}<p>Window {{.OperationID}}: candidate {{.Candidate}} is loaded and not confirmed; it reverts unless confirmed.</p>
{{end}}{{range .Closed}}<p>Window {{.OperationID}}: {{.State}}{{if .Reason}} ({{.Reason}}){{end}}.</p>
{{end}}<h2>Changes</h2>
<ul>
{{range .Acts}}<li>{{.Verb}}: disabled ({{$.ChangesCode}}). Equivalent command: {{.Command}}</li>
{{end}}</ul>
</body>
</html>
`))

// Page serves the Firewall page for s, GET and HEAD only: each measured port and the interfaces
// it answers on, what the policy always keeps, each window, and each act with why it is disabled
// and its equivalent command. It copies s when called and has no form and no act of its own.
func Page(s Snapshot) http.Handler {
	view := pageView{SignInStatement: s.SignInStatement, ChangesCode: s.ChangesCode, Reason: s.Reason}
	if view.ChangesCode == "" {
		view.ChangesCode = CodeActNotAdopted
	}
	m := s.Measurement
	view.Measured = s.Reason == "" && m.SchemaVersion != ""
	if view.Reason == "" && !view.Measured {
		view.Reason = "no measurement was read"
	}
	if view.Measured {
		view.Digest, view.BootID, view.At, view.Input = m.PolicyDigest, m.BootID, m.MeasuredAt, m.InputPolicy
		for _, r := range m.Rows {
			row := pageRow{Port: r.Port, Where: where(r.Interfaces)}
			switch r.Port {
			case policy.ConsolePort:
				row.Note = "The console's row on the management interfaces; removing it reverts unless confirmed."
			case policy.DHCPv6ClientPort:
				row.Note = "Link-local DHCPv6 server replies only."
			}
			view.Rows = append(view.Rows, row)
		}
	}
	acts := map[string]string{VerbApply: "olivares-appliance firewall apply --revert-after <s>", VerbAppRow: "olivares-appliance firewall app-row <app> add|remove"}
	pending := ""
	for _, w := range s.Windows {
		pw := pageWindow{OperationID: w.OperationID, Candidate: w.CandidateDigest, State: w.State, Reason: w.Reason}
		if w.State == WindowPending {
			view.Open = append(view.Open, pw)
			pending = w.OperationID
		} else {
			view.Closed = append(view.Closed, pw)
		}
	}
	operation := "<operation>"
	if pending != "" {
		operation = pending
	}
	acts[VerbConfirm] = "olivares-appliance firewall confirm " + operation
	acts[VerbRevert] = "olivares-appliance firewall revert " + operation
	for _, verb := range []string{VerbApply, VerbConfirm, VerbRevert, VerbAppRow} {
		view.Acts = append(view.Acts, pageAct{Verb: verb, Command: acts[verb]})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = firewallPage.Execute(w, view)
	})
}

// where says which interfaces a row answers on.
func where(interfaces []string) string {
	if len(interfaces) == 1 && interfaces[0] == policy.EveryInterface {
		return "every interface"
	}
	return strings.Join(interfaces, ", ")
}
