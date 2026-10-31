// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/core/webpush"
)

// A person's own WEB PUSH DEVICES (#2840): the browsers they chose, under Settings, to be
// told in even with the console closed, when a session of theirs needs an approval or has
// ended. Like a pin (run_pin.go) a device is the caller's own interface state, so the routes
// take the READ permission and write no ledger row; unlike a pin it is addressed to the
// browser's push service, so the engine signs and encrypts what it sends (core/webpush) and
// that service relays text it cannot read.
//
// WHO IS TOLD is the account that launched the session (launcher_user_id), the one the
// session is waiting for. A colleague's phone learns nothing of it.
const (
	// pushSubscriptionKind is one browser of one person.
	pushSubscriptionKind model.Kind = "sessions.push_subscription"
	// pushSubscriptionTable is the device row's physical table.
	pushSubscriptionTable = "sessions_push_subscription"
	// maxPushSubscriptions is the per-person ceiling. Past it the oldest leaves: a person
	// who registers one more browser means to replace one that is gone.
	maxPushSubscriptions = 10

	// pushDeliveryBudget bounds the delivery of one message to all of a person's devices.
	pushDeliveryBudget = 30 * time.Second
	// pushApprovalTTL is how long a push service keeps an approval request for a device that
	// is off: a request older than this is one nobody is waiting to answer.
	pushApprovalTTL = 15 * time.Minute
	// pushEndedTTL is how long it keeps "your session ended".
	pushEndedTTL = 24 * time.Hour
	// maxPushNameRunes bounds the session name in a message: it is shown on a lock screen.
	maxPushNameRunes = 80
)

// sessions.push_subscription columns. The endpoint and the two keys are what the browser's
// PushManager.subscribe returned, stored as it gave them.
const (
	colPushOwner       = "owner"           // the user id of the person who registered the device
	colPushEndpointSHA = "endpoint_sha256" // the device's id: hex SHA-256 of its endpoint
	colPushEndpoint    = "endpoint"
	colPushP256dh      = "p256dh"
	colPushAuth        = "auth"
)

// registerPushSubscriptionSchema declares the device entity. The unique index makes one
// browser registering twice an update, never a second row.
func (m *Module) registerPushSubscriptionSchema(reg store.ExtensionRegistry) error {
	return reg.Register(model.EntityDescriptor{
		Kind:  pushSubscriptionKind,
		Table: pushSubscriptionTable,
		Fields: []model.FieldSpec{
			{Name: colPushOwner, Kind: model.KindUUID, Indexed: true, Principal: model.Ref(model.EncodeUserID, model.ClassEvidence)},
			{Name: colPushEndpointSHA, Kind: model.KindText, Principal: model.None("the hex SHA-256 of the endpoint the browser's push service gave, the device's id in the routes, never an account: push.go:151, push.go:197")},
			{Name: colPushEndpoint, Kind: model.KindText, Principal: model.None("the https address of the browser's push service for this device, posted to and never resolved to an account: push.go:151, push.go:262")},
			{Name: colPushP256dh, Kind: model.KindText, Principal: model.None("the browser's P-256 public key the message is encrypted to, never an account: push.go:151, push.go:262")},
			{Name: colPushAuth, Kind: model.KindText, Principal: model.None("the browser's 16-byte authentication secret for the message coding, never an account: push.go:151, push.go:262")},
		},
		Indexes: []model.IndexSpec{{
			Name:    "sessions_push_subscription_uniq",
			Columns: []string{model.ColTenantID, colPushOwner, colPushEndpointSHA},
			Unique:  true,
		}},
	})
}

// PushSender is the engine's Web Push application server (core/webpush.Sender). A port, so
// the module sends nothing it was not given and a test sees every message.
type PushSender interface {
	// PublicKey is the application server key a browser subscribes with.
	PublicKey() string
	Send(ctx context.Context, sub webpush.Subscription, payload []byte, opts webpush.Options) error
}

// pushRoutes mounts the device routes under /v1/m/sessions/push. READ permission, as the
// pin's: every signed-in role keeps its own devices.
func (m *Module) pushRoutes(reg api.RouteRegistrar) {
	reg.Handle("GET", "/push/key", permRunRead, m.handlePushKey)
	reg.Handle("POST", "/push/subscriptions", permRunRead, m.handlePushSubscribe)
	reg.Handle("DELETE", "/push/subscriptions/{id}", permRunRead, m.handlePushUnsubscribe)
}

var errPushUnavailable = errors.New("web push is not available on this server")

// pushSender is the wired sender, or nil when this engine has none.
func (m *Module) pushSender() PushSender {
	if m.rt == nil || m.rt.Dependencies == nil {
		return nil
	}
	return m.rt.PushSender
}

// handlePushKey answers the application server key a browser subscribes with.
func (m *Module) handlePushKey(w http.ResponseWriter, _ *http.Request, _ api.ModuleContext) {
	sender := m.pushSender()
	if sender == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody(errPushUnavailable.Error()))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]string{"public_key": sender.PublicKey()})
}

// pushSubscribeBody is what a browser's PushSubscription holds, the shape its toJSON() gives
// without the expiry no push service sets.
type pushSubscribeBody struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

func pushEndpointID(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return hex.EncodeToString(sum[:])
}

// ownPushRows reads the caller's devices oldest first.
func ownPushRows(ctx context.Context, repo store.GenericRepo, owner string) ([]model.Record, error) {
	recs, _, err := repo.List(ctx, model.Query{
		Filters: []model.Filter{eq(colPushOwner, owner)}, Limit: maxPushSubscriptions + 1,
		Sort: []model.Sort{{Column: model.ColCreatedAt}, {Column: model.ColID}},
	})
	return recs, err
}

// handlePushSubscribe registers the caller's browser, idempotently. It refuses an endpoint
// the sender would refuse (core/webpush.Subscription.Validate), because the endpoint is the
// caller's word about where the engine will connect.
func (m *Module) handlePushSubscribe(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if m.pushSender() == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody(errPushUnavailable.Error()))
		return
	}
	if mc.Principal.UserID.IsZero() {
		writeJSON(w, http.StatusForbidden, errorBody("web push belongs to a signed-in person"))
		return
	}
	var in pushSubscribeBody
	if !decodeJSONBody(w, r, &in) {
		return
	}
	sub := webpush.Subscription{Endpoint: in.Endpoint, P256dh: in.Keys.P256dh, Auth: in.Keys.Auth}
	if err := sub.Validate(); err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody("this subscription cannot be pushed to: "+err.Error()))
		return
	}
	owner, id := mc.Principal.UserID.String(), pushEndpointID(sub.Endpoint)
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(pushSubscriptionKind)
		if err != nil {
			return err
		}
		own, err := ownPushRows(r.Context(), repo, owner)
		if err != nil {
			return err
		}
		for _, rec := range own {
			if rec.String(colPushEndpointSHA) == id {
				rec[colPushP256dh], rec[colPushAuth] = sub.P256dh, sub.Auth
				_, err := repo.Update(r.Context(), rec)
				return err
			}
		}
		if len(own) >= maxPushSubscriptions {
			oldest, err := model.ParseID(own[0].String(model.ColID))
			if err != nil {
				return err
			}
			if err := repo.Delete(r.Context(), oldest); err != nil {
				return err
			}
		}
		_, err = repo.Create(r.Context(), model.Record{
			colPushOwner: owner, colPushEndpointSHA: id, colPushEndpoint: sub.Endpoint,
			colPushP256dh: sub.P256dh, colPushAuth: sub.Auth,
		})
		return err
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

// handlePushUnsubscribe forgets one of the caller's own devices, idempotently: a device that
// is not there, or is someone else's, is already as the caller asked.
func (m *Module) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	owner, id := mc.Principal.UserID.String(), chi.URLParam(r, "id")
	err := mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(pushSubscriptionKind)
		if err != nil {
			return err
		}
		own, err := ownPushRows(r.Context(), repo, owner)
		if err != nil {
			return err
		}
		for _, rec := range own {
			if rec.String(colPushEndpointSHA) != id {
				continue
			}
			rowID, err := model.ParseID(rec.String(model.ColID))
			if err != nil {
				return err
			}
			return repo.Delete(r.Context(), rowID)
		}
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// What a message says. The console shows the text in the person's language from the kind, so
// the message carries no sentence.
const (
	pushApproval = "approval" // a tool of the session waits for the person's decision
	pushFinished = "finished" // the session's tool exited cleanly
	pushFailed   = "failed"   // the session's tool died or could not run
)

// pushPayload is the message. It names a session by the person's own name for it, or by none,
// and never by what the session wrote: the console paints it on a lock screen. Encrypted to
// the device, so the browser's push service sees none of it.
type pushPayload struct {
	Kind string `json:"kind"`
	Name string `json:"name,omitempty"`
	// URL is a path on this console that opens the session, where an approval it waits for is
	// answered. Relative on purpose: the console resolves it against its own origin.
	URL string `json:"url"`
	// Tag makes a newer message about one session replace the older one on screen.
	Tag string `json:"tag"`
}

func pushMessageFor(kind string, run model.Record) ([]byte, webpush.Options) {
	ref := run.String(colRunRef)
	name := []rune(run.String(colRunName))
	if len(name) > maxPushNameRunes {
		name = name[:maxPushNameRunes]
	}
	body, _ := json.Marshal(pushPayload{
		Kind: kind, Name: string(name),
		URL: "/sessions?session=" + url.QueryEscape("run:"+ref) + "&pane=narrative",
		Tag: "run:" + ref,
	})
	if kind == pushApproval {
		return body, webpush.Options{TTL: pushApprovalTTL, Urgency: webpush.UrgencyHigh}
	}
	return body, webpush.Options{TTL: pushEndedTTL, Urgency: webpush.UrgencyNormal}
}

// pushAttention tells the person who launched the run that it needs them or has ended. It
// never blocks the run's own transition and never fails it: the push is a courtesy to a
// closed tab, and the console still shows the state. Delivery runs on its own goroutine,
// counted so Stop can wait for it.
func (m *Module) pushAttention(tenant model.TenantID, run model.Record, kind string) {
	sender := m.pushSender()
	owner := run.String(colRunLauncherUserID)
	if sender == nil || owner == "" || m.Data == nil {
		return
	}
	body, opts := pushMessageFor(kind, run)
	m.rt.mu.Lock()
	if m.rt.pushStopped {
		m.rt.mu.Unlock()
		return
	}
	m.rt.pushWG.Add(1)
	m.rt.mu.Unlock()
	go func() {
		defer m.rt.pushWG.Done()
		ctx, cancel := context.WithTimeout(context.Background(), pushDeliveryBudget)
		defer cancel()
		m.deliverPush(ctx, sender, tenant, owner, kind, body, opts)
	}()
}

// deliverPush sends one message to each of the owner's devices. A device the push service no
// longer knows is forgotten; any other failure keeps it, because a service that is down today
// answers tomorrow.
func (m *Module) deliverPush(ctx context.Context, sender PushSender, tenant model.TenantID, owner, kind string, body []byte, opts webpush.Options) {
	if m.rt.NotificationAllowed != nil {
		allowed, err := m.rt.NotificationAllowed(ctx, tenant, owner, kind)
		if err != nil {
			m.warnf("sessions: could not read notification choices", "err", redactErr(err))
			return
		}
		if !allowed {
			return
		}
	}
	var devices []model.Record
	if err := m.Data.View(ctx, tenant, func(sc store.Scope) error {
		repo, err := sc.Ext(pushSubscriptionKind)
		if err != nil {
			return err
		}
		devices, err = ownPushRows(ctx, repo, owner)
		return err
	}); err != nil {
		m.warnf("sessions: could not read the push devices", "err", redactErr(err))
		return
	}
	for _, rec := range devices {
		err := sender.Send(ctx, webpush.Subscription{
			Endpoint: rec.String(colPushEndpoint), P256dh: rec.String(colPushP256dh), Auth: rec.String(colPushAuth),
		}, body, opts)
		switch {
		case err == nil:
		case errors.Is(err, webpush.ErrGone):
			m.forgetPushDevice(ctx, tenant, rec)
		default:
			m.warnf("sessions: a push was not delivered", "err", redactErr(err))
		}
	}
}

func (m *Module) forgetPushDevice(ctx context.Context, tenant model.TenantID, rec model.Record) {
	id, err := model.ParseID(rec.String(model.ColID))
	if err == nil {
		err = m.Data.Mutate(ctx, tenant, func(sc store.Scope) error {
			repo, err := sc.Ext(pushSubscriptionKind)
			if err != nil {
				return err
			}
			return repo.Delete(ctx, id)
		})
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		m.warnf("sessions: could not forget a push device the service dropped", "err", redactErr(err))
	}
}

// stopPush ends new deliveries and waits, within ctx, for those in flight.
func (m *Module) stopPush(ctx context.Context) {
	m.rt.mu.Lock()
	m.rt.pushStopped = true
	m.rt.mu.Unlock()
	done := make(chan struct{})
	go func() { m.rt.pushWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
