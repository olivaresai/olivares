// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"fmt"

	"github.com/olivaresai/olivares/core/api"
)

// businessSourceKinds are the enterprise-system connectors that belong to Business
// (docs/editions.md): the SharePoint, SAP OData and Salesforce document sources, and
// the ServiceNow CMDB, Oracle audit, KMIP and Claude Compliance API observation
// sources. Community does not construct them: the Business edition adds them through
// inProcSourceFactories and thisEdition.contentSource. This set only lets a Community
// build say "Business" for them instead of "unknown kind". The value is the connector's
// directory under connectors/, which scripts/check-public-counts.sh reads to know the
// connector has an activation path in the Business edition.
var businessSourceKinds = map[string]string{
	"claude-compliance": "claude-compliance",
	"kmip":              "kmip",
	"oracle-audit":      "oracle-audit",
	"servicenow-cmdb":   "servicenow",
	"salesforce":        "salesforce",
	"sap_odata":         "sapodata",
	"sharepoint":        "sharepoint",
}

// businessKindUnavailable reports a Business kind this build cannot construct.
func businessKindUnavailable(kind string) bool {
	if _, business := businessSourceKinds[kind]; !business {
		return false
	}
	if _, ok := buildInProcSource(kind); ok {
		return false
	}
	_, ok := buildContentSource(kind, nil)
	return !ok
}

// errBusinessSourceKind is the one refusal for a Business kind: 501 connector_unavailable
// through the API, a startup error naming the kind on the command line.
func errBusinessSourceKind(kind string) error {
	return fmt.Errorf("%w: the %q connector is a Business capability; run the Business edition or remove it from the configuration", api.ErrConnectorUnavailable, kind)
}

// unsupportedSourceKindReason is the sentence for a kind this build does not wire.
func unsupportedSourceKindReason(kind string) string {
	if businessKindUnavailable(kind) {
		return errBusinessSourceKind(kind).Error()
	}
	return fmt.Sprintf("unknown or unsupported source kind %q", kind)
}

// validateBusinessSourcesConfig refuses a first-party Business kind in the operator's
// sources config, the way validateIdentitySourcesConfig refuses a directory source. An
// external plugin is the operator's own binary; its kind is not consulted.
func validateBusinessSourcesConfig(cfg sourcesConfig) error {
	for _, src := range cfg.Sources {
		if src.Plugin == nil && businessKindUnavailable(src.Kind) {
			return errBusinessSourceKind(src.Kind)
		}
	}
	for _, doc := range cfg.Documents {
		if doc.Plugin == nil && businessKindUnavailable(doc.Kind) {
			return errBusinessSourceKind(doc.Kind)
		}
	}
	return nil
}
