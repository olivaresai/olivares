// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package hubble

import (
	"strconv"
	"strings"
	"time"

	flow "github.com/cilium/cilium/api/v1/flow"

	"github.com/olivaresai/olivares/connectors/internal/meshobs"
	"github.com/olivaresai/olivares/connectors/internal/tracecontext"
)

// flowToRecord maps one Hubble flow to a meshobs record, or reports ok=false when the
// flow is not worth an observation. To stay signal-dense it KEEPS egress (a resolved
// destination FQDN), L7 flows and ALL denials, and DROPS intra-cluster L3/L4 forwarded
// noise. An ERROR verdict is skipped (it is not an access decision).
func flowToRecord(fl *flow.Flow, now func() time.Time) (meshobs.Record, bool) {
	verdict := fl.GetVerdict()
	if verdict == flow.Verdict_ERROR {
		return meshobs.Record{}, false
	}
	// A DROPPED flow is a policy denial; an AUDIT flow is a would-be denial observed in
	// audit mode (the policy WOULD drop it, audit mode let it through). Both are the
	// permitted-path / anti-evasion signal and must be recorded as a DENIAL, never as an
	// allowed edge — treating AUDIT as allowed would invert the signal (docs/SECURITY-HARDENING.md).
	denied := verdict == flow.Verdict_DROPPED || verdict == flow.Verdict_AUDIT

	hasName := len(fl.GetDestinationNames()) > 0
	http := fl.GetL7().GetHttp() // nil-safe: a nil Layer7 returns a nil HTTP
	if !denied && !hasName && http == nil {
		return meshobs.Record{}, false
	}

	fqdn := ""
	if hasName {
		fqdn = fl.GetDestinationNames()[0]
	}
	if fqdn == "" {
		fqdn = fl.GetIP().GetDestination()
	}
	if fqdn == "" {
		return meshobs.Record{}, false
	}

	rec := meshobs.Record{
		OriginRef:  endpointIdentity(fl.GetSource()),
		FQDN:       fqdn,
		Port:       destPort(fl.GetL4()),
		Source:     SignalHubble,
		Tool:       "hubble.flow",
		ObservedAt: flowTime(fl, now),
	}
	if http != nil {
		rec.Method = http.GetMethod()
		rec.Trace = tracecontext.FromHeaderMap(httpHeaders(http))
	}
	if denied {
		// An egress drop (or audit-mode would-be-drop) is the permitted-path violation
		// / anti-evasion signal. The drop reason rides the (scrubbed, hashed) detail; no
		// taxonomy is asserted — a denied egress has no single honest OWASP/ATLAS mapping
		//.
		rec.Verdict = meshobs.VerdictDenied
		rec.DenyReason = fl.GetDropReasonDesc().String()
	} else {
		rec.Verdict = meshobs.VerdictAllowed
	}
	return rec, true
}

// endpointIdentity renders a Cilium source endpoint as a stable workload reference
// ("namespace/pod"), falling back to the pod, namespace, numeric security identity, or
// empty. It is label-derived (the CNI's view), NOT a cryptographic peer identity —
// meshobs therefore classifies it Approximate, the same honesty as the eBPF backstop.
func endpointIdentity(ep *flow.Endpoint) string {
	if ep == nil {
		return ""
	}
	ns, pod := strings.TrimSpace(ep.GetNamespace()), strings.TrimSpace(ep.GetPodName())
	switch {
	case ns != "" && pod != "":
		return ns + "/" + pod
	case pod != "":
		return pod
	case ns != "":
		return ns
	}
	if id := ep.GetIdentity(); id != 0 {
		return "cilium-identity:" + strconv.Itoa(int(id))
	}
	return ""
}

// destPort returns the destination TCP/UDP port of a flow, or 0 when absent.
func destPort(l4 *flow.Layer4) int {
	if l4 == nil {
		return 0
	}
	if tcp := l4.GetTCP(); tcp != nil {
		return int(tcp.GetDestinationPort())
	}
	if udp := l4.GetUDP(); udp != nil {
		return int(udp.GetDestinationPort())
	}
	return 0
}

// httpHeaders flattens a flow's L7 HTTP headers into a lowercase-keyed map, used only
// to extract the W3C traceparent (never persisted; docs/SECURITY-HARDENING.md).
func httpHeaders(http *flow.HTTP) map[string]string {
	hs := http.GetHeaders()
	if len(hs) == 0 {
		return nil
	}
	m := make(map[string]string, len(hs))
	for _, h := range hs {
		if k := strings.ToLower(h.GetKey()); k != "" {
			m[k] = h.GetValue()
		}
	}
	return m
}

// flowTime returns the flow's timestamp, falling back to the connector clock.
func flowTime(fl *flow.Flow, now func() time.Time) time.Time {
	if ts := fl.GetTime(); ts != nil {
		return ts.AsTime()
	}
	if now != nil {
		return now()
	}
	return time.Now()
}
