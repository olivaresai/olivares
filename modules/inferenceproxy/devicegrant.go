// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package inferenceproxy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

const (
	DeviceGrantPending  = "pending"
	DeviceGrantApproved = "approved"
	DeviceGrantDenied   = "denied"
	DeviceGrantConsumed = "consumed"
	DeviceGrantExpired  = "expired"
)

type DeviceGrantPollStatus string

const (
	DeviceGrantPollPending  DeviceGrantPollStatus = "pending"
	DeviceGrantPollSlowDown DeviceGrantPollStatus = "slow_down"
	DeviceGrantPollApproved DeviceGrantPollStatus = "approved"
	DeviceGrantPollDenied   DeviceGrantPollStatus = "denied"
	DeviceGrantPollExpired  DeviceGrantPollStatus = "expired"
	DeviceGrantPollConsumed DeviceGrantPollStatus = "consumed"
)

var (
	ErrDeviceGrantNotFound = errors.New("inferenceproxy: device grant not found")
	ErrDeviceGrantExpired  = errors.New("inferenceproxy: device grant expired")
	ErrDeviceGrantConsumed = errors.New("inferenceproxy: device grant consumed")
	ErrDeviceGrantDenied   = errors.New("inferenceproxy: device grant denied")
)

type DeviceGrant struct {
	ID         model.ID
	DeviceCode string
	UserCode   string
	State      string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	ApprovedBy string
	Tenant     model.TenantID
	LastPollAt time.Time
}

type approveDeviceGrantRequest struct {
	UserCode string `json:"user_code"`
	Deny     bool   `json:"deny,omitempty"`
}

// CreateDeviceGrant stores a pending OAuth device grant under storageTenant. When no
// tenant can be resolved yet, callers may use model.SystemTenantID; the approving
// tenant is recorded later in colGrantTenant.
func (m *Module) CreateDeviceGrant(ctx context.Context, storageTenant model.TenantID, deviceCode, userCode string, now time.Time, ttl time.Duration) (DeviceGrant, error) {
	data := m.moduleData()
	if data == nil {
		return DeviceGrant{}, ErrNotWired
	}
	storageTenant = normalizeStorageTenant(storageTenant)
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	now = now.UTC()
	var out DeviceGrant
	err := data.Mutate(ctx, storageTenant, func(sc store.Scope) error {
		repo, err := sc.Ext(deviceGrantKind)
		if err != nil {
			return err
		}
		rec, err := repo.Create(ctx, model.Record{
			colDeviceCode:   strings.TrimSpace(deviceCode),
			colUserCode:     normalizeUserCode(userCode),
			colGrantState:   DeviceGrantPending,
			colGrantExpires: model.NewTimestamp(now.Add(ttl)).String(),
		})
		if err != nil {
			return err
		}
		out = deviceGrantFromRecord(rec)
		return nil
	})
	return out, err
}

// PollDeviceGrant implements RFC 8628 polling state, including lazy expiry and
// interval enforcement for pending grants.
func (m *Module) PollDeviceGrant(ctx context.Context, storageTenant model.TenantID, deviceCode string, now time.Time, interval time.Duration) (DeviceGrant, DeviceGrantPollStatus, error) {
	data := m.moduleData()
	if data == nil {
		return DeviceGrant{}, "", ErrNotWired
	}
	storageTenant = normalizeStorageTenant(storageTenant)
	now = now.UTC()
	if interval <= 0 {
		interval = 5 * time.Second
	}
	var out DeviceGrant
	var status DeviceGrantPollStatus
	err := data.Mutate(ctx, storageTenant, func(sc store.Scope) error {
		rec, ok, err := findDeviceGrantByDeviceCode(ctx, sc, strings.TrimSpace(deviceCode))
		if err != nil || !ok {
			return firstErr(err, ErrDeviceGrantNotFound)
		}
		grant := deviceGrantFromRecord(rec)
		if grantExpired(grant, now) && (grant.State == DeviceGrantPending || grant.State == DeviceGrantApproved) {
			grant, err = updateDeviceGrantState(ctx, sc, rec, DeviceGrantExpired, now)
			if err != nil {
				return err
			}
			out, status = grant, DeviceGrantPollExpired
			return nil
		}
		switch grant.State {
		case DeviceGrantPending:
			if !grant.LastPollAt.IsZero() && now.Sub(grant.LastPollAt) < interval {
				grant, err = updateDeviceGrantLastPoll(ctx, sc, rec, now)
				if err != nil {
					return err
				}
				out, status = grant, DeviceGrantPollSlowDown
				return nil
			}
			grant, err = updateDeviceGrantLastPoll(ctx, sc, rec, now)
			if err != nil {
				return err
			}
			out, status = grant, DeviceGrantPollPending
		case DeviceGrantApproved:
			out, status = grant, DeviceGrantPollApproved
		case DeviceGrantDenied:
			out, status = grant, DeviceGrantPollDenied
		case DeviceGrantConsumed:
			out, status = grant, DeviceGrantPollConsumed
		case DeviceGrantExpired:
			out, status = grant, DeviceGrantPollExpired
		default:
			return ErrDeviceGrantNotFound
		}
		return nil
	})
	return out, status, err
}

// ConsumeDeviceGrant marks an approved grant consumed. The caller should mint only after
// this succeeds, so concurrent token polls cannot mint the same grant twice.
func (m *Module) ConsumeDeviceGrant(ctx context.Context, storageTenant model.TenantID, id model.ID, now time.Time) (DeviceGrant, error) {
	data := m.moduleData()
	if data == nil {
		return DeviceGrant{}, ErrNotWired
	}
	storageTenant = normalizeStorageTenant(storageTenant)
	now = now.UTC()
	var out DeviceGrant
	err := data.Mutate(ctx, storageTenant, func(sc store.Scope) error {
		repo, err := sc.Ext(deviceGrantKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(ctx, id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return ErrDeviceGrantNotFound
			}
			return err
		}
		grant := deviceGrantFromRecord(rec)
		if grantExpired(grant, now) && grant.State == DeviceGrantApproved {
			_, err = updateDeviceGrantState(ctx, sc, rec, DeviceGrantExpired, now)
			if err != nil {
				return err
			}
			return ErrDeviceGrantExpired
		}
		switch grant.State {
		case DeviceGrantApproved:
			updated, err := updateDeviceGrantState(ctx, sc, rec, DeviceGrantConsumed, now)
			if err != nil {
				return err
			}
			out = updated
			return nil
		case DeviceGrantDenied:
			return ErrDeviceGrantDenied
		case DeviceGrantExpired:
			return ErrDeviceGrantExpired
		default:
			return ErrDeviceGrantConsumed
		}
	})
	return out, err
}

// ApproveDeviceGrant binds a pending grant to the authenticated approver's tenant and
// actor. It first checks the approver tenant, then the system tenant for deployments
// where unauthenticated device_authorization could not pre-resolve a tenant.
func (m *Module) ApproveDeviceGrant(ctx context.Context, tenant model.TenantID, actor, actorKind, userCode string, deny bool) (DeviceGrant, error) {
	tenant = normalizeStorageTenant(tenant)
	code := normalizeUserCode(userCode)
	if code == "" {
		return DeviceGrant{}, ErrDeviceGrantNotFound
	}
	grant, err := m.approveDeviceGrantInTenant(ctx, tenant, tenant, actor, actorKind, code, deny)
	if err == nil || !errors.Is(err, ErrDeviceGrantNotFound) || tenant == model.SystemTenantID {
		return grant, err
	}
	return m.approveDeviceGrantInTenant(ctx, model.SystemTenantID, tenant, actor, actorKind, code, deny)
}

func (m *Module) handleApproveDeviceGrant(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	if !requireAAL3(w, mc) {
		return
	}
	var in approveDeviceGrantRequest
	if !decodeJSON(w, r, &in) {
		return
	}
	grant, err := m.ApproveDeviceGrant(r.Context(), mc.Tenant, mc.Principal.Actor(), mc.Principal.ActorKind(), in.UserCode, in.Deny)
	if err != nil {
		switch {
		case errors.Is(err, ErrDeviceGrantNotFound):
			writeJSON(w, http.StatusNotFound, errorBody("device code not found"))
		case errors.Is(err, ErrDeviceGrantExpired), errors.Is(err, ErrDeviceGrantConsumed):
			writeJSON(w, http.StatusGone, errorBody("device code expired"))
		default:
			writeStoreError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": grant.ID.String(), "user_code": grant.UserCode, "state": grant.State,
	})
}

func (m *Module) approveDeviceGrantInTenant(ctx context.Context, storageTenant, approvedTenant model.TenantID, actor, actorKind, userCode string, deny bool) (DeviceGrant, error) {
	data := m.moduleData()
	if data == nil {
		return DeviceGrant{}, ErrNotWired
	}
	now := m.clock.Now().Time().UTC()
	var out DeviceGrant
	err := data.Mutate(ctx, storageTenant, func(sc store.Scope) error {
		rec, ok, err := findDeviceGrantByUserCode(ctx, sc, userCode)
		if err != nil || !ok {
			return firstErr(err, ErrDeviceGrantNotFound)
		}
		grant := deviceGrantFromRecord(rec)
		if grantExpired(grant, now) && grant.State == DeviceGrantPending {
			_, err = updateDeviceGrantState(ctx, sc, rec, DeviceGrantExpired, now)
			if err != nil {
				return err
			}
			return ErrDeviceGrantExpired
		}
		switch grant.State {
		case DeviceGrantPending:
			state := DeviceGrantApproved
			action := "inferenceproxy.device.approve"
			if deny {
				state = DeviceGrantDenied
				action = "inferenceproxy.device.deny"
			}
			rec[colGrantState] = state
			rec[colApprovedBy] = actor
			rec[colGrantTenant] = approvedTenant.String()
			repo, err := sc.Ext(deviceGrantKind)
			if err != nil {
				return err
			}
			updated, err := repo.Update(ctx, rec)
			if err != nil {
				return err
			}
			out = deviceGrantFromRecord(updated)
			_, err = sc.Audit().Append(ctx, model.AuditDraft{
				Actor: actor, ActorKind: actorKind, Action: action,
				TargetKind: deviceGrantKind, TargetID: out.ID,
				Meta: map[string]any{"user_code": out.UserCode, "state": out.State},
			})
			return err
		case DeviceGrantExpired:
			return ErrDeviceGrantExpired
		case DeviceGrantConsumed:
			return ErrDeviceGrantConsumed
		default:
			out = grant
			return nil
		}
	})
	return out, err
}

func findDeviceGrantByDeviceCode(ctx context.Context, sc store.Scope, code string) (model.Record, bool, error) {
	repo, err := sc.Ext(deviceGrantKind)
	if err != nil {
		return nil, false, err
	}
	recs, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colDeviceCode, code)}, Limit: 1})
	if err != nil || len(recs) == 0 {
		return nil, false, err
	}
	return recs[0], true, nil
}

func findDeviceGrantByUserCode(ctx context.Context, sc store.Scope, code string) (model.Record, bool, error) {
	repo, err := sc.Ext(deviceGrantKind)
	if err != nil {
		return nil, false, err
	}
	recs, _, err := repo.List(ctx, model.Query{Filters: []model.Filter{eq(colUserCode, code)}, Limit: 1})
	if err != nil || len(recs) == 0 {
		return nil, false, err
	}
	return recs[0], true, nil
}

func updateDeviceGrantState(ctx context.Context, sc store.Scope, rec model.Record, state string, _ time.Time) (DeviceGrant, error) {
	rec[colGrantState] = state
	repo, err := sc.Ext(deviceGrantKind)
	if err != nil {
		return DeviceGrant{}, err
	}
	updated, err := repo.Update(ctx, rec)
	if err != nil {
		return DeviceGrant{}, err
	}
	return deviceGrantFromRecord(updated), nil
}

func updateDeviceGrantLastPoll(ctx context.Context, sc store.Scope, rec model.Record, now time.Time) (DeviceGrant, error) {
	rec[colLastPollAt] = model.NewTimestamp(now).String()
	repo, err := sc.Ext(deviceGrantKind)
	if err != nil {
		return DeviceGrant{}, err
	}
	updated, err := repo.Update(ctx, rec)
	if err != nil {
		return DeviceGrant{}, err
	}
	return deviceGrantFromRecord(updated), nil
}

func deviceGrantFromRecord(rec model.Record) DeviceGrant {
	return DeviceGrant{
		ID:         model.ID(rec.String(model.ColID)),
		DeviceCode: rec.String(colDeviceCode),
		UserCode:   rec.String(colUserCode),
		State:      rec.String(colGrantState),
		CreatedAt:  recordTime(rec, model.ColCreatedAt),
		ExpiresAt:  recordTime(rec, colGrantExpires),
		ApprovedBy: rec.String(colApprovedBy),
		Tenant:     model.TenantID(rec.String(colGrantTenant)),
		LastPollAt: recordTime(rec, colLastPollAt),
	}
}

func recordTime(rec model.Record, col string) time.Time {
	if rec.IsNull(col) {
		return time.Time{}
	}
	ts, err := model.ParseTimestamp(rec.String(col))
	if err != nil {
		return time.Time{}
	}
	return ts.Time()
}

func grantExpired(grant DeviceGrant, now time.Time) bool {
	return !grant.ExpiresAt.IsZero() && !now.UTC().Before(grant.ExpiresAt)
}

func normalizeStorageTenant(tenant model.TenantID) model.TenantID {
	if tenant.IsZero() {
		return model.SystemTenantID
	}
	return tenant
}

func normalizeUserCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

func firstErr(err, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}
