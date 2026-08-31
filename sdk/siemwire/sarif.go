// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package siemwire

import (
	"encoding/json"
	"fmt"
)

const (
	sarifSchema              = "https://docs.oasis-open.org/sarif/sarif/v2.1.0/errata01/os/schemas/sarif-schema-2.1.0.json"
	sarifVersion             = "2.1.0"
	sarifSourceRoot          = "%SRCROOT%"
	sarifDescriptionMaxRunes = 1024
)

// ToolInfo identifies the SARIF producer and the automation category under
// which an ingester groups repeated uploads.
type ToolInfo struct {
	Name           string
	Version        string
	InformationURI string
	AutomationID   string
	// SourceRootURI is the absolute URI the result locations are relative to, if
	// the caller knows it. Empty is the normal case for an export that does not
	// know where a consumer checked the tree out: the %SRCROOT% base is still
	// declared, described, and left without a fabricated uri.
	SourceRootURI string
}

// SARIFFinding is the caller-resolved projection of one finding. The caller owns
// rule taxonomy, severity policy, artifact selection, and stable fingerprinting;
// SARIF only applies the 2.1.0 wire contract.
type SARIFFinding struct {
	RuleID           string
	RuleName         string
	ShortDescription string
	// HelpText is the plain-text remediation guidance a consumer shows when it
	// does not render markdown; HelpMarkdown is the optional rich form. SARIF
	// requires the text form whenever help is present, so a finding that supplies
	// only markdown gets it in both (raw markup in front of an analyst) — supply
	// HelpText.
	HelpText         string
	HelpMarkdown     string
	Level            string
	SecuritySeverity string
	Tags             []string
	Message          string
	ArtifactURI      string
	StartLine        int
	Fingerprint      string
}

type sarifLog struct {
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	Runs    []sarifRun `json:"runs"`
}

type sarifRun struct {
	AutomationDetails  sarifAutomationDetails  `json:"automationDetails"`
	Tool               sarifTool               `json:"tool"`
	OriginalURIBaseIDs map[string]sarifURIBase `json:"originalUriBaseIds"`
	Results            []sarifResult           `json:"results"`
}

// sarifURIBase is one originalUriBaseIds entry: what a uriBaseId in a result
// location resolves against. uri is omitted when the emitter does not know the
// consumer's checkout root — a described base with no uri is valid and honest,
// an invented absolute path is neither.
type sarifURIBase struct {
	URI         string    `json:"uri,omitempty"`
	Description sarifText `json:"description"`
}

type sarifAutomationDetails struct {
	ID string `json:"id"`
}

type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

type sarifDriver struct {
	Name           string                     `json:"name"`
	Version        string                     `json:"version,omitempty"`
	InformationURI string                     `json:"informationUri,omitempty"`
	Rules          []sarifReportingDescriptor `json:"rules"`
}

type sarifReportingDescriptor struct {
	ID                   string                `json:"id"`
	Name                 string                `json:"name,omitempty"`
	ShortDescription     sarifText             `json:"shortDescription"`
	Help                 *sarifMultiformatText `json:"help,omitempty"`
	DefaultConfiguration sarifConfiguration    `json:"defaultConfiguration"`
	Properties           sarifRuleProperties   `json:"properties"`
}

type sarifText struct {
	Text string `json:"text"`
}

type sarifMultiformatText struct {
	Text     string `json:"text"`
	Markdown string `json:"markdown,omitempty"`
}

type sarifConfiguration struct {
	Level string `json:"level"`
}

type sarifRuleProperties struct {
	Tags             []string `json:"tags"`
	SecuritySeverity string   `json:"security-severity"`
}

type sarifResult struct {
	RuleID              string                   `json:"ruleId"`
	RuleIndex           int                      `json:"ruleIndex"`
	Level               string                   `json:"level"`
	Message             sarifText                `json:"message"`
	Locations           []sarifLocation          `json:"locations"`
	PartialFingerprints sarifPartialFingerprints `json:"partialFingerprints"`
}

type sarifLocation struct {
	PhysicalLocation sarifPhysicalLocation `json:"physicalLocation"`
}

type sarifPhysicalLocation struct {
	ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
	Region           sarifRegion           `json:"region"`
}

type sarifArtifactLocation struct {
	URI       string `json:"uri"`
	URIBaseID string `json:"uriBaseId"`
}

type sarifRegion struct {
	StartLine int `json:"startLine"`
}

type sarifPartialFingerprints struct {
	PrimaryLocationLineHash string `json:"primaryLocationLineHash"`
}

// SARIF encodes findings as one deterministic SARIF 2.1.0 Errata 01 run. Rules
// are de-duplicated by rule id in first-appearance order so every result has a
// stable ruleIndex for identical input ordering.
func SARIF(tool ToolInfo, findings []SARIFFinding) ([]byte, error) {
	// Fail closed on the two fields a consumer rejects the whole run for. An
	// export that "succeeds" into a file GitHub or GitLab refuses is worse than an
	// error here: the operator learns about it at ingestion time, from the other
	// side of the wire.
	for i, f := range findings {
		if f.RuleID == "" {
			return nil, fmt.Errorf("siemwire: SARIF finding %d has an empty ruleId", i)
		}
		if !validSARIFLevel(f.Level) {
			return nil, fmt.Errorf("siemwire: SARIF finding %d has level %q, want none|note|warning|error", i, f.Level)
		}
	}

	rules := make([]sarifReportingDescriptor, 0)
	results := make([]sarifResult, 0, len(findings))
	ruleIndexes := make(map[string]int)

	for _, finding := range findings {
		ruleIndex, exists := ruleIndexes[finding.RuleID]
		if !exists {
			ruleIndex = len(rules)
			ruleIndexes[finding.RuleID] = ruleIndex
			tags := append([]string{}, finding.Tags...)
			rules = append(rules, sarifReportingDescriptor{
				ID:               finding.RuleID,
				Name:             finding.RuleName,
				ShortDescription: sarifText{Text: truncateSARIFDescription(finding.ShortDescription)},
				Help:             sarifHelp(finding),
				DefaultConfiguration: sarifConfiguration{
					Level: finding.Level,
				},
				Properties: sarifRuleProperties{
					Tags:             tags,
					SecuritySeverity: finding.SecuritySeverity,
				},
			})
		}

		startLine := finding.StartLine
		if startLine < 1 {
			startLine = 1
		}
		results = append(results, sarifResult{
			RuleID:    finding.RuleID,
			RuleIndex: ruleIndex,
			Level:     finding.Level,
			Message:   sarifText{Text: finding.Message},
			Locations: []sarifLocation{{
				PhysicalLocation: sarifPhysicalLocation{
					ArtifactLocation: sarifArtifactLocation{
						URI:       finding.ArtifactURI,
						URIBaseID: sarifSourceRoot,
					},
					Region: sarifRegion{StartLine: startLine},
				},
			}},
			PartialFingerprints: sarifPartialFingerprints{
				PrimaryLocationLineHash: finding.Fingerprint,
			},
		})
	}

	return json.Marshal(sarifLog{
		Schema:  sarifSchema,
		Version: sarifVersion,
		Runs: []sarifRun{{
			AutomationDetails: sarifAutomationDetails{ID: tool.AutomationID},
			Tool: sarifTool{Driver: sarifDriver{
				Name:           tool.Name,
				Version:        tool.Version,
				InformationURI: tool.InformationURI,
				Rules:          rules,
			}},
			OriginalURIBaseIDs: map[string]sarifURIBase{
				sarifSourceRoot: {
					URI:         tool.SourceRootURI,
					Description: sarifText{Text: "Result locations are relative to the root of the governed source tree."},
				},
			},
			Results: results,
		}},
	})
}

// sarifHelp builds the rule's help object, or nil when the finding carries no
// guidance (help is optional; an empty one is noise). SARIF requires the text
// form whenever help is present, so a caller that supplied only markdown gets it
// in text too rather than an object the spec does not allow.
func sarifHelp(f SARIFFinding) *sarifMultiformatText {
	text := f.HelpText
	if text == "" {
		text = f.HelpMarkdown
	}
	if text == "" {
		return nil
	}
	return &sarifMultiformatText{Text: text, Markdown: f.HelpMarkdown}
}

// validSARIFLevel reports whether level is one of the four values SARIF 2.1.0
// allows for result.level and defaultConfiguration.level.
func validSARIFLevel(level string) bool {
	switch level {
	case "none", "note", "warning", "error":
		return true
	default:
		return false
	}
}

func truncateSARIFDescription(s string) string {
	runes := []rune(s)
	if len(runes) <= sarifDescriptionMaxRunes {
		return s
	}
	return string(runes[:sarifDescriptionMaxRunes])
}
