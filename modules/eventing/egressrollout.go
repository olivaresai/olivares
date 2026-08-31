// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package eventing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/egress"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// The rollout side of the egress destination policy — Unit G.
//
// Unit F gave this module a policy with three states, and one of them is a
// compromise it could not avoid: an ABSENT policy PERMITS, because anything else
// breaks every subscription in the field the moment the binary carrying the control
// is deployed. That reading is correct for an upgrade and wrong for a fresh install,
// which has nothing in the field to protect and therefore stays ungoverned by
// default, indefinitely, in the module whose product thesis is governing egress.
//
// The tri-state cannot tell the two apart, because the difference is not in the
// configuration — it is in the DEPLOYMENT'S HISTORY. So the engine classifies that
// history once, before it creates this module's tables, and records it durably
// (core/store/rollout.go). This file is what the module does with the answer.
//
// The full matrix, because "permitted" means three different things here:
//
//	                  | no policy authored | policy authored
//	------------------+--------------------+---------------------------------------
//	enforced          | DENY, terminal,    | the policy decides, and nothing else
//	                  | egress_policy_     |
//	                  | required           |
//	legacy_compat     | permit             | the policy decides, OR an exact seeded
//	                  |                    | destination this deployment already had
//	policy_optional   | permit             | the policy decides, and nothing else
//
// policy_optional is therefore never WEAKER than legacy_compat, which is why it is
// not called "unrestricted": an authored policy is authoritative in it. The mode says
// the control need not be configured, not that it can be bypassed.
//
// Three properties are worth stating because they are easy to lose:
//
//   - An UNREADABLE rollout state is not a permit. It denies, retryably, exactly
//     like an unreadable policy. The failure this campaign keeps finding is a plane
//     that could not decide being read as a plane that said yes.
//   - Compatibility is an EXACT list, not a mode that waves destinations through.
//     It is recorded per tenant, per subscription, per authority, so it can be
//     counted — an operator who cannot count what stops working cannot consent to it.
//   - The list is drawn at the tenant's FIRST decision on the new binary, not when a
//     policy is first authored. Drawing it later would let every subscription created
//     during an operator's delay become grandfathered, which is the original defect
//     wearing a different hat.

// EgressRolloutControlKey identifies this control in the durable rollout record. It
// is exported because the composition root wires the adapter that reads it and the
// operator's CLI names it.
//
// The version suffix is load-bearing: if the MEANING of the control changes, it
// ships as a new key and classifies afresh, rather than inheriting a disposition an
// operator decided about a different rule.
const EgressRolloutControlKey = "eventing.egress.destination.v1"

// egressRolloutControl is the declaration the engine classifies. The witness is this
// module's own subscription table: the question is not "is this control plane new?"
// but "could a destination have been authored here before this gate existed?".
//
// For the FIRST-PARTY binary those two questions have the same answer, because this
// module is constructed unconditionally and every successful historical boot created
// the table — an earlier draft of this design claimed otherwise and was wrong. The
// witness is still the better signal: it is the question the control actually asks,
// so a custom embedder that makes eventing optional gets the right answer for free,
// and the engine corroborates it against the module tracker rather than trusting it
// (see classifyRolloutControls).
func egressRolloutControl() store.RolloutControl {
	return store.RolloutControl{
		Key:          EgressRolloutControlKey,
		WitnessTable: subscriptionTable,
		LegacyMode:   store.RolloutLegacyCompat,
		FreshMode:    store.RolloutEnforced,
	}
}

// EgressRolloutSource reads this deployment's durable disposition for the egress
// destination control. The composition root wires it over the store's
// store.RolloutStater capability.
//
// A nil source means the embedder has not wired one, and the module then behaves
// exactly as it did before this unit existed: an absent policy permits. That is the
// only upgrade-safe reading of a seam an embedder has not adopted yet — but it is
// also how a control ends up not wired to the binary, which this campaign has
// already shipped once. So the FIRST-PARTY composition root treats a store without
// the capability as a boot failure, and a test pins that it wires this seam.
type EgressRolloutSource interface {
	EgressRollout(ctx context.Context) (store.RolloutState, error)
}

// WithEgressRollout wires the durable rollout state.
func WithEgressRollout(s EgressRolloutSource) Option {
	return func(m *Module) {
		if s != nil {
			m.rollout = s
		}
	}
}

// EgressPurpose is why a destination is being decided about. It is EXPLICIT rather
// than inferred, and that is a correction: an earlier draft used "the subscription
// reference is empty" as an implicit purpose, so a future create path that happened
// to pass an id would silently have gained the ability to inherit a compatibility
// exception. A closed vocabulary makes the rule exhaustively testable instead.
type EgressPurpose uint8

const (
	// EgressCreate is a destination that does not exist yet. It can NEVER use a
	// compatibility exception — compatibility preserves what a deployment had and never
	// manufactures a new entitlement — and any subscription id supplied with it is
	// ignored rather than trusted.
	EgressCreate EgressPurpose = iota
	// EgressUpdate is an edit of an existing subscription.
	EgressUpdate
	// EgressRestore is a revision being written back, which is exactly how a
	// destination an operator has since narrowed reappears.
	EgressRestore
	// EgressSend is the authoritative seam: the URL about to be dialed.
	EgressSend
	// EgressTest is an operator-triggered delivery to a stored subscription.
	EgressTest
	// EgressDryRun asks what the answer WOULD be. With a subscription id it asks as
	// that subscription; without one it asks as a create, which is the stricter
	// question and the right one before authoring.
	EgressDryRun
)

// mayUseException reports whether this purpose is allowed to consult the compatibility
// record at all.
//
// It is an ALLOW-LIST and not `!= EgressCreate`, which is the difference between a closed
// vocabulary and one that fails open. This type is exported, so a caller outside this package can
// construct EgressPurpose(99); under the negation it would have been permitted to borrow a
// subscription's exception while rendering as "unknown". A value this binary does not know gets
// the strictest answer available.
func (p EgressPurpose) mayUseException() bool {
	switch p {
	case EgressUpdate, EgressRestore, EgressSend, EgressTest, EgressDryRun:
		return true
	case EgressCreate:
		return false
	}
	return false
}

func (p EgressPurpose) String() string {
	switch p {
	case EgressCreate:
		return "create"
	case EgressUpdate:
		return "update"
	case EgressRestore:
		return "restore"
	case EgressSend:
		return "send"
	case EgressTest:
		return "test"
	case EgressDryRun:
		return "dry_run"
	}
	return "unknown"
}

// rolloutCacheTTL bounds how long a node may act on a stale disposition.
//
// It exists because the alternative shapes are both wrong. Reading the row on every
// decision puts a query in front of every delivery attempt for a fact that changes
// perhaps twice in a deployment's life. Resolving it once at boot — which is what
// the unit-D blinding precedent does — would make a rollout decision require a
// restart, and during a rolling restart the fleet would disagree: the same delivery
// retried on two nodes would be permitted by one and refused by the other, so the
// outcome would depend on which worker picked it up.
//
// Five seconds is short enough that a decision converges across the fleet before an
// operator can finish reading the confirmation, and long enough that the query
// disappears from the hot path.
const rolloutCacheTTL = 5 * time.Second

// rolloutCache holds the last successfully read state. Failures are NOT cached: a
// cached error would extend an outage past the outage, and the deny it produces is
// retryable precisely because it is expected to resolve.
type rolloutCache struct {
	mu sync.Mutex
	at time.Time
	st store.RolloutState
	ok bool
}

// resolveRollout returns the disposition in force, or an error the caller must turn
// into a retryable denial.
func (m *Module) resolveRollout(ctx context.Context) (store.RolloutState, error) {
	if m.rollout == nil {
		// Not wired: behave as unit F did. Reported at Start, not here, because a
		// per-decision log line on the commonest path would be noise.
		return store.RolloutState{Key: EgressRolloutControlKey,
			ClassifiedMode: store.RolloutPolicyOptional, CurrentMode: store.RolloutPolicyOptional}, nil
	}
	now := m.clock.Now().Time()
	m.rolloutState.mu.Lock()
	if m.rolloutState.ok && now.Sub(m.rolloutState.at) < rolloutCacheTTL {
		st := m.rolloutState.st
		m.rolloutState.mu.Unlock()
		return st, nil
	}
	m.rolloutState.mu.Unlock()

	st, err := m.rollout.EgressRollout(ctx)
	if err != nil {
		return store.RolloutState{}, err
	}
	if !st.CurrentMode.Valid() {
		// A row holding a mode this binary does not know is NOT a permit. It is most
		// likely a downgrade — a newer binary wrote a mode this one predates — and
		// guessing at it would silently pick a disposition the operator never chose.
		return store.RolloutState{}, fmt.Errorf("eventing: durable rollout state for %q holds mode %q, which this binary does not know", st.Key, st.CurrentMode)
	}
	m.rolloutState.mu.Lock()
	m.rolloutState.st, m.rolloutState.at, m.rolloutState.ok = st, now, true
	m.rolloutState.mu.Unlock()
	return st, nil
}

// authorityKind versions the compatibility key.
//
// Two kinds exist because unit F deliberately supports two classes of destination. A
// canonicalizable one is recorded by its canonical authority. One whose host the
// strict IDNA profile REJECTS — an underscore in a label, which is ordinary in
// internal names — is recorded by its exact spelling, because unit F promised those
// keep working while no policy is authored and grandfathering them is the only way
// to keep that promise once one is.
//
// Recording the kind, rather than inferring it from the shape of the key, is what
// lets a later release add a third without a stored row becoming ambiguous. The
// digest is domain-separated by kind for the same reason: two kinds must never
// collide onto one match.
type authorityKind string

const (
	// authorityCanonicalV1 is scheme + canonical host + effective port.
	authorityCanonicalV1 authorityKind = "canonical_v1"
	// authorityLegacyRawV1 is scheme + the exact authority spelling, for endpoints the
	// strict parser refuses. It is EQUALITY-ONLY: never usable by a create, and never
	// consulted by the operator's policy, which decides over canonical destinations. It
	// is a frozen compatibility grammar, not a second policy parser — the mistake this
	// campaign has already paid for twice is a second copy of a rule that drifts, and
	// this one is deliberately incapable of deciding anything.
	authorityLegacyRawV1 authorityKind = "legacy_raw_v1"
)

// egressAuthority is exactly what a compatibility exception preserves: a place to
// connect to. Not a URL — a path or a query can carry tenant data and would make the
// exception a record of what the tenant SENDS rather than of where it sends it. Not
// a resolved address either: addresses legitimately change, and pinning one would
// turn a compatibility record into an outage the first time DNS moved.
type egressAuthority struct {
	kind authorityKind
	// scheme, host and port are the operator-readable projection. For
	// authorityLegacyRawV1, host carries the exact authority as written (which may
	// include a port) and port is zero.
	scheme string
	host   string
	port   int
}

// canonicalAuthority is the authority of a destination the strict parser accepted.
func canonicalAuthority(d egress.Destination) egressAuthority {
	return egressAuthority{kind: authorityCanonicalV1, scheme: d.Scheme, host: d.Host, port: d.Port}
}

// legacyRawAuthority is the authority of an endpoint the strict parser REFUSED.
//
// It normalizes NOTHING beyond trimming, and that is deliberate. An exact match may
// conservatively fail to recognize a harmless respelling — an explicit :443, a
// different case — and that false negative is a denial an operator can fix by
// authoring the destination. The opposite error would be broadening an invalid name,
// which is how an unparseable host becomes a wildcard.
func legacyRawAuthority(rawURL string) (egressAuthority, error) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return egressAuthority{}, fmt.Errorf("endpoint is not a URL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return egressAuthority{}, fmt.Errorf("endpoint has no scheme or authority")
	}
	return egressAuthority{kind: authorityLegacyRawV1, scheme: u.Scheme, host: u.Host}, nil
}

// authorityFor derives the authority to record or match for an endpoint, preferring
// the canonical form and falling back to the exact spelling.
//
// Both the seeding pass and the match go through THIS function, so a destination is
// recorded under the same key it will later be looked up by. Deriving them
// separately is how a compatibility record comes to hold entries nothing can match.
func authorityFor(rawURL string) (egressAuthority, error) {
	if d, err := egress.ParseDestination(rawURL); err == nil {
		return canonicalAuthority(d), nil
	}
	return legacyRawAuthority(rawURL)
}

// key is the exact text the digest is taken over.
func (a egressAuthority) key() string {
	if a.kind == authorityLegacyRawV1 {
		return a.scheme + "://" + a.host
	}
	return a.scheme + "://" + a.host + ":" + strconv.Itoa(a.port)
}

// String is the operator-readable rendering, and it says which grammar produced it —
// an operator planning to enforce needs to know that a destination is on the list
// only because its name is not canonicalizable.
func (a egressAuthority) String() string {
	if a.kind == authorityLegacyRawV1 {
		return a.key() + " (legacy spelling)"
	}
	return a.key()
}

// digest is the match key: one fixed-width, indexable column, domain-separated by
// kind so two grammars can never collide onto one match.
func (a egressAuthority) digest() string {
	h := sha256.New()
	h.Write([]byte(a.kind))
	h.Write([]byte{0})
	h.Write([]byte(a.key()))
	return hex.EncodeToString(h.Sum(nil))
}

// EgressCompatStore is the store surface the compatibility record needs: two
// tenant-pinned transactions and nothing else.
//
// It is an interface rather than a concrete type so that the ENGINE (which holds an
// api.ModuleData) and the operator's CLI (which holds a store.Store) share one
// implementation of the compatibility rules. They must: this campaign has twice
// shipped a second copy of a destination rule that drifted from the first — the
// CLI's endpoint check and the CLI's HTTP client — and both times the copy was the
// one that was wrong.
type EgressCompatStore interface {
	View(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error
	Mutate(ctx context.Context, tenant model.TenantID, fn func(store.Scope) error) error
}

// egressCompat owns the compatibility record: seeding it, reading it, and reporting
// it. One instance per process per store.
type egressCompat struct {
	data  EgressCompatStore
	clock model.Clock
	// seeded is an OPTIMIZATION and not the authority. The durable seed row stays
	// authoritative across a restart or a reconnect; this only saves the read on the
	// hot path, and the fact it caches is monotonic (the row is append-only and unique
	// per tenant) so it cannot go stale in the unsafe direction.
	seeded sync.Map
	// locks serializes the seeding pass per tenant within this process. Across
	// processes the unique index on the seed row is what serializes it.
	locks sync.Map
}

func newEgressCompat(data EgressCompatStore, clock model.Clock) *egressCompat {
	if clock == nil {
		clock = model.SystemClock{}
	}
	return &egressCompat{data: data, clock: clock}
}

// seedPageSize is how many subscriptions one seeding query reads. The pass paginates
// to exhaustion rather than capping: a fixed cap would make a large tenant
// permanently unseedable, and an unseedable tenant is one whose every decision is
// refused.
const seedPageSize = 500

// ensureSeed records this tenant's pre-existing destinations, once, and reports
// whether the record is complete.
//
// WHEN the line is drawn is a real decision and not an implementation detail. It is
// drawn at the tenant's FIRST DECISION ON THIS BINARY — before that decision is
// answered — and not when a policy is first authored. An earlier draft drew it at the
// first decision that NEEDED an exception, and that was wrong in a way worth
// recording: every subscription created between the upgrade and the operator
// authoring a policy would have been recorded as pre-existing, so an operator's delay
// accumulated grandfathered destinations indefinitely. That is the defect this whole
// unit exists to close, arriving from the other side.
//
// It is a BARRIER: a decision may not be answered before the seed commits, because
// an unseeded tenant is indistinguishable from a tenant with no legacy destinations,
// and those two demand opposite answers.
//
// What it CANNOT do is fence out a writer on an older binary. Nothing in this tree
// proves every node that can author a subscription carries this gate, so in a
// mixed-version fleet an old node can create after the line is drawn; that
// subscription is then not grandfathered and is refused once a policy is authored.
// The refusal is loud and the remedy is to author the destination. Making it
// impossible needs a writer fence, which the unit's own record names as missing
// rather than implying it exists.
func (c *egressCompat) ensureSeed(ctx context.Context, tenant model.TenantID) error {
	if _, done := c.seeded.Load(tenant); done {
		return nil
	}
	// One seeding pass per tenant per process. Without this, a burst of workers waking
	// on the same tenant all scan and all insert, and every loser rolls back a
	// transaction that read the whole subscription table.
	lk, _ := c.locks.LoadOrStore(tenant, &sync.Mutex{})
	mu := lk.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	if _, done := c.seeded.Load(tenant); done {
		return nil
	}
	present, err := c.seedPresent(ctx, tenant)
	if err != nil {
		return err
	}
	if present {
		// The marker exists. That is NOT the same as the marked set being intact, and conflating
		// the two moved the very defect this marker was introduced to prevent one level up: a
		// restore that kept eventing_egress_seed and lost eventing_egress_exception would report a
		// complete record with nothing in it, so a policy that excludes an old destination would
		// deny it while the operator's diff said nothing would break.
		//
		// So the stored count and digest — which this code wrote and then never read — are checked
		// against what is actually there, once per tenant per process, on the same pass that would
		// otherwise just set the cache.
		if verr := c.verifySeed(ctx, tenant); verr != nil {
			return verr
		}
		c.seeded.Store(tenant, struct{}{})
		return nil
	}

	batch := c.clock.Now().String()
	err = c.data.Mutate(ctx, tenant, func(sc store.Scope) error {
		// Re-read inside the transaction. Another node may have seeded between the check
		// above and this write, and the unique index would otherwise turn a benign race
		// into a permanent error for this tenant.
		seeds, err := sc.Ext(egressSeedKind)
		if err != nil {
			return err
		}
		existing, _, err := seeds.List(ctx, model.Query{Limit: 1})
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			return nil
		}
		subs, err := sc.Ext(subscriptionKind)
		if err != nil {
			return err
		}
		exceptions, err := sc.Ext(egressExceptionKind)
		if err != nil {
			return err
		}
		// Deduplicated by (subscription, kind, authority): many subscriptions
		// legitimately share one collector, and one row per tuple is what the unique
		// index expresses.
		seen := make(map[string]bool)
		var tuples []string
		total, unparsed := 0, 0
		cursor := ""
		for {
			// Paginated to EXHAUSTION. Every subscription, enabled or not: a disabled one is
			// one an editor can re-enable, and discovering at that moment that its
			// destination was never grandfathered is the same surprise arriving later.
			page, pg, err := subs.List(ctx, model.Query{Limit: seedPageSize, Cursor: cursor})
			if err != nil {
				return err
			}
			for _, rec := range page {
				total++
				subID := rec.String(model.ColID)
				auth, aerr := authorityFor(rec.String(colSubEndpoint))
				if aerr != nil {
					// Not even a URL with a scheme and an authority. It cannot be recorded and it
					// cannot be dialed either, so it is counted rather than swallowed: an operator
					// planning to enforce needs to know N destinations are in that position instead
					// of discovering it from a dead letter.
					unparsed++
					continue
				}
				dg := auth.digest()
				tk := subID + "\x00" + string(auth.kind) + "\x00" + dg
				if seen[tk] {
					continue
				}
				seen[tk] = true
				tuples = append(tuples, tk)
				if _, cerr := exceptions.Create(ctx, model.Record{
					colExcSubRef: subID,
					colExcKind:   string(auth.kind),
					colExcDigest: dg,
					colExcScheme: auth.scheme,
					colExcHost:   auth.host,
					colExcPort:   int64(auth.port),
					colExcBatch:  batch,
				}); cerr != nil {
					return cerr
				}
			}
			if !pg.HasMore || pg.Cursor == "" {
				break
			}
			cursor = pg.Cursor
		}
		// The seed row is REQUIRED even for a tenant with no subscriptions at all:
		// absence of exception rows cannot distinguish "nothing to grandfather" from
		// "never recorded", and those two are the difference between enforcing and
		// silently permitting.
		_, err = seeds.Create(ctx, model.Record{
			colSeedBatch:    batch,
			colSeedSubs:     int64(total),
			colSeedExcs:     int64(len(tuples)),
			colSeedUnparsed: int64(unparsed),
			colSeedDigest:   seedDigest(tuples),
		})
		return err
	})
	if err != nil {
		// A losing race shows up here as a unique-constraint failure. Re-read: if the row
		// is now there, the tenant IS seeded and the error described the write, not the
		// state.
		if present, perr := c.seedPresent(ctx, tenant); perr == nil && present {
			c.seeded.Store(tenant, struct{}{})
			return nil
		}
		return err
	}
	c.seeded.Store(tenant, struct{}{})
	return nil
}

// seedDigest fingerprints the exact entitlement SET, over (subscription, kind,
// authority-digest) tuples rather than authorities alone.
//
// The tuple matters: two tenants — or one tenant before and after an edit — can hold
// the same set of authorities distributed differently across subscriptions, and a
// digest over authorities alone would call those the same set. A decision to enforce
// is approved against this proof, so it has to distinguish them.
func seedDigest(tuples []string) string {
	sorted := append([]string(nil), tuples...)
	sort.Strings(sorted)
	h := sha256.New()
	for _, t := range sorted {
		h.Write([]byte(t))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// verifySeed proves the recorded compatibility set is still the one the seed row describes.
//
// It recomputes the tuple-set digest from the exception rows that are actually there and compares
// it — and the count — with what the seed committed. A mismatch is an INTEGRITY failure, not a
// verdict about any destination: the plane can no longer tell a grandfathered destination from a
// refused one, so the caller parks rather than deciding. Parking is right because this does not
// heal on its own and it must not destroy deliveries in the meantime; an operator has to restore
// the record.
func (c *egressCompat) verifySeed(ctx context.Context, tenant model.TenantID) error {
	var (
		storedCount  int64
		storedDigest string
		batch        string
	)
	tuples := make([]string, 0, 16)
	err := c.data.View(ctx, tenant, func(sc store.Scope) error {
		seeds, err := sc.Ext(egressSeedKind)
		if err != nil {
			return err
		}
		rows, _, err := seeds.List(ctx, model.Query{Limit: 1})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return fmt.Errorf("the compatibility seed row disappeared between two reads")
		}
		storedCount = rows[0].Int(colSeedExcs)
		storedDigest = rows[0].String(colSeedDigest)
		batch = rows[0].String(colSeedBatch)
		excs, err := sc.Ext(egressExceptionKind)
		if err != nil {
			return err
		}
		cursor := ""
		for {
			page, pg, perr := excs.List(ctx, model.Query{Limit: seedPageSize, Cursor: cursor})
			if perr != nil {
				return perr
			}
			for _, rec := range page {
				tuples = append(tuples, rec.String(colExcSubRef)+"\x00"+
					rec.String(colExcKind)+"\x00"+rec.String(colExcDigest))
			}
			if !pg.HasMore || pg.Cursor == "" {
				break
			}
			cursor = pg.Cursor
		}
		return nil
	})
	if err != nil {
		return err
	}
	if int64(len(tuples)) != storedCount || seedDigest(tuples) != storedDigest {
		return fmt.Errorf("the egress compatibility record for this tenant does not match its own seed (batch %s): it recorded %d exception(s) with digest %s and %d are present with digest %s. Restore %s before deciding — until then a refusal cannot be told from a destination this deployment already had",
			batch, storedCount, storedDigest, len(tuples), seedDigest(tuples), egressExceptionTable)
	}
	return nil
}

// seedPresent reports whether this tenant's compatibility record exists.
func (c *egressCompat) seedPresent(ctx context.Context, tenant model.TenantID) (bool, error) {
	var found bool
	err := c.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(egressSeedKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(ctx, model.Query{Limit: 1})
		if err != nil {
			return err
		}
		found = len(rows) > 0
		return nil
	})
	return found, err
}

// exceptionAllows reports whether this exact (subscription, authority) pair was
// recorded as pre-existing.
//
// Both halves are required. Keying on the authority alone would let any subscription
// borrow any other's grandfathered destination, which is a tenant-internal privilege
// escalation dressed as compatibility. Keying on the subscription alone would let an
// editor point a grandfathered subscription anywhere at all — the exact defect unit F
// closed, reopened under a friendlier name.
func (c *egressCompat) exceptionAllows(ctx context.Context, tenant model.TenantID, subRef model.ID, auth egressAuthority) (bool, error) {
	if subRef == "" {
		return false, nil
	}
	var allowed bool
	err := c.data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(egressExceptionKind)
		if err != nil {
			return err
		}
		rows, _, err := repo.List(ctx, model.Query{
			Filters: []model.Filter{
				eq(colExcSubRef, subRef.String()),
				eq(colExcDigest, auth.digest()),
			},
			Limit: 1,
		})
		if err != nil {
			return err
		}
		allowed = len(rows) > 0
		return nil
	})
	return allowed, err
}

// exceptionAllowsAny tries every grammar this destination could have been recorded under.
//
// The canonical authority is tried when the strict parser accepted the URL, and the exact spelling
// ALWAYS is. Trying both cannot permit a destination that was never recorded — every candidate is
// still matched against rows belonging to this subscription — and it is what keeps a durable record
// readable after the parser's behavior moves under it.
func (c *egressCompat) exceptionAllowsAny(ctx context.Context, tenant model.TenantID, subRef model.ID, canonical egressAuthority, rawURL string) (bool, error) {
	candidates := []egressAuthority{canonical}
	if raw, err := legacyRawAuthority(rawURL); err == nil {
		candidates = append(candidates, raw)
	}
	for _, a := range candidates {
		ok, err := c.exceptionAllows(ctx, tenant, subRef, a)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// seedPresentCached answers "is this tenant's record already drawn" without drawing it.
//
// It exists for the read-tier dry run, which must not be able to choose when an irreversible,
// once-only line falls.
func (c *egressCompat) seedPresentCached(ctx context.Context, tenant model.TenantID) (bool, error) {
	if _, done := c.seeded.Load(tenant); done {
		return true, nil
	}
	return c.seedPresent(ctx, tenant)
}

// LegacyExceptionReport is what an operator reads before deciding: the destinations
// that work today only because this deployment predates the control, and which stop
// working when it is enforced.
type LegacyExceptionReport struct {
	// Seeded reports that this tenant's compatibility record was drawn. When it is false
	// the rest of the report describes nothing, and a decision approved against it would
	// be approved against an unknown.
	Seeded bool `json:"seeded"`
	// Intact reports that the exception rows actually present still reproduce the count and
	// digest the seed committed. Seeded WITHOUT Intact is the shape a partial restore
	// produces, and it is the more dangerous of the two: the report looks complete and
	// describes a set that has lost members. A coverage proof requires both.
	Intact bool `json:"intact"`
	// IntegrityNote says what disagrees, when something does.
	IntegrityNote string `json:"integrity_note,omitempty"`
	// SeededAt is when the line was drawn, and SeedDigest fingerprints the exact set.
	SeededAt   string `json:"seeded_at,omitempty"`
	SeedDigest string `json:"seed_digest,omitempty"`
	// Subscriptions is how many existed when the line was drawn; Unparsed is how many
	// carry an endpoint that is not even a URL with an authority, and which therefore
	// cannot be grandfathered or dialed.
	Subscriptions int `json:"subscriptions"`
	Unparsed      int `json:"unparsed"`
	// Authorities lists the distinct destinations preserved, most-used first. It is
	// operator-facing: it names hosts, so it must never be served to a tenant caller.
	// JSONArray, not a plain slice: it is built with append, and "no legacy
	// destination at all" is the answer a fresh deployment gives — [] , not null.
	Authorities api.JSONArray[LegacyAuthorityCount] `json:"authorities"`
	// StillNeeded counts the authorities NOT covered by the policy in force, i.e. the
	// ones that actually stop working. Zero means enforcing changes nothing.
	StillNeeded int `json:"still_needed"`
}

// LegacyAuthorityCount is one preserved destination and how many subscriptions use it.
type LegacyAuthorityCount struct {
	Authority     string `json:"authority"`
	Kind          string `json:"kind"`
	Subscriptions int    `json:"subscriptions"`
	// Covered reports that the policy in force already permits it, so it survives
	// enforcement on its own merits and is not part of the breaking change. A
	// legacy-spelling authority is NEVER covered: the operator's policy decides over
	// canonical destinations, and one that cannot be canonicalized cannot be named by
	// a rule.
	Covered bool `json:"covered"`
}

// report builds the pre-decision diff for one tenant, against the policy pol that is
// in force for it right now.
//
// It never SEEDS. A report is a read, and drawing the compatibility line as a side
// effect of looking at it would move the line by observing it.
func (c *egressCompat) report(ctx context.Context, tenant model.TenantID, pol egress.Policy) (LegacyExceptionReport, error) {
	var out LegacyExceptionReport
	// Keyed by digest so two spellings of one authority cannot count twice, carrying
	// the parsed components so coverage is decided without re-parsing text this code
	// just serialized — a round trip is where a second, subtly different parse creeps
	// in.
	type authCount struct {
		auth egressAuthority
		n    int
	}
	counts := map[string]*authCount{}
	err := c.data.View(ctx, tenant, func(sc store.Scope) error {
		seeds, err := sc.Ext(egressSeedKind)
		if err != nil {
			return err
		}
		rows, _, err := seeds.List(ctx, model.Query{Limit: 1})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		out.Seeded = true
		out.SeededAt = rows[0].String(model.ColCreatedAt)
		out.SeedDigest = rows[0].String(colSeedDigest)
		out.Subscriptions = int(rows[0].Int(colSeedSubs))
		out.Unparsed = int(rows[0].Int(colSeedUnparsed))
		excs, err := sc.Ext(egressExceptionKind)
		if err != nil {
			return err
		}
		cursor := ""
		for {
			page, pg, err := excs.List(ctx, model.Query{Limit: seedPageSize, Cursor: cursor})
			if err != nil {
				return err
			}
			for _, rec := range page {
				a := egressAuthority{
					kind:   authorityKind(rec.String(colExcKind)),
					scheme: rec.String(colExcScheme),
					host:   rec.String(colExcHost),
					port:   int(rec.Int(colExcPort)),
				}
				dg := rec.String(colExcDigest)
				if ac, ok := counts[dg]; ok {
					ac.n++
					continue
				}
				counts[dg] = &authCount{auth: a, n: 1}
			}
			if !pg.HasMore || pg.Cursor == "" {
				break
			}
			cursor = pg.Cursor
		}
		return nil
	})
	if err != nil {
		return LegacyExceptionReport{}, err
	}
	if !out.Seeded {
		return out, nil
	}
	// A report over a damaged set is worse than no report, because it looks complete. The
	// integrity check runs here too — the CLI reads this, and the CLI is what an operator
	// approves a durable decision against.
	if verr := c.verifySeed(ctx, tenant); verr != nil {
		out.IntegrityNote = verr.Error()
	} else {
		out.Intact = true
	}
	// Coverage is computed against the policy IN FORCE right now, because that is what
	// the operator is deciding about. An authority the policy already permits is not
	// part of the breaking change even though it is on the compatibility list. With NO
	// policy authored, nothing is covered — enforcing would deny everything, and
	// reporting that plainly is the whole value of the report.
	for _, ac := range counts {
		covered := false
		if ac.auth.kind == authorityCanonicalV1 {
			covered = egress.CoversAuthority(pol, egress.Destination{
				Host: ac.auth.host, Port: ac.auth.port, Scheme: ac.auth.scheme,
				// A recorded authority whose host is an address must be decided by the CIDR
				// rules, exactly as it would be at send time; net.ParseIP is how
				// ParseDestination itself detects that case, so the two agree by construction.
				IP: net.ParseIP(ac.auth.host),
			})
		}
		if !covered {
			out.StillNeeded++
		}
		out.Authorities = append(out.Authorities, LegacyAuthorityCount{
			Authority: ac.auth.String(), Kind: string(ac.auth.kind),
			Subscriptions: ac.n, Covered: covered,
		})
	}
	sort.Slice(out.Authorities, func(i, j int) bool {
		if out.Authorities[i].Subscriptions != out.Authorities[j].Subscriptions {
			return out.Authorities[i].Subscriptions > out.Authorities[j].Subscriptions
		}
		return out.Authorities[i].Authority < out.Authorities[j].Authority
	})
	return out, nil
}

// CompatReporter builds the compatibility report from outside the module, over any
// store surface with tenant-pinned transactions.
//
// It is exported for the operator's CLI, which has to answer "what does enforcing
// break" for every tenant in the deployment and holds a store rather than a running
// module. It is the SAME code the API endpoint serves, which is the whole point: a
// second implementation of the diff would be a second answer to the question an
// operator is about to make a durable decision on.
type CompatReporter struct {
	compat *egressCompat
	policy EgressPolicySource
}

// NewCompatReporter builds a reporter. A nil policy source means none is authored,
// and then nothing is covered — which is the correct and load-bearing answer:
// enforcing with no policy denies everything.
func NewCompatReporter(data EgressCompatStore, pol EgressPolicySource) CompatReporter {
	return CompatReporter{compat: newEgressCompat(data, nil), policy: pol}
}

// Report describes one tenant's compatibility record against the policy in force for
// it.
func (r CompatReporter) Report(ctx context.Context, tenant model.TenantID) (LegacyExceptionReport, error) {
	pol := egress.Policy{}
	if r.policy != nil {
		p, err := r.policy.EgressPolicy(ctx, tenant)
		if err != nil {
			return LegacyExceptionReport{}, err
		}
		pol = p
	}
	return r.compat.report(ctx, tenant, pol)
}
