// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"os"
)

const (
	payloadType   = "application/vnd.in-toto+json"
	predicateType = "https://slsa.dev/provenance/v1"
)

func main() {
	binary := flag.String("binary", "", "plugin binary to sign")
	bundlePath := flag.String("bundle", "", "path to write the Sigstore-shaped DSSE bundle")
	pubPath := flag.String("pubkey", "", "path to write the PEM public key")
	flag.Parse()
	if *binary == "" || *bundlePath == "" || *pubPath == "" {
		fmt.Fprintln(os.Stderr, "usage: admission-check --binary <path> --bundle <path> --pubkey <path>")
		os.Exit(2)
	}

	bytes, err := os.ReadFile(*binary)
	must(err)
	sum := sha256.Sum256(bytes)
	digest := hex.EncodeToString(sum[:])

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	must(err)
	statement := map[string]any{
		"_type":         "https://in-toto.io/Statement/v1",
		"subject":       []map[string]any{{"name": "fabworks-connector", "digest": map[string]string{"sha256": digest}}},
		"predicateType": predicateType,
		"predicate":     map[string]any{"buildType": "fabworks-offline-smoke"},
	}
	payload, err := json.Marshal(statement)
	must(err)
	pae := fmt.Sprintf("DSSEv1 %d %s %d %s", len(payloadType), payloadType, len(payload), payload)
	sig := ed25519.Sign(priv, []byte(pae))
	bundle := map[string]any{
		"mediaType":            "application/vnd.dev.sigstore.bundle.v0.3+json",
		"verificationMaterial": map[string]any{"publicKey": map[string]string{"hint": "fabworks-smoke-key"}},
		"dsseEnvelope": map[string]any{
			"payload":     base64.StdEncoding.EncodeToString(payload),
			"payloadType": payloadType,
			"signatures":  []map[string]string{{"sig": base64.StdEncoding.EncodeToString(sig)}},
		},
	}
	bundleJSON, err := json.MarshalIndent(bundle, "", "  ")
	must(err)
	der, err := x509.MarshalPKIXPublicKey(pub)
	must(err)
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})

	must(os.WriteFile(*bundlePath, bundleJSON, 0o600))
	must(os.WriteFile(*pubPath, pubPEM, 0o600))

	if verify(bundleJSON, nil, digest) {
		fmt.Fprintln(os.Stderr, "deny-closed check failed: unsigned/no-anchor admission unexpectedly passed")
		os.Exit(1)
	}
	if !verify(bundleJSON, pub, digest) {
		fmt.Fprintln(os.Stderr, "admission check failed: signed pinned artifact was refused")
		os.Exit(1)
	}

	fmt.Println("deny-closed check: refused without trusted key")
	fmt.Println("admission: admitted signed artifact with matching sha256")
	fmt.Println("plugin_sha256:", digest)
	fmt.Println("operator connector_trust:")
	trust, _ := json.MarshalIndent(map[string]any{
		"trusted_keys":       []string{string(pubPEM)},
		"allowed_predicates": []string{predicateType},
	}, "", "  ")
	fmt.Println(string(trust))
}

func verify(bundleJSON []byte, trusted ed25519.PublicKey, expectedDigest string) bool {
	if len(trusted) == 0 {
		return false
	}
	var bundle struct {
		DSSEEnvelope struct {
			Payload     string `json:"payload"`
			PayloadType string `json:"payloadType"`
			Signatures  []struct {
				Sig string `json:"sig"`
			} `json:"signatures"`
		} `json:"dsseEnvelope"`
	}
	if err := json.Unmarshal(bundleJSON, &bundle); err != nil {
		return false
	}
	if bundle.DSSEEnvelope.PayloadType != payloadType || len(bundle.DSSEEnvelope.Signatures) == 0 {
		return false
	}
	payload, err := base64.StdEncoding.DecodeString(bundle.DSSEEnvelope.Payload)
	if err != nil {
		return false
	}
	sig, err := base64.StdEncoding.DecodeString(bundle.DSSEEnvelope.Signatures[0].Sig)
	if err != nil {
		return false
	}
	pae := fmt.Sprintf("DSSEv1 %d %s %d %s", len(payloadType), payloadType, len(payload), payload)
	if !ed25519.Verify(trusted, []byte(pae), sig) {
		return false
	}
	var statement struct {
		PredicateType string `json:"predicateType"`
		Subject       []struct {
			Digest map[string]string `json:"digest"`
		} `json:"subject"`
	}
	if err := json.Unmarshal(payload, &statement); err != nil {
		return false
	}
	return statement.PredicateType == predicateType &&
		len(statement.Subject) == 1 &&
		statement.Subject[0].Digest["sha256"] == expectedDigest
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
