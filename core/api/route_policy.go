// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// Per-ROW authorization for list routes (V269 / architecture §7.1).
//
// ⛔ THE WHOLE POINT IS THE ORDER: DECIDE, THEN MATERIALISE. A list handler that builds
// its page and then filters has already computed, over rows the caller may not see, every
// number it is about to return — the total, the "has more", the aggregate. Those numbers
// are the leak: they answer "how many are there that I cannot see", which is precisely the
// question row authorization exists to refuse. Filtering afterwards removes the ROWS and
// keeps the ANSWER.
//
// So the contract is stated in terms a handler cannot satisfy accidentally:
//
//   - candidates are read tenant-pinned in a stable (sort_key, id) order, in batches of at
//     most 4*limit;
//   - ResourceAttrs are built from the STORED row, never from the request;
//   - DecideRows runs before any DTO is constructed;
//   - the cursor advances by the last candidate EXAMINED, not the last one returned, so a
//     page of denials still makes progress instead of looping;
//   - total_count is omitted unless an exhaustive authorized count was actually computed;
//   - and a decision slice of the wrong length DENIES THE PAGE rather than being zipped
//     against the candidates, because a mismatched zip silently pairs each row with its
//     neighbour's verdict.

// # THE INVARIANTS OF ROUTE AUTHORIZATION
//
// Five sentences. Each one is here because it was BROKEN, and the way it broke is written next
// to it: an invariant with no failure attached is a wish, and the next person cannot tell which
// half of it matters.
//
// ⛔ I · THE QUESTION IS principal × permission × action × resource(+Sensitivity).
//
// Not the resource. Authorization answers "may THIS caller do THIS thing to THIS row, under the
// rules in force AT THIS TIME". Any subset of that is a different question with the same shape,
// and the shapes are indistinguishable once the answer is stored.
// The evaluated fact coordinates, outcome and time window bind separately through
// EvidenceDigest. QuestionDigest does not include PolicyVersion or an authorization epoch.
//
// How it broke: ResourceDigest binds tenant and resource, so a witness proves the row it is
// about — and nothing else. An AUTHENTIC witness minted for principal A, permission p, action x
// can be presented as evidence for principal B, permission q, action y over the same row, and
// every check passes. The forgery the seal stops is the easy case; this one needs no forgery.
//
// ⛔ II · THE WITNESS BINDS THE WHOLE QUESTION, OR IT BINDS NOTHING USEFUL.
//
// A digest that covers part of the question is not a weaker binding: it is a binding to a
// DIFFERENT question, and it reads exactly like the strong one. Whatever decides must be inside
// the digest — including Sensitivity, which today changes the answer and is not digested.
//
// And a window needs both ends. A decision with no lower bound is one that was valid "since
// forever", which is not a claim any evaluation can make.
//
// ⛔ III · TIGHTEN IS MONOTONE IN THE DECISION LATTICE, NOT IN THE FIELDS.
//
// An operation belongs in the tightening vocabulary only if it can never move the outcome toward
// ALLOW for any principal. Setting a field to false is not evidence of that.
//
// How it broke, in code written for this very rule: WithoutSessionInheritance turns the session's
// agent-group inheritance OFF, and I justified it as the strict direction because inheriting is
// what widens which GRANTS match. True — and silent about FORBIDS. More groups also make more
// forbids match, so switching inheritance off can REMOVE a forbid that depended on those groups
// (modules/governance/grants.go, the scoped-forbid arm). A widen, inside "tighten-only", argued
// with a sentence that was true about the neighbouring half.
//
// ⛔ IV · ONE REGISTRATION GRAMMAR, OR NONE.
//
// While a route can be registered through a door that does not demand the seal, the seal is an
// option — and an option is absent exactly in the case where it would have mattered, because the
// person who skipped it is the person who did not know they should not.
//
// How it broke: HandleSealed demands a SealedRoute and then delegates to HandlePolicy, which
// still accepts a bare literal. Two grammars, and the permissive one is the older and better
// known.
//
// ⛔ V · THE HTTP EFFECT HAPPENS ONLY AFTER A WITNESS.
//
// A boolean cannot carry the question it answered. Once the answer is `true`, everything I, II
// and III protect has already been discarded, and no invariant of the witness type can reach the
// bytes on the wire.
//
// How it broke: the mounted route authorized through a booleanised path and ModuleContext carried
// no witness, so nothing downstream could check what had been decided — and the type that existed
// to carry it had neither producer nor consumer, which its own comment admitted.
//
// Cured: the governed door calls AuthorizeRoute, the witness travels in
// ModuleContext.Authorization, and the type that only described the mechanism is gone.
//
// ⚠ And the error mapping is part of the invariant, not a detail of it. A denial by POLICY keeps
// writing the error the ROUTE chose, because that is what makes ConcealDeniedAsNotFound answer
// 404 — swapping in the authorizer's typed error would turn that into a 403 and CONFIRM THE ROW
// EXISTS. The third answer, "could not establish", is 503 and is produced BEFORE any row is read,
// identically with conceal and without it: an undecided that differed between the two would be
// the existence oracle the conceal exists to close.

// RouteMetadata is the api-level sealed metadata of a route: the authorization half the
// engine already understands, plus the core entity kind whose loader must resolve the
// resource before the decision.
//
// ⛔ IT EMBEDS auth.RouteMetadata RATHER THAN RESTATING IT. A parallel copy would have to
// be kept in step field by field, and the field most likely to drift is exactly the one
// whose default must never change: SessionInheritsAgentGroups is opt-in because the scope
// resolver is global, and a copy that defaulted it the other way would move authorization
// for every caller in the engine.
type RouteMetadata struct {
	auth.RouteMetadata
	// CoreKind names the core entity a loader must fetch before authorizing, so the
	// decision is taken over the STORED row rather than over anything the request said.
	// CoreKindNone is every route that resolves its own resource.
	CoreKind CoreKind

	// IDParam and BodyIDField say WHERE the entity id is, and without one of them a
	// CoreKind route cannot be served at all (B01 of the 2026-09-03 contrast).
	//
	// ⛔ HandlePolicy BUILT AN EntityRef WITH A KIND AND NO LOCATOR, and the engine
	// requires exactly one of the two before it will look a row up: it denies BEFORE the
	// handler. So every route that declared a CoreKind through this path was mounted and
	// unusable - fourteen of them in the session cockpit - and the failure appeared as an
	// ordinary authorization denial rather than as the wiring fault it is.
	//
	// The declaration is the route's because the route owns its own path: only it knows
	// whether its id arrives as `{core_session_id}` or in a body field, and a locator
	// guessed from the pattern would be a second source for a fact the table already has.
	IDParam     string
	BodyIDField string

	// ResourceKind overrides the authorization resource kind the server would otherwise
	// DERIVE from the permission, and its absence moved authorization rather than
	// presentation.
	//
	// ⛔ WITHOUT IT A TRANSCRIPT ROUTE AUTHORIZES AS `transcript`, AND THE DEPARTMENT FORBID
	// NEVER REACHES IT. With no override the server takes Permission.Resource, which is the
	// permission's penultimate segment; the scope resolver only re-reads the Session and adds
	// its agent groups when the kind is `session` (modules/governance/grants.go). So a route
	// over a session's transcript is decided as a different kind of thing from the session it
	// belongs to - and governance's own test already documents that the department forbid
	// reaches kind=session and does NOT reach the derived transcript
	// (modules/governance/cockpit_policy_test.go). Before this field, the policy door could
	// not express the cure that test requires.
	//
	// Empty keeps the derivation exactly as it was, which is what every route that has not
	// opted in wants.
	ResourceKind string

	// ConcealDeniedAsNotFound makes a denial indistinguishable from a row that does not
	// exist, which is the only way a listing route can refuse without confirming existence.
	//
	// It lives here because the policy door had no way to ask for it: HandleEntity carries
	// the whole EntityRef and forces zero metadata, HandlePolicy carried metadata and built
	// an EntityRef out of three of its seven fields. A governed route needs both halves, and
	// needing both is not the same as being able to say both.
	ConcealDeniedAsNotFound bool
}

// IsZero reports whether the route declared nothing, which is every route that has not
// opted in. Its decision is bit-for-bit what it was before this type existed.
func (m RouteMetadata) IsZero() bool {
	// ⛔ THE TWO NEW FIELDS ARE IN HERE, and leaving them out would have made IsZero lie in a
	// way nothing would catch. A route declaring ResourceKind and nothing else does nothing
	// today - entityRefFor returns nil without a CoreKind - so the omission would have been
	// harmless AND wrong: IsZero would answer "this route declared nothing" about a route that
	// declared something. The next reader to trust that answer for a decision that DOES depend
	// on it inherits a bug with no line to blame.
	return m.RouteMetadata.IsZero() && m.CoreKind == CoreKindNone &&
		m.IDParam == "" && m.BodyIDField == "" &&
		m.ResourceKind == "" && !m.ConcealDeniedAsNotFound
}

// Validate is the boot check. A route whose metadata is impossible must refuse to start,
// not fail at the first request that reaches it.
//
// ⛔ AAL 2 IS REJECTED HERE, and it is not a typo guard: the engine defines AAL1 and AAL3
// only (core/auth/assurance.go), so a route asking for 2 would compare a principal's
// effective 1 or 3 against a level no ceremony can produce — silently denying every AAL1
// caller and admitting every AAL3 one, which is neither of the two things the author
// meant.
func (m RouteMetadata) Validate() error {
	switch m.MinimumAAL {
	case 0, auth.AAL1, auth.AAL3:
	default:
		return fmt.Errorf("api: route declares MinimumAAL %d; the engine defines only 0, %d and %d",
			m.MinimumAAL, auth.AAL1, auth.AAL3)
	}
	if m.RequireScopedGrant && m.CedarAction == "" {
		return fmt.Errorf("api: route requires a scoped grant and declares no Cedar action: " +
			"the grant would be evaluated against an action derived from the permission, " +
			"which is not the action the route means")
	}
	// ⛔ AN UNKNOWN ROLE IS A FLOOR THAT ADMITS EVERYBODY, WHICH IS WHY A TYPO HAS TO STOP THE
	// PROCESS HERE. RoleRank returns 0 for anything it does not know (core/auth/permission.go),
	// and rbacPermitted denies only when RoleRank(theirs) < RoleRank(the floor). So a floor of
	// "editorr" ranks 0, no real role ranks below 0, and the restriction vanishes: the route
	// mounts, serves, and looks exactly like a route that meant to have no floor at all.
	//
	// It fails at BOOT and not at the first request for the same reason the locator check does:
	// this defect wears the shape of an ordinary allow, and nothing downstream can tell a floor
	// that was never declared from one that was declared wrong.
	if m.RBACMinimumRole != "" && !auth.IsRole(m.RBACMinimumRole) {
		return fmt.Errorf("api: route declares RBACMinimumRole %q, which is not a role the engine "+
			"knows; an unknown role ranks 0, so this floor would admit every principal that has "+
			"any role at all - the typo REMOVES a restriction instead of tightening one",
			m.RBACMinimumRole)
	}
	if !m.CoreKind.valid() && m.CoreKind != CoreKindNone {
		return fmt.Errorf("api: route declares an unknown CoreKind %d", m.CoreKind)
	}

	// ⛔ A CoreKind WITHOUT A LOCATOR IS A ROUTE THAT CANNOT BE SERVED, and it is caught at
	// BOOT rather than at the first request. The engine requires exactly one of IDParam and
	// BodyIDField to find the row, so this combination denies every caller - and it does so
	// wearing the shape of an authorization decision, which is the one failure a reader
	// cannot tell from working correctly.
	//
	// Both is refused for the same reason the EntityRef refuses it: two locators are two
	// answers to "which row", and the route means one.
	hasParam, hasBody := m.IDParam != "", m.BodyIDField != ""
	if m.CoreKind != CoreKindNone {
		switch {
		case !hasParam && !hasBody:
			return fmt.Errorf("api: route declares CoreKind %d and no IDParam or BodyIDField; "+
				"the engine cannot locate the row and would deny every caller before the "+
				"handler runs", m.CoreKind)
		case hasParam && hasBody:
			return fmt.Errorf("api: route declares both IDParam %q and BodyIDField %q; a route "+
				"authorizes over ONE row", m.IDParam, m.BodyIDField)
		}
	} else if hasParam || hasBody {
		// A locator with no CoreKind is a declaration nothing reads: the route resolves its
		// own resource, so the engine never looks a row up. Saying so at boot is cheaper
		// than a reader believing the id is being used.
		return fmt.Errorf("api: route declares a locator (IDParam %q, BodyIDField %q) with no "+
			"CoreKind; nothing reads it, because the route resolves its own resource",
			m.IDParam, m.BodyIDField)
	}
	return nil
}

// AuthorizedRowSet is the per-row verdict for one batch of candidates. Allowed[i] and
// Witnesses[i] both describe candidates[i]; the two slices always have the same length as
// the input, and a caller that finds otherwise must deny the page.
type AuthorizedRowSet struct {
	ReadDecisions []auth.RouteReadDecision
	Allowed       []bool
	Witnesses     []auth.RouteAuthorizationWitness
}

// RouteActionRequirement is one action a caller may be allowed to take on a row, with the
// metadata that action's own route declares.
//
// ⛔ THE METADATA COMES FROM THE ACTION'S ROUTE, NOT FROM THE ROUTE BEING SERVED. A read
// route asking "may this caller also stop this session" must evaluate the stop route's
// requirements — AAL3, scoped grant, editor floor — or the answer describes a permission
// nobody has. Computing the button from the read route's own metadata is how a UI offers
// an action the server will refuse.
type RouteActionRequirement struct {
	Permission auth.Permission
	Metadata   RouteMetadata
}

// AuthorizedActionSet is the set of actions a caller may take on one resource, with the
// witness for each.
type AuthorizedActionSet struct {
	Actions   []auth.CedarAction
	Witnesses []auth.RouteAuthorizationWitness
}

// RowAuthorizationPort is what a module handler uses to authorize rows and actions.
type RowAuthorizationPort interface {
	// DecideRows authorizes one permission over many resources in a stable order.
	DecideRows(ctx context.Context, p auth.Principal, tenant model.TenantID,
		perm auth.Permission, meta RouteMetadata, rows []auth.ResourceAttrs) (AuthorizedRowSet, error)
	// DecideActions authorizes many actions over one resource.
	DecideActions(ctx context.Context, p auth.Principal, tenant model.TenantID,
		res auth.ResourceAttrs, reqs []RouteActionRequirement) (AuthorizedActionSet, error)
}

// ErrRouteActionRequirementInvalid is the answer to a requirement this gate will not put a question
// to, and it is deliberately none of the decision answers.
//
// ⛔ THREE THINGS TRIGGER IT, AND THEY DO NOT ALL HAVE THE SAME COUNTERPART AT BOOT — which is
// worth stating, because "the engine would refuse to mount this" is true of two of them and not of
// the third:
//
//  1. The metadata is impossible: a role the engine does not know, an assurance level it does not
//     define. The registrar refuses a route declaring it by PANICKING as it mounts.
//  2. The requirement names a Cedar action the caller's module did not declare. The engine refuses
//     a route declaring it by returning an error that stops the server from starting — an error,
//     not a panic.
//  3. The gate is bound to no module and the requirement names an action at all. Nothing at boot
//     corresponds to this one, because at boot the namespace is never in doubt: the registrar
//     carries it. The remedy is the caller's and it is one line — bind the gate with ForModule and
//     ask again — and it applies even to an action the caller's module DID declare.
//
// ⛔ AND IN NONE OF THE THREE WAS ANYTHING EVALUATED, which is why this is not a decision answer.
// The honest answer names the caller's own bug. Folded into a denial it would refuse somebody no
// policy refused; folded into the undecided it would tell them to retry a request that can never
// succeed — except in case 3, where a retry is right and only after binding.
//
// ⛔ AND IT EXISTS BECAUSE THE TYPO REMOVES A RESTRICTION INSTEAD OF ADDING ONE. An unknown role
// ranks 0 and a floor denies only a caller ranking BELOW it, so a mistyped floor admits every
// caller that has any role at all — an ALLOW wearing the shape of a tightening. That is not a
// failure a caller can be asked to notice, because it looks exactly like working correctly.
var ErrRouteActionRequirementInvalid = errors.New(
	"api: the action's route metadata is not one the engine would mount; nothing was evaluated, " +
		"so this is neither a denial nor an undecided decision")

// RouteActionAuthorizationPort answers ONE question: may this principal take this one action on
// this one resource — and when the answer is no, whether that is a DENIAL or an UNDECIDED.
//
// ⛔ IT IS A SEPARATE INTERFACE AND NOT A METHOD ON RowAuthorizationPort, and the separation buys
// two different things. Adding it there would change the method set of a port that is already
// wired, so every implementer — test doubles included — would stop compiling for a capability
// most of them do not want. And it would seat a GATE's question inside the type whose other
// method is deliberately an OFFER list: the two shapes answer an undecided action differently ON
// PURPOSE (see DecideActions below), and a reader who finds them side by side under one name will
// eventually make one behave like the other.
//
// ⛔ WHAT IT MUST NEVER BE WIDENED TO, written here because the pressure to widen a port arrives
// as a convenience. It returns a witness and never a row. It must not expose the authorizer
// itself, evidence authorization, any policy store, any authorization epoch or compare-and-set,
// any repository or store scope — and it must not mint a principal: a caller has to already hold
// one the engine authenticated. It adds no AUTHORITY: it is AuthorizeRoute with its answer
// preserved, so the same algebra and the same credential ceiling that decide on the governed door
// decide here, and no principal reaches an entity that the same question would not reach there.
//
// ⚠ WHAT IT DOES NOT ESTABLISH IS WHETHER THE CALLER WAS ENTITLED TO ASK THIS QUESTION. The door
// settles that before a route mounts; this gate settles part of it, on trust, and the two limits
// below say which part and what a consumer does about each. So the reach this port adds is bounded
// by the PRINCIPAL's authority and not by the CALLING MODULE's entitlement — a distinction this
// paragraph used to elide, and the whole subject of those two limits.
//
// ⛔ A GATE MUST BE BOUND TO THE MODULE THAT ASKS THROUGH IT BEFORE IT CAN NAME AN ACTION, because
// "the engine would mount this route" is THREE checks, and this gate can run two of them. One asks
// whether the declaration is POSSIBLE — a role the engine knows, an assurance level it defines —
// and is a pure function of the metadata, so this gate runs it on every call. The second asks WHOSE
// action this is: a module may only name Cedar actions it declares itself, and the engine refuses
// to START a server whose governed route breaks that rule. It needs to know who is asking, and it
// is not cosmetic — the action chooses which policy statements match, and it is sealed into the
// witness's question digest, so a foreign one attributes an effect to a decision about somebody
// else's verb. The THIRD is the permission check, and this gate cannot run it at all; it is the
// second of the two limits below.
//
// ⛔ SO AN UNBOUND GATE REFUSES A REQUIREMENT THAT NAMES AN ACTION rather than deciding one it
// cannot attribute, and the value the engine wires is unbound. Bind it with ForModule, an optional
// capability on the concrete type that a caller discovers by a checked assertion — exactly as the
// governed registration door is discovered — so this interface stays at ONE method:
//
//	if binder, ok := port.(interface {
//		ForModule(namespace string) RouteActionAuthorizationPort
//	}); ok {
//		port = binder.ForModule(myNamespace)
//	}
//
// ⚠ AND TWO THINGS THIS GATE CANNOT ESTABLISH, SAID HERE RATHER THAN LEFT TO BE DISCOVERED.
//
// The NAMESPACE is the caller's own assertion. At the door it is the engine's: the registrar carries
// the namespace the module registered under, and a duplicate is refused before anything mounts.
// Here it arrives as an argument, so what this gate catches is MISNAMING — a module asking about an
// action nobody gave it — and NOT a caller presenting another module's namespace. That is not a
// check this gate can make: a cure would have to live where the engine is composed, handing each
// module a gate already bound to its registered namespace, and that wiring is outside this file.
//
// WHAT TO DO: bind only with the namespace your own module registered under, and do not hand the
// UNBOUND value to code that could bind it to a different one — binding is the whole of the
// attribution, so whoever can call ForModule chooses whose actions you may ask about.
//
// The PERMISSION half of the mount check is not run here at all. The engine also refuses to mount a
// module whose route requires a permission that module never declared — and a permission's
// namespace is deliberately NOT required to equal the module's, because route-only modules reuse
// another's on purpose, so nothing in a permission string says who may ask with it. A requirement
// that names NO action therefore still decides, unattributed: with no action, the evaluated action
// IS the permission, and it reaches the policy engine and the witness's question digest as it
// stands.
//
// WHAT TO DO: pass only permissions your own module declared. Nothing here will tell you that you
// did not, and the witness will name the permission you passed as the action that was evaluated.
//
// ⛔ EVERY ANSWER HAS A NAME, AND NO TWO OF THEM MATCH EACH OTHER. This is the whole set a caller
// can receive, with the row of the client contract each belongs to. Read them with errors.Is, in
// any order — every sentinel matches itself and no other — and give each one its own arm: a map
// with fewer arms than the engine has answers falls through to its default, and a default that
// retries serves a refusal as an outage, which is the inverse of the confusion this port cures.
//
//   - nil, with a witness — the decision PERMITTED. 2xx. This is the only answer that is not an
//     error, and the witness is what ties the effect to the decision that allowed it.
//   - auth.ErrStepUpRequired — assurance is missing, not permission. 403 step_up_required; the
//     remedy is to repeat the ceremony and retry, which no other answer shares.
//   - auth.ErrRouteDenied — the policy DENIED. 403 carrying the denial THE ROUTE chose, or 404
//     not_found when that route conceals (see the obligation below). Do not retry.
//   - auth.ErrScopedGrantRequired — denied, and breadth of role is not the remedy: a scoped grant
//     is. The same 403 row as a denial, with a different thing to tell the caller.
//   - auth.ErrRouteUndecided — the decision COULD NOT BE ESTABLISHED. 503
//     route_decision_unavailable with Retry-After: 5; retry, because nobody said no.
//   - auth.ErrAuthorizerUnavailable — there was nothing to ask. The same 503 row: it is not a
//     verdict either, and rendering it as a plain 403 would tell an operator that policy refused
//     something no policy evaluated.
//   - ErrRouteActionRequirementInvalid — the REQUEST is malformed: the declaration is impossible,
//     or it names an action the caller's module did not declare, or it names one at all through a
//     gate bound to no module — and that last one's remedy is to bind with ForModule and ask
//     again. No row of the client contract: the first two are what the engine refuses to start
//     with, the third has no counterpart at boot, and none of the three ever reaches a client. It
//     is the caller's own bug, and it must never be answered 403 or 404.
//
// Two of these are DECISIONS the engine reached (denied, scoped grant required), one is a
// precondition of authentication (step-up), two are refusals to decide (undecided, no authorizer),
// and the last is a refusal to ask at all.
//
// ⛔ CONCEALMENT IS THE CALLER'S OBLIGATION, AND IT IS ONE SENTENCE: when the requirement you
// passed carried ConcealDeniedAsNotFound, a denial — EITHER ErrRouteDenied OR
// ErrScopedGrantRequired, because the answer set has two of them — MUST be served as the denial
// that route chose, 404 not_found, and never as the typed error's own status, because a 403 where
// the route answers 404 confirms the row exists, which is the single thing concealment prevents.
//
// ⚠ BOTH, BECAUSE ONE ARM PER SENTINEL IS WHAT THIS DOCUMENT ASKS FOR. A consumer told about
// concealment under the ordinary denial alone conceals that one as 404 and answers the scoped-grant
// one 403 — same route, same row, and the arm nobody mentioned is the one that confirms it exists.
// The governed door makes no such distinction: both land in the arm that writes the route's own
// denial.
//
// The port cannot do it for you, and must not: its answer is identical with and without that
// field, which is pinned by a case, because an answer that varied with it would itself be the
// existence oracle — provoke the variation and you have separated "no such row" from "not yours".
// The field you need is the one you supplied, so nothing is withheld and nothing is missing.
//
// ⚠ NOTHING IN THIS REPOSITORY CALLS IT YET, said here rather than left to be discovered: it is
// the seam a module outside this tree needs in order to gate one act on one row, and its consumer
// arrives with that module — until then this package's own cases are the only thing measuring it,
// which is why they enumerate the whole answer set instead of the happy path.
//
// ⚠ AND IT MIRRORS THE GOVERNED DOOR, NOT THE COMPLETE-READ PATH. The value the engine wires as
// the row port overrides DecideRows to demand complete authority of a human reader; this gate
// deliberately does not follow that sibling, because the question it answers is the DOOR's — one
// action, one row, one witness — and a gate that quietly took a different path from the door it
// claims to mirror would be a second authorization flow, which is how two flows drift until the
// one fewer people read is the one that admits.
type RouteActionAuthorizationPort interface {
	// AuthorizeAction decides ONE action over ONE resource, returning the witness of that
	// decision or the error that names which of the other answers this is.
	AuthorizeAction(ctx context.Context, p auth.Principal, tenant model.TenantID,
		res auth.ResourceAttrs, req RouteActionRequirement) (auth.RouteAuthorizationWitness, error)
}

// ⛔ RouteSecurityContext ESTUVO AQUI Y SE HA RETIRADO, que es la unica respuesta honesta a un
// tipo con dos ocurrencias en todo el arbol: su comentario y su declaracion. Nadie lo construia,
// nadie lo leia, ningun test lo tocaba — y su primera frase decia, en presente, que era "lo que
// una ruta gobernada entrega a su handler". Un lector que buscara como llega el testigo a un
// handler lo encontraba, creia que el cableado estaba, y se iba a buscar el fallo a otra parte.
//
// Lo que hace ese trabajo ahora es `ModuleContext.Authorization`, que existe de verdad: lo rellena
// la puerta gobernada y lo recibe el handler. Un tipo que describe un mecanismo inexistente es
// peor que su ausencia, porque la ausencia no miente.

// AuthenticationFacts is the subset of authentication a handler may rely on.
//
// ⛔ AuthenticatedAt IS NOT HERE, AND ITS ABSENCE IS DELIBERATE RATHER THAN AN OVERSIGHT.
// The architecture's §7.5 wants "the AAL3 ceremony happened less than five minutes ago",
// and the engine cannot answer that today: model.AuthSession records AAL and AALExpiresAt
// but not the instant of the ceremony, and §7.1 explicitly forbids reconstructing that
// instant by subtracting the window — a legacy row would yield an invented time that
// looks exactly like a measured one. Publishing a zero timestamp in a struct called
// "AuthenticationFacts" is worse than omitting it: every caller that forgets to check for
// zero reads it as "authenticated at the epoch", which is always older than five minutes
// and denies, or, with the comparison written the other way, always passes.
//
// What the engine CAN establish is the effective AAL, which effectiveAAL already degrades
// to AAL1 when the elevation window has closed, and that is what routes enforce. The
// five-minute freshness lands when model.AuthSession gains a nullable AALAuthenticatedAt
// that the elevation ceremony writes in the same mutation.
type AuthenticationFacts struct {
	// Credential is the exact credential reference, absent for synthetic principals.
	Credential auth.PrincipalRef
	// HasCredential reports whether Credential was established.
	HasCredential bool
	// AAL is the EFFECTIVE assurance level, already degraded if the window closed.
	AAL int
}

// AuthenticationFactsOf reads what the engine can establish about a principal.
func AuthenticationFactsOf(p auth.Principal) AuthenticationFacts {
	ref, ok := p.Ref()
	return AuthenticationFacts{Credential: ref, HasCredential: ok, AAL: p.AAL}
}

// ErrRowDecisionMismatch is returned when a row port answers with a different number of
// decisions than it was asked about.
var ErrRowDecisionMismatch = errors.New("api: the row authorizer returned a different number of decisions than rows; the page is denied rather than zipped")

// ErrRowWitnessUnverified is the CONTENT half of the same guard, and it exists because the
// arithmetic half cannot see it: a set of the right length whose witnesses do not hold up.
//
// ⛔ IT WAS CALLED ErrRowWitnessMismatch, AND THE NAME OUTLIVED ITS MEANING BY ONE COMMIT. It
// began as "this witness is about a different row", which is what comparing ResourceDigest by
// hand could prove. The guard now asks auth's whole question - minted by the authorizer, not
// edited since, still inside its window, and about THIS row - so "mismatch" named one of four
// terms and would have sent the next reader looking for a pairing bug when the witness was
// simply stale, or forged.
// ErrRowQuestionMismatch separates "this witness answers a DIFFERENT question" from "this
// witness does not hold up", and the split exists because one comparison used to report both as
// tampering.
//
// ⛔ ITS MESSAGE NAMES THE OBSERVATION AND BOTH READINGS, AND PICKS NEITHER. From inside
// CheckRowSet the two causes are INDISTINGUISHABLE: a caller verifying with a different Route and
// a witness for another question landing in this slot produce the same different byte. Asserting
// either would be a failure message asserting a cause — believed as readily as a comment, and as
// prone to ageing — sending a reader to hunt an attacker who is not there, or excusing one who is.
//
// ⛔ AND IT IS 500-CLASS UNDER BOTH READINGS, which is what makes the class defensible without
// knowing which happened: an AuthorizedRowSet is produced and consumed IN THE SAME PROCESS, so
// neither reading is a client asking for something it may not have. A 403 would be a lie in both
// cases; a 404 would leak in both.
//
// Telling the two apart needs the question the decision was MADE with, which the set does not
// carry yet. Stored, the comparison moves out of the loop and each cause gets its own verdict.
// That is a separate row: it changes the type the overlay consumes, and that consumer cannot
// compile against the current pin.
// ⚠ Y SU RAMA NO SE ALCANZA TODAVÍA, dicho aquí para que nadie la cuente como cubierta. Llegar a
// ella exige un testigo SÓLIDO —acuñado, en ventana, sin editar— y `minted` no se exporta: sólo
// AuthorizeRoute lo pone, y sólo acuña para un principal con evidencia sellada, que hoy no instala
// nada en el camino HTTP. ⇒ Cualquier caso escrito en este paquete toma la rama `!IsSound` y no
// llega aquí. El estado que este error nombra —sólido y sobre otra pregunta— se afirma donde SÍ se
// puede construir (`core/auth`, TestASoundWitnessCanStillAnswerAnotherQuestion); lo que falta es el
// MAPEO de ese estado a este error, y su testigo va en el carril que cablea la evidencia, que es
// cuando esta rama pasa a ser alcanzable en producción por primera vez.
var ErrRowQuestionMismatch = errors.New(
	"api: a row's authorization witness is authentic and answers ANOTHER question: either the " +
		"question it is verified with is not the one that decided, or a witness for another " +
		"question reached this position")

var ErrRowWitnessUnverified = errors.New("api: a row's authorization witness does not verify for that row; the page is denied rather than zipped")

// authorizerRows implements RowAuthorizationPort over the core Authorizer.
//
// ⛔ ns IS THE MODULE THAT ASKS THROUGH THE ACTION GATE, AND IT IS EMPTY IN THE VALUE THE ENGINE
// WIRES. That value is built once for the whole engine and knows no module, so it cannot answer
// "may this caller name this action" — a question the engine answers per module and refuses a boot
// over. An empty ns is therefore UNATTRIBUTABLE, and the gate refuses a named action instead of
// deciding one it cannot attribute. See ForModule.
type authorizerRows struct {
	az *auth.Authorizer
	ns string
}

// NewRowAuthorizationPort wires the row port to an Authorizer. A nil authorizer produces
// a port that denies every row and says so, rather than one that allows them: a route
// whose row port was never installed must not serve rows.
// The value it returns is bound to no module, so the action gate reached from it by assertion
// refuses a requirement that names a Cedar action until ForModule binds it.
func NewRowAuthorizationPort(az *auth.Authorizer) RowAuthorizationPort { return authorizerRows{az: az} }

// ForModule returns this gate bound to the namespace of the module that will ask through it, so it
// can run the half of the mount check that asks WHOSE action a requirement names.
//
// ⛔ IT IS ON THE CONCRETE TYPE AND NOT ON THE PORT, which is this repository's idiom for an
// optional capability — the governed registration door is discovered the same way. Adding it to the
// interface would turn a one-method contract a third party can implement today (a test double, a
// recording wrapper) into a two-method one it cannot. A caller that finds it absent must NOT fall
// back to the unbound gate for a named action: the unbound gate is precisely the one that cannot
// answer the question.
//
// ⛔ AND AN EMPTY NAMESPACE BINDS NOTHING, deliberately rather than by omission. No module registers
// under one, so it declares no action, and a gate bound to it refuses every named action exactly as
// an unbound gate does. That is the deny-closed direction, and it is the same direction the
// engine's own check takes for a module that never registered anything.
//
// ⚠ THE NAMESPACE IS TAKEN ON TRUST, because this method cannot do otherwise: it is an argument
// and not something the engine handed over. So binding catches a module asking about an action
// nobody gave it, and does NOT catch a caller presenting another module's namespace. WHAT TO DO:
// pass the namespace your own module registered under and no other, and keep the UNBOUND value out
// of the hands of code that could bind it elsewhere. The port's own documentation carries the rest
// of this limit, and the cure would belong to whatever composes the engine.
//
// ⛔ AND WHAT IT RETURNS CARRIES THE GATE AND NOTHING ELSE, which is the reason for the extra type.
// The value this method is promoted onto is a ROW port whose own DecideRows refuses a human read
// without complete authority; handing that value back would give a caller who asked for something
// NARROWER a row port with the override shed, one assertion away — and the consequence of using it
// is a page served, not an error raised. "They differ only in DecideRows" is true of the interface
// and would have been false of the value.
func (a authorizerRows) ForModule(namespace string) RouteActionAuthorizationPort {
	a.ns = namespace
	return boundRouteActionGate{rows: a}
}

// boundRouteActionGate is a gate bound to one module's namespace, and nothing else.
//
// ⛔ IT HOLDS THE ROW VALUE IN A NAMED FIELD RATHER THAN EMBEDDING IT, and that is the whole type.
// Embedding PROMOTES, so DecideRows and DecideActions would come with it and this value would
// answer the row port again — the exact widening binding exists to avoid. A field forwards one
// method and no more, and a method added to the row value tomorrow cannot arrive here by accident.
type boundRouteActionGate struct{ rows authorizerRows }

// AuthorizeAction decides one action over one resource for the module this gate is bound to. It is
// the only question this value answers.
func (g boundRouteActionGate) AuthorizeAction(ctx context.Context, p auth.Principal,
	tenant model.TenantID, res auth.ResourceAttrs, req RouteActionRequirement,
) (auth.RouteAuthorizationWitness, error) {
	return g.rows.AuthorizeAction(ctx, p, tenant, res, req)
}

func (a authorizerRows) DecideRows(ctx context.Context, p auth.Principal, tenant model.TenantID,
	perm auth.Permission, meta RouteMetadata, rows []auth.ResourceAttrs) (AuthorizedRowSet, error) {
	if a.az == nil {
		return AuthorizedRowSet{}, fmt.Errorf("api: no authorizer is installed for row decisions: %w",
			auth.ErrAuthorizerUnavailable)
	}
	out := AuthorizedRowSet{
		Allowed:   make([]bool, len(rows)),
		Witnesses: make([]auth.RouteAuthorizationWitness, len(rows)),
	}
	for i, r := range rows {
		// Each row is its OWN request. Reusing one decision across rows is the defect
		// this port exists to prevent: two rows of the same kind can live in different
		// workspaces and belong to different agent groups.
		w, err := a.az.AuthorizeRoute(ctx, auth.Request{
			Principal:  p,
			Permission: perm,
			Tenant:     tenant,
			Resource:   r,
			Route:      meta.RouteMetadata,
		})
		switch {
		case err == nil:
			out.Allowed[i] = true
			out.Witnesses[i] = w
		case errors.Is(err, auth.ErrRouteDenied), errors.Is(err, auth.ErrScopedGrantRequired),
			errors.Is(err, auth.ErrStepUpRequired):
			// A denial is a per-row answer and the page continues: the caller simply
			// does not see this row.
			out.Allowed[i] = false
		default:
			// ⛔ AN UNDECIDED ROW FAILS THE WHOLE PAGE, not just that row. Serving the
			// rest would publish a page whose completeness nobody established, and the
			// caller cannot tell a page missing a row it may not see from a page missing
			// a row nobody could decide about.
			return AuthorizedRowSet{}, fmt.Errorf("api: row %d could not be decided: %w", i, err)
		}
	}
	return out, nil
}

func (a authorizerRows) DecideActions(ctx context.Context, p auth.Principal, tenant model.TenantID,
	res auth.ResourceAttrs, reqs []RouteActionRequirement) (AuthorizedActionSet, error) {
	if a.az == nil {
		return AuthorizedActionSet{}, fmt.Errorf("api: no authorizer is installed for action decisions: %w",
			auth.ErrAuthorizerUnavailable)
	}
	var out AuthorizedActionSet
	for _, req := range reqs {
		w, err := a.az.AuthorizeRoute(ctx, auth.Request{
			Principal:  p,
			Permission: req.Permission,
			Tenant:     tenant,
			Resource:   res,
			Route:      req.Metadata.RouteMetadata,
		})
		if err != nil {
			// An action that cannot be decided is simply NOT offered. Unlike a row, an
			// absent action is not a claim about the world: it is the safe default, and
			// the caller can still attempt it and receive the real answer.
			continue
		}
		out.Actions = append(out.Actions, w.CedarAction)
		out.Witnesses = append(out.Witnesses, w)
	}
	return out, nil
}

// AuthorizeAction makes the already-wired row port answer the GATE's question, and it asks exactly
// what the governed door asks: the auth.Request built here is field for field the one the door
// builds for a governed route, carrying the ACTION's own route metadata rather than the metadata
// of whatever route is being served.
//
// ⛔ TWO GATES BEFORE IT, AND THE DECISION'S ANSWER UNTOUCHED AFTER. They are two of the THREE
// checks the engine makes before it will mount a route, run in the engine's own order — whose
// action is this, then is this declaration possible — and both are the caller's bug rather than a
// decision. The third, whether the caller's module declared the PERMISSION, cannot be run here and
// is stated as a limit on the port. Past these two, whatever AuthorizeRoute answered comes back.
// Wrapping that answer with anything that does not carry %w, or folding every non-nil error into a
// denial, turns "nothing evaluated this" into "the policy said no": a retryable outage leaves as a
// final refusal, and the caller that would have succeeded on the second attempt gives up on the
// first.
//
// ⛔ AND IT DOES NOT RE-DERIVE THE DECISION. One place decides. Asking it twice, or asking a
// cheaper question and calling the answer the same, is how two authorization paths drift until the
// one fewer people read is the one that admits.
func (a authorizerRows) AuthorizeAction(ctx context.Context, p auth.Principal, tenant model.TenantID,
	res auth.ResourceAttrs, req RouteActionRequirement) (auth.RouteAuthorizationWitness, error) {
	// ⛔ WHOSE ACTION IS THIS — the half of the mount check that is NOT a function of the metadata,
	// and the half this gate used to skip. A module may only name Cedar actions it declares itself,
	// and the engine refuses to START a server whose governed route names another module's; the
	// rule is per module and not a global set, because a flat "does anybody declare this" would let
	// a route name a foreign action while its owner happens to be loaded and stop working the day
	// it is not — the hidden dependency the engine's check exists to turn into a boot failure.
	//
	// ⛔ AND IT IS NOT COSMETIC: THE ACTION CHOOSES WHOSE POLICY STATEMENTS ANSWER. It travels into
	// the request, the engine matches statements written about THAT action, and it is sealed into
	// the witness's question digest — so a foreign one attributes an effect to a decision taken
	// about somebody else's verb, on a route the engine would refuse to start with.
	//
	// An EMPTY action is left alone, because the door skips ITS action check for a route that
	// declares none — but that is not parity, and the port's documentation says so in terms. The
	// door also refuses to mount a module whose route requires a permission that module never
	// declared, and this gate is handed a permission with no declaration to compare it against.
	// With no action named the permission IS the evaluated action, so it reaches the policy engine
	// and the witness's question digest unattributed.
	if action := req.Metadata.CedarAction; action != "" {
		if a.ns == "" {
			return auth.RouteAuthorizationWitness{}, fmt.Errorf(
				"%w: the requirement names Cedar action %q and this gate is bound to no module, so "+
					"nothing can establish that its caller may name it; bind the gate with "+
					"ForModule before asking about a named action",
				ErrRouteActionRequirementInvalid, action)
		}
		if !auth.ModuleDeclaresAction(a.ns, auth.CedarAction(action)) {
			return auth.RouteAuthorizationWitness{}, fmt.Errorf(
				"%w: the requirement names Cedar action %q, which module %q does not declare; a "+
					"module may only name actions it declares itself",
				ErrRouteActionRequirementInvalid, action, a.ns)
		}
	}
	// ⛔ AND IS THE DECLARATION POSSIBLE AT ALL, which is another of the door's checks: the
	// registrar validates a route's metadata at MOUNT and refuses to start on one it will not
	// serve, long before any request exists. A gate handed a LITERAL has no boot to do that at, so
	// it does it here — SECOND, after the attribution above, which is the order the engine itself
	// uses. Neither ordering changes an answer: both refusals carry the same sentinel, and either
	// way an impossible declaration is refused BEFORE the authorizer and cannot be decided by
	// accident. What still reaches the authorizer is a permission the engine would not mount for
	// this module, because that is the check no gate here can run — the second limit on the port.
	//
	// ⛔ BECAUSE WHAT THIS CATCHES IS AN ALLOW, NOT A DENIAL. A role floor the engine does not know
	// ranks 0, and a floor denies only a caller ranking BELOW it, so one mistyped character admits
	// every caller that has any role: the typo REMOVES the restriction it was written to add, and
	// the gate hands back a minted witness. An assurance floor outside the levels the engine
	// defines is the same family — no ceremony can produce it, so it denies everyone below and
	// admits everyone above.
	//
	// ⛔ AND ITS ANSWER IS NOT A DECISION ANSWER. errors.Is must not match a denial or an
	// undecided here: nothing was evaluated, and the two sentinels that mean "the engine looked"
	// would both be a lie. The validation error travels inside it so the caller is told WHICH
	// field of its own declaration is impossible.
	if err := req.Metadata.Validate(); err != nil {
		return auth.RouteAuthorizationWitness{}, fmt.Errorf(
			"%w: %w", ErrRouteActionRequirementInvalid, err)
	}
	if a.az == nil {
		// Stated here rather than inherited from the authorizer's own nil check, so this port's
		// refusals are readable in this method: a port that was never installed answers "I could
		// not look", which is a refusal to decide and never a refusal of the caller.
		return auth.RouteAuthorizationWitness{}, fmt.Errorf(
			"api: no authorizer is installed for action decisions: %w", auth.ErrAuthorizerUnavailable)
	}
	return a.az.AuthorizeRoute(ctx, auth.Request{
		Principal:  p,
		Permission: req.Permission,
		Tenant:     tenant,
		Resource:   res,
		Route:      req.Metadata.RouteMetadata,
	})
}

// CheckRowSet verifies what a caller must never assume before zipping decisions against
// candidates: that there is one verdict and one witness per row, AND that each witness is
// about the row it sits beside.
//
// ⛔ IT TAKES THE ROWS AND NOT THEIR COUNT, AND THAT IS THE WHOLE FIX. RouteAuthorization-
// Witness documents ResourceDigest as binding a witness to its resource "so a witness cannot
// be carried from one row to its neighbour", and the digest genuinely does change per row -
// its own test proves that by mutating the resource and demanding a different digest. But a
// digest that changes is a binding only if somebody COMPARES it, and nobody did: this
// function could see a COUNT, so the strongest thing it could say was arithmetic. A row set
// whose witnesses were correct answers about the WRONG rows passed it with the right length.
// The protection existed in a comment and in a field, and nowhere else.
//
// Only ALLOWED rows are compared. A denial leaves its witness at the zero value by
// construction (see DecideRows), so demanding a digest there would deny every page that
// contains a row the caller may not see, which is nearly all of them. A zero witness on an
// ALLOWED row is still caught, because a digest over length-prefixed input is never the zero
// array - the check does not need a separate term for it, and this sentence is here so the
// next reader does not add one believing it is missing.
// ⛔ TOMA LA PREGUNTA, NO EL TENANT. Con `tenant` sólo se podía preguntar «¿es sobre esta fila?»,
// y un testigo AUTÉNTICO de otro principal, con otro permiso, sobre esa misma fila respondía que
// sí a todo lo demás. La pregunta viaja entera y el recurso de cada fila se pone encima.
//
// ⛔ Y EL CONTRATO QUE ESO CREA HAY QUE ENUNCIARLO, PORQUE SU INCUMPLIMIENTO SE DISFRAZA DE
// ATAQUE. La pregunta que se pasa aquí tiene que ser LA MISMA con la que DecideRows decidió, en
// los SEIS términos que el digest ata: identidad del principal (Actor), su referencia de
// credencial, el permiso, la acción resuelta de la Route, el tenant y el recurso. El recurso es
// el único que varía —lo pone esta función, fila a fila—; los otros cinco tiene que escribirlos
// igual quien llame.
//
// Hoy esa coincidencia no la garantiza nada: DecideRows construye su Request y el llamante
// construye otro, en paquetes distintos, y el compilador no compara listas de campos. Si difieren,
// FALLAN TODAS las filas y lo que sale se lee como manipulación de testigos. Por eso el desajuste
// de pregunta tiene error propio (ErrRowQuestionMismatch) y no se confunde con el de una fila que
// no verifica; y por eso la cura de verdad —que el conjunto RECUERDE la pregunta con la que se
// decidió, en vez de que el otro extremo la reconstruya— es una fila con dueño y no una nota:
// un valor que viaja no se puede escribir distinto en el otro extremo.
func CheckRowSet(set AuthorizedRowSet, now time.Time, req auth.Request, rows []auth.ResourceAttrs) error {
	if len(set.Allowed) != len(rows) || len(set.Witnesses) != len(rows) {
		return fmt.Errorf("%w: %d rows, %d verdicts, %d witnesses",
			ErrRowDecisionMismatch, len(rows), len(set.Allowed), len(set.Witnesses))
	}
	for i := range rows {
		if !set.Allowed[i] {
			continue
		}
		// ⛔ auth's WHOLE QUESTION, ASKED ONCE, INSTEAD OF THE ONE TERM THIS PACKAGE COULD
		// COMPARE BY HAND. Comparing ResourceDigest here proved "it is about this row" and
		// nothing else: a witness nobody minted, one minted and then edited, and one whose
		// window closed an hour ago all carry the right digest and used to pass. VerifyFor is
		// where the four terms live together, and keeping them there is what stops this
		// package from re-deriving three of them and getting one wrong.
		q := req
		q.Resource = rows[i]
		// ⛔ SOUNDNESS FIRST, THE QUESTION SECOND, AND THE ORDER WAS A DEFECT BEFORE IT WAS A
		// RULE. Asking about the question first reported a witness NOBODY MINTED as "authentic
		// and about something else" — its QuestionDigest is zero, so of course it matches
		// nothing — and "authentic" is precisely what a hand-typed value is not. A witness that
		// does not hold up answers no question at all, so that is the prior question, and a
		// pre-existing case (TestAWitnessThisPackageTypedIsRefused) is what said so.
		if !set.Witnesses[i].IsSound(now) {
			// ⛔ THE POSITION AND THE KIND, NOT THE ID, AND THE ABSENCE IS DELIBERATE. This
			// error travels wrapped in the module's ErrPageUndecided, and every message in
			// that family names COUNTS - never an identifier. The index already says which
			// candidate of the batch failed, which is what a reader debugging this needs;
			// the id would be the only part that could carry a resource name outward.
			return fmt.Errorf("%w: candidate %d of kind %q", ErrRowWitnessUnverified, i, rows[i].Kind)
		}
		// And only now: sound, and about something else. If the caller verifies with a question
		// other than the one that decided, EVERY row lands here — which is why it gets its own
		// error instead of being buried under N identical symptoms, where a reader looks for
		// what the rows have in common and that is exactly where the cause is not.
		if !set.Witnesses[i].AnswersQuestion(q) {
			return fmt.Errorf("%w: candidate %d of kind %q", ErrRowQuestionMismatch, i, rows[i].Kind)
		}
	}
	return nil
}

// SealedRoute is route metadata that has been through SealRoute and can only be tightened
// afterwards. It is what HandleSealed accepts, and a composite literal is not one.
//
// ⛔ EMBEDDING MADE THE SEALED FIELDS ASSIGNABLE, WHICH IS THE HOLE THIS CLOSES. api.RouteMetadata
// embeds auth.RouteMetadata, and embedding PROMOTES its exported fields: any code in this package
// could write MinimumAAL back to 0, RequireScopedGrant to false, or SessionInheritsAgentGroups to
// true, and the route would mount looking exactly like one that had asked for less. The type said
// "sealed" in its doc and the compiler said nothing.
//
// The value here is unexported and there is no setter. What exists is Tighten, and every
// tightening it accepts moves in ONE direction: there is no operation that lowers a floor, so the
// dangerous edit is not something a caller has to remember not to write — it is something they
// cannot spell.
type SealedRoute struct {
	m      RouteMetadata
	sealed bool
}

// SealRoute validates the metadata and seals it. It is the only constructor.
//
// The validation happens HERE and not at the door for a reason: a route that cannot be described
// should fail where it is described, next to the declaration a human wrote, rather than three
// frames later inside a registrar that can only say "some route in this module".
func SealRoute(m RouteMetadata) (SealedRoute, error) {
	if err := m.Validate(); err != nil {
		return SealedRoute{}, err
	}
	return SealedRoute{m: m, sealed: true}, nil
}

// Metadata returns the sealed value. It is a copy: the caller may read it and may not change
// what the route was registered with.
func (s SealedRoute) Metadata() RouteMetadata { return s.m }

// IsSealed reports whether this value came from SealRoute. The zero SealedRoute is NOT sealed,
// and that matters because the zero RouteMetadata inside it is perfectly legal metadata — it is
// what every route that has not opted in declares. Without this flag a zero SealedRoute would be
// indistinguishable from a deliberately unrestricted route that had been through the constructor.
func (s SealedRoute) IsSealed() bool { return s.sealed }

// Tightening is one monotone change to a sealed route.
//
// ⛔ THERE IS DELIBERATELY NO OPERATION FOR THE OTHER DIRECTION. A Tighten that took a whole
// RouteMetadata and compared field by field would be a check a future field could forget to join;
// a set of named operations is a vocabulary in which loosening has no word. The cost is that
// adding a monotone field means adding its operation, and that cost is the point: it makes
// widening a decision somebody writes down.
type Tightening struct {
	name  string
	apply func(*RouteMetadata) error
}

// RaiseMinimumAAL raises the assurance floor. Lowering it is refused, including to zero.
func RaiseMinimumAAL(aal int) Tightening {
	return Tightening{name: "RaiseMinimumAAL", apply: func(m *RouteMetadata) error {
		if aal < m.MinimumAAL {
			return fmt.Errorf("api: RaiseMinimumAAL(%d) on a route whose floor is already %d would "+
				"LOWER it; a sealed route only tightens", aal, m.MinimumAAL)
		}
		m.MinimumAAL = aal
		return nil
	}}
}

// RequireScopedGrant fixes the RBAC term to false for this route. There is no operation that
// unfixes it: breadth of role does not become sufficient again once a route has said it is not.
func RequireScopedGrant() Tightening {
	return Tightening{name: "RequireScopedGrant", apply: func(m *RouteMetadata) error {
		m.RequireScopedGrant = true
		return nil
	}}
}

// ⛔ WithoutSessionInheritance IS GONE FROM THE VOCABULARY, AND ITS ABSENCE IS THE POINT.
//
// It used to be here, justified like this: "turning the session's agent-group inheritance OFF is
// the strict direction, because the resolver is global and inheriting is what WIDENS who reaches
// a row." That sentence is TRUE ABOUT GRANTS and says nothing about forbids — and more groups in
// scope make more FORBIDS match too. So switching inheritance off can remove a forbid that
// depended on those groups, which moves the outcome toward ALLOW: a widen, sitting in a
// vocabulary whose whole promise is that widening has no word (see invariant III above, and
// modules/governance/grants.go's scoped-forbid arm).
//
// It is REMOVED rather than made conditional because "tighten unless it would widen" is not a
// tightening: it is an operation whose direction depends on data the route table cannot see at
// mount time. A route that needs inheritance off declares it in the metadata it seals — where it
// is a DECLARATION, reviewed as one — instead of deriving it from a value that was already sealed.
//
// The lesson is worth more than the function: an operation belongs in this vocabulary only if it
// is monotone in the DECISION LATTICE. Monotone in the field is not the same thing, and reads
// identical.

// RaiseRBACMinimumRole raises the role floor. A role the engine does not know is refused here as
// it is in Validate, because an unknown role ranks 0 and a floor of 0 admits everyone: a typo
// would read as a tightening and BE a loosening.
func RaiseRBACMinimumRole(role string) Tightening {
	return Tightening{name: "RaiseRBACMinimumRole", apply: func(m *RouteMetadata) error {
		if !auth.IsRole(role) {
			return fmt.Errorf("api: RaiseRBACMinimumRole(%q): not a role the engine knows; an "+
				"unknown role ranks 0 and would admit every principal that has any role", role)
		}
		if auth.RoleRank(role) < auth.RoleRank(m.RBACMinimumRole) {
			return fmt.Errorf("api: RaiseRBACMinimumRole(%q) on a route whose floor is already %q "+
				"would LOWER it; a sealed route only tightens", role, m.RBACMinimumRole)
		}
		m.RBACMinimumRole = role
		return nil
	}}
}

// Tighten applies the tightenings in order and re-validates. A refusal names the operation, so a
// boot failure says which line of the route table to look at.
func (s SealedRoute) Tighten(ts ...Tightening) (SealedRoute, error) {
	if !s.sealed {
		return SealedRoute{}, errors.New("api: Tighten on a route that never went through SealRoute")
	}
	m := s.m
	for i, t := range ts {
		// ⛔ EL CERO SE RECHAZA, Y ANTES HACIA PANIC. `Tightening{}` es un valor perfectamente
		// construible —un literal a medio rellenar, un elemento de slice sin inicializar— y su
		// `apply` es nil, asi que llamarlo tiraba el proceso en el REGISTRO de rutas: un boot que
		// muere con un nil pointer en vez de decir cual de los tightenings estaba vacio. Un
		// endurecimiento que no endurece nada no es un no-op benigno: es una linea que el lector
		// cuenta como restriccion aplicada.
		if t.apply == nil {
			return SealedRoute{}, fmt.Errorf(
				"api: tightening %d is the zero value and restricts nothing; a Tightening comes "+
					"from RaiseMinimumAAL, RequireScopedGrant or RaiseRBACMinimumRole", i)
		}
		if err := t.apply(&m); err != nil {
			return SealedRoute{}, err
		}
	}
	if err := m.Validate(); err != nil {
		return SealedRoute{}, fmt.Errorf("api: the tightened route is not valid: %w", err)
	}
	return SealedRoute{m: m, sealed: true}, nil
}
