// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package tui

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
	"github.com/olivaresai/olivares/appliance/layer/portal/helperclient"
)

// CertificateFingerprint reads the stored public certificate, without accessing a key.
// A successful read does not verify the pair or the portal's currently served certificate.
type CertificateFingerprint func() (fingerprint string, readable bool, reason string)

// WithCertificateFingerprint returns the console reading the public certificate through m.
func (c Console) WithCertificateFingerprint(m CertificateFingerprint) Console {
	c.certificateFingerprint = m
	return c
}

// certificateTimeout bounds one certificate request: generating a P-256 pair and writing two files.
const certificateTimeout = 30 * time.Second

// certificateConfirm is the question Run asks after certificate is selected.
const certificateConfirm = "Type generate to ask the certificate helper for a new self-signed pair naming this " +
	"host's current origin and addresses, or anything else to ask for nothing:"

// showCertificate states only the fingerprint of the stored public certificate.
func (c Console) showCertificate() string {
	if c.certificateFingerprint == nil {
		return "certificate: this console cannot read the stored certificate in this version."
	}
	fingerprint, readable, reason := c.certificateFingerprint()
	if !readable {
		return "certificate: the stored certificate cannot be read (" + reason + ")."
	}
	return "certificate: the stored certificate has SHA-256 fingerprint " + fingerprint + "."
}

// Certificate asks the certificate helper to generate a new pair when answer is generate, and then
// reads the public certificate again. It never assumes a fingerprint from the helper's
// answer. Without a qualified sign-in it refuses and asks nothing.
func (c Console) Certificate(answer string) string {
	if !c.qualified() {
		return "certificate: " + signInRequired
	}
	if answer != helperschema.CertGenerate {
		return "Nothing was asked of the certificate helper."
	}
	if c.helper == nil {
		return "certificate: no helper is wired to this console, so nothing was asked and nothing was performed."
	}
	random := c.random
	if random == nil {
		random = rand.Reader
	}
	operation, err := helperschema.NewOperationID(random)
	if err != nil {
		return "certificate: no operation id could be minted, so nothing was asked and nothing was performed."
	}
	ctx, cancel := context.WithTimeout(context.Background(), certificateTimeout)
	defer cancel()
	response, err := c.helper.Call(ctx, helperschema.HelperCert, &helperschema.CertRequest{Op: helperschema.CertGenerate, OperationID: operation})
	var failure *helperclient.Error
	switch {
	case errors.As(err, &failure) && failure.Code == helperschema.CodeConsumerUnavailable:
		return "certificate: the certificate helper is unavailable (" + failure.Reason + "), so nothing was asked and nothing was performed."
	case errors.As(err, &failure) && failure.Code == helperclient.CodeOutcomeUnknown:
		return "certificate: the generate request was sent and no answer came back, so whether the pair changed is unknown."
	case err != nil:
		return "certificate: the generate request was not sent, so nothing was performed."
	case response.Result == helperschema.ResultRefused:
		return "certificate: the certificate helper refused generate (" + response.Code + "), so nothing was performed."
	case response.Result != helperschema.ResultPerformed:
		return "certificate: the certificate helper could not perform generate (" + response.Code + "), so nothing was performed."
	}
	said := "certificate: the certificate helper performed generate. "
	if c.certificateFingerprint == nil {
		return said + "This console cannot read the stored certificate, so it shows no fingerprint."
	}
	fingerprint, readable, reason := c.certificateFingerprint()
	if !readable {
		return said + "The stored certificate cannot be read (" + reason + "), so no fingerprint is shown as the new certificate."
	}
	return said + "The stored certificate has SHA-256 fingerprint " + fingerprint +
		"; the portal checks its key pair when it starts."
}
