// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package claudewif

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestExchangeAuditValueContract(t *testing.T) {
	obtained := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	token := MintedToken{
		AccessToken: "sk-ant-oat01-local-audit-probe", TokenType: "Bearer", Scope: "workspace:developer",
		ObtainedAt: obtained, ExpiresIn: 60,
		rule: "fdrl_probe", org: "org-probe", serviceAccount: "svac_probe", workspace: "wrkspc_probe",
	}
	audit := token.Audit()
	if !token.ExpiresAt().Equal(obtained.Add(time.Minute)) || !audit.ExpiresAt.Equal(token.ExpiresAt()) {
		t.Error("audit must preserve the credential's absolute expiry")
	}
	if audit.FederationRuleID != token.rule || audit.OrganizationID != token.org || audit.ServiceAccountID != token.serviceAccount || audit.WorkspaceID != token.workspace || audit.Scope != token.Scope || audit.TokenType != token.TokenType {
		t.Error("audit must preserve non-secret exchange provenance")
	}
	raw, err := json.Marshal(audit)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), token.AccessToken) || strings.Contains(string(raw), "AccessToken") {
		t.Error("audit must never contain credential data")
	}
	if !hasOATPrefix(token.AccessToken) || hasOATPrefix("ordinary-key") {
		t.Error("minted token prefix contract changed")
	}
}
