// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/olivaresai/olivares/appliance/layer/helpers/helperschema"
)

// HelperName is the storage helper's socket name: /run/olivares-helpers/storage.sock.
const HelperName = "storage"

// SubcommandInventory is the storage helper's read. It changes nothing.
const SubcommandInventory = "inventory"

// MaxDocumentBytes bounds the inventory document a helper answer carries, so that the answer
// and its envelope fit the helper client's answer bound.
const MaxDocumentBytes = 60 << 10

// InventoryRequest is the storage helper's read document, {"op": "inventory"}. It names no
// device, no path and no operation: the helper reads the whole inventory.
type InventoryRequest struct {
	Op string `json:"op"`
}

// Subcommand implements helperschema.Request.
func (r *InventoryRequest) Subcommand() string { return r.Op }

// Operation implements helperschema.Request: a read carries no operation id.
func (r *InventoryRequest) Operation() string { return "" }

// Validate implements helperschema.Request.
func (r *InventoryRequest) Validate() error {
	switch r.Op {
	case SubcommandInventory:
		return nil
	case "":
		return &helperschema.InputError{Field: "$.op", Reason: "required: inventory"}
	}
	return &helperschema.InputError{Field: "$.op", Reason: "expected inventory"}
}

// HelperRules is the storage helper's admission table: the inventory read, for the Appliance
// Console alone, attested by the kernel. The tty1 repair console has no storage read.
func HelperRules() []helperschema.Rule {
	return []helperschema.Rule{{Subcommand: SubcommandInventory, Invokers: []helperschema.Invoker{helperschema.Portal}}}
}

// Answer is the storage helper's answer to a read: answered, with the inventory document in the
// seam's raw document field, or failed with no document when the read failed or the document
// exceeds MaxDocumentBytes. Its detail is fixed text.
func Answer(inv Inventory, err error) helperschema.Response {
	if err != nil {
		return helperschema.Response{Result: helperschema.ResultFailed, Code: helperschema.CodeEffectFailed,
			Detail: "UDisks2 or the kernel did not answer the inventory read; nothing changed"}
	}
	data, err := json.Marshal(inv)
	if err != nil || len(data) > MaxDocumentBytes {
		return helperschema.Response{Result: helperschema.ResultFailed, Code: helperschema.CodeEffectFailed,
			Detail: "the inventory exceeds the document bound; nothing changed"}
	}
	return helperschema.Response{Result: helperschema.ResultAnswered, Bundle: json.RawMessage(data)}
}

// Reader reads one inventory.
type Reader func(ctx context.Context) (Inventory, error)

// CallFunc sends one document to one helper and returns its answer; the helper client's Call
// has this shape.
type CallFunc func(ctx context.Context, name string, request helperschema.Request) (helperschema.Response, error)

// HelperReader reads the inventory through call from the storage helper. An answer other than
// answered is a Refusal with the helper's code; an answered document must be one closed
// inventory of this schema within MaxDocumentBytes.
func HelperReader(call CallFunc) Reader {
	return func(ctx context.Context) (Inventory, error) {
		response, err := call(ctx, HelperName, &InventoryRequest{Op: SubcommandInventory})
		if err != nil {
			return Inventory{}, err
		}
		if response.Result != helperschema.ResultAnswered {
			code := response.Code
			if code == "" {
				code = helperschema.CodeConsumerUnavailable
			}
			return Inventory{}, &Refusal{Code: code, Reason: "the storage helper did not answer the inventory"}
		}
		return decodeInventory(response.Bundle)
	}
}

// decodeInventory reads one closed inventory document.
func decodeInventory(data []byte) (Inventory, error) {
	errDocument := errors.New("the storage helper's answer is not an inventory")
	if len(data) == 0 || len(data) > MaxDocumentBytes {
		return Inventory{}, errDocument
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var inv Inventory
	if err := decoder.Decode(&inv); err != nil || inv.Schema != Schema {
		return Inventory{}, errDocument
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Inventory{}, errDocument
	}
	return inv, nil
}
