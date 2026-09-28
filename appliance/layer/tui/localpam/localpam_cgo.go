// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

//go:build linux && cgo && olivares_pam

package localpam

/*
#cgo LDFLAGS: -lpam -lpam_misc
#include <stdlib.h>
#include <security/pam_appl.h>
#include <security/pam_misc.h>

static struct pam_conv olivares_conversation = { misc_conv, NULL };

static int olivares_pam_start(const char *service, const char *user, pam_handle_t **handle) {
	return pam_start(service, user, &olivares_conversation, handle);
}

static const char *olivares_pam_user(pam_handle_t *handle) {
	const void *item = NULL;
	if (pam_get_item(handle, PAM_USER, &item) != PAM_SUCCESS) {
		return NULL;
	}
	return (const char *)item;
}
*/
import "C"

import (
	"unsafe"

	"github.com/olivaresai/olivares/appliance/layer/portal/auth"
)

// Built reports that this binary carries the adapter.
const Built = true

// Start opens a transaction on service for login, naming the console's terminal.
func (Stack) Start(service, login string) (auth.Transaction, error) {
	cService := C.CString(service)
	defer C.free(unsafe.Pointer(cService))
	cLogin := C.CString(login)
	defer C.free(unsafe.Pointer(cLogin))
	var handle *C.pam_handle_t
	status := C.olivares_pam_start(cService, cLogin, &handle)
	if status != C.PAM_SUCCESS || handle == nil {
		if handle != nil {
			C.pam_end(handle, status)
		}
		return nil, errRefused
	}
	t := &transaction{handle: handle, login: login, status: status}
	cTerminal := C.CString(consoleTerminal)
	defer C.free(unsafe.Pointer(cTerminal))
	if t.status = C.pam_set_item(handle, C.int(C.PAM_TTY), unsafe.Pointer(cTerminal)); t.status != C.PAM_SUCCESS {
		_ = t.Close()
		return nil, errRefused
	}
	return t, nil
}

// transaction is one open transaction. status is the last result, which the library's end takes.
type transaction struct {
	handle *C.pam_handle_t
	login  string
	status C.int
	closed bool
}

// Authenticate runs the service's auth group, refusing an empty credential, and refuses a module
// that changed whom the transaction is for.
func (t *transaction) Authenticate() error {
	t.status = C.pam_authenticate(t.handle, C.int(C.PAM_DISALLOW_NULL_AUTHTOK))
	if t.status != C.PAM_SUCCESS {
		return errRefused
	}
	user := C.olivares_pam_user(t.handle)
	if user == nil || C.GoString(user) != t.login {
		t.status = C.PAM_AUTH_ERR
		return errRefused
	}
	return nil
}

// Account runs the service's account group. An account whose credential must be changed first is
// refused: changing it is E12's entry, not a sign-in.
func (t *transaction) Account() error {
	t.status = C.pam_acct_mgmt(t.handle, C.int(C.PAM_DISALLOW_NULL_AUTHTOK))
	if t.status != C.PAM_SUCCESS {
		return errRefused
	}
	return nil
}

// Close ends the transaction once.
func (t *transaction) Close() error {
	if t.closed {
		return nil
	}
	t.closed = true
	C.pam_end(t.handle, t.status)
	return nil
}
