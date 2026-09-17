// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
)

// The launch-readiness READ, mounted beside the rest of the provider-profile
// plane and under the SAME permission as the profile read it explains.
//
// ⛔ IT IS A POINT READ OF ONE PROFILE, NOT AN INVENTORY. There is deliberately
// no /sessions/runtime roster: a list render must not stat the homes and the
// binaries of every profile on the node, and a reader who only needs one profile
// must not be handed a map of the node's configuration.
//
// ⛔ AND IT GRANTS NOTHING. sessions:profile:read lets a caller see what a launch
// would require; creating a run still needs sessions:run:write and every gate
// behind it. A caller without profile:read is refused BEFORE any filesystem or
// configuration is inspected, by the router, exactly like the profile read.

// launchReadinessQuery is the CLOSED query of this route. Unknown keys, repeated
// keys and malformed syntax are refused with 400 rather than ignored: silently
// dropping a parameter would answer a question nobody asked and let a console
// enable a form on the wrong observation.
type launchReadinessQuery struct {
	transport Transport
	isolation Isolation
}

// parseLaunchReadinessQuery applies the closed grammar. It parses RawQuery
// itself because r.URL.Query() discards malformed pairs silently, and a
// discarded pair here is a different question with the same answer.
func parseLaunchReadinessQuery(raw string) (launchReadinessQuery, error) {
	out := launchReadinessQuery{transport: TransportStreamJSON, isolation: IsolationNative}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return out, badRequest("the query string is malformed")
	}
	for key, vs := range values {
		switch key {
		case "transport", "isolation":
			if len(vs) != 1 {
				return out, badRequest("query parameter " + key + " was given more than once")
			}
		default:
			return out, badRequest("unknown query parameter: only transport and isolation are accepted")
		}
	}
	if v, ok := values["transport"]; ok {
		t := Transport(v[0])
		if !ValidTransport(t) {
			return out, badRequest("transport must be stream-json or remote-control")
		}
		out.transport = t
	}
	if v, ok := values["isolation"]; ok {
		i := Isolation(v[0])
		if !ValidIsolation(i) {
			return out, badRequest("isolation must be native, container or sandbox")
		}
		out.isolation = i
	}
	return out, nil
}

// readinessErrorBody is the ordinary error envelope of this route PLUS the
// contract's stable identifier, and it lives here — beside the one handler that
// writes it — rather than in the shared helper. Widening `errorBody` would add a
// field to every module error at once; this route needs one code, so it declares
// one code.
func readinessErrorBody(code, msg string) map[string]any {
	return map[string]any{"error": map[string]string{"code": code, "message": msg}}
}

// writeReadinessErr serializes this route's errors. A coded one carries its
// identifier; everything else keeps the envelope writeRunErr already produces,
// so no other run error changes shape because this one gained a code.
func writeReadinessErr(w http.ResponseWriter, err error) {
	var coded *launchReadinessErr
	if errors.As(err, &coded) {
		writeJSON(w, coded.err.status, readinessErrorBody(coded.code, coded.err.msg))
		return
	}
	writeRunErr(w, err)
}

// handleProfileLaunchReadiness reports the LOCAL launch requirements observed for one provider profile under one transport and isolation, with the checks this read cannot make left explicitly unknown; it starts nothing, mints nothing and authorizes nothing.
func (m *Module) handleProfileLaunchReadiness(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	// ⛔ SET BEFORE ANY BRANCH, so EVERY answer this handler writes carries it.
	// It used to be set on the last line of the success path only, which meant the
	// 409 that says "this observation was incoherent, read it again" was the one
	// response a cache was free to keep. A dated observation is not a token: it
	// carries no authority, the next filesystem change can falsify it, and a
	// stored copy must never be reused to enable a form — the POST revalidates
	// everything regardless. (401 and 403 are refused by the router before this
	// handler runs and are not this function's to stamp.)
	w.Header().Set("Cache-Control", "no-store")
	q, err := parseLaunchReadinessQuery(r.URL.RawQuery)
	if err != nil {
		writeReadinessErr(w, err)
		return
	}
	out, err := m.EvaluateLaunchReadiness(r.Context(), mc.Tenant, chi.URLParam(r, "ref"), LaunchReadinessSelection{
		Transport: string(q.transport), Isolation: string(q.isolation),
	})
	if err != nil {
		writeReadinessErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
