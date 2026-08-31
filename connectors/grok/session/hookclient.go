// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package session

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// hookclient.go es el lado del PROCESO: lee el payload que Grok manda por stdin, lo reenvía al
// PEP gobernado y devuelve qué escribir y con qué código salir.
//
// Es DENY-CLOSED de punta a punta. Endpoint sin fijar, inalcanzable, no-2xx, cuerpo ilegible —
// cada uno emite una negativa que este paquete renderiza él mismo, en la forma que ESE evento
// honra. Un plano de control caído tiene que bloquear al agente, no volverse invisible.
//
// ⛔ Y AQUÍ SE CIERRA EL CONTRATO QUE EL PEP ABRIÓ. El PEP contesta un `Response` con el
//    veredicto y NO el cuerpo renderizado, porque `Render` está hecho para el proceso y su
//    salida para un deny no expresable es indistinguible de un allow. El renderizado ocurre
//    aquí, que es donde hay un stdout y un código de salida de verdad. Cada lado hace lo suyo y
//    ninguno adivina lo del otro.

// ClientConfig es la configuración del comando de hook, que pone el entorno.
type ClientConfig struct {
	Endpoint string
	Token    string
	Tenant   string
	Agent    string
	Org      string
	Account  string
	Timeout  time.Duration
	Client   *http.Client
	// Env es la segunda vía para el nombre del evento. Va explícita y no se lee del proceso:
	// un paquete que consulta el entorno por su cuenta no se puede probar sin ensuciarlo.
	Env func(string) string
}

// ClientResult es lo que el proceso llamante debe hacer: escribir Stdout, escribir Stderr y
// salir con ExitCode. Se DEVUELVE en vez de ejecutarse para que el camino entero sea probable
// sin una frontera de proceso.
type ClientResult struct {
	Stdout   []byte
	Stderr   string
	ExitCode int
}

// RunClient lee el payload, lo reenvía y devuelve qué emitir. NUNCA devuelve error: no hay
// ningún modo de fallo en el que la respuesta correcta sea no escribir nada, porque un stdout
// vacío el agente lo lee como «sin objeción».
func RunClient(ctx context.Context, in io.Reader, cfg ClientConfig) ClientResult {
	body, _ := io.ReadAll(io.LimitReader(in, maxHookBody))

	// ⛔ EL EVENTO SE RECUPERA POR LAS DOS VÍAS, y ésta es la diferencia con el hermano de
	//    Codex. Un cuerpo truncado —la malformación que produce un stdin cortado— no parsea, y
	//    sin el evento la negativa se emitiría en la forma más estricta por no saber cuál es.
	//    Grok pone `GROK_HOOK_EVENT` en el entorno ADEMÁS del cuerpo: dos canales que no se
	//    corrompen el uno al otro.
	event := RecoverEvent(body, cfg.Env)

	denyLocal := func(reason string) ClientResult {
		return render(event, VerdictDeny, reason)
	}

	if cfg.Endpoint == "" {
		return denyLocal("no governance endpoint is configured (deny-closed)")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return denyLocal("could not build the governance-plane request (deny-closed)")
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	for k, v := range map[string]string{
		hdrTenant: cfg.Tenant, hdrAgent: cfg.Agent, hdrOrg: cfg.Org, hdrAccount: cfg.Account,
	} {
		if v != "" {
			req.Header.Set(k, v)
		}
	}

	cli := cfg.Client
	if cli == nil {
		t := cfg.Timeout
		if t <= 0 {
			t = 5 * time.Second
		}
		cli = &http.Client{Timeout: t}
	}
	resp, err := cli.Do(req)
	if err != nil {
		return denyLocal("the governance plane is unreachable (deny-closed)")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return denyLocal("the governance plane returned an unusable status (deny-closed)")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxHookBody))
	if err != nil {
		return denyLocal("could not read the governance-plane response (deny-closed)")
	}
	var env Response
	if err := json.Unmarshal(raw, &env); err != nil {
		return denyLocal("the governance-plane response is not a verdict (deny-closed)")
	}
	// ⛔ UN VEREDICTO QUE NO ES NI ALLOW NI DENY ES UN DENY. Un valor desconocido en este campo
	//    no puede degradar a permiso: sería exactamente la puerta que un plano de control roto
	//    abriría sin que nadie lo notase.
	switch env.Verdict {
	case "allow":
		return render(eventoDe(env, event), VerdictAllow, "")
	case "deny":
		return render(eventoDe(env, event), VerdictDeny, env.Reason)
	default:
		return denyLocal("unrecognized verdict from the governance plane (deny-closed)")
	}
}

// eventoDe prefiere el evento que devuelve el PEP: él pudo recuperarlo por el entorno de SU lado
// cuando este proceso no lo tenía. Cae al local sólo si el sobre no lo trae.
func eventoDe(env Response, local string) string {
	if env.Event != "" {
		return env.Event
	}
	return local
}

// render traduce un veredicto a lo que el proceso debe emitir.
//
// ⛔ EL DENY NO EXPRESABLE NO SE PIERDE. `Render` devuelve `expressed=false` cuando el agente no
//
//	honra un deny en ese evento, y entonces lo correcto hacia el AGENTE es salir 0 con el cuerpo
//	vacío: emitir un 2 que ignora no impide nada y además deja en el registro la ilusión de que
//	sí. Pero la razón va a **stderr** igualmente, porque el operador que lee el log del agente
//	tiene que poder ver que hubo una negativa aunque no pudiera aplicarse. Callarla sería
//	convertir «no pude impedirlo» en «no pasó nada».
func render(event string, v Verdict, reason string) ClientResult {
	stdout, code, expressed := Render(event, v, reason)
	res := ClientResult{Stdout: stdout, ExitCode: code}
	if v == VerdictDeny && reason != "" {
		res.Stderr = reason
		if !expressed {
			res.Stderr = "warning: " + reason + " (this event does not support veto: nothing was blocked)"
		}
	}
	return res
}
