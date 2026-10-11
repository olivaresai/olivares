// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
)

// supersedingResolver is the production Authenticator with one seam: it runs
// hook once, either after the effect's successful binding read and before the
// exact pair acquires its directory epoch (ResolvePrincipalScope), or after
// that acquisition. Every read and decision is the real Authenticator's.
type supersedingResolver struct {
	*auth.Authenticator
	beforeAuthority bool
	hook            func()

	mu          sync.Mutex
	bindingRead bool
	fired       int
}

func (r *supersedingResolver) ResolveCredentialBinding(
	ctx context.Context, b auth.CredentialBinding, s auth.CredentialBindingSubject,
) (auth.PrincipalRef, error) {
	ref, err := r.Authenticator.ResolveCredentialBinding(ctx, b, s)
	r.mu.Lock()
	r.bindingRead = r.bindingRead || err == nil
	r.mu.Unlock()
	return ref, err
}

func (r *supersedingResolver) ResolvePrincipalScope(
	ctx context.Context, ref auth.PrincipalRef, tenant model.TenantID,
) (auth.Principal, error) {
	if r.beforeAuthority {
		r.fire()
	}
	p, err := r.Authenticator.ResolvePrincipalScope(ctx, ref, tenant)
	if !r.beforeAuthority && err == nil {
		r.fire()
	}
	return p, err
}

func (r *supersedingResolver) fire() {
	r.mu.Lock()
	ready := r.bindingRead && r.fired == 0 && r.hook != nil
	if ready {
		r.fired++
	}
	r.mu.Unlock()
	if ready {
		r.hook()
	}
}

func (r *supersedingResolver) firedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fired
}

// R1 barrier oracle at the real effect seams: a workflow publish and a
// workflow Ack through the production Authenticator, the composed Authorizer
// and the real commit. The binding's liveness and the principal's exact
// authority must be proved at the one directory epoch the effect's commit
// consumes. A supersession (W1) that lands after the effect read its binding
// — before or after the exact pair acquired its epoch — refuses the old effect
// and writes nothing; an effect with no supersession, or one that committed
// before W1, commits. The directory fakes follow the real epoch after W1, as
// the real resolvers would, so the binding proof has to refuse on its own
// wherever no earlier observation pins the epoch.
func TestBindingReadThenSupersessionRefusesTheOldEffect(t *testing.T) {
	for _, effect := range []string{"publish", "ack"} {
		for _, tc := range []struct {
			name string
			// hook is where W1 lands; "" is no supersession during the effect.
			hook      string
			supersede bool
		}{
			{name: "no supersession commits", hook: ""},
			{name: "committed before W1 commits", hook: "", supersede: true},
			{name: "old binding read, then W1, then the old effect", hook: "before-authority"},
			{name: "authority acquired, then W1, then the old commit", hook: "after-authority"},
		} {
			t.Run(effect+"/"+tc.name, func(t *testing.T) {
				f := newWorkflowBindingFixture(t, workflowSQLiteBackend(t, "coherence"), false)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()
				var successor auth.CredentialBinding
				supersede := func() {
					fresh := f.freshSession()
					var err error
					successor, err = f.authr.RebindCredential(ctx, f.binding, 1, fresh, f.subject(f.runID), fresh)
					if err != nil {
						t.Errorf("W1: supersede the binding: %v", err)
					}
					f.resync()
				}
				// run performs the effect under actor; an Ack acknowledges a
				// self-addressed WorkTask published first, outside the seam.
				var delivery model.ID
				if effect == "ack" {
					cmd := f.task(f.bound, "coherence-self:"+model.NewID().String())
					cmd.Recipient = RecipientRef{Kind: RecipientUser, Ref: f.sender.String()}
					published, err := f.send(ctx, cmd)
					if err != nil {
						t.Fatalf("self publish before the Ack: %v", err)
					}
					delivery = published.DeliveryID
				}
				run := func(actor WorkflowCommunicationActor, key string) (bool, error) {
					if effect == "ack" {
						acked, err := f.ack(ctx, WorkflowMessageAckCommand{
							Actor: actor, WorkItemID: f.workID, ChannelID: f.channel.ID,
							DeliveryID: delivery, ExpectedVersion: 1, IdempotencyKey: key,
						})
						return err == nil && !acked.AckID.IsZero(), err
					}
					sent, err := f.send(ctx, f.task(actor, key))
					return err == nil && !sent.MessageID.IsZero(), err
				}

				sources := f.m.CommunicationAuthority
				resolver := &supersedingResolver{
					Authenticator: f.authr, beforeAuthority: tc.hook == "before-authority",
				}
				if tc.hook != "" {
					resolver.hook = supersede
				}
				f.m.CommunicationAuthority = &communicationRequestAuthoritySources{
					resolver: resolver, source: sources.source,
				}

				before := f.effectCounts()
				committed, err := run(f.bound, "coherence-old:"+model.NewID().String())
				if tc.hook != "" {
					if resolver.firedCount() != 1 {
						t.Fatalf("W1 landed %d times at the %s seam, want once", resolver.firedCount(), tc.hook)
					}
					if err == nil || committed {
						t.Fatalf("the old %s committed under a binding superseded after its binding read", effect)
					}
					// Before the epoch is acquired, the binding proof refuses:
					// the run's binding no longer holds. After it, the commit
					// refuses: it consumes the epoch W1 moved.
					want := ErrWorkflowReauthenticationRequired
					if tc.hook == "after-authority" {
						want = ErrCommunicationEvidenceUnknown
					}
					if !errors.Is(err, want) {
						t.Fatalf("the old %s answered %v, want %v", effect, err, want)
					}
					t.Logf("the old %s was refused: %v", effect, err)
					f.requireNoEffect(before, "an old effect superseded after its binding read")
				} else {
					if err != nil || !committed {
						t.Fatalf("%s with no supersession during it: committed %v, %v", effect, committed, err)
					}
					if !tc.supersede {
						f.requireLegacyUnused()
						return
					}
					supersede()
					if _, err := run(f.bound, "coherence-after:"+model.NewID().String()); !errors.Is(err, ErrWorkflowReauthenticationRequired) {
						t.Fatalf("old binding after W1 = %v, want ErrWorkflowReauthenticationRequired", err)
					}
				}
				if effect == "publish" {
					if committed, err := run(f.actorFor(successor, f.runID), "coherence-successor:"+model.NewID().String()); err != nil || !committed {
						t.Fatalf("successor effect: committed %v, %v", committed, err)
					}
				}
				f.requireLegacyUnused()
			})
		}
	}
}
