// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package governance

import (
	"time"

	codexmanagedconfig "github.com/olivaresai/olivares/connectors/codex-managed-config"
	geminicli "github.com/olivaresai/olivares/connectors/gemini-cli"
	"github.com/olivaresai/olivares/connectors/grok"
	"github.com/olivaresai/olivares/connectors/managedsettings"
	"github.com/olivaresai/olivares/connectors/opencode"
	sdkmodel "github.com/olivaresai/olivares/sdk/model"
)

// surfaceComparers maps each surface that can be compared with a host's file to the
// comparison of its tool: the published (PERMITTED) document, the file the host reports
// (OBSERVED), and the findings between them. The Claude Code hooks, managed-mcp and sandbox
// surfaces are absent: their drift is the connector findings stream. This table is the one
// place that says which surfaces have a content diff.
var surfaceComparers = map[string]func(scope string, authored, observed []byte, at time.Time) ([]sdkmodel.FindingReport, error){
	surfaceManagedSettings: managedsettings.VerifyDriftJSON,
	surfaceCodexRequirements: func(scope string, authored, observed []byte, at time.Time) ([]sdkmodel.FindingReport, error) {
		req, err := codexmanagedconfig.ParseRequirementsTOML(authored)
		if err != nil {
			return nil, err
		}
		typed := codexmanagedconfig.VerifyDriftTOML(scope, codexmanagedconfig.Policy{Requirements: req}, observed, nil, at)
		exact, err := codexmanagedconfig.VerifyRequirementsFileTOML(scope, authored, observed, at)
		return typedOrExact(typed, exact), err
	},
	surfaceCodexManagedConfig: func(scope string, authored, observed []byte, at time.Time) ([]sdkmodel.FindingReport, error) {
		defaults, err := codexmanagedconfig.ParseManagedConfigTOML(authored)
		if err != nil {
			return nil, err
		}
		typed := codexmanagedconfig.VerifyDriftTOML(scope, codexmanagedconfig.Policy{Defaults: defaults}, nil, observed, at)
		exact, err := codexmanagedconfig.VerifyManagedConfigFileTOML(scope, authored, observed, at)
		return typedOrExact(typed, exact), err
	},
	surfaceGrokRequirements:  grok.VerifyDriftTOML,
	surfaceOpenCodeConfig:    opencode.VerifyDriftJSON,
	surfaceGeminiCLISettings: geminicli.VerifyDriftJSON,
}

// typedOrExact picks the Codex findings of one comparison. The typed model says what a
// finding means (an allowlist that widens, a default that drifts); the file-level comparison
// covers every key the typed model does not hold. Both describe the same drifting fact when
// the typed model sees one, so it is reported once, by the typed model.
func typedOrExact(typed, exact []sdkmodel.FindingReport) []sdkmodel.FindingReport {
	if len(typed) > 0 {
		return typed
	}
	return exact
}

// surfaceHasContentDiff reports whether the published document of a surface can be
// compared with the file a host reports.
func surfaceHasContentDiff(surface string) bool {
	_, ok := surfaceComparers[surface]
	return ok
}

// surfaceDrift compares the published document of a surface with the file a host reports,
// through the tool's own connector, so the finding a publish or a check-in records is the
// one the connector's read-only source would raise. supported is false for a surface without
// a content diff. A malformed authored document is an error: it must never have been
// published.
func surfaceDrift(surface, scope string, authored, observed []byte, at time.Time) (reports []sdkmodel.FindingReport, supported bool, err error) {
	compare, ok := surfaceComparers[surface]
	if !ok {
		return nil, false, nil
	}
	reports, err = compare(scope, authored, observed, at)
	return reports, true, err
}
