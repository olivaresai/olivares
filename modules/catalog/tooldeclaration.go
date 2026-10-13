// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/olivaresai/olivares/core/api"
	"github.com/olivaresai/olivares/core/model"
	"github.com/olivaresai/olivares/core/store"
)

// ToolDeclaration is the pinned installable payload in an agent or MCP entry's
// spec.tool_install. It shares that entry's immutable approval and content hash.
// Archives contain bin/<program> and optional lib/<program> resources.
type ToolDeclaration struct {
	SourceURL     string `json:"source_url"`
	Version       string `json:"version"`
	SHA256        string `json:"sha256"`
	Program       string `json:"program"`
	ConfigHome    string `json:"config_home"`
	Kind          string `json:"kind"`
	ArchiveURL    string `json:"archive_url,omitempty"`
	ArchivePrefix string `json:"archive_prefix,omitempty"`
}

var ErrToolUnadmitted = errors.New("unadmitted_declaration: admit this source and pinned version in the catalog first")

func toolURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(raw, "\x00\r\n\\") {
		return errors.New("tool source must be an HTTPS URL without credentials, query or fragment")
	}
	return nil
}

// ArtifactURL maps a GitHub Git source to its pinned tag archive. Other Git
// hosts use the archive_url the curator explicitly pins; no build scripts run.
func (d ToolDeclaration) ArtifactURL() string {
	if d.ArchiveURL != "" {
		return d.ArchiveURL
	}
	u, err := url.Parse(d.SourceURL)
	if err == nil && u.Host == "github.com" {
		repo := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
		if len(strings.Split(repo, "/")) == 2 {
			return "https://codeload.github.com/" + repo + "/tar.gz/refs/tags/v" + d.Version
		}
	}
	return d.SourceURL
}

// Prefix is the single archive root stripped before confined placement.
func (d ToolDeclaration) Prefix() string {
	if d.ArchivePrefix != "" {
		return d.ArchivePrefix
	}
	if d.ArchiveURL == "" && d.ArtifactURL() != d.SourceURL {
		u, _ := url.Parse(d.SourceURL)
		parts := strings.Split(strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git"), "/")
		return parts[1] + "-" + d.Version
	}
	return ""
}

func (d ToolDeclaration) Driver() string { return "declared-" + d.Program }

func (d ToolDeclaration) Validate() error {
	if err := toolURL(d.SourceURL); err != nil {
		return err
	}
	if d.ArchiveURL != "" {
		if err := toolURL(d.ArchiveURL); err != nil {
			return err
		}
	}
	if strings.HasSuffix(d.SourceURL, ".git") && d.ArchiveURL == "" && d.ArtifactURL() == d.SourceURL {
		return errors.New("this Git source requires its pinned HTTPS archive_url")
	}
	if !validSemver(d.Version) || len(d.Version) > 64 || strings.Contains(d.Version, "..") {
		return errors.New("tool version must be a pinned semantic version")
	}
	if len(d.SHA256) != 64 || strings.Trim(d.SHA256, "0123456789abcdef") != "" {
		return errors.New("tool sha256 must be 64 lowercase hexadecimal characters")
	}
	for _, p := range []string{d.Program, d.ConfigHome} {
		if !validSlug(p) || strings.HasPrefix(p, "-") || strings.HasPrefix(p, "_") {
			return errors.New("tool program and config_home must be lowercase identifiers")
		}
	}
	if d.Kind != "cli" && d.Kind != "mcp" {
		return errors.New("tool kind must be cli or mcp")
	}
	if prefix := d.Prefix(); prefix != "" && (len(prefix) > 128 || strings.ContainsAny(prefix, "/\\\x00\r\n") || prefix == "." || prefix == "..") {
		return errors.New("archive_prefix must be a single directory name")
	}
	return nil
}

func parseToolDeclaration(spec map[string]any) (*ToolDeclaration, error) {
	value, ok := spec["tool_install"]
	if !ok {
		return nil, nil
	}
	b, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var d ToolDeclaration
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("tool_install: %w", err)
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return &d, nil
}

// ResolveToolDeclaration only returns a currently approved, integrity-verified
// declaration in this request's tenant. Duplicate sources refuse ambiguity.
func (m *Module) ResolveToolDeclaration(ctx context.Context, mc api.ModuleContext, source, version string) (*ToolDeclaration, error) {
	if mc.Data == nil || toolURL(source) != nil {
		return nil, ErrToolUnadmitted
	}
	version = strings.TrimPrefix(version, "v")
	var found *ToolDeclaration
	err := mc.Data.View(ctx, func(sc store.Scope) error {
		repo, err := sc.Ext(entryKind)
		if err != nil {
			return err
		}
		q := model.Query{Limit: 1000, Filters: []model.Filter{eq(colStatus, statusApproved), eq(colVersion, version)}}
		for {
			rows, page, err := repo.List(ctx, q)
			if err != nil {
				return err
			}
			for _, rec := range rows {
				d, err := parseToolDeclaration(parseSpec(rec.String(colSpec)))
				if err != nil {
					return err
				}
				if d == nil || d.SourceURL != source || d.Version != version {
					continue
				}
				kind := rec.String(colEntryKind)
				if (d.Kind == "mcp" && kind != kindMCP) || (d.Kind == "cli" && kind != kindAgent) {
					return ErrToolUnadmitted
				}
				integrity := verify(rec.String(colContentHash), rec.String(colSignature), rec.String(colSignedBy), m.expectedFingerprint(), rec.String(colName), kind, rec.String(colSlug), rec.String(colVersion), rec.String(colSummary), rec.String(colOwnerRef), parseSpec(rec.String(colSpec)))
				if !integrity.Verified || found != nil {
					return ErrToolUnadmitted
				}
				found = d
			}
			if !page.HasMore {
				break
			}
			q.Cursor = page.Cursor
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if found == nil {
		return nil, ErrToolUnadmitted
	}
	return found, nil
}

// Admission uses the existing approval transaction, including its MCP policy
// gate, signing and audit. A supplied MCP attestation still uses its old route.
func (m *Module) admitDeclaredTool(w http.ResponseWriter, r *http.Request, mc api.ModuleContext) bool {
	var declared bool
	err := mc.Data.View(r.Context(), func(sc store.Scope) error {
		repo, err := sc.Ext(entryKind)
		if err != nil {
			return err
		}
		rec, err := repo.Get(r.Context(), model.ID(chi.URLParam(r, "id")))
		if err != nil {
			return err
		}
		d, err := parseToolDeclaration(parseSpec(rec.String(colSpec)))
		if err != nil {
			return err
		}
		declared = d != nil
		return nil
	})
	if err != nil {
		writeStoreError(w, err)
		return true
	}
	if !declared {
		return false
	}
	m.approveEntry(w, r, mc, true)
	return true
}
