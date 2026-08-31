// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const cuerpoHerramienta = `{"hookEventName":"pre_tool_use","sessionId":"s-1","toolName":"Bash"}`

func servidorQueContesta(t *testing.T, estado int, cuerpo string, visto *http.Header) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if visto != nil {
			*visto = r.Header.Clone()
		}
		w.WriteHeader(estado)
		_, _ = w.Write([]byte(cuerpo))
	}))
	t.Cleanup(s.Close)
	return s
}

func correr(t *testing.T, cuerpo string, cfg ClientConfig) ClientResult {
	t.Helper()
	return RunClient(context.Background(), strings.NewReader(cuerpo), cfg)
}

// ⛔ LAS CUATRO FORMAS DE QUE EL PLANO DE CONTROL NO CONTESTE SON UN DENY. Un plano caído tiene
// que BLOQUEAR al agente, no volverse invisible — y en `pre_tool_use`, que es donde el veto se
// honra, la diferencia es que el acto ocurra o no.
func TestElPlanoCaidoDeniegaEnLasCuatroFormas(t *testing.T) {
	t.Parallel()

	noJSON := servidorQueContesta(t, 200, "esto no es un veredicto", nil)
	quinientos := servidorQueContesta(t, 500, `{"verdict":"allow"}`, nil)

	casos := map[string]struct {
		cfg    ClientConfig
		reason string
	}{
		"sin endpoint": {
			reason: "no governance endpoint is configured (deny-closed)",
		},
		"inalcanzable": {
			cfg:    ClientConfig{Endpoint: "http://127.0.0.1:1/hook"},
			reason: "the governance plane is unreachable (deny-closed)",
		},
		"estado inservible": {
			cfg:    ClientConfig{Endpoint: quinientos.URL},
			reason: "the governance plane returned an unusable status (deny-closed)",
		},
		"cuerpo que no es un veredicto": {
			cfg:    ClientConfig{Endpoint: noJSON.URL},
			reason: "the governance-plane response is not a verdict (deny-closed)",
		},
	}
	for nombre, caso := range casos {
		res := correr(t, cuerpoHerramienta, caso.cfg)
		if res.ExitCode != ExitDeny {
			t.Fatalf("%s: tiene que salir %d (deny), salió %d", nombre, ExitDeny, res.ExitCode)
		}
		if !strings.Contains(string(res.Stdout), `"decision":"deny"`) {
			t.Fatalf("%s: el cuerpo tiene que llevar la negativa: %s", nombre, res.Stdout)
		}
		if res.Stderr != caso.reason {
			t.Fatalf("%s: razón = %q, quiere %q", nombre, res.Stderr, caso.reason)
		}
	}
}

// ⛔ UN VEREDICTO NO RECONOCIDO ES UN DENY, no un permiso. Es la puerta que un plano de control
// roto —o una versión futura del sobre— abriría sin que nadie lo notase.
func TestUnVeredictoNoReconocidoEsDeny(t *testing.T) {
	t.Parallel()

	for _, v := range []string{`"maybe"`, `""`, `"ALLOW"`, `null`} {
		s := servidorQueContesta(t, 200, `{"verdict":`+v+`}`, nil)
		res := correr(t, cuerpoHerramienta, ClientConfig{Endpoint: s.URL})
		if res.ExitCode != ExitDeny {
			t.Fatalf("verdict=%s tenía que denegar, salió %d", v, res.ExitCode)
		}
		if res.Stderr != "unrecognized verdict from the governance plane (deny-closed)" {
			t.Fatalf("verdict=%s: razón inesperada: %q", v, res.Stderr)
		}
	}
	// Control positivo: el veredicto BUENO sí permite, o lo de arriba lo pasaría un cliente que
	// deniegue siempre.
	ok := servidorQueContesta(t, 200, `{"verdict":"allow","event":"pre_tool_use"}`, nil)
	res := correr(t, cuerpoHerramienta, ClientConfig{Endpoint: ok.URL})
	if res.ExitCode != ExitAllow || len(res.Stdout) != 0 {
		t.Fatalf("un allow es NO INTERFERENCIA: código %d, cuerpo %q", res.ExitCode, res.Stdout)
	}
}

// ⛔ UN DENY QUE EL AGENTE NO PUEDE HONRAR NO SE PIERDE. Hacia el agente sale 0 con cuerpo vacío
// —un 2 que ignora no impide nada y falsea el registro—, pero la razón va a stderr para que el
// operador vea que HUBO una negativa. Callarla convertiría «no pude impedirlo» en «no pasó nada».
func TestUnDenyNoExpresableSaleEnStderrAunqueNoBloquee(t *testing.T) {
	t.Parallel()

	s := servidorQueContesta(t, 200, `{"verdict":"deny","reason":"politica X","event":"stop"}`, nil)
	res := correr(t, `{"hookEventName":"stop","sessionId":"s"}`, ClientConfig{Endpoint: s.URL})

	if res.ExitCode != ExitAllow {
		t.Fatalf("un 2 que el agente no honra no impide nada: salió %d", res.ExitCode)
	}
	if len(res.Stdout) != 0 {
		t.Fatalf("sin veto no hay cuerpo que el agente lea: %q", res.Stdout)
	}
	if !strings.Contains(res.Stderr, "politica X") {
		t.Fatalf("la razón tiene que llegar al operador: %q", res.Stderr)
	}
	if !strings.Contains(res.Stderr, "nothing was blocked") {
		t.Fatalf("y tiene que decir que NO impidió nada, o se lee como un bloqueo: %q", res.Stderr)
	}
}

// ⛔ EL CUERPO TRUNCADO SE SALVA POR EL ENTORNO, que es la segunda vía que Grok ofrece y la
// diferencia con el hermano de Codex. Sin ella la negativa se emitiría sin saber de qué evento
// es, y por tanto en la forma más estricta.
func TestUnCuerpoTruncadoRecuperaElEventoYDeniegaEnSuForma(t *testing.T) {
	t.Parallel()

	env := func(k string) string {
		if k == EnvHookEvent {
			return EventPreToolUse
		}
		return ""
	}
	res := correr(t, `{"hookEventName":"pre_tool_`, ClientConfig{Env: env})
	if res.ExitCode != ExitDeny {
		t.Fatalf("sin endpoint y con evento conocido se deniega con veto: %d", res.ExitCode)
	}
	if !strings.Contains(string(res.Stdout), `"decision":"deny"`) {
		t.Fatalf("y en la forma que pre_tool_use honra: %s", res.Stdout)
	}
	// Sin la segunda vía, el mismo cuerpo no puede saber el evento y NO puede emitir el veto.
	sinEnv := correr(t, `{"hookEventName":"pre_tool_`, ClientConfig{})
	if sinEnv.ExitCode == ExitDeny && len(sinEnv.Stdout) > 0 {
		t.Fatal("sin entorno no debería poder rendir la forma de pre_tool_use: la celda no mide el entorno")
	}
}

// Las pistas de identidad y el portador viajan en cabeceras, y el token NUNCA en stdout/stderr.
func TestElTokenViajaEnCabeceraYNoEnLaSalida(t *testing.T) {
	t.Parallel()

	var visto http.Header
	s := servidorQueContesta(t, 200, `{"verdict":"allow","event":"pre_tool_use"}`, &visto)
	res := correr(t, cuerpoHerramienta, ClientConfig{
		Endpoint: s.URL, Token: "t-secreto", Tenant: "acme", Agent: "a1",
	})
	if visto.Get("Authorization") != "Bearer t-secreto" {
		t.Fatalf("el portador tiene que viajar en la cabecera: %q", visto.Get("Authorization"))
	}
	if visto.Get(hdrTenant) != "acme" || visto.Get(hdrAgent) != "a1" {
		t.Fatalf("las pistas de identidad no llegaron: %v", visto)
	}
	if strings.Contains(string(res.Stdout)+res.Stderr, "t-secreto") {
		t.Fatal("el token no puede aparecer en la salida del proceso")
	}
}
