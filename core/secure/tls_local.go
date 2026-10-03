// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package secure

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/netip"
	"os"
	"slices"
	"time"
)

const selfSignedValidity = 825 * 24 * time.Hour

// LocalLANAddresses returns this host's usable non-loopback interface addresses,
// IPv4 before IPv6, sorted and deduplicated. Both the console panel and generated
// TLS certificates use this list. Down interfaces, link-local and multicast
// addresses cannot supply a usable console URL. Enumeration failure is best effort.
func LocalLANAddresses() []netip.Addr {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	seen := map[netip.Addr]bool{}
	var ips []netip.Addr
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addrs {
			ipnet, ok := address.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipnet.IP)
			if !ok {
				continue
			}
			ip = ip.Unmap().WithZone("")
			if seen[ip] || ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
				continue
			}
			seen[ip] = true
			ips = append(ips, ip)
		}
	}
	slices.SortFunc(ips, func(a, b netip.Addr) int { return a.Compare(b) })
	return ips
}

// Names are supplied at this OS seam so address changes can be tested without
// changing the test host's network configuration.
func ensureTLSCert(certPath, keyPath string, dns []string, ips []net.IP) (bool, string, error) {
	var existing *x509.Certificate
	var bundledCA *x509.Certificate
	if fileExists(certPath) && fileExists(keyPath) {
		if _, err := readSecret(keyPath); err != nil {
			return false, "", err
		}
		pair, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return false, "", fmt.Errorf("secure: load server keypair: %w", err)
		}
		existing, err = x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			return false, "", err
		}
		if len(pair.Certificate) > 1 {
			bundledCA, _ = x509.ParseCertificate(pair.Certificate[1])
		}
	}
	caPath, caKeyPath := certPath+".ca", keyPath+".ca"
	// Only our own historical generated leaf can be upgraded automatically.
	// A supplied operator certificate must never be replaced by local material.
	if existing != nil && !legacyLocalTLSCert(existing) {
		managed := bundledCA != nil && bundledCA.IsCA &&
			bundledCA.Subject.CommonName == "Olivares AI local CA" && existing.CheckSignatureFrom(bundledCA) == nil
		if managed && !fileExists(caPath) {
			return false, "", fmt.Errorf("secure: local TLS CA certificate is missing: restore %s from the CA in %s", caPath, certPath)
		}
		if !managed && fileExists(caPath) {
			// Public material suffices to classify ownership. An unused local CA's
			// missing key or expiry must not block a valid operator replacement.
			if data, err := os.ReadFile(caPath); err == nil {
				if block, _ := pem.Decode(data); block != nil {
					if ca, err := x509.ParseCertificate(block.Bytes); err == nil {
						managed = existing.CheckSignatureFrom(ca) == nil
					}
				}
			}
		}
		if !managed {
			fp, err := certFingerprint(certPath)
			return false, fp, err
		}
	}
	ca, caKey, caPEM, err := ensureLocalTLSCA(caPath, caKeyPath)
	if err != nil {
		return false, "", err
	}
	if existing != nil {
		if !legacyLocalTLSCert(existing) && (existing.CheckSignatureFrom(ca) != nil ||
			(bundledCA != nil && !bytes.Equal(bundledCA.Raw, ca.Raw))) {
			return false, "", fmt.Errorf("secure: local TLS CA does not match the server certificate: restore %s and %s", caPath, caKeyPath)
		}
		if existing.CheckSignatureFrom(ca) == nil && sameTLSNames(existing, dns, ips) &&
			existing.NotAfter.After(time.Now().Add(30*24*time.Hour)) {
			fp, err := certFingerprint(certPath)
			return false, fp, err
		}
	}
	key, err := localTLSKey(keyPath)
	if err != nil {
		return false, "", err
	}
	leaf, err := localTLSTemplate(pkix.Name{Organization: []string{"Olivares AI"}, CommonName: "olivares"})
	if err != nil {
		return false, "", err
	}
	leaf.KeyUsage = x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment
	leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	leaf.DNSNames, leaf.IPAddresses = dns, ips
	if leaf.NotAfter.After(ca.NotAfter) {
		leaf.NotAfter = ca.NotAfter
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, ca, key.Public(), caKey)
	if err != nil {
		return false, "", fmt.Errorf("secure: create local TLS certificate: %w", err)
	}
	bundle := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), caPEM...)
	if err := writeTLSCertificate(certPath, bundle); err != nil {
		return false, "", err
	}
	fp, err := certFingerprint(certPath)
	return true, fp, err
}

func legacyLocalTLSCert(cert *x509.Certificate) bool {
	key, ok := cert.PublicKey.(*ecdsa.PublicKey)
	return ok && key.Curve == elliptic.P256() && cert.SignatureAlgorithm == x509.ECDSAWithSHA256 &&
		!cert.IsCA && len(cert.Subject.Names) == 2 && cert.Subject.CommonName == "olivares" &&
		slices.Equal(cert.Subject.Organization, []string{"Olivares AI"}) &&
		cert.NotAfter.Sub(cert.NotBefore) == selfSignedValidity+time.Hour &&
		cert.KeyUsage == x509.KeyUsageDigitalSignature|x509.KeyUsageKeyEncipherment &&
		slices.Equal(cert.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}) &&
		len(cert.DNSNames) >= 1 && len(cert.DNSNames) <= 2 && cert.DNSNames[0] == "localhost" &&
		len(cert.IPAddresses) == 2 && cert.IPAddresses[0].Equal(net.IPv4(127, 0, 0, 1)) && cert.IPAddresses[1].Equal(net.IPv6loopback) &&
		bytes.Equal(cert.RawIssuer, cert.RawSubject) &&
		cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature) == nil
}

func sameTLSNames(cert *x509.Certificate, dns []string, ips []net.IP) bool {
	if !slices.Equal(cert.DNSNames, dns) || len(cert.IPAddresses) != len(ips) {
		return false
	}
	for i, ip := range ips {
		if !ip.Equal(cert.IPAddresses[i]) {
			return false
		}
	}
	return true
}

func localTLSKey(path string) (crypto.Signer, error) {
	if fileExists(path) {
		data, err := readSecret(path)
		if err != nil {
			return nil, err
		}
		block, _ := pem.Decode(data)
		if block == nil {
			return nil, fmt.Errorf("secure: no private key in %s", path)
		}
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("secure: parse local TLS key: %w", err)
		}
		signer, ok := key.(crypto.Signer)
		if !ok {
			return nil, fmt.Errorf("secure: local TLS key is not a signer")
		}
		return signer, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := writeSecret(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})); err != nil {
		return nil, err
	}
	return key, nil
}

func localTLSTemplate(subject pkix.Name) (*x509.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	now := time.Now()
	return &x509.Certificate{SerialNumber: serial, Subject: subject,
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(selfSignedValidity), BasicConstraintsValid: true}, nil
}

func ensureLocalTLSCA(certPath, keyPath string) (*x509.Certificate, crypto.Signer, []byte, error) {
	// Losing the CA key must refuse renewal rather than silently changing trust.
	if fileExists(certPath) && !fileExists(keyPath) {
		return nil, nil, nil, fmt.Errorf("secure: local TLS CA key is missing: restore %s", keyPath)
	}
	key, err := localTLSKey(keyPath)
	if err != nil {
		return nil, nil, nil, err
	}
	if fileExists(certPath) {
		pair, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("secure: load local TLS CA: %w", err)
		}
		ca, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil {
			return nil, nil, nil, err
		}
		if !ca.IsCA || ca.KeyUsage&x509.KeyUsageCertSign == 0 || !ca.NotAfter.After(time.Now()) {
			return nil, nil, nil, fmt.Errorf("secure: local TLS CA is invalid or expired: %s", certPath)
		}
		data, err := os.ReadFile(certPath)
		return ca, key, data, err
	}
	ca, err := localTLSTemplate(pkix.Name{Organization: []string{"Olivares AI"}, CommonName: "Olivares AI local CA"})
	if err != nil {
		return nil, nil, nil, err
	}
	ca.IsCA, ca.MaxPathLenZero = true, true
	ca.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	ca.NotAfter = time.Now().AddDate(10, 0, 0)
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, key.Public(), key)
	if err != nil {
		return nil, nil, nil, err
	}
	data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := writeTLSCertificate(certPath, data); err != nil {
		return nil, nil, nil, err
	}
	ca, err = x509.ParseCertificate(der)
	return ca, key, data, err
}

// Publish certificates atomically, just like their private keys. Keeping the
// leaf key avoids a mismatched pair during renewal and preserves SPKI pins.
func writeTLSCertificate(path string, data []byte) error {
	if err := writeSecret(path, data); err != nil {
		return err
	}
	return os.Chmod(path, 0o644)
}
