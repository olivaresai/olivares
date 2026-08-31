// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package fscontent

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/olivaresai/olivares/connectors/contentsource"
	"github.com/olivaresai/olivares/connectors/internal/content"
	"github.com/olivaresai/olivares/sdk"
)

// Config field keys the Descriptor declares.
const (
	fMode          = "mode"
	fRoot          = "root"
	fInclude       = "include"
	fExclude       = "exclude"
	fMaxFileBytes  = "max_file_bytes"
	fMaxFiles      = "max_files"
	fMaxTotalBytes = "max_total_bytes"
	fTextOnly      = "text_only"
	fMapPOSIXACL   = "map_posix_acl"
	fClassifyDef   = "classification"
	fClassXattr    = "classification_xattr"
	fLabelsXattr   = "labels_xattr"
	fUserPrefix    = "user_prefix"
	fGroupPrefix   = "group_prefix"
	fSpaceRef      = "space_ref"
)

// Defaults + ceilings.
const (
	defaultMaxFileBytes  = content.MaxBodyBytes // 1 MiB
	defaultMaxFiles      = 100_000
	defaultMaxTotalBytes = int64(1) << 30 // 1 GiB read budget for a full walk
	defaultClassXattr    = "user.classification"
	defaultLabelsXattr   = "user.olivares.labels"
	defaultUserPrefix    = "user:"
	defaultGroupPrefix   = "group:"

	maxFilesCeiling = 5_000_000
)

// sourceConfig is the parsed, validated filesystem source definition.
type sourceConfig struct {
	mode string // reported by Mode(); a filesystem read is "live"

	root     string
	include  []string // relative globs; empty ⇒ include everything
	exclude  []string // relative globs
	spaceRef string

	maxFileBytes  int
	maxFiles      int
	maxTotalBytes int64
	textOnly      bool
	mapPOSIXACL   bool

	classifyDefault string
	classXattr      string
	labelsXattr     string
	userPrefix      string
	groupPrefix     string

	// extractor, when non-nil, turns a sniffed rich document (OOXML today) into text
	// via an out-of-process sandboxed helper the engine injects (WithExtractor). When
	// nil the connector never attempts extraction — a rich document is a counted skip.
	extractor       contentsource.RichDocExtractor
	extractRichDocs bool // == (extractor != nil); the walk includes OOXML files only then
}

// parseConfig reads and validates the operator's settings. A source with no `root`
// opens as an EMPTY source (declared offline), never a hard failure — the caller
// handles that; parseConfig only rejects malformed values.
func parseConfig(cfg sdk.Config) (sourceConfig, error) {
	get := func(k string) string { return strings.TrimSpace(cfg.Get(k)) }

	sc := sourceConfig{
		mode:            normalizeMode(get(fMode)),
		root:            get(fRoot),
		include:         splitList(get(fInclude)),
		exclude:         splitList(get(fExclude)),
		spaceRef:        get(fSpaceRef),
		textOnly:        boolOr(get(fTextOnly), true),
		mapPOSIXACL:     boolOr(get(fMapPOSIXACL), true),
		classifyDefault: get(fClassifyDef),
		classXattr:      orDefault(get(fClassXattr), defaultClassXattr),
		labelsXattr:     orDefault(get(fLabelsXattr), defaultLabelsXattr),
		userPrefix:      orDefault(get(fUserPrefix), defaultUserPrefix),
		groupPrefix:     orDefault(get(fGroupPrefix), defaultGroupPrefix),
	}

	n, err := parseIntDefault(get(fMaxFileBytes), defaultMaxFileBytes)
	if err != nil {
		return sourceConfig{}, err
	}
	if n > content.MaxBodyBytes {
		n = content.MaxBodyBytes
	}
	sc.maxFileBytes = n

	files, err := parseIntDefault(get(fMaxFiles), defaultMaxFiles)
	if err != nil {
		return sourceConfig{}, err
	}
	if files > maxFilesCeiling {
		files = maxFilesCeiling
	}
	sc.maxFiles = files

	total, err := parseInt64Default(get(fMaxTotalBytes), defaultMaxTotalBytes)
	if err != nil {
		return sourceConfig{}, err
	}
	sc.maxTotalBytes = total

	// Validate globs early (a malformed pattern is an operator error at Open).
	for _, g := range append(append([]string{}, sc.include...), sc.exclude...) {
		if err := validateGlob(g); err != nil {
			return sourceConfig{}, err
		}
	}
	return sc, nil
}

const (
	modeLive   = "live"
	modeExport = "export"
)

func normalizeMode(m string) string {
	if strings.EqualFold(strings.TrimSpace(m), modeExport) {
		return modeExport
	}
	return modeLive
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func boolOr(v string, def bool) bool {
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func splitList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseIntDefault(s string, def int) (int, error) {
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("fscontent: invalid integer %q: %w", s, err)
	}
	if n <= 0 {
		return 0, errors.New("fscontent: value must be positive")
	}
	return n, nil
}

func parseInt64Default(s string, def int64) (int64, error) {
	if s == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("fscontent: invalid integer %q: %w", s, err)
	}
	if n <= 0 {
		return 0, errors.New("fscontent: value must be positive")
	}
	return n, nil
}
