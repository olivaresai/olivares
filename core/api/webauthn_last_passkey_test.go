// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package api_test

import (
	"net/http"
	"testing"

	"github.com/olivaresai/olivares/core/auth"
)

// passkeyStepUp steps the token's session up with soft's passkey.
func passkeyStepUp(t *testing.T, h *harness, token string, soft *softAuthenticator) {
	t.Helper()
	opts := h.do("POST", "/v1/auth/webauthn/authenticate/options", token, nil, nil)
	if r := h.do("POST", "/v1/auth/webauthn/authenticate", token,
		map[string]any{"credential": soft.assert(t, opts, flagUP|flagUV, testOrigin, false)}, nil); r.code != http.StatusOK {
		t.Fatalf("step-up = %d %s", r.code, r.raw)
	}
}

// onlyPasskeyID returns the id of the token owner's single passkey.
func onlyPasskeyID(t *testing.T, h *harness, token string) string {
	t.Helper()
	list := h.do("GET", "/v1/auth/webauthn/credentials", token, nil, nil)
	items, _ := list.body["items"].([]any)
	if list.code != http.StatusOK || len(items) != 1 {
		t.Fatalf("passkeys = %d %s, want one", list.code, list.raw)
	}
	return items[0].(map[string]any)["id"].(string)
}

// Deleting the last passkey follows the step-up
// policy. Under none or totp the person still steps up with their sign-in or their
// code, so it is allowed; under passkey it is refused with the way out.
func TestDeletingTheLastPasskeyFollowsTheStepUpPolicy(t *testing.T) {
	for _, policy := range []string{auth.StepUpNone, auth.StepUpTOTP, auth.StepUpPasskey} {
		t.Run(policy, func(t *testing.T) {
			h := newHarness(t)
			h.requireStepUp(policy)
			token := h.adminLogin()
			soft := newSoftAuthenticator(t)
			registerOK(t, h, token, soft)
			id := onlyPasskeyID(t, h, token)
			passkeyStepUp(t, h, token, soft)

			r := h.do("DELETE", "/v1/auth/webauthn/credentials/"+id, token, nil, nil)
			if policy != auth.StepUpPasskey {
				if r.code != http.StatusNoContent {
					t.Fatalf("delete the last passkey under %s = %d %s, want 204", policy, r.code, r.raw)
				}
				return
			}
			e, _ := r.body["error"].(map[string]any)
			if r.code != http.StatusConflict || e["code"] != "last_webauthn_credential" || e["message"] != auth.ErrLastWebAuthnCredential.Error() {
				t.Fatalf("delete the last passkey under passkey = %d %s, want 409 with the way out", r.code, r.raw)
			}
		})
	}
}
