// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux && cgo && olivares_pam

package nativepam

/*
#cgo LDFLAGS: -lpam
#include <security/pam_appl.h>
#include <stdlib.h>
#include <string.h>

struct request_secret { char *password; int answered; };

static void wipe_free(char *value) {
	if (value != NULL) { explicit_bzero(value, strlen(value)); free(value); }
}

// This is a request conversation, never a terminal or a generic prompt relay.
// The only credential it owns is this request's single nonempty password.
static int request_conv(int count, const struct pam_message **messages,
	struct pam_response **output, void *data) {
	struct request_secret *secret = data;
	if (count <= 0 || count > PAM_MAX_NUM_MSG || messages == NULL || output == NULL)
		return PAM_CONV_ERR;
	struct pam_response *replies = calloc(count, sizeof(*replies));
	if (replies == NULL) return PAM_BUF_ERR;
	for (int i = 0; i < count; i++) {
		if (messages[i] == NULL) goto refuse;
		switch (messages[i]->msg_style) {
		case PAM_PROMPT_ECHO_OFF:
			if (secret->answered || messages[i]->msg == NULL ||
				(strcmp(messages[i]->msg, "Password:") != 0 &&
				 strcmp(messages[i]->msg, "Password: ") != 0)) goto refuse;
			replies[i].resp = strdup(secret->password);
			if (replies[i].resp == NULL) goto refuse;
			secret->answered = 1;
			break;
		case PAM_TEXT_INFO:
		case PAM_ERROR_MSG:
			break;
		default:
			goto refuse;
		}
	}
	*output = replies; // PAM owns and disposes these replies.
	return PAM_SUCCESS;
refuse:
	for (int i = 0; i < count; i++) wipe_free(replies[i].resp);
	free(replies);
	return PAM_CONV_ERR;
}

static int exact_user(pam_handle_t *handle, const char *login) {
	const void *user = NULL;
	return pam_get_item(handle, PAM_USER, &user) == PAM_SUCCESS && user != NULL &&
		strcmp((const char *)user, login) == 0;
}

static int check_request(const char *service, const char *login, char *password) {
	struct request_secret secret = { password, 0 };
	struct pam_conv conversation = { request_conv, &secret };
	pam_handle_t *handle = NULL;
	int result = 0;
	int status = pam_start(service, login, &conversation, &handle);
	if (status == PAM_SUCCESS && handle != NULL) {
		status = pam_authenticate(handle, PAM_DISALLOW_NULL_AUTHTOK);
		if (status == PAM_SUCCESS && secret.answered && exact_user(handle, login)) {
			result = 1;
			status = pam_acct_mgmt(handle, PAM_DISALLOW_NULL_AUTHTOK);
			if (status == PAM_SUCCESS && exact_user(handle, login)) result |= 2;
		}
	}
	if (handle != NULL && pam_end(handle, status) != PAM_SUCCESS) result = 0;
	return result;
}
*/
import "C"

import "unsafe"

const nativeBuilt = true

func authenticate(service, login string, password []byte) (Result, error) {
	if !ValidRequest(login, password) || (service != RepairService && service != AccountService) {
		return Result{}, ErrRefused
	}
	cService := C.CString(service)
	defer C.free(unsafe.Pointer(cService))
	cLogin := C.CString(login)
	defer C.free(unsafe.Pointer(cLogin))
	secret := C.calloc(C.size_t(len(password)+1), 1)
	if secret == nil {
		return Result{}, ErrRefused
	}
	defer func() { C.explicit_bzero(secret, C.size_t(len(password)+1)); C.free(secret) }()
	C.memcpy(secret, unsafe.Pointer(&password[0]), C.size_t(len(password)))
	status := int(C.check_request(cService, cLogin, (*C.char)(secret)))
	return Result{Authenticated: status&1 != 0, AccountAllowed: status&2 != 0}, nil
}
