// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// Route-level authorization: one evaluation, one witness (V269 / architecture §7.1).
//
// This file adds the seam a governed route needs and adds NOTHING to the historical
// path. RouteMetadata already lives in routemetadata.go and is already read by the
// Authorizer's RBAC term; what was missing is a way to authorize a route and KEEP the
// evidence, so a later effect can be tied to the exact decision that permitted it.
//
// ⛔ THE WITNESS IS NOT A BEARER. It is produced by the server, for one request, and no
// handler may accept one from a client. It exists so an effect can be attributed to a
// decision — which decision, over which resource, at which policy version — not so a
// decision can be replayed. Every field is either an opaque digest or a value the client
// already sent; none of them re-authorizes anything on its own.

// CedarAction is a registered Cedar Action ID.
//
// It is a DEFINED TYPE and not a bare string because the permission parser is a grammar
// whose last segment must be read, write or admin (permission.go). An action such as
// `shell:open` cannot be spelled inside that grammar without breaking the verb tiering
// for every other module, so a route names its action separately and keeps a permission
// the grammar can still rank.
type CedarAction string

// String returns the action id.
func (a CedarAction) String() string { return string(a) }

// RouteEvidenceDigestFormat names the codec of RouteAuthorizationWitness.EvidenceDigest.
//
// It is a format identifier and nothing more: it does not select a version, accept a caller
// option, expose lease internals or verify authority. The digest it names is an unkeyed
// SHA-256 consistency commitment over one process-local witness — not a MAC, not a wire or
// cross-process authority format, and not a record from which an old decision can be
// replayed. The exact byte codec is specified in docs/design/route-evidence-digest-v2.md and
// implemented, privately, by evidenceDigest in this file. Old byte values do not satisfy the
// current recomputation; a witness is reconstructed and authorized normally, never decoded.
const RouteEvidenceDigestFormat = "olivares.auth.route-evidence.v2"

// RouteAuthorizationWitness is in-process evidence of one route authorization.
// This type does not persist a decision or its evaluated inputs for historical replay.
//
// ⛔ ScopedEffect IS THE REAL EFFECT AND NOT A RESTATEMENT OF THE OUTCOME, and keeping it
// separate is the point of this type. A route that required a scoped grant and got one
// looks, in the outcome alone, exactly like a route that was allowed by breadth of role;
// telling them apart afterwards is what makes an audit of "who could do this, and why"
// possible at all.
type RouteAuthorizationWitness struct {
	// Decision is the evidence the single evaluation produced.
	Decision AuthorizationEvidence
	// CedarAction is the action that was evaluated, which for a governed route is NOT
	// derivable from the permission.
	CedarAction CedarAction
	// ScopedEffect is the grant/forbid/abstain the scoped engine actually returned.
	ScopedEffect Effect
	// ResourceDigest binds the witness to the exact resource attributes evaluated, so a
	// witness cannot be carried from one row to its neighbour.
	ResourceDigest [32]byte
	// EvidenceDigest binds it to the evidence itself, under the RouteEvidenceDigestFormat codec.
	EvidenceDigest [32]byte
	// PolicyVersion is the legacy maximum positive version among Decision.Facts.
	// Facts advance independently, so a fact can change without changing this maximum.
	// It is not a policy identity or a cache, freshness, revocation or replay key.
	// EvidenceDigest binds every fact's complete coordinates (Kind, ID, Version and, for a
	// leased fact, its lease presence, subject, fence and deadline) as well as this value;
	// consumers must retain the complete witness and required current-authority checks.
	PolicyVersion uint64

	// QuestionDigest binds the WHOLE question this witness answered: which principal, which
	// permission, which action, over which resource of which tenant.
	//
	// ⛔ ResourceDigest ANSWERS ONLY "WHICH ROW", AND THAT WAS THE HOLE THE SEAL DID NOT COVER.
	// An AUTHENTIC witness minted for principal A with permission p could be presented as
	// evidence for principal B with permission q over the same row, and every check passed: it
	// was really minted, really unedited, really about that row, and really fresh. No forgery
	// was needed - only a transplant.
	QuestionDigest [32]byte

	// minted is set by AuthorizeRoute and by nothing else, and it is the witness's origin.
	//
	// ⛔ IT IS UNEXPORTED BECAUSE EVERY OTHER FIELD IS NOT, AND THAT WAS THE HOLE. A value
	// built outside this package with an ALLOW outcome and a future window satisfied Allows,
	// so "a witness cannot be fabricated outside the authorization" was false: only the ZERO
	// value denied. A composite literal in another package cannot set this field and cannot
	// assign it afterwards, so a fabricated witness is now inert no matter how well it is
	// filled in - including one filled in with the correct ResourceDigest, which is what the
	// row guard compares.
	//
	// Copying a real witness and editing it is covered by the other half, VerifyFor's
	// recomputation of EvidenceDigest: the seal says WHO made it, the digest says it has not
	// been altered since. Neither alone is enough.
	minted bool
}

// ⛔ THE WITNESS NO LONGER CARRIES ITS OWN COPY OF THE WINDOW, and the removal is the fix.
// It used to hold ObservedAt and FreshUntil beside Decision's, set from the same evidence -
// two copies of one fact. evidenceDigest binds Decision's pair; nothing bound the top-level
// pair, and Allows read the UNBOUND one. So a witness with Decision.FreshUntil in the past and
// a top-level FreshUntil in the future passed both the digest and the freshness check.
// Removing the copy leaves one truth, and it is the one already inside the digest.

// IsFresh reports whether the witness still stands at now.
//
// A zero FreshUntil is NOT treated as "forever": an evidence window that was never
// established is unknown, and unknown denies.
func (w RouteAuthorizationWitness) IsFresh(now time.Time) bool {
	if w.Decision.FreshUntil.IsZero() || w.Decision.ObservedAt.IsZero() {
		return false
	}
	// ⛔ LOS DOS EXTREMOS. Sin el inferior, la ventana dice "válida desde siempre", que no es una
	// afirmación que ninguna evaluación pueda hacer: un testigo con ObservedAt en el futuro -
	// reloj mal puesto, evidencia sellada por otro nodo, un valor construido - autorizaba HOY
	// por una decisión que aún no se ha tomado. Un intervalo con un solo extremo no es un
	// intervalo.
	return !now.Before(w.Decision.ObservedAt) && now.Before(w.Decision.FreshUntil)
}

// Allows reports whether this witness authorizes an effect at now. It is the only
// question a caller should ask of a witness.
func (w RouteAuthorizationWitness) Allows(now time.Time) bool {
	return w.minted && w.Decision.Outcome == EvidenceAllow && w.IsFresh(now)
}

// VerifyFor is the whole question a consumer of a witness has, asked once.
//
// ⛔ Allows ANSWERS ONLY HALF OF IT, AND THE HALF IT ANSWERS IS THE ONE NOBODY GETS WRONG. A
// witness that authorizes SOMETHING is not a witness that authorizes THIS: the row guard used
// to compare ResourceDigest by hand and could therefore prove "it is about this row" while
// proving nothing about whether the row was ever authorized, and Allows proves the opposite
// half. Splitting the question is how a caller ends up holding one half and believing it has
// both.
//
// The four terms, and none of them is redundant:
//
//   - minted (inside Allows): this came out of AuthorizeRoute. Without it a fabricated value
//     with the right fields passes everything else.
//   - the recomputed EvidenceDigest: it has not been edited since. The seal survives a copy,
//     so without this term a real witness could be copied and its action or verdict changed.
//   - the outcome and the window (inside Allows): it said yes, and it still says yes NOW.
//   - the QUESTION: it answered the one being asked now - same principal, same permission, same
//     action, same resource. Without this term an authentic witness for another question passes
//     every other check, because every other check is true of it.
//
// ⛔ IT TAKES THE REQUEST AND NOT A RESOURCE, and the change of shape is the cure. A consumer
// that can only name the row can only be told about the row; making it state the whole question
// is what allows the whole question to be checked.
// AnswersQuestion reports whether this witness was minted for exactly this question.
//
// ⛔ IT IS THE ONE TERM OF VerifyFor A CALLER MAY NEED ON ITS OWN, and the reason is that its
// failure has a different meaning from all the others. A witness that fails HERE is authentic
// and about something else; one that fails the rest of VerifyFor — unminted, edited, out of
// window — is not sound at all. Those two have different readers and different remedies, and a
// consumer that can only ask the whole question has to report the second when it means the first.
//
// ⛔ AND IT RETURNS A BOOL, NOT THE DIGEST. Handing out the digest would let a caller build its
// own comparison, and a second implementation of a binding is how the two drift until one binds
// less than the other. There is one place that computes this, and this is how it is asked.
func (w RouteAuthorizationWitness) AnswersQuestion(req Request) bool {
	return w.QuestionDigest == questionDigest(req)
}

// IsSound reports whether this witness holds up ON ITS OWN: minted by the authorization, inside
// its window, and not edited since. It says NOTHING about which question it answers.
//
// ⛔ IT EXISTS SO THAT "NOT SOUND" AND "ANOTHER QUESTION" CANNOT BE CONFUSED, and the confusion
// was measured. A witness typed by hand has a zero QuestionDigest, so a consumer that asks about
// the question FIRST reports a forged witness as "authentic and about something else" — and
// "authentic" is exactly what it is not. Soundness is the prior question: a value nobody minted
// does not answer a different question, it answers none.
func (w RouteAuthorizationWitness) IsSound(now time.Time) bool {
	return w.Allows(now) && evidenceDigest(w) == w.EvidenceDigest
}

// VerifyFor is the whole check, and it is DEFINED as its two halves so the three cannot drift.
// A consumer that needs to tell them apart asks IsSound and AnswersQuestion in that order; one
// that does not, asks this.
func (w RouteAuthorizationWitness) VerifyFor(now time.Time, req Request) bool {
	return w.IsSound(now) && w.AnswersQuestion(req)
}

// PrincipalAuthorityAvailable reports whether this principal's authority can be reconstructed at
// all inside this tenant. It is NOT the route decision and must never be used as one.
//
// ⛔ EXISTE PARA QUE EL TERCER ESTADO PUEDA SALIR ANTES DE LEER LA FILA. En una ruta gobernada de
// entidad, el motor lee la fila para autorizar sobre sus atributos — ése es el diseño de
// HandleEntity y no cambia—. Pero el 503 de «no pude decidir» se emitía DESPUÉS de esa lectura, así
// que una petición que jamás podría decidirse provocaba igualmente una lectura no autorizada: coste,
// latencia, bloqueos y trazas del resolvedor, que son un oráculo por otra vía aunque el cuerpo de la
// respuesta sea idéntico. Y hacía falsa la promesa escrita de «CERO lecturas antes del tercer
// estado».
//
// ⛔ Y ES UNA PUERTA DE DISPONIBILIDAD, NO MEDIA DECISIÓN. Pregunta lo único que NO depende del
// recurso: si la evidencia sellada del principal existe. Por eso puede correr antes del cargador
// sin adelantar nada de la política, y por eso su respuesta es «no pude mirar» y nunca «no puedes».
//
// ⚠ EL PRECIO, DICHO Y NO ESCONDIDO: un principal cuya autoridad no se puede reconstruir recibe 503
// aunque la política, con la fila delante, hubiera dado una DENEGACIÓN. Se acepta a propósito —
// averiguar cuál de las dos era exige justamente la lectura que esto evita, y 503 filtra
// estrictamente menos que un 403 o un 404. Es «no pude mirar» dicho con honestidad, que es la
// tercera respuesta de la regla 5 del canon.
func (az *Authorizer) PrincipalAuthorityAvailable(p Principal, tenant model.TenantID) error {
	if az == nil {
		return ErrAuthorizerUnavailable
	}
	if _, _, ok := principalAuthorizationEvidence(p, tenant); !ok {
		return fmt.Errorf("%w: the principal's authority could not be reconstructed for this tenant",
			ErrRouteUndecided)
	}
	return nil
}

// AuthorizeRoute performs ONE tri-state evaluation for a governed route and returns the
// witness for it.
//
// ⛔ ONE EVALUATION, NOT TWO. Asking the engine twice — once for the answer and once for
// the evidence — is not a slower version of the same thing: policy, grants and directory
// facts can change between the two calls, so the pair can disagree, and the disagreement
// resolves in whichever direction the code happens to read second. AuthorizeRoute calls
// AuthorizeEvidence exactly once and derives everything from that result.
//
// ⛔ AND IT RETURNS A WITNESS ONLY FOR AN ALLOW. A witness for a denial would be a record
// that reads like a permission at every call site that forgets to check the outcome, and
// forgetting is the normal failure of a struct with a boolean inside it.
//
// MinimumAAL is enforced HERE and deliberately not inside Authorize: a step-up is a
// precondition of authentication, not a term of the authorization algebra, and folding it
// in would make "prove who you are again" indistinguishable from "you may not do this" —
// two answers with different remedies. The step-up answer is returned as
// ErrStepUpRequired, which core/api already renders as 403 step_up_required.
func (az *Authorizer) AuthorizeRoute(ctx context.Context, req Request) (RouteAuthorizationWitness, error) {
	if az == nil {
		return RouteAuthorizationWitness{}, ErrAuthorizerUnavailable
	}
	if req.Route.RequiresStepUp(req.Principal.AAL) {
		// Assurance is checked BEFORE the policy evaluation, so a principal who must
		// step up is not told, by timing or by error, whether they would have been
		// allowed afterwards.
		return RouteAuthorizationWitness{}, ErrStepUpRequired
	}

	ev := az.AuthorizeEvidence(ctx, req)

	// ⛔ THE SCOPED EFFECT COMES FROM THAT EVALUATION AND NOT FROM A SECOND ONE. This block
	// used to call scopedSafe here, immediately after AuthorizeEvidence had already asked
	// the same engine the same question - so this function did exactly the two evaluations
	// its own comment says it exists to avoid, and the comment was the only thing saying
	// otherwise.
	//
	// It is not a wasted call, it is an inconsistent one: policy, grants and directory
	// facts can change between the two, so the Decision could allow on the strength of a
	// grant that the second call reports as forbid, and the witness would attest an effect
	// that had no part in the decision it accompanies. AuthorizationEvidence now reports
	// the effect the evaluation actually used.
	effect := ev.ScopedEffect

	w := RouteAuthorizationWitness{
		Decision:       ev,
		CedarAction:    resolveCedarAction(req),
		ScopedEffect:   effect,
		ResourceDigest: ResourceDigest(req.Tenant, req.Resource),
		PolicyVersion:  policyVersionOf(ev.Facts),
		QuestionDigest: questionDigest(req),
		minted:         true,
	}
	w.EvidenceDigest = evidenceDigest(w)

	if ev.Outcome != EvidenceAllow {
		return RouteAuthorizationWitness{}, DenialFor(ev)
	}

	// ⛔ THE MINTER ASKS THE QUESTION IT DOCUMENTS AS THE ONLY ONE WORTH ASKING. Allows is
	// described as "the only question a caller should ask of a witness", and the function that
	// produces witnesses did not ask it: success was decided by the outcome alone, so a
	// decision whose window had already closed was minted as an authorization. The window can
	// be closed at mint time and not only later, because a contribution is validated for
	// end-after-start and never for containing the present (evidence.go, validContributionWindow).
	//
	// It is ErrRouteUndecided and not a denial, and the difference is the remedy: nothing said
	// no. The evidence simply is not fresh enough to say anything, and DecideRows fails the
	// whole page on it rather than hiding the row - a page missing a row nobody could decide
	// about is not the same as a page missing a row the caller may not see.
	// ⛔ THE SENTINEL IS SHARED, SO THIS EMITTER LEAVES A TRACE. DenialFor returns the same
	// ErrRouteUndecided for an unknown outcome, which is right - to a CALLER the two are one
	// answer with one remedy. To whoever is debugging they are not, and I read a test failure
	// here as "my new window check fired" when it was the other emitter. The wrap costs nothing
	// at the seam, since errors.Is still matches, and it is the difference between debugging
	// the change you just made and debugging code that has been there a month.
	if !w.Allows(az.clock()) {
		return RouteAuthorizationWitness{}, fmt.Errorf(
			"%w: the decision's window did not contain the present at mint time", ErrRouteUndecided)
	}
	return w, nil
}

// resolveCedarAction returns the action the route declared, falling back to the
// permission exactly as modules/governance does. The fallback is what keeps every route
// that has not opted in deciding as before.
func resolveCedarAction(req Request) CedarAction {
	if req.Route.CedarAction != "" {
		return CedarAction(req.Route.CedarAction)
	}
	return CedarAction(req.Permission)
}

// ResourceDigest is a stable digest of the tenant and the resource attributes an
// authorization was taken over.
//
// ⛔ IT IS LENGTH-PREFIXED, NOT CONCATENATED. Joining fields with a separator lets two
// different resources produce one digest whenever a field can contain the separator — and
// an entity id can contain almost anything. Two resources sharing a digest means a
// witness for one authorizes the other.
func ResourceDigest(tenant model.TenantID, r ResourceAttrs) [32]byte {
	h := sha256.New()
	write := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	write(string(tenant))
	write(r.Kind)
	write(r.ID)
	write(string(r.WorkspaceID))
	// ⛔ Sensitivity ENTRA, y su ausencia era un agujero con la forma exacta del que este digest
	// existe para cerrar: es un campo que DECIDE — el operador se lo asigna a la fila y la
	// decisión cambia con él — y no estaba digerido, así que dos recursos que la política trata
	// distinto producían el mismo digest. Lo que decide, se liga.
	write(r.Sensitivity)
	// Extra is map[string]string, so the keys are sorted and both halves are written
	// with their length. Ranging a map without sorting would digest the same resource
	// differently between two calls in ONE process.
	keys := make([]string, 0, len(r.Extra))
	for k := range r.Extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		write(k)
		write(r.Extra[k])
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// evidenceDigest is the RouteEvidenceDigestFormat codec: SHA-256 over the exact
// concatenation specified in docs/design/route-evidence-digest-v2.md.
//
//	text(s)   u64(len([]byte(s))) || []byte(s) — the exact Go string bytes, with no UTF-8
//	          validation, replacement or normalization
//	u64(v)    unsigned 64-bit big-endian; signed fact fields use the existing uint64 conversion
//	digests   the raw 32 bytes, without framing
//	presence  one byte, 0 or 1
//
// In order: the format label; the action, outcome and scoped effect; the three check
// verdicts and codes; the resource and question digests; both window endpoints as exact UTC
// RFC3339Nano calendar text; the fact count and, per fact in existing canonical order, Kind,
// ID, Version and the lease presence byte followed — when present — by the subject, the
// fence and deadline.String(); and PolicyVersion.
//
// ⛔ THE FACTS ENTER BY THEIR COMPLETE COORDINATES, AND THE LEASE HALF WAS THE FINDING.
// Before G1 each fact contributed Kind, ID and Version only, while canonicalEvidenceFacts
// and the final SQL validation already treated lease presence, subject, fence and deadline
// as fact identity. A copied witness whose leased fact changed only in a lease coordinate
// therefore recomputed to its original digest and stayed sound. Binding the lease here
// repairs the consistency commitment this digest claims to be; it is not a new
// authorization control, and it demonstrates no bypass: the final SQL validation refused a
// missing or mismatched lease before and refuses it still.
//
// ⛔ THE WINDOW IS CALENDAR TEXT, NOT UnixNano. Existing admission accepts instants outside
// the int64 nanosecond range, and u64(UnixNano()) folded them silently. UTC removes
// location distinctions, the evidence window carries no monotonic reading, and length
// framing keeps the variable-width (and, for an accepted extended year, wider) rendering
// unambiguous. Nothing is truncated, clamped or rejected here to fit a narrower encoding.
//
// The codec never sorts, deduplicates or discards a fact. Canonical order and duplicate
// refusal are issuance-time properties, so a mutated fact vector recomputes to a different
// value instead of being repaired into a matching one.
func evidenceDigest(w RouteAuthorizationWitness) [32]byte {
	h := sha256.New()
	text := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	num := func(v uint64) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], v)
		h.Write(n[:])
	}
	text(RouteEvidenceDigestFormat)
	text(string(w.CedarAction))
	// ⛔ THE OUTCOME, THE VERDICTS AND THE SCOPED EFFECT ARE IN THE DIGEST, and their
	// absence was the finding. This field is documented as binding the evidence, and it
	// covered the action, three CODES, the resource and the highest fact version -
	// nothing else. Two witnesses over the same route and resource, with the same maximum
	// fact version but DIFFERENT outcomes, different verdicts, a different scoped effect, a
	// different freshness window and entirely different facts, produced the SAME digest.
	// A digest that does not change when the evidence changes binds nothing; it names the
	// question, not the answer.
	num(uint64(w.Decision.Outcome))
	num(uint64(w.ScopedEffect))
	for _, c := range []CheckEvidence{
		w.Decision.CorePermission, w.Decision.ResourceGuard, w.Decision.ForbidAbsence,
	} {
		num(uint64(c.Verdict))
		text(c.Code)
	}
	h.Write(w.ResourceDigest[:])
	// El digest de la PREGUNTA entra aqui: sin esto, un testigo copiado podria cambiarlo y seguir
	// pareciendo intacto, que es justo lo que la recomputacion existe para impedir.
	h.Write(w.QuestionDigest[:])
	// The freshness window: two evaluations of the same facts at different times are
	// different evidence, and the witness is only valid inside its window. These are exactly
	// the bytes of the named Go rendering; no claim is made that every accepted value
	// satisfies an RFC 3339 parser.
	text(w.Decision.ObservedAt.UTC().Format(time.RFC3339Nano))
	text(w.Decision.FreshUntil.UTC().Format(time.RFC3339Nano))
	// ⛔ THE FACTS BY THEIR COMPLETE IDENTITY, NOT BY THEIR MAXIMUM VERSION. The digest
	// once took policyVersionOf(facts) - one number, the highest - so a decision resting on
	// fact A and one resting on fact B digested identically whenever their versions
	// matched. Facts are canonical and duplicate-free at issuance, so their order is already
	// stable; a leased fact additionally contributes the server-issued lease witness.
	num(uint64(len(w.Decision.Facts)))
	for _, f := range w.Decision.Facts {
		text(string(f.Kind))
		text(string(f.ID))
		num(uint64(f.Version))
		if subject, fence, deadline, ok := f.LeaseFenceWitness(); ok {
			h.Write([]byte{1})
			text(subject)
			num(uint64(fence))
			text(deadline.String())
		} else {
			h.Write([]byte{0})
		}
	}
	num(w.PolicyVersion)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// policyVersionOf returns the legacy maximum positive fact version, or zero if absent.
// This lossy summary is retained for compatibility with the witness digest. Independent
// fact versions do not form a shared epoch; their complete coordinates remain in Facts.
func policyVersionOf(facts []store.AuthorizationFactRef) uint64 {
	var v uint64
	for _, f := range facts {
		if f.Version > 0 && uint64(f.Version) > v {
			v = uint64(f.Version)
		}
	}
	return v
}

// ErrAuthorizerUnavailable is returned when there is no authorizer to ask.
//
// ⛔ IT IS AN ERROR AND NOT A DENIAL, and the distinction is canon rule 5: "I could not
// look" is the third answer. Rendering it as a plain 403 would tell an operator their
// policy denies something when in fact nothing evaluated it.
var ErrAuthorizerUnavailable = routeError("auth: the authorizer is unavailable: this is not a denial, nothing was evaluated")

// ErrRouteDenied is the ordinary denial.
var ErrRouteDenied = routeError("auth: the route is not authorized for this principal")

// ErrRouteUndecided is the third answer: a necessary predicate could not be established.
var ErrRouteUndecided = routeError("auth: the route decision could not be established with fresh evidence")

type routeError string

func (e routeError) Error() string { return string(e) }

// DenialFor maps an evidence outcome to the error a route should answer with, keeping
// deny and undecided apart all the way to the client.
func DenialFor(ev AuthorizationEvidence) error {
	switch ev.Outcome {
	case EvidenceDeny:
		if strings.Contains(ev.CorePermission.Code, "scoped_grant") {
			return ErrScopedGrantRequired
		}
		return ErrRouteDenied
	default:
		return ErrRouteUndecided
	}
}

// ErrScopedGrantRequired distinguishes "your role is broad but this route does not take
// breadth" from an ordinary denial, so the client can be told which remedy applies.
var ErrScopedGrantRequired = routeError("auth: this route requires a scoped grant; a tenant-wide role does not reach it")

// questionDigest binds the WHOLE question a witness answered: who asked, what permission, which
// action, and over which resource of which tenant.
//
// ⛔ IT EXISTS BECAUSE ResourceDigest ANSWERS ONLY "WHICH ROW". A witness minted for principal A
// with permission p and action x was, until this, indistinguishable from evidence for principal
// B with permission q and action y over that same row: every check passed, and no forgery was
// needed — the witness was AUTHENTIC, just for a different question. The seal proves who made
// it; the resource digest proves what it is about; neither proves it answered the question being
// asked now.
//
// The principal enters by its Ref, which is the identity the engine can establish (kind,
// credential and version) rather than anything the caller supplies. A principal whose Ref cannot
// be established digests as the empty ref, and that is deliberate: it makes the witness of an
// unestablished principal match only another unestablished one.
//
// The evaluated fact versions are not part of the question a caller states. The complete
// fact coordinates and the legacy PolicyVersion maximum bind through EvidenceDigest as
// evaluation outputs; the maximum alone does not identify the policy or evaluated inputs.
func questionDigest(req Request) [32]byte {
	h := sha256.New()
	var n [8]byte
	write := func(s string) {
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
	// ⛔ LA IDENTIDAD VA ANTES QUE LA CREDENCIAL, Y SU AUSENCIA ERA EL DEFECTO. Esto ataba solo
	// la REFERENCIA DE CREDENCIAL (kind, id, version) y, cuando no habia ninguna establecida,
	// escribia cadenas vacias en su lugar — asi que DOS PRINCIPALES DISTINTOS SIN CREDENCIAL
	// RESUELTA producian el MISMO digest, y un testigo de uno verificaba para el otro. No hacia
	// falta falsificar nada: bastaba con presentarlo donde no tocaba.
	//
	// Y no era un caso de laboratorio: hoy NINGUN principal del camino HTTP tiene referencia
	// establecida (nada instala la evidencia; ver principal_evidence.go), o sea que la rama que
	// colisionaba era la unica que se recorreria en produccion.
	//
	// Actor() es la identidad estable —"user:<id>" o "token:<id>"— y es la misma con la que el
	// registro de acceso atribuye la peticion, asi que atarla aqui no inventa un concepto nuevo.
	// La credencial SIGUE entrando: son dos preguntas distintas, QUIEN pregunta y COMO lo probo,
	// y el testigo responde por las dos.
	write(req.Principal.Actor())
	ref, ok := req.Principal.Ref()
	if ok {
		write(string(ref.kind))
		write(string(ref.credentialID))
		binary.BigEndian.PutUint64(n[:], uint64(ref.version))
		h.Write(n[:])
	} else {
		write("")
		write("")
		binary.BigEndian.PutUint64(n[:], 0)
		h.Write(n[:])
	}
	write(string(req.Permission))
	write(string(resolveCedarAction(req)))

	// ⛔ LA RUTA ENTERA, NO SOLO SU ACCION, Y ESA OMISION ERA EL AGUJERO. Esto ataba
	// `resolveCedarAction(req)` y nada mas de `req.Route`, asi que DOS PREGUNTAS QUE SE DECIDEN
	// DISTINTO daban el MISMO digest: mismo actor, mismo permiso, misma accion y misma fila, pero
	// una ruta que exige grant scoped y otra que no; una con suelo de rol y otra sin el; una que
	// hereda los grupos del agente y otra que no; una con suelo de AAL y otra sin.
	//
	// ⇒ Un testigo AUTENTICO de la ruta DEBIL verificaba para la ruta ESTRICTA. No hacia falta
	// falsificar nada: bastaba con acuñarlo donde se concede facil y presentarlo donde se concede
	// dificil, que es la escalada exacta que esta funcion existe para cerrar y la cerraba solo
	// para el eje del actor.
	//
	// Se atan TODOS los campos que deciden, en orden fijo y con longitud delante. Un campo nuevo
	// en RouteMetadata que decida algo entra aqui, y el test que cambia cada uno de uno en uno es
	// lo que obliga a acordarse.
	num := func(v uint64) {
		binary.BigEndian.PutUint64(n[:], v)
		h.Write(n[:])
	}
	bit := func(b bool) {
		if b {
			num(1)
			return
		}
		num(0)
	}
	bit(req.Route.RequireScopedGrant)
	write(req.Route.RBACMinimumRole)
	bit(req.Route.SessionInheritsAgentGroups)
	num(uint64(req.Route.MinimumAAL))
	// Y el nivel de garantia EFECTIVO del que pregunta: el suelo de la ruta dice cuanto hace
	// falta, y este dice cuanto trae. Dos peticiones que difieren solo en eso no son la misma
	// pregunta, aunque la ruta sea la misma.
	num(uint64(req.Principal.AAL))

	d := ResourceDigest(req.Tenant, req.Resource)
	h.Write(d[:])
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// RequiresStepUp reports whether this route's assurance floor is above what the caller has.
//
// ⛔ EXISTE PARA QUE LA REGLA SE ESCRIBA UNA VEZ MIENTRAS LA COMPROBACION SIGUE EN DOS SITIOS. El
// suelo se aplica en dos puertas a proposito — la del middleware, ANTES de la decision, y la de
// AuthorizeRoute, porque un step-up es precondicion de la autenticacion y no un termino del
// algebra de autorizacion. Dos puertas es el diseño; dos copias del PREDICADO fue un accidente, y
// el comentario que introdujo la segunda argumenta, tres parrafos mas arriba de si mismo, que dos
// copias de un flujo de autorizacion DERIVAN y la que deriva es la que menos gente lee.
//
// Un suelo cero no exige nada, que es lo que declara toda ruta que no ha optado por entrar.
func (m RouteMetadata) RequiresStepUp(aal int) bool {
	return m.MinimumAAL > 0 && aal < m.MinimumAAL
}
