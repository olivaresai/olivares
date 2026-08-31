// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package siemwire

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSARIFGolden(t *testing.T) {
	got, err := SARIF(ToolInfo{
		Name:           "Olivares AI",
		Version:        "v1.2.3",
		InformationURI: "https://olivares.ai",
		AutomationID:   "security/findings",
	}, []SARIFFinding{{
		RuleID:           "OLV001",
		RuleName:         "Prompt injection",
		ShortDescription: "Untrusted instructions reached an agent",
		HelpText:         "Review the governed tool result.",
		HelpMarkdown:     "Review the governed **tool result**.",
		Level:            "error",
		SecuritySeverity: "9.5",
		Tags:             []string{"external/owasp_llm/LLM01:2025"},
		Message:          "Prompt injection detected",
		ArtifactURI:      "policies/agent.cedar",
		StartLine:        17,
		Fingerprint:      "stable-fingerprint",
	}})
	if err != nil {
		t.Fatalf("SARIF: %v", err)
	}

	want := `{"$schema":"https://docs.oasis-open.org/sarif/sarif/v2.1.0/errata01/os/schemas/sarif-schema-2.1.0.json","version":"2.1.0","runs":[{"automationDetails":{"id":"security/findings"},"tool":{"driver":{"name":"Olivares AI","version":"v1.2.3","informationUri":"https://olivares.ai","rules":[{"id":"OLV001","name":"Prompt injection","shortDescription":{"text":"Untrusted instructions reached an agent"},"help":{"text":"Review the governed tool result.","markdown":"Review the governed **tool result**."},"defaultConfiguration":{"level":"error"},"properties":{"tags":["external/owasp_llm/LLM01:2025"],"security-severity":"9.5"}}]}},"originalUriBaseIds":{"%SRCROOT%":{"description":{"text":"Result locations are relative to the root of the governed source tree."}}},"results":[{"ruleId":"OLV001","ruleIndex":0,"level":"error","message":{"text":"Prompt injection detected"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"policies/agent.cedar","uriBaseId":"%SRCROOT%"},"region":{"startLine":17}}}],"partialFingerprints":{"primaryLocationLineHash":"stable-fingerprint"}}]}]}`
	if string(got) != want {
		t.Fatalf("SARIF golden mismatch:\n got: %s\nwant: %s", got, want)
	}
}

func TestSARIFEmptyFindingsIsValid(t *testing.T) {
	got, err := SARIF(ToolInfo{Name: "Olivares AI", AutomationID: "security/findings"}, nil)
	if err != nil {
		t.Fatalf("SARIF: %v", err)
	}
	if !json.Valid(got) {
		t.Fatalf("SARIF is invalid JSON: %s", got)
	}
	for _, want := range []string{
		`"$schema":"https://docs.oasis-open.org/sarif/sarif/v2.1.0/errata01/os/schemas/sarif-schema-2.1.0.json"`,
		`"version":"2.1.0"`,
		`"runs":[{`,
		`"rules":[]`,
		`"results":[]`,
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("empty SARIF missing %s: %s", want, got)
		}
	}
}

func TestSARIFTruncatesShortDescriptionOnRuneBoundary(t *testing.T) {
	description := strings.Repeat("a", 1023) + "界tail"
	got, err := SARIF(ToolInfo{Name: "tool"}, []SARIFFinding{{
		RuleID: "rule", Level: "warning", ShortDescription: description, StartLine: -4,
	}})
	if err != nil {
		t.Fatalf("SARIF: %v", err)
	}

	var doc struct {
		Runs []struct {
			Tool struct {
				Driver struct {
					Rules []struct {
						ShortDescription struct {
							Text string `json:"text"`
						} `json:"shortDescription"`
					} `json:"rules"`
				} `json:"driver"`
			} `json:"tool"`
			Results []struct {
				Locations []struct {
					PhysicalLocation struct {
						Region struct {
							StartLine int `json:"startLine"`
						} `json:"region"`
					} `json:"physicalLocation"`
				} `json:"locations"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("decode SARIF: %v", err)
	}
	text := doc.Runs[0].Tool.Driver.Rules[0].ShortDescription.Text
	if utf8.RuneCountInString(text) != 1024 || !strings.HasSuffix(text, "界") {
		t.Fatalf("shortDescription = %q (%d runes), want 1024 runes ending on 界",
			text, utf8.RuneCountInString(text))
	}
	if got := doc.Runs[0].Results[0].Locations[0].PhysicalLocation.Region.StartLine; got != 1 {
		t.Fatalf("startLine = %d, want minimum 1", got)
	}
}

func TestSARIFDeduplicatesRulesInFirstAppearanceOrder(t *testing.T) {
	got, err := SARIF(ToolInfo{Name: "tool"}, []SARIFFinding{
		{RuleID: "rule-b", Level: "note", ShortDescription: "first B"},
		{RuleID: "rule-a", Level: "note", ShortDescription: "only A"},
		{RuleID: "rule-b", Level: "note", ShortDescription: "later B"},
	})
	if err != nil {
		t.Fatalf("SARIF: %v", err)
	}

	var doc struct {
		Runs []struct {
			Tool struct {
				Driver struct {
					Rules []struct {
						ID               string `json:"id"`
						ShortDescription struct {
							Text string `json:"text"`
						} `json:"shortDescription"`
					} `json:"rules"`
				} `json:"driver"`
			} `json:"tool"`
			Results []struct {
				RuleIndex int `json:"ruleIndex"`
			} `json:"results"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("decode SARIF: %v", err)
	}
	rules := doc.Runs[0].Tool.Driver.Rules
	if len(rules) != 2 {
		t.Fatalf("rules = %d, want 2", len(rules))
	}
	if rules[0].ID != "rule-b" || rules[0].ShortDescription.Text != "first B" || rules[1].ID != "rule-a" {
		t.Fatalf("rule order/content = %+v, want first-appearance rule-b then rule-a", rules)
	}
	results := doc.Runs[0].Results
	if len(results) != 3 || results[0].RuleIndex != 0 || results[1].RuleIndex != 1 || results[2].RuleIndex != 0 {
		t.Fatalf("result rule indexes = %+v, want [0 1 0]", results)
	}
}

func TestSARIFDeclaresTheURIBaseItUses(t *testing.T) {
	// Every result location carries uriBaseId %SRCROOT%. SARIF 2.1.0 resolves a
	// uriBaseId through the run's originalUriBaseIds; without that entry the base
	// is undefined and a consumer is left guessing what the relative URIs are
	// relative to. The uri itself is optional — this export does not know where a
	// consumer checked the tree out — but the base MUST be declared.
	got, err := SARIF(ToolInfo{Name: "Olivares AI", AutomationID: "security/findings"}, []SARIFFinding{{
		RuleID: "OLV001", Level: "error", Message: "m", ArtifactURI: "policies/agent.cedar", StartLine: 1,
	}})
	if err != nil {
		t.Fatalf("SARIF: %v", err)
	}
	var doc struct {
		Runs []struct {
			OriginalURIBaseIDs map[string]struct {
				URI         string `json:"uri"`
				Description struct {
					Text string `json:"text"`
				} `json:"description"`
			} `json:"originalUriBaseIds"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	base, ok := doc.Runs[0].OriginalURIBaseIDs["%SRCROOT%"]
	if !ok {
		t.Fatalf("run does not declare the %%SRCROOT%% base it uses: %s", got)
	}
	if base.Description.Text == "" {
		t.Errorf("the declared base must say what it is relative to: %s", got)
	}
	if base.URI != "" {
		t.Errorf("no source root was supplied, so no uri may be invented: %q", base.URI)
	}

	// When the caller DOES know the root, it is carried verbatim.
	got, err = SARIF(ToolInfo{
		Name: "Olivares AI", AutomationID: "security/findings",
		SourceRootURI: "file:///srv/policies/",
	}, nil)
	if err != nil {
		t.Fatalf("SARIF: %v", err)
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if uri := doc.Runs[0].OriginalURIBaseIDs["%SRCROOT%"].URI; uri != "file:///srv/policies/" {
		t.Errorf("source root uri = %q, want the caller's", uri)
	}
}

func TestSARIFHelpTextIsNotMarkdown(t *testing.T) {
	// help.text is the plain-text rendering a consumer shows when it cannot render
	// markdown. Copying the markdown into it puts raw ** and [](…) in front of an
	// analyst. The caller supplies both forms; markdown is omitted when it has none.
	got, err := SARIF(ToolInfo{Name: "Olivares AI", AutomationID: "a"}, []SARIFFinding{{
		RuleID: "OLV001", Level: "error", Message: "m",
		HelpText:     "Review the governed tool result.",
		HelpMarkdown: "Review the governed **tool result**.",
	}, {
		RuleID: "OLV002", Level: "note", Message: "m",
		HelpText: "Plain guidance only.",
	}})
	if err != nil {
		t.Fatalf("SARIF: %v", err)
	}
	s := string(got)
	if !strings.Contains(s, `"help":{"text":"Review the governed tool result.","markdown":"Review the governed **tool result**."}`) {
		t.Errorf("help text/markdown not carried separately: %s", s)
	}
	if !strings.Contains(s, `"help":{"text":"Plain guidance only."}`) {
		t.Errorf("markdown must be omitted when the caller has none: %s", s)
	}
}

func TestSARIFRejectsResultsAConsumerWouldReject(t *testing.T) {
	// GitLab and GitHub both reject a run whose results carry an empty ruleId or a
	// level outside the SARIF enum. Failing here — with the offending index — beats
	// shipping a file that the consumer rejects wholesale, after the export looked
	// like it succeeded.
	for _, tc := range []struct {
		name    string
		finding SARIFFinding
		wantIn  string
	}{
		{name: "empty ruleId", finding: SARIFFinding{Level: "error", Message: "m"}, wantIn: "ruleId"},
		{name: "empty level", finding: SARIFFinding{RuleID: "OLV001", Message: "m"}, wantIn: "level"},
		{name: "level outside the enum", finding: SARIFFinding{RuleID: "OLV001", Level: "critical", Message: "m"}, wantIn: "level"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := SARIF(ToolInfo{Name: "Olivares AI", AutomationID: "a"}, []SARIFFinding{tc.finding})
			if err == nil {
				t.Fatalf("%s must fail the export, not produce a rejected file", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantIn) || !strings.Contains(err.Error(), "0") {
				t.Errorf("error must name the field and the finding index, got %v", err)
			}
		})
	}
	for _, level := range []string{"none", "note", "warning", "error"} {
		if _, err := SARIF(ToolInfo{Name: "n", AutomationID: "a"}, []SARIFFinding{{RuleID: "r", Level: level, Message: "m"}}); err != nil {
			t.Errorf("level %q is in the SARIF enum and must be accepted: %v", level, err)
		}
	}
}
