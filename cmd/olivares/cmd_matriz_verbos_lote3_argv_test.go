// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// THE SECOND HALF OF THE MATRIX SWEEP: the governed verbs that only exist against a server.
//
// Same contract as cmd_matriz_verbos_lote2_argv_test.go and the same harness: a COMPLETE argv
// against `https://127.0.0.1:9`, where nothing listens, so a failure that is not a USAGE error is
// the expected end of the road. What is asserted is that the verb is registered and parses its own
// shape — not what it does once connected.
//
// ⛔ `audit recover` SEALS A CORRUPT AUDIT TAIL and `audit checkpoint` SIGNS one. They are in this
// table on purpose and they are safe here for one reason only: with an unreachable server they
// cannot reach the state they would change. That is the same reason cmd_confirm_test.go gives for
// exercising the destructive verbs, and it is the reason this table must never gain a case that
// points at a REAL server.
//
// One argv can credit more than one verb, and that is not a trick: `governance guardian rules`
// really does exercise all three levels of that path, and the matrix reads each of them.
var lote3VerbArgv = []struct {
	name string
	argv []string
}{
	// ⛔ LOS TRES DE `audit` NO LLEVAN `--server`, y descubrirlo es la mitad del valor de este
	// fichero: mi primera versión les pasó el argv de red y los tres salieron «unknown flag:
	// --server». No son verbos de red — operan sobre el ALMACÉN local (`addStoreFlags`:
	// --engine/--data-dir/--dsn). El testigo falló RUIDOSAMENTE y dijo exactamente eso, que es
	// para lo que existe la aserción de «usage error».
	//
	// Y por eso van con `--data-dir` en t.TempDir(): `audit recover` SELLA una cola de auditoría
	// corrupta y `checkpoint` FIRMA una. Contra un almacén de usar y tirar no pueden alcanzar
	// ningún estado que importe; contra el almacén real habrían sido una prueba destructiva.
	{"audit checkpoint", []string{"audit", "checkpoint", "--engine", "sqlite",
		"--data-dir", "@TMP@", "--tenant", "00000000-0000-0000-0000-000000000000"}},
	{"audit observe-report", []string{"audit", "observe-report", "--engine", "sqlite",
		"--data-dir", "@TMP@", "--tenant", "00000000-0000-0000-0000-000000000000"}},
	// `recover` exige ademas --pubkey: la clave de checkpoint fijada FUERA de la caja. Sin ella
	// se niega, y esa negativa es correcta — sellar una cola corrupta sin una raiz de confianza
	// externa seria exactamente el fallo que el verbo existe para evitar. Se le da una Ed25519
	// valida POR FORMA (32 bytes en base64); no tiene que verificar nada contra un almacen vacio.
	{"audit recover", []string{"audit", "recover", "--engine", "sqlite",
		"--data-dir", "@TMP@", "--tenant", "00000000-0000-0000-0000-000000000000",
		"--pubkey", "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="}},

	{"compliance holds custody", []string{"compliance", "holds", "custody", "hold-1",
		"--server", "https://127.0.0.1:9", "--token", "t",
		"--tenant", "00000000-0000-0000-0000-000000000000"}},
	{"compliance depth us-law", []string{"compliance", "depth", "us-law",
		"--server", "https://127.0.0.1:9", "--token", "t",
		"--tenant", "00000000-0000-0000-0000-000000000000"}},
	{"compliance oscal ls", []string{"compliance", "oscal", "ls",
		"--server", "https://127.0.0.1:9", "--token", "t",
		"--tenant", "00000000-0000-0000-0000-000000000000"}},

	{"governance guardian rules", []string{"governance", "guardian", "rules",
		"--server", "https://127.0.0.1:9", "--token", "t",
		"--tenant", "00000000-0000-0000-0000-000000000000"}},
	{"governance guardian actions", []string{"governance", "guardian", "actions",
		"--server", "https://127.0.0.1:9", "--token", "t",
		"--tenant", "00000000-0000-0000-0000-000000000000"}},
	{"governance rbac delegation-authority", []string{"governance", "rbac", "delegation-authority",
		"--server", "https://127.0.0.1:9", "--token", "t",
		"--tenant", "00000000-0000-0000-0000-000000000000"}},
}

func TestLote3GovernedVerbsParseTheirOwnShape(t *testing.T) {
	for _, c := range lote3VerbArgv {
		t.Run(c.name, func(t *testing.T) {
			argv := make([]string, len(c.argv))
			copy(argv, c.argv)
			for i := range argv {
				if argv[i] == "@TMP@" {
					argv[i] = t.TempDir()
				}
			}
			out, err := runCLI(t, argv...)
			if err == nil {
				return
			}
			for _, usage := range usageMarkers {
				if strings.Contains(err.Error(), usage) || strings.Contains(out, usage) {
					t.Fatalf("%q is a USAGE error, so the verb's shape moved: %v\n%s", usage, err, out)
				}
			}
		})
	}
}

// TestCodexManagedConfigRendersFromAPolicy is a REAL exercise, not a shape check: `codex
// managed-config` is offline by construction — it reads a governance Policy JSON and renders the
// two Codex artifacts — so there is no excuse for testing it against an unreachable port.
//
// It also pins the deny-closed half. The command's own help says an invalid policy FAILS the
// render, and `DisallowUnknownFields` is what enforces it; a witness that only proved the happy
// path would pass just as well if that were removed.
func TestCodexManagedConfigRendersFromAPolicy(t *testing.T) {
	tmp := t.TempDir()
	good := filepath.Join(tmp, "policy.json")
	if err := os.WriteFile(good, []byte(`{"requirements":{"allowed_approval_policies":["untrusted"]},"managed_config":{}}`), 0o600); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if _, err := runCLI(t, "codex", "managed-config", "--policy", good, "--validate"); err != nil {
		t.Fatalf("a well-formed policy must validate offline: %v", err)
	}

	bad := filepath.Join(tmp, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"requirements":{"allowed_approval_policies":["untrusted"]},"managed_config":{},"teleport":true}`), 0o600); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	if _, err := runCLI(t, "codex", "managed-config", "--policy", bad, "--validate"); err == nil {
		t.Fatal("an unknown field must fail the render — the command documents itself as deny-closed")
	}
}

// TestDDILKeygenIsOfflineAndReal — the ddil area needed one argv literal and keygen is the subverb
// that needs nothing but entropy, so it gets exercised for real rather than parked against port 9.
func TestDDILKeygenIsOfflineAndReal(t *testing.T) {
	out, err := runCLI(t, "ddil", "keygen")
	if err != nil {
		t.Fatalf("ddil keygen must work offline: %v\n%s", err, out)
	}
	if strings.TrimSpace(out) == "" {
		t.Fatal("ddil keygen produced no key material at all")
	}
}
