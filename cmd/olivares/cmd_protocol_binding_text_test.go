// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// ⛔ ESTE TESTIGO EXISTE PARA ZANJAR UN DESACUERDO, no para adornar el arreglo.
//
// `main` lleva un comentario `render-exempt` sobre esta misma función que dice: «the response is a
// server-defined object decoded into `any`… There is no schema here to lay out in columns, so
// indented JSON IS the text form — Deliberate, not the defect». Esta rama dice lo contrario:
// que `-o text` devolvía JSON y eso ES el defecto.
//
// Las dos afirmaciones no pueden ser ciertas, y la que decide no es cuál aterrizó antes.
// `renderStatusOut` NO necesita esquema: `writeStatusLines` (render.go:266-269) aplana CUALQUIER
// JSON decodificado en líneas `path\tvalue`, con los mapas en orden de clave. Así que «no hay
// esquema que poner en columnas» describe mal lo que la herramienta puede hacer — y esto lo mide
// en vez de discutirlo.
func nuevoCmdConSalida(formato string) (*cobra.Command, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	cmd := &cobra.Command{Use: "binding"}
	cmd.Flags().String("output", "", "output format")
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	if formato != "" {
		_ = cmd.Flags().Set("output", formato)
	}
	return cmd, buf
}

// El veredicto tiene que ser uno de los que el comando reconoce (`CLEAN`/`LIMPIO`/`BROKEN`/`ROTO`/
// `UNKNOWN`/`NO_HE_PODIDO_MIRAR`): con cualquier otro, la funcion rechaza ANTES de que el render
// importe, y el testigo mediria el validador en vez de la salida. Me paso al escribirlo — puse
// "allow" y el rojo era del veredicto, no del formato.
const cuerpoBinding = `{"verdict":"CLEAN","binding":{"protocol":"mcp","version":"2025-06-18"},"checked_at":"2026-08-23T00:00:00Z"}`

func TestProtocolBindingTextIsNotJSON(t *testing.T) {
	cmd, buf := nuevoCmdConSalida("text")
	if err := renderProtocolBindingResponse(cmd, []byte(cuerpoBinding)); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := buf.String()

	// La prueba directa: pedir texto y recibir JSON es el defecto.
	if strings.Contains(out, `"verdict"`) || strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("-o text devolvio JSON, que es exactamente lo que el operador NO pidio:\n%s", out)
	}
	// Y la mitad afirmativa, sin la cual «no es JSON» lo cumpliria tambien una salida vacia.
	if !strings.Contains(out, "verdict") || !strings.Contains(out, "CLEAN") {
		t.Errorf("-o text no lleva el veredicto en forma legible:\n%s", out)
	}
	// El objeto anidado tiene que aplanarse: es lo que el comentario de `main` dice imposible.
	if !strings.Contains(out, "protocol") || !strings.Contains(out, "mcp") {
		t.Errorf("-o text no aplano el objeto anidado — «no hay esquema» describiria bien el limite "+
			"si esto fallara, y no falla:\n%s", out)
	}
}

func TestProtocolBindingDefaultIsStillJSON(t *testing.T) {
	// El control que impide «arreglarlo» rompiendo a quien ya lo usa: SIN flag, sigue saliendo
	// JSON, que es lo que este comando imprime hoy y lo que sus tests esperan.
	cmd, buf := nuevoCmdConSalida("")
	if err := renderProtocolBindingResponse(cmd, []byte(cuerpoBinding)); err != nil {
		t.Fatalf("render: %v", err)
	}
	out := strings.TrimSpace(buf.String())
	if !strings.HasPrefix(out, "{") || !strings.Contains(out, `"verdict"`) {
		t.Errorf("sin flag el comando dejo de imprimir JSON: eso rompe a sus consumidores:\n%s", out)
	}
}

func TestProtocolBindingJSONFlagStillJSON(t *testing.T) {
	cmd, buf := nuevoCmdConSalida("json")
	if err := renderProtocolBindingResponse(cmd, []byte(cuerpoBinding)); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(buf.String(), `"verdict"`) {
		t.Errorf("-o json no devolvio JSON:\n%s", buf.String())
	}
}
