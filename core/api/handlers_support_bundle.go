// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/olivaresai/olivares/core/auth"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/secret"
	"github.com/olivaresai/olivares/core/store"
	"github.com/olivaresai/olivares/core/supportbundle"
)

const supportBundleSkippedRedactor = "skipped: canonical support-bundle redactor not configured\n"

// handleSupportBundle builds the console support archive from in-process data
// only. It never invokes journalctl or loops back through HTTP.
func (s *Server) handleSupportBundle(w http.ResponseWriter, r *http.Request) {
	p, ok := s.authzSystem(w, r, "system:admin")
	if !ok {
		return
	}
	if !s.requireAAL3(w, r, p) {
		return
	}

	assembler := supportbundle.NewAssembler()
	if err := s.assembleConsoleSupportBundle(r.Context(), assembler); err != nil {
		s.writeError(w, r, err)
		return
	}

	tempDir, err := os.MkdirTemp("", "olivares-console-support-")
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	createdAt := s.clock.Now().Time().UTC()
	filename := "olivares-support-" + createdAt.Format("20060102-150405Z") + ".tar.gz"
	bundlePath := filepath.Join(tempDir, filename)
	containsSensitive := s.supportBundleContainsSensitive
	if containsSensitive == nil {
		containsSensitive = conservativeSupportBundleGuard
	}
	manifestDigest, err := supportbundle.Write(
		bundlePath, s.version, createdAt, assembler, containsSensitive,
	)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	f, err := os.Open(bundlePath)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if info.Mode().Perm() != 0o600 {
		s.writeError(w, r, fmt.Errorf("support bundle: temporary archive mode is %o, want 600", info.Mode().Perm()))
		return
	}

	sections := assembler.Paths()
	if err := s.auditConsoleSupportBundle(r.Context(), p, sections, info.Size()); err != nil {
		s.writeError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Olivares-Manifest-SHA256", manifestDigest)
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
}

func (s *Server) assembleConsoleSupportBundle(ctx context.Context, assembler *supportbundle.Assembler) error {
	if err := s.addSupportConfig(assembler); err != nil {
		return err
	}
	if err := s.addSupportStatus(ctx, assembler); err != nil {
		return err
	}
	if err := s.addSupportLogs(assembler); err != nil {
		return err
	}
	if err := addSupportManifestNote(assembler); err != nil {
		return err
	}
	return s.addSupportSecretMetadata(ctx, assembler)
}

func (s *Server) addSupportConfig(assembler *supportbundle.Assembler) error {
	projection := s.effectiveConfigProjection()
	var out strings.Builder
	redactions := 0
	for _, entry := range projection.Entries {
		if !validEffectiveConfigKey(entry.Key) {
			continue
		}
		value := strings.NewReplacer("\r", `\r`, "\n", `\n`).Replace(entry.Value)
		if s.supportBundleRedact == nil && !entry.Redacted {
			value = effectiveConfigRedactedValue
			entry.Redacted = true
		}
		if entry.Redacted {
			redactions++
		}
		_, _ = fmt.Fprintf(&out, "%s=%s\n", entry.Key, value)
	}
	source := "live effective config registry (env + activation)"
	if s.supportBundleRedact == nil {
		source += "; non-secret values conservatively redacted because the canonical redactor is unavailable"
	}
	return assembler.Add("config/effective.txt", source, []byte(out.String()), redactions)
}

func validEffectiveConfigKey(key string) bool {
	if key == "" {
		return false
	}
	for _, r := range key {
		if r != '_' && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func (s *Server) addSupportStatus(ctx context.Context, assembler *supportbundle.Assembler) error {
	raw, err := json.MarshalIndent(s.publicStatusProjection(ctx, false), "", "  ")
	if err != nil {
		return fmt.Errorf("support bundle: marshal public status: %w", err)
	}
	raw = append(raw, '\n')
	redacted, count := s.redactSupportBundleText(raw)
	return assembler.Add("status/status.json", "internal GET /status projection", redacted, count)
}

func (s *Server) addSupportLogs(assembler *supportbundle.Assembler) error {
	if s.logBroker == nil {
		return assembler.Add(
			"logs/engine.log",
			"skipped: API log broker not configured",
			[]byte("skipped: API log broker not configured\n"),
			0,
		)
	}
	if s.supportBundleRedact == nil {
		return assembler.Add(
			"logs/engine.log",
			"skipped: canonical support-bundle redactor not configured",
			[]byte(supportBundleSkippedRedactor),
			0,
		)
	}

	entries, _ := s.logBroker.Buffer(LogFilter{}, 0) // pass-all: every captured level
	// The match count is redundant here: limit 0 means "everything", so the page IS the set.
	var raw bytes.Buffer
	for _, entry := range entries {
		line, err := json.Marshal(entry)
		if err != nil {
			return fmt.Errorf("support bundle: marshal buffered log entry: %w", err)
		}
		raw.Write(line)
		raw.WriteByte('\n')
	}
	if raw.Len() == 0 {
		raw.WriteString("no buffered logs\n")
	}
	redacted, count := s.redactSupportBundleText(raw.Bytes())
	return assembler.Add("logs/engine.log", "API LogBroker ring snapshot", redacted, count)
}

func addSupportManifestNote(assembler *supportbundle.Assembler) error {
	note := struct {
		Skipped string `json:"skipped"`
	}{
		Skipped: "compiled schema manifest is owned by the CLI composition root and is unavailable in the API layer",
	}
	raw, err := json.MarshalIndent(note, "", "  ")
	if err != nil {
		return err
	}
	return assembler.Add(
		"manifests/schema.json",
		"skipped: schema manifest unavailable without importing cmd/olivares",
		append(raw, '\n'),
		0,
	)
}

func (s *Server) addSupportSecretMetadata(ctx context.Context, assembler *supportbundle.Assembler) error {
	if s.secretStore == nil {
		return assembler.Add(
			"secrets/inventory.txt",
			"skipped: runtime secret-store metadata seam not configured",
			[]byte("skipped: runtime secret-store metadata seam not configured\n"),
			0,
		)
	}
	if s.supportBundleRedact == nil {
		return assembler.Add(
			"secrets/inventory.txt",
			"skipped: canonical support-bundle redactor not configured",
			[]byte(supportBundleSkippedRedactor),
			0,
		)
	}

	views, err := s.secretStore.List(ctx, auth.GlobalSecretScope)
	if err != nil {
		return fmt.Errorf("support bundle: list secret metadata: %w", err)
	}
	var raw strings.Builder
	if len(views) == 0 {
		raw.WriteString("no secrets stored\n")
	} else {
		tw := tabwriter.NewWriter(&raw, 0, 2, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "NAME\tHINT\tDESCRIPTION\tUPDATED")
		for _, view := range views {
			updated := ""
			if !view.UpdatedAt.IsZero() {
				updated = view.UpdatedAt.String()
			}
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", view.Name, view.Hint, view.Description, updated)
		}
		if err := tw.Flush(); err != nil {
			return fmt.Errorf("support bundle: format secret metadata: %w", err)
		}
	}
	redacted, count := s.redactSupportBundleText([]byte(raw.String()))
	return assembler.Add("secrets/inventory.txt", "runtime secret-store metadata (List only)", redacted, count)
}

func (s *Server) redactSupportBundleText(raw []byte) ([]byte, int) {
	if s.supportBundleRedact == nil {
		return append([]byte(nil), raw...), 0
	}
	redacted, count := s.supportBundleRedact(string(raw))
	return []byte(redacted), count
}

func conservativeSupportBundleGuard(text string) bool {
	low := strings.ToLower(text)
	if strings.Contains(low, "private key-----") || secret.ContainsInlineCredential(text) {
		return true
	}
	for _, line := range strings.Split(text, "\n") {
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "x" || value == effectiveConfigRedactedValue ||
			supportbundle.IsExactSecretReference(value) {
			continue
		}
		if effectiveConfigKeyRequiresRedaction(key, value) {
			return true
		}
	}
	return false
}

func (s *Server) auditConsoleSupportBundle(
	ctx context.Context,
	p auth.Principal,
	sections []string,
	size int64,
) error {
	return s.st.Mutate(ctx, model.SystemTenantID, func(sc store.Scope) error {
		event, err := sc.Audit().Append(ctx, model.AuditDraft{
			Actor: p.Actor(), ActorKind: p.ActorKind(),
			Action: "console.support_bundle", TargetKind: model.Kind("core.support_bundle"),
			Meta: map[string]any{
				"sections": append([]string(nil), sections...),
				"bytes":    size,
			},
		})
		if err != nil {
			return err
		}
		if event.Seq == 0 {
			return store.ErrAuditSpoolFull
		}
		return nil
	})
}
