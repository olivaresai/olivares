// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package auth

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/core/webaddr"
)

// ErrExposureNeedsStepUp applies to managed exposure, never initial startup.
var ErrExposureNeedsStepUp = errors.New("Set Sign-in settings → Administrative step-up to totp or passkey before enabling public exposure or removing its protection.")

// ConsoleExposureSettings is part of the existing deployment settings document.
// Enabled entries reserve protection before an external exposure act starts. A
// failed or uncertain act keeps its reservation until a confirmed stop.
// ponytail: native reservations survive stop/uninstall until native stop attestation can release them.
type ConsoleExposureSettings struct {
	PublicURL string          `json:"public_url,omitempty"`
	Enabled   map[string]bool `json:"enabled,omitempty"`
}

func exposureDocument(ctx context.Context, as store.AuthScope) (ConsoleExposureSettings, []model.DeploymentSettings, map[string]json.RawMessage, error) {
	var exposure ConsoleExposureSettings
	rows, _, err := as.DeploymentSettings().List(ctx, model.Query{})
	doc := map[string]json.RawMessage{}
	if err != nil {
		return exposure, rows, doc, err
	}
	if len(rows) > 0 {
		if err = json.Unmarshal([]byte(rows[0].Doc), &doc); err != nil {
			return exposure, rows, doc, err
		}
		if doc == nil {
			return exposure, rows, doc, errors.New("Deployment settings must be a JSON object.")
		}
		if data, ok := doc["console_exposure"]; ok {
			err = json.Unmarshal(data, &exposure)
		}
	}
	return exposure, rows, doc, err
}

func (x ConsoleExposureSettings) ManagedPublic() bool {
	if x.PublicURL != "" {
		return true
	}
	for _, enabled := range x.Enabled {
		if enabled {
			return true
		}
	}
	return false
}

func (a *Authenticator) ConsoleExposure(ctx context.Context) (ConsoleExposureSettings, error) {
	var exposure ConsoleExposureSettings
	err := a.st.AuthView(ctx, func(as store.AuthScope) error {
		var err error
		exposure, _, _, err = exposureDocument(ctx, as)
		return err
	})
	return exposure, err
}

func (a *Authenticator) SetConsolePublicURL(ctx context.Context, actor Principal, raw string) error {
	if raw != "" {
		addr, err := webaddr.Parse("public_url", raw)
		if err != nil {
			return err
		}
		if addr.Scheme != "https" {
			return errors.New("Public console exposure requires an HTTPS address.")
		}
		raw = addr.Origin
	}
	return a.changeExposure(ctx, actor, raw != "", false, func(x *ConsoleExposureSettings) {
		// Other nodes can still apply the old origin until their next restart. A
		// saved removal alone is no proof that all managed exposure has stopped.
		if x.PublicURL != "" {
			if x.Enabled == nil {
				x.Enabled = map[string]bool{}
			}
			x.Enabled["public-url"] = true
		}
		x.PublicURL = raw
	})
}

// SetManagedExposure reserves protection before enabling an appliance effect;
// disabling is called only after the native consumer confirms exposure stopped.
// The caller is the native identity owner, after independently checking the OS
// binding and fresh installation-wide administration evidence for this exact act.
func (a *Authenticator) SetManagedExposure(ctx context.Context, actor Principal, kind string, enabled bool) error {
	if !slices.Contains([]string{"cloudflared", "tailscale-funnel", "firewall", "nginx-proxy-manager"}, kind) {
		return errors.New("Unknown managed exposure.")
	}
	return a.changeExposure(ctx, actor, enabled, true, func(x *ConsoleExposureSettings) {
		if x.Enabled == nil {
			x.Enabled = map[string]bool{}
		}
		if enabled {
			x.Enabled[kind] = true
		} else {
			delete(x.Enabled, kind)
		}
	})
}

func (a *Authenticator) changeExposure(ctx context.Context, actor Principal, enabling, native bool, change func(*ConsoleExposureSettings)) error {
	if actor.Kind != KindUser || (!native && actor.CredID.IsZero()) || actor.UserID.IsZero() {
		return ErrUnauthenticated
	}
	return a.st.AuthMutate(ctx, func(as store.AuthScope) error {
		if err := LockDeploymentSettings(ctx, as); err != nil {
			return err
		}
		policies, _, err := as.AuthPolicy().List(ctx, model.Query{})
		if err != nil {
			return err
		}
		policy := StepUpNone
		if len(policies) > 0 && policies[0].AdminStepUp != "" {
			policy = policies[0].AdminStepUp
			if !ValidStepUpPolicy(policy) {
				return ErrStepUpPolicyInvalid
			}
		}
		if enabling && policy == StepUpNone {
			return ErrExposureNeedsStepUp
		}
		if !native && !StepUpSatisfied(WithStepUpSource(ctx, func(context.Context) (string, error) { return policy, nil }), actor) {
			return StepUpRequiredFor(policy)
		}
		exposure, rows, doc, err := exposureDocument(ctx, as)
		if err != nil {
			return err
		}
		change(&exposure)
		doc["console_exposure"], err = json.Marshal(exposure)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			doc["version"] = json.RawMessage(`"olivares.deployment.settings.v1"`)
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			_, err = as.DeploymentSettings().Create(ctx, model.DeploymentSettings{Doc: string(raw)})
		} else {
			row := rows[0]
			row.Doc = string(raw)
			_, err = as.DeploymentSettings().Update(ctx, row)
		}
		if err != nil {
			return err
		}
		_, err = as.Audit().Append(ctx, model.AuditDraft{Actor: actor.Actor(), ActorKind: actor.ActorKind(), Action: "console.exposure", TargetKind: "core.deployment_settings", Meta: map[string]any{"managed_public": exposure.ManagedPublic()}})
		return err
	})
}

// LockDeploymentSettings serializes all writers of the singleton document with
// the policy decision that protects managed exposure, including on PostgreSQL.
func LockDeploymentSettings(ctx context.Context, as store.AuthScope) error {
	return lockAuthTransaction(ctx, as, "auth.deployment-settings")
}
