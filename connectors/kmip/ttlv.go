// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package kmip

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// TTLV is the OASIS KMIP wire encoding (KMIP v2.1 §9.1). Every item is
// Tag(3 bytes big-endian) + Type(1 byte) + Length(4 bytes big-endian, the value
// length EXCLUDING padding) + Value, where the value is padded with zero bytes to
// the next 8-byte boundary. This is a focused, PURE-GO encoder/decoder for the
// read-only subset the inventory client needs (Locate + GetAttributes); it never
// touches key material. Verified against docs.oasis-open.org/kmip/kmip-spec/v2.1/.

// Item type bytes (KMIP v2.1 §9.1.1.2, Table). On the wire the type is the LSByte.
const (
	typeStructure   byte = 0x01
	typeInteger     byte = 0x02
	typeLongInteger byte = 0x03
	typeBigInteger  byte = 0x04
	typeEnumeration byte = 0x05
	typeBoolean     byte = 0x06
	typeTextString  byte = 0x07
	typeByteString  byte = 0x08
	typeDateTime    byte = 0x09
	typeInterval    byte = 0x0A
)

// Tag values (the 0x42xxxx registry; KMIP v2.1 §9.1.3.1, Table 487). Only the tags
// the read-only client emits or interprets are declared.
const (
	tagProtocolVersion      uint32 = 0x420069
	tagProtocolVersionMajor uint32 = 0x42006A
	tagProtocolVersionMinor uint32 = 0x42006B
	tagBatchCount           uint32 = 0x42000D
	tagRequestMessage       uint32 = 0x420078
	tagRequestHeader        uint32 = 0x420077
	tagBatchItem            uint32 = 0x42000F
	tagResponseMessage      uint32 = 0x42007B
	tagResponseHeader       uint32 = 0x42007A
	tagOperation            uint32 = 0x42005C
	tagResultStatus         uint32 = 0x42007F
	tagResultReason         uint32 = 0x42007E
	tagResultMessage        uint32 = 0x42007D
	tagRequestPayload       uint32 = 0x420079
	tagResponsePayload      uint32 = 0x42007C
	tagUniqueIdentifier     uint32 = 0x420094
	tagAttributes           uint32 = 0x420125
	tagObjectType           uint32 = 0x420057
	tagCryptographicAlg     uint32 = 0x420028
	tagCryptographicLength  uint32 = 0x42002A
	tagState                uint32 = 0x42008D
	tagName                 uint32 = 0x420053
)

// Operation enumeration values (KMIP v2.1 §9.1.3.2.27, Table 467). Only the two
// read-only operations the client issues are declared; Get (0x0A) is deliberately
// ABSENT — it returns key material and must never be used.
const (
	opLocate        uint32 = 0x00000008
	opGetAttributes uint32 = 0x0000000B
)

// Result Status enumeration (KMIP v2.1, Table 480).
const (
	resultSuccess         uint32 = 0x00000000
	resultOperationFailed uint32 = 0x00000001
)

// Protocol version 2.1.
const (
	protocolMajor int64 = 2
	protocolMinor int64 = 1
)

// item is a decoded TTLV node. Exactly one of the value fields is meaningful for a
// given typ; structures carry children.
type item struct {
	tag      uint32
	typ      byte
	children []item // structure
	i        int64  // integer / long-integer / date-time / interval
	u        uint32 // enumeration
	b        bool   // boolean
	s        string // text-string
	raw      []byte // byte-string / big-integer
}

// --- encoding -----------------------------------------------------------------

// encode appends the TTLV encoding of one logical value to dst. value is the
// LOGICAL value bytes (4 for integer/enumeration, 8 for long/datetime, N for
// strings, the child encoding for a structure); the 8-byte padding is added here so
// every type follows the one padding rule.
func encode(dst []byte, tag uint32, typ byte, value []byte) []byte {
	var hdr [8]byte
	hdr[0] = byte(tag >> 16)
	hdr[1] = byte(tag >> 8)
	hdr[2] = byte(tag)
	hdr[3] = typ
	binary.BigEndian.PutUint32(hdr[4:], uint32(len(value)))
	dst = append(dst, hdr[:]...)
	dst = append(dst, value...)
	if pad := (8 - len(value)%8) % 8; pad > 0 {
		dst = append(dst, make([]byte, pad)...)
	}
	return dst
}

func encInteger(dst []byte, tag uint32, v int32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(v))
	return encode(dst, tag, typeInteger, b[:])
}

func encEnumeration(dst []byte, tag uint32, v uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return encode(dst, tag, typeEnumeration, b[:])
}

func encTextString(dst []byte, tag uint32, s string) []byte {
	return encode(dst, tag, typeTextString, []byte(s))
}

func encStructure(dst []byte, tag uint32, body []byte) []byte {
	return encode(dst, tag, typeStructure, body)
}

// --- decoding -----------------------------------------------------------------

var errTruncated = errors.New("kmip: truncated TTLV")

// decode parses one TTLV item from b and returns it plus the number of bytes
// consumed (including padding).
func decode(b []byte) (item, int, error) {
	if len(b) < 8 {
		return item{}, 0, errTruncated
	}
	tag := uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2])
	typ := b[3]
	length := int(binary.BigEndian.Uint32(b[4:8]))
	padded := length + (8-length%8)%8
	end := 8 + padded
	if length < 0 || end > len(b) {
		return item{}, 0, errTruncated
	}
	val := b[8 : 8+length]
	it := item{tag: tag, typ: typ}
	switch typ {
	case typeStructure:
		off := 0
		for off < len(val) {
			child, n, err := decode(val[off:])
			if err != nil {
				return item{}, 0, err
			}
			it.children = append(it.children, child)
			off += n
		}
	case typeInteger, typeEnumeration:
		if length < 4 {
			return item{}, 0, errTruncated
		}
		u := binary.BigEndian.Uint32(val[:4])
		it.u = u
		it.i = int64(int32(u))
	case typeLongInteger, typeDateTime, typeInterval:
		if length < 8 {
			return item{}, 0, errTruncated
		}
		it.i = int64(binary.BigEndian.Uint64(val[:8]))
	case typeBoolean:
		if length < 8 {
			return item{}, 0, errTruncated
		}
		it.b = binary.BigEndian.Uint64(val[:8]) != 0
	case typeTextString:
		it.s = string(val)
	case typeByteString, typeBigInteger:
		it.raw = append([]byte(nil), val...)
	default:
		it.raw = append([]byte(nil), val...) // forward-compatible: keep unknown bytes
	}
	return it, end, nil
}

// find returns the first child with the given tag, and whether it was found.
func (it item) find(tag uint32) (item, bool) {
	for _, c := range it.children {
		if c.tag == tag {
			return c, true
		}
	}
	return item{}, false
}

// findAll returns every child with the given tag (Locate returns repeated
// UniqueIdentifier items).
func (it item) findAll(tag uint32) []item {
	var out []item
	for _, c := range it.children {
		if c.tag == tag {
			out = append(out, c)
		}
	}
	return out
}

// firstText returns the first TextString descendant (depth 1) of a structure — used
// to read a Name's value without relying on the (unverified) NameValue sub-tag.
func (it item) firstText() (string, bool) {
	for _, c := range it.children {
		if c.typ == typeTextString {
			return c.s, true
		}
	}
	return "", false
}

func tagHex(t uint32) string { return fmt.Sprintf("0x%06X", t) }
