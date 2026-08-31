// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0
//
// El CICLO DE VIDA del receptor: apagado por defecto, ligado en Open, cerrado en Close.
//
// ⛔ Existe porque un receptor cableado a medias no se nota. Compila, sus pruebas de unidad pasan y
// el puerto nunca se abre — o peor, se abre y nadie lo cierra. Estas celdas prueban el CABLE, no el
// componente.
package codex

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

// APAGADO POR DEFECTO. Un conector que abre un puerto sin que nadie lo pida es una superficie nueva
// que nadie decidió: la telemetría de Codex se habilita, no se hereda.
func TestOTLPReceiverIsOffByDefault(t *testing.T) {
	s := New()
	if err := s.Open(context.Background(), sdk.Config{}); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if s.otlp != nil {
		t.Fatal("el receptor se ha levantado sin pedirlo")
	}
	if n, d := s.drainOTLP(); n != 0 || d != 0 {
		t.Fatalf("drenaje con receptor apagado devolvió %d/%d", n, d)
	}
}

// LIGADO EN Open y SIRVIENDO: se comprueba con una petición real, no con `s.otlp != nil`. Un puntero
// no prueba que el puerto acepte.
func TestOTLPReceiverBindsAndServesWhenEnabled(t *testing.T) {
	s := New()
	cfg := sdk.Config{Settings: map[string]string{
		"otlp_http":      "true",
		"otlp_http_addr": "127.0.0.1:0",
	}}
	if err := s.Open(context.Background(), cfg); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	if s.otlpLis == nil {
		t.Fatal("no se ligó ningún listener")
	}
	addr := s.otlpLis.Addr().String()
	resp, err := http.Post("http://"+addr+"/", "application/x-protobuf",
		nil)
	if err != nil {
		t.Fatalf("el puerto no acepta: %v", err)
	}
	_ = resp.Body.Close()

	// ⛔ Y CIERRA DE VERDAD. Un Close que no cierra deja el puerto tomado hasta que muere el
	//    proceso, y el siguiente arranque falla con «address already in use» sin decir por qué —
	//    exactamente lo que me pasó hoy levantando motores de prueba.
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
		_ = c.Close()
		t.Fatal("el puerto sigue aceptando después de Close")
	}
}
