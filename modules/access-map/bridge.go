// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package accessmap

import (
	"context"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// Canonical graph origin kinds. They are the subset of the connector
// EdgeObservation.OriginKind vocabulary onto which the bridge maps a raw origin.
const (
	originAgent    = "agent"
	originSession  = "session"
	originIdentity = "identity"
)

// attribution is the outcome of resolving a raw connector observation's origin
// onto a canonical graph origin. It is the heart of module III.
//
// A store's native audit attributes an access to a credential/role, never to an
// agent (ARCHITECTURE.md, docs/contracts). The bridge lifts that credential to
// a discovered agent WHEN — and only when — a per-agent identity makes it
// possible AND that agent resolves UNAMBIGUOUSLY, and otherwise reports the
// access honestly as approximate rather than inventing or guessing an
// attribution the signal does not support (docs/SECURITY-HARDENING.md). "No finge lo que no
// sabe" is the whole point of module III's credibility.
type attribution struct {
	// OriginKind is the canonical kind: "agent", "session" or "identity".
	OriginKind string
	// OriginID is the resolved entity id (zero means "could not attribute").
	OriginID model.ID
	// Confidence is the trust AFTER bridging: attributed only when the access is
	// firmly tied to a single agent/session/identity; approximate when a
	// shared/pooled credential or an ambiguous match makes the agent uncertain.
	Confidence sdkmodel.Confidence
	// Bridged reports whether a store-side credential was lifted to a cooperative
	// agent/session identity (the OTEL↔audit bridge held). It is false for a
	// direct cooperative origin and for an unbridged credential.
	Bridged bool
	// Reason is a short, non-sensitive explanation of the decision, surfaced in
	// the edge metadata and (full module) in least-privilege findings.
	Reason string
	// Tier is the honest per-edge attribution firmness (firm/approximate/unknown),
	// computed from this resolution plus the resource's coverage tier by
	// attributionTier (attribution.go). It is STRICTER than Confidence: it is firm
	// only when a real per-agent identity signal (SVID/WIF/dedicated credential)
	// backs the attribution (G8). Empty until attributionTier runs.
	Tier string
	// TierReason is a short, non-sensitive explanation of the Tier decision.
	TierReason string
}

// resolveOrigin maps a connector observation's raw origin onto a canonical graph
// origin, bridging the per-agent identity gap that decides whether module III
// can honestly attribute a store access to an agent (ARCHITECTURE.md — the PoC #1
// gate). It is deliberately conservative: it NEVER fabricates an agent
// attribution the signal does not support, and it attributes to a single
// agent/session ONLY when that entity resolves unambiguously (exactly one
// match) — a credential or id shared by two entities collapses to approximate.
//
//   - A cooperative origin (Claude Code OTEL/hook, OriginKind "session"/"agent")
//     already names the operational unit and resolves directly.
//   - A store-audit / kernel origin (OriginKind "identity": pgAudit, eBPF, …)
//     names a credential/role and is routed through bridgeIdentity.
//
// It performs lookups against entities OTHER sessions discover — From the
// cooperative stream from identity sources — and never invents an Agent,
// because inventing one would be the very fabrication this guards against. It
// does find-or-create the Session a cooperative edge names (the telemetry
// observed it) and the credential Identity an audit edge names (the raw
// reference the audit already emitted) — neither is an invented attribution.
func resolveOrigin(ctx context.Context, sc store.Scope, edge sdkmodel.EdgeObservation) (attribution, error) {
	ref := edge.OriginRef
	if ref == "" {
		return attribution{}, nil // nothing to attribute — honest skip
	}
	switch edge.OriginKind {
	case originAgent:
		// A connector that already resolved an agent (a future cooperative or
		// runtime resolver). Attribute to it only if exactly one agent carries the
		// external id; 0 or 2+ must not be attributed to one of several candidates.
		id, found, ambiguous, err := agentByExternalID(ctx, sc, ref)
		if err != nil {
			return attribution{}, err
		}
		if found {
			return attribution{OriginKind: originAgent, OriginID: id, Confidence: confOr(edge.Confidence), Reason: "direct agent attribution"}, nil
		}
		if ambiguous {
			return identityAttribution(ctx, sc, ref, sdkmodel.ConfidenceApproximate, "multiple agents share this external id — agent ambiguous")
		}
		return identityAttribution(ctx, sc, ref, confOr(edge.Confidence), "named as agent but not discovered; attributed to credential")

	case originSession:
		// Cooperative session telemetry: the session is the finest non-shared unit
		// of attribution from the cooperative path (ARCHITECTURE.md).
		s, found, ambiguous, err := sessionByExternalID(ctx, sc, ref)
		if err != nil {
			return attribution{}, err
		}
		if ambiguous {
			// Duplicate session rows for one external id: the access IS this session
			// id, but not provably which row — report it, marked approximate.
			return attribution{OriginKind: originSession, OriginID: s.ID, Confidence: sdkmodel.ConfidenceApproximate, Reason: "duplicate session rows — attribution approximate"}, nil
		}
		if !found {
			created, err := sc.Sessions().Create(ctx, model.Session{
				ExternalID: ref, State: model.SessionRunning, Metadata: map[string]any{"discovered_via": "access-map"},
			})
			if err != nil {
				return attribution{}, err
			}
			return attribution{OriginKind: originSession, OriginID: created.ID, Confidence: confOr(edge.Confidence), Reason: "session (newly discovered)"}, nil
		}
		if !s.AgentID.IsZero() {
			return attribution{OriginKind: originAgent, OriginID: s.AgentID, Confidence: confOr(edge.Confidence), Reason: "session → agent"}, nil
		}
		return attribution{OriginKind: originSession, OriginID: s.ID, Confidence: confOr(edge.Confidence), Reason: "session (no agent link yet)"}, nil

	case originIdentity:
		return bridgeIdentity(ctx, sc, edge)

	default:
		// mcp_server and any unknown kind are not a data-access origin for the R/RW
		// map; skip honestly rather than guess (ARCHITECTURE.md).
		return attribution{}, nil
	}
}

// bridgeIdentity resolves a store-audit / kernel credential to a graph origin.
// This is the make-or-break of the PoC: a credential is bridged to a discovered
// agent ONLY when a per-agent identity carries through AND resolves to a single
// agent; it collapses to the credential with approximate confidence (shared
// pool) or attributes to the credential identity with the agent link pending
// (ambiguous/unlinked) — never a faked or guessed agent.
//
// The bridge attempts, in order of directness:
//  1. application_name == a discovered Claude Code session id (the deployment
//     convention that makes attribution possible: the MCP→store connection
//     propagates the session id into application_name; ARCHITECTURE.md — the link to
//     module VI governance). The session must resolve UNAMBIGUOUSLY.
//  2. the credential == a discovered agent's external id (a per-agent token/role),
//     resolving to exactly ONE agent.
//  3. the credential == a discovered identity bound to exactly ONE agent.
//
// If none matches, the access is attributed to the (real, stable) credential
// identity with the agent link reported as pending — true, not guessed.
func bridgeIdentity(ctx context.Context, sc store.Scope, edge sdkmodel.EdgeObservation) (attribution, error) {
	ref := edge.OriginRef

	// The connector already declared this credential shared/pooled
	// (docs/contracts): the agent is ambiguous by construction. Record the
	// identity with approximate confidence; do not pretend to recover an agent.
	if edge.Confidence == sdkmodel.ConfidenceApproximate {
		return identityAttribution(ctx, sc, ref, sdkmodel.ConfidenceApproximate,
			"shared/pooled credential — per-agent attribution unavailable")
	}

	// (1) application_name == a discovered session id (must be unambiguous).
	if s, found, ambiguous, err := sessionByExternalID(ctx, sc, ref); err != nil {
		return attribution{}, err
	} else if found && !ambiguous {
		if !s.AgentID.IsZero() {
			return attribution{OriginKind: originAgent, OriginID: s.AgentID, Confidence: sdkmodel.ConfidenceAttributed, Bridged: true,
				Reason: "bridged: application_name == session id → agent"}, nil
		}
		return attribution{OriginKind: originSession, OriginID: s.ID, Confidence: sdkmodel.ConfidenceAttributed, Bridged: true,
			Reason: "bridged: application_name == session id"}, nil
	}
	// An ambiguous session id does not let us claim an agent — fall through.

	// (2) credential == a discovered agent's external id (must be unambiguous).
	if id, found, _, err := agentByExternalID(ctx, sc, ref); err != nil {
		return attribution{}, err
	} else if found {
		return attribution{OriginKind: originAgent, OriginID: id, Confidence: sdkmodel.ConfidenceAttributed, Bridged: true,
			Reason: "bridged: credential == agent external id"}, nil
	}
	// An ambiguous/absent agent does not let us claim one — fall through.

	// (3) credential == a discovered identity bound to exactly one agent.
	if i, ok, err := findOne(ctx, sc.Identities(), eq("external_id", ref)); err != nil {
		return attribution{}, err
	} else if ok {
		if agentID, ok, err := singleAgentForIdentity(ctx, sc, i.ID); err != nil {
			return attribution{}, err
		} else if ok {
			return attribution{OriginKind: originAgent, OriginID: agentID, Confidence: sdkmodel.ConfidenceAttributed, Bridged: true,
				Reason: "bridged: credential identity → its sole agent"}, nil
		}
		return attribution{OriginKind: originIdentity, OriginID: i.ID, Confidence: sdkmodel.ConfidenceAttributed,
			Reason: "per-agent credential; agent link pending"}, nil
	}

	// (4) per-agent credential not yet linked to any discovered agent. Attribute
	// to the credential identity (real, attributed), agent link pending — true,
	// not guessed.
	return identityAttribution(ctx, sc, ref, sdkmodel.ConfidenceAttributed,
		"per-agent credential not yet linked to a discovered agent")
}

// identityAttribution find-or-creates the credential Identity named by ref and
// returns an attribution to it. Recording the raw reference the audit already
// emitted is not invention; it is the honest floor of attribution.
func identityAttribution(ctx context.Context, sc store.Scope, ref string, conf sdkmodel.Confidence, reason string) (attribution, error) {
	id, err := foIdentity(ctx, sc, ref)
	if err != nil {
		return attribution{}, err
	}
	return attribution{OriginKind: originIdentity, OriginID: id, Confidence: conf, Reason: reason}, nil
}

// agentByExternalID resolves an agent by external id with UNAMBIGUOUS semantics:
// found is true only when exactly one agent carries the id. Zero matches yields
// (found=false, ambiguous=false); two or more yields (found=false,
// ambiguous=true) — never a silent pick of one of several candidates (ARCHITECTURE.md
// §6). The core entity tables carry no DB-level unique index on external_id
// (single-writer model, see inventory/entities.go), so this guard is what
// keeps attribution honest under duplicate rows.
func agentByExternalID(ctx context.Context, sc store.Scope, ref string) (id model.ID, found, ambiguous bool, err error) {
	list, _, err := sc.Agents().List(ctx, model.Query{Filters: []model.Filter{eq("external_id", ref)}, Limit: 2})
	if err != nil {
		return "", false, false, err
	}
	switch len(list) {
	case 0:
		return "", false, false, nil
	case 1:
		return list[0].ID, true, false, nil
	default:
		return "", false, true, nil
	}
}

// sessionByExternalID resolves a session by external id. found is true for one
// or more matches; ambiguous is true for two or more. On ambiguity it returns
// the first row so the caller can still report the (approximate) access, but
// the ambiguous flag forbids claiming a single agent from it.
func sessionByExternalID(ctx context.Context, sc store.Scope, ref string) (s model.Session, found, ambiguous bool, err error) {
	list, _, err := sc.Sessions().List(ctx, model.Query{Filters: []model.Filter{eq("external_id", ref)}, Limit: 2})
	if err != nil {
		return model.Session{}, false, false, err
	}
	switch len(list) {
	case 0:
		return model.Session{}, false, false, nil
	case 1:
		return list[0], true, false, nil
	default:
		return list[0], true, true, nil
	}
}

// singleAgentForIdentity returns the id of the unique agent whose IdentityID is
// identityID, or ok=false when zero or more than one agent shares it — a shared
// identity cannot disambiguate the agent (so it must not be bridged to one).
func singleAgentForIdentity(ctx context.Context, sc store.Scope, identityID model.ID) (model.ID, bool, error) {
	agents, _, err := sc.Agents().List(ctx, model.Query{
		Filters: []model.Filter{eq("identity_id", identityID.String())},
		Limit:   2,
	})
	if err != nil {
		return "", false, err
	}
	if len(agents) == 1 {
		return agents[0].ID, true, nil
	}
	return "", false, nil
}

// confOr defaults an empty/unknown confidence to approximate: an unclassified
// origin is never silently treated as firmly attributed (ARCHITECTURE.md).
func confOr(c sdkmodel.Confidence) sdkmodel.Confidence {
	if c == sdkmodel.ConfidenceAttributed {
		return sdkmodel.ConfidenceAttributed
	}
	return sdkmodel.ConfidenceApproximate
}

// --- lookup / find-or-create helpers (mirrors of the inventory module's, which
// are unexported there; kept minimal and identical in semantics) ---

// findOne returns the first entity matching the AND of filters, or ok=false. The
// engine validates each filter column against the entity descriptor (an unknown
// column is rejected) and binds the value (never interpolated). Use it only for
// existence checks; for attribution-bearing lookups use the *ByExternalID
// helpers, which refuse to pick one of several matches.
func findOne[T any](ctx context.Context, repo store.ReadRepository[T], filters ...model.Filter) (T, bool, error) {
	var zero T
	list, _, err := repo.List(ctx, model.Query{Filters: filters, Limit: 1})
	if err != nil {
		return zero, false, err
	}
	if len(list) == 0 {
		return zero, false, nil
	}
	return list[0], true, nil
}

// foIdentity find-or-creates the credential Identity by external id.
func foIdentity(ctx context.Context, sc store.Scope, externalID string) (model.ID, error) {
	if i, ok, err := findOne(ctx, sc.Identities(), eq("external_id", externalID)); err != nil {
		return "", err
	} else if ok {
		return i.ID, nil
	}
	i, err := sc.Identities().Create(ctx, model.Identity{
		Name:       externalID,
		Kind:       "credential",
		ExternalID: externalID,
	})
	return i.ID, err
}

// foResource find-or-creates the Resource by (kind, uri). The reference is the
// connector's already-redacted natural ref (e.g. "appdb.public.customers"); no
// payload is involved.
func foResource(ctx context.Context, sc store.Scope, kind, uri, name string) (model.ID, error) {
	if r, ok, err := findOne(ctx, sc.Resources(), eq("kind", kind), eq("uri", uri)); err != nil {
		return "", err
	} else if ok {
		return r.ID, nil
	}
	r, err := sc.Resources().Create(ctx, model.Resource{Name: name, Kind: kind, URI: uri})
	return r.ID, err
}

// eq is a shorthand for an equality filter.
func eq(col, val string) model.Filter {
	return model.Filter{Column: col, Op: model.OpEq, Value: val}
}
