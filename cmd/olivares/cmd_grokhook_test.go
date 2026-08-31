// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"strings"
	"testing"
)

// El comando tiene que estar REGISTRADO en la raíz, no sólo existir. Un comando que compila y
// que nadie monta es exactamente el defecto que ya nos costó una corrida de 4484 s con el
// conector: cableado, verde y sin ofrecer.
func TestGrokHookEstaMontadoEnLaRaiz(t *testing.T) {
	t.Parallel()

	root := newRootCmd()
	var visto bool
	for _, c := range root.Commands() {
		if c.Name() == "grok-hook" {
			visto = true
			if c.Short == "" {
				t.Fatal("un comando sin Short no aparece usable en la ayuda")
			}
		}
	}
	if !visto {
		t.Fatal("grok-hook no está montado en la raíz: existe y no lo alcanza nadie")
	}
	// Control positivo del barrido: si `Commands()` viniera vacío, el bucle pasaría solo.
	if len(root.Commands()) < 5 {
		t.Fatalf("la raíz sólo monta %d comandos: el barrido no examinó nada", len(root.Commands()))
	}
}

// ⛔ SIN PLANO DE CONTROL, UN EVENTO SIN VETO SALE 0 Y AUN ASÍ DEJA RASTRO. Es el camino que
// prueba las tres piezas juntas —comando, cliente y render— y el único deny-closed que se puede
// ejercitar EN PROCESO: el de `pre_tool_use` termina en `os.Exit(2)`, que mataría al propio
// binario de test.
//
// ⚠ Ese hueco se dice y no se tapa: la salida 2 la cubre una prueba de BINARIO, como el
// `codexhookpep_e2e_test.go` del hermano. Aquí se verifica todo lo demás del camino.
func TestGrokHookDenyClosedSinEndpointDejaRastroYNoBloquea(t *testing.T) {
	t.Parallel()

	root := newRootCmd()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetIn(strings.NewReader(`{"hookEventName":"stop","sessionId":"s-1"}`))
	root.SetArgs([]string{"grok-hook"})

	if _, err := root.ExecuteC(); err != nil {
		t.Fatalf("un evento sin veto no puede devolver error: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("sin veto no hay cuerpo que el agente lea: %q", out.String())
	}
	// La frase la emite `session.render` (hookclient.go:149) y es la MISMA que afirma su hermano
	// `TestUnDenyNoExpresableSaleEnStderrAunqueNoBloquee` (hookclient_test.go:119). Está en inglés
	// desde 1629672cb («emit user-facing diagnostics in English»), que actualizó todos los tests de
	// `connectors/grok/` y no vio éste, que vive en otro paquete. El diagnóstico sigue en castellano:
	// el idioma del producto y el de la prueba son cosas distintas.
	if !strings.Contains(errb.String(), "nothing was blocked") {
		t.Fatalf("la negativa tiene que llegar al operador diciendo que no impidió nada: %q", errb.String())
	}
	if !strings.Contains(errb.String(), "deny-closed") {
		t.Fatalf("y tiene que decir por qué: %q", errb.String())
	}
}
