// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package cloudevents

import (
	"encoding/json"
	"fmt"
	"time"
)

// This file extends the shared CloudEvents helper with
// the BROKER protocol bindings the messaging session needs, additively
// — it does not touch the HTTP binding in cloudevents.go that already ships and
// tests. Each transport binding follows the CloudEvents 1.0.2 Protocol Bindings
// (github.com/cloudevents/spec). The only thing that varies per transport is the
// BINARY-mode attribute prefix; STRUCTURED mode is one and the same JSON document
// (application/cloudevents+json) for every transport.
//
// Verified prefixes (CloudEvents Protocol Bindings, fetched 2026-06):
//   - HTTP   : "ce-"          (cloudevents.go BinaryHTTP)
//   - Kafka  : "ce_"          (record headers; partitionkey extension → record key)
//   - AMQP   : "cloudEvents_" (application-properties; "_" preferred for JMS compat)
//   - MQTT   : ""             (User Property name is the bare attribute name)
//
// datacontenttype never rides a prefixed property in binary mode — it maps to the
// transport's own content-type field. These match the spec exactly.
const (
	// KafkaPrefix is the Kafka binary-mode header prefix (record headers).
	KafkaPrefix = "ce_"
	// AMQPPrefix is the AMQP 1.0 binary-mode application-properties prefix. The
	// underscore form is used (preferred for JMS 2.0 compatibility per the spec).
	AMQPPrefix = "cloudEvents_"
	// MQTTPrefix is the MQTT 5.0 binary-mode user-property prefix: none. The
	// attribute name is used unchanged as the User Property name.
	MQTTPrefix = ""
	// PartitionKeyExtension is the CloudEvents Partitioning extension attribute the
	// Kafka binding maps to the record key when present.
	PartitionKeyExtension = "partitionkey"
)

// StructuredBytes returns the event as one application/cloudevents+json document —
// the transport-agnostic structured-mode body. The caller sets the transport's
// content-type field to ContentTypeStructured. Identical bytes for Kafka, AMQP,
// MQTT and HTTP; only the carrier metadata differs.
func (e Event) StructuredBytes() ([]byte, error) { return e.MarshalJSON() }

// binaryAttributes builds the binary-mode context-attribute map for the given
// transport prefix. It includes the required attributes (specversion/id/source/
// type) and the present optional ones (subject/time/dataschema) plus extensions,
// but NEVER datacontenttype (that maps to the transport content-type field) and
// NEVER data (that is the body). Values are strings, the form every broker header/
// property accepts. The event is validated first so a malformed event cannot be
// rendered.
func (e Event) binaryAttributes(prefix string) (map[string]string, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	attrs := map[string]string{
		prefix + "specversion": SpecVersion,
		prefix + "id":          e.ID,
		prefix + "source":      e.Source,
		prefix + "type":        e.Type,
	}
	if e.Subject != "" {
		attrs[prefix+"subject"] = e.Subject
	}
	if !e.Time.IsZero() {
		attrs[prefix+"time"] = e.Time.UTC().Format(time.RFC3339)
	}
	if e.DataSchema != "" {
		attrs[prefix+"dataschema"] = e.DataSchema
	}
	for k, v := range e.Extensions {
		attrs[prefix+k] = v
	}
	return attrs, nil
}

// BinaryEnvelope renders the event in binary content mode for the given transport
// prefix: the prefixed context-attribute map, the data content-type (to be placed
// in the transport's own content-type field, not a prefixed attribute), and the
// raw data body. It is the generic core the per-transport helpers wrap.
func (e Event) BinaryEnvelope(prefix string) (attrs map[string]string, contentType string, body []byte, err error) {
	attrs, err = e.binaryAttributes(prefix)
	if err != nil {
		return nil, "", nil, err
	}
	return attrs, e.DataContentType, e.Data, nil
}

// KafkaBinary renders the event for Kafka binary content mode: ce_-prefixed record
// headers (including the content-type header carrying datacontenttype), the record
// key derived from the Partitioning partitionkey extension when present, and the
// body. Header values are bytes, as Kafka record headers require.
func (e Event) KafkaBinary() (headers map[string][]byte, key []byte, body []byte, err error) {
	attrs, ct, data, err := e.BinaryEnvelope(KafkaPrefix)
	if err != nil {
		return nil, nil, nil, err
	}
	headers = make(map[string][]byte, len(attrs)+1)
	for k, v := range attrs {
		headers[k] = []byte(v)
	}
	if ct != "" {
		// datacontenttype maps to the Kafka "content-type" header (unprefixed).
		headers["content-type"] = []byte(ct)
	}
	if pk, ok := e.Extensions[PartitionKeyExtension]; ok && pk != "" {
		key = []byte(pk)
	}
	return headers, key, data, nil
}

// AMQPBinary renders the event for AMQP 1.0 binary content mode: cloudEvents_-
// prefixed application-properties, the content-type to set on the AMQP message's
// content-type property (from datacontenttype), and the body.
func (e Event) AMQPBinary() (appProps map[string]string, contentType string, body []byte, err error) {
	return e.BinaryEnvelope(AMQPPrefix)
}

// MQTTBinary renders the event for MQTT 5.0 binary content mode: User Properties
// named by the bare attribute name, the PUBLISH Content Type (from
// datacontenttype), and the payload body.
func (e Event) MQTTBinary() (userProps map[string]string, contentType string, body []byte, err error) {
	return e.BinaryEnvelope(MQTTPrefix)
}

// Parse reads a structured-mode application/cloudevents+json document back into an
// Event, the ingest-recognition counterpart of MarshalJSON. It is how a connector
// recognizes a CloudEvent on a consumed message and recovers its context
// attributes (type/source/subject…) to enrich a topology edge. Unknown members are
// preserved as string extensions; the data member is kept as raw JSON. The result
// is validated so a document missing a required attribute is rejected, not guessed.
func Parse(doc []byte) (Event, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(doc, &raw); err != nil {
		return Event{}, fmt.Errorf("cloudevents: parse structured: %w", err)
	}
	var e Event
	e.Extensions = map[string]string{}
	for k, v := range raw {
		switch k {
		case "specversion":
			// recognized; the wire value is "1.0" — not stored on Event.
		case "id":
			_ = json.Unmarshal(v, &e.ID)
		case "source":
			_ = json.Unmarshal(v, &e.Source)
		case "type":
			_ = json.Unmarshal(v, &e.Type)
		case "subject":
			_ = json.Unmarshal(v, &e.Subject)
		case "datacontenttype":
			_ = json.Unmarshal(v, &e.DataContentType)
		case "dataschema":
			_ = json.Unmarshal(v, &e.DataSchema)
		case "time":
			var ts string
			if json.Unmarshal(v, &ts) == nil && ts != "" {
				if t, err := time.Parse(time.RFC3339, ts); err == nil {
					e.Time = t
				}
			}
		case "data":
			e.Data = append(json.RawMessage(nil), v...)
		case "data_base64":
			// Binary data carried base64 in structured mode; preserved verbatim as an
			// extension so nothing is silently dropped (a connector that needs the bytes
			// decodes it explicitly — minimal-data observers do not).
			var s string
			if json.Unmarshal(v, &s) == nil {
				e.Extensions["data_base64"] = s
			}
		default:
			// Any other top-level member is an extension context attribute (string).
			var s string
			if json.Unmarshal(v, &s) == nil {
				e.Extensions[k] = s
			}
		}
	}
	if len(e.Extensions) == 0 {
		e.Extensions = nil
	}
	return e, e.Validate()
}

// FromBinary reconstructs an Event from a transport's binary-mode representation:
// the prefixed attribute map, the transport content-type (datacontenttype), and the
// body. It is the ingest counterpart of BinaryEnvelope and the inverse of the
// per-transport *Binary helpers. An attribute that does not start with prefix is
// ignored (it is not a CloudEvents attribute on this transport); recognized
// attributes are mapped to typed fields and the rest become string extensions.
func FromBinary(prefix string, attrs map[string]string, contentType string, body []byte) (Event, error) {
	e := Event{DataContentType: contentType, Data: append([]byte(nil), body...), Extensions: map[string]string{}}
	for k, v := range attrs {
		if prefix != "" {
			if len(k) < len(prefix) || k[:len(prefix)] != prefix {
				continue // not a CloudEvents attribute under this binding
			}
			k = k[len(prefix):]
		}
		switch k {
		case "specversion":
		case "id":
			e.ID = v
		case "source":
			e.Source = v
		case "type":
			e.Type = v
		case "subject":
			e.Subject = v
		case "dataschema":
			e.DataSchema = v
		case "time":
			if t, err := time.Parse(time.RFC3339, v); err == nil {
				e.Time = t
			}
		default:
			e.Extensions[k] = v
		}
	}
	if len(e.Extensions) == 0 {
		e.Extensions = nil
	}
	return e, e.Validate()
}
