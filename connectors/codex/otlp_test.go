// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0
//
// El receptor OTLP de Codex, probado contra el contrato MEDIDO en vivo.
//
// ⛔ Las constantes de este fichero no son inventadas: salen de una captura real de
// `codex-cli 0.147.0` con una sonda que volcaba los bytes sin parsearlos
// (`an internal design note (not shipped)`). Un test escrito contra un esquema supuesto
// mide la suposición.
package codex

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

func kv(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{
		Key:   k,
		Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}},
	}
}

// exportMedido reproduce la FORMA capturada: service.name=codex_exec, version 0.147.0 y un
// registro con su `event.name`.
func exportMedido(eventos ...string) []byte {
	var recs []*logspb.LogRecord
	for i, e := range eventos {
		recs = append(recs, &logspb.LogRecord{
			TimeUnixNano: uint64(time.Unix(1700000000+int64(i), 0).UnixNano()),
			Attributes:   []*commonpb.KeyValue{kv("event.name", e)},
		})
	}
	req := &collogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				kv("service.name", "codex_exec"),
				kv("service.version", "0.147.0"),
			}},
			ScopeLogs: []*logspb.ScopeLogs{{LogRecords: recs}},
		}},
	}
	b, _ := proto.Marshal(req)
	return b
}

func TestOTLPReceiverAcceptsTheMeasuredShape(t *testing.T) {
	r := newOTLPReceiver("/", 100, func() time.Time { return time.Unix(0, 0) })
	// ⛔ LA RUTA ES «/», y esa es la mitad del valor de este test. El exportador de Codex usa el
	//    endpoint autorizado TAL CUAL: un receptor que sólo sirviera `/v1/logs` devolvería 404 y el
	//    agente reintentaría para siempre, con la consola diciendo «Codex no emite telemetría».
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(
		exportMedido("codex.startup_phase", "codex.user_prompt")))
	req.Header.Set("Content-Type", "application/x-protobuf")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("POST a / devolvió %d, se esperaba 200", w.Code)
	}
	evs, dropped := r.drain()
	if dropped != 0 {
		t.Errorf("descartados=%d, se esperaba 0", dropped)
	}
	if len(evs) != 2 {
		t.Fatalf("recibidos %d eventos, se esperaban 2", len(evs))
	}
	if evs[0].Name != "codex.startup_phase" || evs[1].Name != "codex.user_prompt" {
		t.Errorf("nombres de evento perdidos: %+v", evs)
	}
	if evs[0].Service != "codex_exec" || evs[0].Version != "0.147.0" {
		t.Errorf("atribución de recurso perdida: service=%q version=%q", evs[0].Service, evs[0].Version)
	}
}

// ⛔ LA RUTA EQUIVOCADA TIENE QUE SER 404, no un 200 silencioso. Si aceptase cualquier ruta, el día
// que el exportador cambie de sitio el receptor seguiría «verde» sin recibir nada — que es la forma
// de fallo que este receptor existe para no tener.
func TestOTLPReceiverRefusesAnotherPath(t *testing.T) {
	r := newOTLPReceiver("/", 100, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/logs", bytes.NewReader(exportMedido("codex.api_request")))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("una ruta distinta devolvió %d, se esperaba 404", w.Code)
	}
	if evs, _ := r.drain(); len(evs) != 0 {
		t.Errorf("se ingirió %d evento(s) desde la ruta equivocada", len(evs))
	}
}

// Un cuerpo ilegible NO es un 200: aceptarlo dejaría el receptor verde sin haber recibido nada.
func TestOTLPReceiverRefusesGarbage(t *testing.T) {
	r := newOTLPReceiver("/", 100, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte("esto no es protobuf")))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("un cuerpo ilegible devolvió %d, se esperaba 400", w.Code)
	}
}

// ⚠ Al llenarse descarta el MÁS VIEJO y lo CUENTA. Un receptor que se queda mudo al llenarse miente
// por omisión, y el número de descartados es la única forma de saberlo desde fuera.
func TestOTLPReceiverCountsWhatItDrops(t *testing.T) {
	r := newOTLPReceiver("/", 2, nil)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(
		exportMedido("uno", "dos", "tres", "cuatro")))
	r.ServeHTTP(w, req)
	evs, dropped := r.drain()
	if len(evs) != 2 {
		t.Fatalf("retuvo %d, se esperaban 2", len(evs))
	}
	if dropped != 2 {
		t.Fatalf("descartados=%d, se esperaban 2", dropped)
	}
	if evs[0].Name != "tres" || evs[1].Name != "cuatro" {
		t.Errorf("descartó los NUEVOS en vez de los viejos: %+v", evs)
	}
}

// CONTROL QUE NO DEBE DISPARAR: un export SIN `event.name` no aporta nada que este conector sepa
// usar, y se ignora sin error. Si esta celda se pusiera roja, el receptor estaría inventando
// eventos a partir de registros que no los nombran.
func TestOTLPReceiverIgnoresRecordsWithoutEventName(t *testing.T) {
	req := &collogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			ScopeLogs: []*logspb.ScopeLogs{{LogRecords: []*logspb.LogRecord{{}}}},
		}},
	}
	b, _ := proto.Marshal(req)
	r := newOTLPReceiver("/", 100, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b)))
	if w.Code != http.StatusOK {
		t.Fatalf("devolvió %d, se esperaba 200", w.Code)
	}
	if evs, _ := r.drain(); len(evs) != 0 {
		t.Errorf("inventó %d evento(s) sin nombre", len(evs))
	}
}
