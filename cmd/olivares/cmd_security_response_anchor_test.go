// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Los dos productores de respuesta a incidentes firman con Ed25519, y una clave PUBLICA y un
// seed miden los dos 32 bytes. Sin un ancla extrinseca, pasar la mitad publica en --sign-key se
// acepta, deriva OTRO par y emite un artefacto firmado por una clave que no tiene nadie, con
// rc=0. Es la confusion que el contraste `sol max` reprodujo para `ddil export` (F-02); estos
// casos la fijan para `security advisories` y `security rulepack sign`.

func secRespDraft(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "draft.json")
	if err := os.WriteFile(p, []byte(draftAdvisoryFeed), 0o644); err != nil {
		t.Fatalf("write draft: %v", err)
	}
	return p
}

// ⛔ UN BORRADOR VALIDO POR PRODUCTOR, y no es cosmetico: el primer intento paso el borrador de
// ADVISORIES a `rulepack sign`, que muere antes de llegar a la firma con «rule-pack version must
// be > 0». El caso salia rojo bajo el mutante —parecia que cazaba— pero por OTRO motivo, asi que
// no testificaba sobre el ancla. Lo destapo el control por mutacion, no la lectura.
func secRespPackDraft(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "pack-draft.json")
	if err := os.WriteFile(p, []byte(draftRulePackGraft), 0o644); err != nil {
		t.Fatalf("write rule-pack draft: %v", err)
	}
	return p
}

func secRespFprint(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return hex.EncodeToString(sum[:])[:8]
}

// ⛔ EL CASO QUE MOTIVA TODO: la mitad PUBLICA pasada como clave de firma.
// Antes del ancla esto salia 0 y escribia un feed firmado por un par derivado.
func TestSecurityResponseRefusesPublicKeyAsSigningSeed(t *testing.T) {
	dir := t.TempDir()
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	draft := secRespDraft(t, dir)
	pubB64 := base64.StdEncoding.EncodeToString(pub)

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"advisories", []string{"advisories", "--in", draft, "--out", filepath.Join(dir, "feed.json"),
			"--sign-key", pubB64, "--expect-pubkey", pubB64}},
		{"rulepack-sign", []string{"rulepack", "sign", "--in", secRespPackDraft(t, dir), "--out", filepath.Join(dir, "pack.json"),
			"--sign-key", pubB64, "--expect-pubkey", pubB64}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runSecResp(tc.args...)
			if err == nil {
				t.Fatalf("la publica pasada como --sign-key fue ACEPTADA; salida: %s", out)
			}
			msg := err.Error() + out
			if !strings.Contains(msg, "REFUSING to sign") {
				t.Fatalf("el rechazo no se nombra; mensaje: %s", msg)
			}
			// La huella de la DERIVADA tiene que aparecer: sin ella el operador no puede
			// casar lo que firmo con lo que la flota fija.
			derived := ed25519.NewKeyFromSeed(pub).Public().(ed25519.PublicKey)
			if !strings.Contains(msg, secRespFprint(derived)) {
				t.Fatalf("falta la huella de 8 hex de la clave derivada; mensaje: %s", msg)
			}
		})
	}
}

// ⛔ Y NI UN BYTE DE CLAVE EN EL MENSAJE. Este es el caso que el encargo pide reproducir:
// el error se escribe SIN la clave. Un diagnostico que imprima el material en base64 publica
// 44 caracteres en el stderr de la ceremonia — y si el error fue apuntar --expect-pubkey al
// fichero de la PRIVADA, eso manda el seed a los logs.
func TestSecurityResponseErrorCarriesFingerprintsNotKeyMaterial(t *testing.T) {
	dir := t.TempDir()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	draft := secRespDraft(t, dir)

	// Un par CORRECTO pero anclado contra OTRA publica: el productor debe negarse.
	out, err := runSecResp("advisories", "--in", draft, "--out", filepath.Join(dir, "feed.json"),
		"--sign-key", base64.StdEncoding.EncodeToString(priv),
		"--expect-pubkey", base64.StdEncoding.EncodeToString(otherPub))
	if err == nil {
		t.Fatalf("firmo contra un ancla que no le corresponde; salida: %s", out)
	}
	msg := err.Error() + out

	// Lo que NO puede aparecer, en las tres codificaciones en que podria colarse.
	forbidden := map[string]string{
		"seed base64 estandar":    base64.StdEncoding.EncodeToString(priv.Seed()),
		"privada completa base64": base64.StdEncoding.EncodeToString(priv),
		"privada raw-url":         base64.RawURLEncoding.EncodeToString(priv),
		"publica propia base64":   base64.StdEncoding.EncodeToString(pub),
		"publica ancla base64":    base64.StdEncoding.EncodeToString(otherPub),
	}
	for what, lit := range forbidden {
		if strings.Contains(msg, lit) {
			t.Fatalf("el diagnostico publica %s — un error no puede filtrar material de clave", what)
		}
	}
	// Y lo que SI: las dos huellas de 8 hex, que identifican sin revelar.
	for _, fp := range []string{secRespFprint(pub), secRespFprint(otherPub)} {
		if !strings.Contains(msg, fp) {
			t.Fatalf("falta una huella de 8 hex; mensaje: %s", msg)
		}
		if len(fp) != 8 {
			t.Fatalf("la huella no mide 8 hex: %q", fp)
		}
	}
}

// Las TRES codificaciones del seed que el cargador acepta tienen que seguir funcionando con el
// ancla puesta: si el ancla solo entendiera una, un formato valido se leeria como «clave
// equivocada», que es el mas caro de diagnosticar de los tres errores posibles.
func TestSecurityResponseAcceptsThreeSeedEncodings(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pubB64 := base64.StdEncoding.EncodeToString(pub)

	for _, tc := range []struct {
		name    string
		signKey func(dir string) string
	}{
		{"base64 estandar (privada de 64 bytes)", func(string) string {
			return base64.StdEncoding.EncodeToString(priv)
		}},
		{"base64 raw-url (seed de 32 bytes)", func(string) string {
			return base64.RawURLEncoding.EncodeToString(priv.Seed())
		}},
		{"@fichero con el seed", func(dir string) string {
			p := filepath.Join(dir, "seed.key")
			if err := os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
				t.Fatalf("write seed: %v", err)
			}
			return "@" + p
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			draft := secRespDraft(t, dir)
			feed := filepath.Join(dir, "feed.json")
			if out, err := runSecResp("advisories", "--in", draft, "--out", feed,
				"--sign-key", tc.signKey(dir), "--expect-pubkey", pubB64); err != nil {
				t.Fatalf("codificacion rechazada: %v; salida: %s", err, out)
			}
			if _, err := os.Stat(feed); err != nil {
				t.Fatalf("no se escribio el feed: %v", err)
			}
		})
	}
}

// El ancla acepta las mismas tres formas, por la misma razon.
func TestSecurityResponseAnchorAcceptsThreeEncodings(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signKey := base64.StdEncoding.EncodeToString(priv)

	for _, tc := range []struct {
		name   string
		anchor func(dir string) string
	}{
		{"base64 estandar", func(string) string { return base64.StdEncoding.EncodeToString(pub) }},
		{"base64 raw-url", func(string) string { return base64.RawURLEncoding.EncodeToString(pub) }},
		{"@fichero", func(dir string) string {
			p := filepath.Join(dir, "anchor.pub")
			if err := os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(pub)+"\n"), 0o644); err != nil {
				t.Fatalf("write anchor: %v", err)
			}
			return "@" + p
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			draft := secRespDraft(t, dir)
			feed := filepath.Join(dir, "feed.json")
			if out, err := runSecResp("advisories", "--in", draft, "--out", feed,
				"--sign-key", signKey, "--expect-pubkey", tc.anchor(dir)); err != nil {
				t.Fatalf("ancla rechazada: %v; salida: %s", err, out)
			}
		})
	}
}

// Sin ancla no se firma: es una bandera requerida en los dos productores, y el mensaje dice
// POR QUE hace falta, no solo que falta.
func TestSecurityResponseAnchorIsRequired(t *testing.T) {
	dir := t.TempDir()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	draft := secRespDraft(t, dir)
	for _, args := range [][]string{
		{"advisories", "--in", draft, "--out", filepath.Join(dir, "f.json"), "--sign-key", base64.StdEncoding.EncodeToString(priv)},
		{"rulepack", "sign", "--in", secRespPackDraft(t, dir), "--out", filepath.Join(dir, "p.json"), "--sign-key", base64.StdEncoding.EncodeToString(priv)},
	} {
		out, err := runSecResp(args...)
		if err == nil {
			t.Fatalf("%s firmo SIN ancla; salida: %s", args[0], out)
		}
		if !strings.Contains(err.Error()+out, "expect-pubkey") {
			t.Fatalf("el error no nombra la bandera que falta: %v", err)
		}
	}
}
