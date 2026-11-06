// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"strings"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

type gitAttributionSetting struct {
	AIAttribution string `json:"ai_attribution"`
}

func validAIAttribution(mode string) bool {
	return mode == "off" || mode == "assisted-by" || mode == "co-authored-by"
}

func attributionSetting(settings map[string]any) (string, bool, error) {
	raw, present := settings["ai_attribution"]
	if !present {
		return "off", false, nil
	}
	mode, ok := raw.(string)
	if !ok || !validAIAttribution(mode) {
		return "", true, badRequest("ai_attribution must be off, assisted-by or co-authored-by")
	}
	return mode, true, nil
}

// handleGitAttributionSettings reads or changes the organization's AI-assistance commit trailer. Changes require session administration and are audited. The default is off; an explicit setting overrides template defaults.
func (m *Module) handleGitAttributionSettings(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) {
	mode := "off"
	var err error
	if r.Method == http.MethodGet {
		err = mc.Data.View(r.Context(), func(sc store.Scope) error {
			org, err := sc.Org(r.Context())
			if err != nil {
				return err
			}
			mode, _, err = attributionSetting(org.Settings)
			return err
		})
	} else {
		var body gitAttributionSetting
		if !decodeJSONBody(w, r, &body) {
			return
		}
		if !validAIAttribution(body.AIAttribution) {
			writeRunErr(w, badRequest("ai_attribution must be off, assisted-by or co-authored-by"))
			return
		}
		mode = body.AIAttribution
		err = mc.Data.Mutate(r.Context(), func(sc store.Scope) error {
			org, err := sc.Org(r.Context())
			if err != nil {
				return err
			}
			settings := maps.Clone(org.Settings)
			if settings == nil {
				settings = map[string]any{}
			}
			settings["ai_attribution"] = mode
			if _, err := sc.SetOrgSettings(r.Context(), settings, org.Version); err != nil {
				return err
			}
			_, err = sc.Audit().Append(r.Context(), model.AuditDraft{Actor: mc.Principal.Actor(), ActorKind: mc.Principal.ActorKind(), Action: "sessions.git_attribution.changed", TargetKind: "sessions.git_attribution", Meta: map[string]any{"ai_attribution": mode}})
			return err
		})
	}
	if err != nil {
		writeRunErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, gitAttributionSetting{mode})
}

// ReadGitAttribution resolves only the effective commit attribution mode from a
// tenant-pinned read transaction. The engine uses this projection so a folder's
// caller needs no access to the organization's row or the template catalog.
func ReadGitAttribution(ctx context.Context, sc store.Scope, templateID string) (string, error) {
	org, err := sc.Org(ctx)
	if err != nil {
		return "", err
	}
	mode, explicit, err := attributionSetting(org.Settings)
	if err != nil || explicit || templateID == "" {
		return mode, err
	}
	repo, err := sc.Ext(templateKind)
	if err != nil {
		return "", err
	}
	rec, err := repo.Get(ctx, model.ID(templateID))
	if err != nil {
		return "", err
	}
	var body tplBody
	if raw := rec.String(colTplBody); raw != "" {
		if err := json.Unmarshal([]byte(raw), &body); err != nil {
			return "", fmt.Errorf("read commit attribution template: %w", err)
		}
	}
	if body.Settings != nil && body.Settings.AIAttribution != "" {
		mode = body.Settings.AIAttribution
	}
	if !validAIAttribution(mode) {
		return "", badRequest("invalid template ai_attribution")
	}
	return mode, nil
}

// commitMessage is shared by the panel and publication paths. Git identifies
// existing trailers; the session cannot submit a DCO and preserves other metadata.
func (g sessionGit) commitMessage(ctx context.Context, message string) (string, error) {
	trailers, err := g.exec(ctx, []string{"interpret-trailers", "--no-divider", "--parse"}, message, nil)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(trailers+"\n"+message, "\n") {
		key, _, _ := strings.Cut(line, ":")
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "signed-off-by":
			return "", badRequest("Signed-off-by is reserved for the human submitter")
		}
	}
	mode := "off"
	if g.tenant != "" && g.module.Data != nil {
		if g.module.GitAttribution != nil {
			mode, err = g.module.GitAttribution(ctx, g.tenant, g.record.String(colTemplateID))
		} else {
			err = g.module.Data.View(ctx, g.tenant, func(sc store.Scope) error {
				var err error
				mode, err = ReadGitAttribution(ctx, sc, g.record.String(colTemplateID))
				return err
			})
		}
		if err != nil {
			return "", err
		}
	}
	if mode == "off" {
		return message, nil
	}
	driver, modelName := g.record.String(colRunProfileDriver), g.record.String(colRunModelRef)
	if driver == "" {
		driver = providerDriverClaude
	}
	if modelName == "" {
		activity, err := g.module.runActivity(ctx, g.tenant, g.record.String(colRunRef))
		if err != nil {
			return "", err
		}
		modelName = activity.Model
	}
	if !validOpenProviderTerm(driver) || !validOpenProviderTerm(modelName) {
		return "", badRequest("AI attribution requires the session's recorded tool and model")
	}
	key := "Assisted-by"
	if mode == "co-authored-by" {
		key = "Co-authored-by"
	}
	value := driver + " (" + modelName + ")"
	for _, line := range strings.Split(trailers, "\n") {
		name, existing, _ := strings.Cut(line, ":")
		if strings.EqualFold(strings.TrimSpace(name), key) && strings.TrimSpace(existing) == value {
			return message, nil
		}
	}
	separator := "\n\n"
	if strings.TrimSpace(trailers) != "" {
		separator = "\n"
	}
	return strings.TrimRight(message, "\r\n") + separator + key + ": " + value + "\n", nil
}
