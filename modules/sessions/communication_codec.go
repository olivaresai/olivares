// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: AGPL-3.0-only
// Additional terms under AGPL-3.0-only section 7(a) disclaim warranty and limit liability: see DISCLAIMER.md at the repository root.

package sessions

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/olivaresai/olivares/core/model"
)

// communicationRecordReader accepts only the engine-neutral Record types
// documented by model.Record. It deliberately does not use the permissive
// Record getters: a NULL, missing column, or driver-shaped value must not be
// silently converted to a domain zero value.
type communicationRecordReader struct {
	kind model.Kind
	rec  model.Record
	err  error
}

func newCommunicationRecordReader(kind model.Kind, rec model.Record) *communicationRecordReader {
	return &communicationRecordReader{kind: kind, rec: rec}
}

func (r *communicationRecordReader) fail(col, format string, args ...any) {
	if r.err != nil {
		return
	}
	detail := fmt.Sprintf(format, args...)
	r.err = communicationError(ErrInvalidCommunicationModel,
		"%s persistence column %q %s", r.kind, col, detail)
}

func (r *communicationRecordReader) required(col string) any {
	if r.err != nil {
		return nil
	}
	value, ok := r.rec[col]
	if !ok || value == nil {
		r.fail(col, "is required")
		return nil
	}
	return value
}

func (r *communicationRecordReader) isNull(col string) bool {
	value, ok := r.rec[col]
	if !ok {
		r.fail(col, "is missing")
		return true
	}
	return value == nil
}

func (r *communicationRecordReader) text(col string) string {
	value := r.required(col)
	if r.err != nil {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		r.fail(col, "has type %T, want string", value)
		return ""
	}
	return text
}

func (r *communicationRecordReader) optionalText(col string) string {
	if r.err != nil || r.isNull(col) {
		return ""
	}
	value := r.rec[col]
	text, ok := value.(string)
	if !ok {
		r.fail(col, "has type %T, want string or NULL", value)
		return ""
	}
	if text == "" {
		r.fail(col, "stores an empty string instead of NULL")
		return ""
	}
	return text
}

func (r *communicationRecordReader) integer(col string) int64 {
	value := r.required(col)
	if r.err != nil {
		return 0
	}
	integer, ok := value.(int64)
	if !ok {
		r.fail(col, "has type %T, want int64", value)
		return 0
	}
	return integer
}

func (r *communicationRecordReader) optionalPositiveInteger(col string) int64 {
	if r.err != nil || r.isNull(col) {
		return 0
	}
	value := r.rec[col]
	integer, ok := value.(int64)
	if !ok {
		r.fail(col, "has type %T, want int64 or NULL", value)
		return 0
	}
	if integer < 1 {
		r.fail(col, "stores %d instead of a positive value or NULL", integer)
		return 0
	}
	return integer
}

func (r *communicationRecordReader) boolean(col string) bool {
	value := r.required(col)
	if r.err != nil {
		return false
	}
	boolean, ok := value.(bool)
	if !ok {
		r.fail(col, "has type %T, want bool", value)
		return false
	}
	return boolean
}

func cloneCommunicationBytes(value []byte) []byte {
	if value == nil {
		return nil
	}
	return bytes.Clone(value)
}

func (r *communicationRecordReader) bytes(col string) []byte {
	value := r.required(col)
	if r.err != nil {
		return nil
	}
	raw, ok := value.([]byte)
	if !ok {
		r.fail(col, "has type %T, want []byte", value)
		return nil
	}
	return cloneCommunicationBytes(raw)
}

func (r *communicationRecordReader) optionalBytes(col string) []byte {
	if r.err != nil || r.isNull(col) {
		return nil
	}
	value := r.rec[col]
	raw, ok := value.([]byte)
	if !ok {
		r.fail(col, "has type %T, want []byte or NULL", value)
		return nil
	}
	if len(raw) == 0 {
		r.fail(col, "stores empty bytes instead of NULL")
		return nil
	}
	return cloneCommunicationBytes(raw)
}

func (r *communicationRecordReader) canonicalJSON(col string) json.RawMessage {
	text := r.text(col)
	if r.err != nil {
		return nil
	}
	raw := json.RawMessage([]byte(text))
	canonical, err := canonicalJSON(raw)
	if err != nil || !bytes.Equal(canonical, raw) {
		r.fail(col, "is not canonical JSON")
		return nil
	}
	return bytes.Clone(raw)
}

func (r *communicationRecordReader) optionalCanonicalJSON(col string) json.RawMessage {
	if r.err != nil || r.isNull(col) {
		return nil
	}
	value := r.rec[col]
	text, ok := value.(string)
	if !ok {
		r.fail(col, "has type %T, want canonical JSON string or NULL", value)
		return nil
	}
	raw := json.RawMessage([]byte(text))
	canonical, err := canonicalJSON(raw)
	if err != nil || !bytes.Equal(canonical, raw) {
		r.fail(col, "is not canonical JSON")
		return nil
	}
	return bytes.Clone(raw)
}

func (r *communicationRecordReader) decodeCanonicalJSON(col string, raw json.RawMessage, out any) {
	if r.err != nil {
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		r.fail(col, "does not decode to its closed shape")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		r.fail(col, "contains trailing JSON values")
		return
	}
	// encoding/json deliberately accepts case-insensitive field names and null
	// for maps, slices and scalars. Those spellings are not injective: decoding
	// and re-encoding can silently change the durable bytes while preserving the
	// same Go value. Require the exact canonical representation of the decoded
	// closed shape so one persisted byte string has one domain meaning.
	canonical, err := canonicalJSON(out)
	if err != nil || !bytes.Equal(canonical, raw) {
		r.fail(col, "is not the canonical encoding of its closed shape")
	}
}

func (r *communicationRecordReader) timestamp(col string) time.Time {
	text := r.text(col)
	if r.err != nil {
		return time.Time{}
	}
	stamp, err := model.ParseTimestamp(text)
	if err != nil || model.NewTimestamp(stamp.Time()).String() != text {
		r.fail(col, "is not a canonical timestamp")
		return time.Time{}
	}
	return stamp.Time()
}

func (r *communicationRecordReader) optionalTimestamp(col string) *time.Time {
	if r.err != nil || r.isNull(col) {
		return nil
	}
	value := r.rec[col]
	text, ok := value.(string)
	if !ok {
		r.fail(col, "has type %T, want timestamp string or NULL", value)
		return nil
	}
	stamp, err := model.ParseTimestamp(text)
	if err != nil || model.NewTimestamp(stamp.Time()).String() != text {
		r.fail(col, "is not a canonical timestamp")
		return nil
	}
	parsed := stamp.Time()
	return &parsed
}

func (r *communicationRecordReader) id(col string) model.ID {
	value := model.ID(r.text(col))
	if r.err == nil && !validCanonicalCommunicationID(value) {
		r.fail(col, "is not a canonical UUIDv7")
		return ""
	}
	return value
}

func (r *communicationRecordReader) optionalID(col string) model.ID {
	text := r.optionalText(col)
	if r.err != nil || text == "" {
		return ""
	}
	value := model.ID(text)
	if !validCanonicalCommunicationID(value) {
		r.fail(col, "is not a canonical UUIDv7")
		return ""
	}
	return value
}

func (r *communicationRecordReader) tenantID(col string) model.TenantID {
	value := model.TenantID(r.text(col))
	if r.err == nil && !validCanonicalCommunicationTenant(value) {
		r.fail(col, "is not a canonical tenant UUIDv7")
		return ""
	}
	return value
}

func (r *communicationRecordReader) mutableEntity() MutableCommunicationEntity {
	return MutableCommunicationEntity{
		CommunicationEntity: CommunicationEntity{
			ID: r.id(model.ColID), TenantID: r.tenantID(model.ColTenantID),
			WorkspaceID: r.id(colWorkWorkspaceID), Version: r.integer(model.ColVersion),
			CreatedAt: r.timestamp(model.ColCreatedAt),
		},
		UpdatedAt: r.timestamp(model.ColUpdatedAt),
	}
}

func (r *communicationRecordReader) appendOnlyEntity() AppendOnlyCommunicationEntity {
	entity := CommunicationEntity{
		ID: r.id(model.ColID), TenantID: r.tenantID(model.ColTenantID),
		WorkspaceID: r.id(colWorkWorkspaceID), Version: r.integer(model.ColVersion),
		CreatedAt: r.timestamp(model.ColCreatedAt),
	}
	updatedAt := r.timestamp(model.ColUpdatedAt)
	if r.err == nil && !updatedAt.Equal(entity.CreatedAt) {
		r.fail(model.ColUpdatedAt, "differs from append-only created_at")
	}
	return AppendOnlyCommunicationEntity{CommunicationEntity: entity}
}

func mutableCommunicationRecord(entity MutableCommunicationEntity) model.Record {
	return model.Record{
		model.ColID: entity.ID.String(), model.ColTenantID: entity.TenantID.String(),
		colWorkWorkspaceID: entity.WorkspaceID.String(), model.ColVersion: entity.Version,
		model.ColCreatedAt: model.NewTimestamp(entity.CreatedAt).String(),
		model.ColUpdatedAt: model.NewTimestamp(entity.UpdatedAt).String(),
	}
}

func appendOnlyCommunicationRecord(entity AppendOnlyCommunicationEntity) model.Record {
	return model.Record{
		model.ColID: entity.ID.String(), model.ColTenantID: entity.TenantID.String(),
		colWorkWorkspaceID: entity.WorkspaceID.String(), model.ColVersion: entity.Version,
		model.ColCreatedAt: model.NewTimestamp(entity.CreatedAt).String(),
		model.ColUpdatedAt: model.NewTimestamp(entity.CreatedAt).String(),
	}
}

func optionalCommunicationText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func optionalCommunicationID(value model.ID) any {
	if value.IsZero() {
		return nil
	}
	return value.String()
}

func optionalCommunicationInteger(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func optionalCommunicationTimestamp(value *time.Time) any {
	if value == nil {
		return nil
	}
	return model.NewTimestamp(*value).String()
}

func optionalCommunicationBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return cloneCommunicationBytes(value)
}

func setCanonicalCommunicationJSON(rec model.Record, col string, value any) error {
	raw, err := canonicalJSON(value)
	if err != nil {
		return communicationError(ErrInvalidCommunicationModel,
			"persistence column %q cannot be canonicalized", col)
	}
	rec[col] = string(raw)
	return nil
}

func setOptionalCanonicalCommunicationJSON(rec model.Record, col string, value json.RawMessage) error {
	if len(value) == 0 {
		rec[col] = nil
		return nil
	}
	canonical, err := canonicalJSON(value)
	if err != nil || !bytes.Equal(canonical, value) {
		return communicationError(ErrInvalidCommunicationModel,
			"persistence column %q is not canonical JSON", col)
	}
	rec[col] = string(value)
	return nil
}

type communicationPayloadColumns struct {
	encoding, plainJSON, sealedJSON, schema, digest        string
	sealKeyVersion, digestKeyVersion, protectionGeneration string
}

func communicationProtectedPayloadColumns(prefix string) communicationPayloadColumns {
	fields := protectedPayloadFields(prefix, true)
	return communicationPayloadColumns{
		encoding: fields[0].Name, plainJSON: fields[1].Name, sealedJSON: fields[2].Name,
		schema: fields[3].Name, digest: fields[4].Name, sealKeyVersion: fields[5].Name,
		digestKeyVersion: fields[6].Name, protectionGeneration: fields[7].Name,
	}
}

func setNullProtectedPayload(rec model.Record, columns communicationPayloadColumns) {
	for _, col := range []string{
		columns.encoding, columns.plainJSON, columns.sealedJSON, columns.schema,
		columns.digest, columns.sealKeyVersion, columns.digestKeyVersion,
		columns.protectionGeneration,
	} {
		rec[col] = nil
	}
}

func encodeProtectedPayload(
	rec model.Record,
	prefix string,
	payload *ProtectedPayload,
	required bool,
) error {
	columns := communicationProtectedPayloadColumns(prefix)
	if payload == nil {
		if required {
			return communicationError(ErrInvalidCommunicationModel,
				"required protected payload %q is nil", prefix)
		}
		setNullProtectedPayload(rec, columns)
		return nil
	}
	if err := ValidateProtectedPayload(*payload); err != nil {
		return err
	}
	rec[columns.encoding] = string(payload.Encoding)
	rec[columns.schema] = payload.Schema
	rec[columns.digest] = cloneCommunicationBytes(payload.Digest)
	rec[columns.protectionGeneration] = payload.ProtectionGeneration
	rec[columns.sealKeyVersion] = optionalCommunicationText(payload.SealKeyVersion)
	rec[columns.digestKeyVersion] = optionalCommunicationText(payload.DigestKeyVersion)
	if payload.Encoding == PayloadPlainJSON {
		if err := setOptionalCanonicalCommunicationJSON(rec, columns.plainJSON, payload.PlainJSON); err != nil {
			return err
		}
		rec[columns.sealedJSON] = nil
		return nil
	}
	rec[columns.plainJSON] = nil
	if err := setCanonicalCommunicationJSON(rec, columns.sealedJSON, payload.Sealed); err != nil {
		return err
	}
	return nil
}

func decodeProtectedPayload(
	reader *communicationRecordReader,
	prefix string,
	required bool,
) *ProtectedPayload {
	columns := communicationProtectedPayloadColumns(prefix)
	metadata := []string{
		columns.encoding, columns.schema, columns.digest, columns.protectionGeneration,
	}
	allMetadataNull := true
	for _, col := range metadata {
		allMetadataNull = allMetadataNull && reader.isNull(col)
	}
	if allMetadataNull {
		for _, col := range []string{
			columns.plainJSON, columns.sealedJSON, columns.sealKeyVersion, columns.digestKeyVersion,
		} {
			if !reader.isNull(col) {
				reader.fail(col, "is set while protected payload metadata is NULL")
			}
		}
		if required {
			reader.fail(columns.encoding, "is required")
		}
		return nil
	}
	for _, col := range metadata {
		if reader.isNull(col) {
			reader.fail(col, "is NULL in a partial protected payload")
		}
	}
	payload := &ProtectedPayload{
		Encoding:  PayloadEncoding(reader.text(columns.encoding)),
		PlainJSON: reader.optionalCanonicalJSON(columns.plainJSON),
		Schema:    reader.text(columns.schema), Digest: reader.bytes(columns.digest),
		SealKeyVersion:       reader.optionalText(columns.sealKeyVersion),
		DigestKeyVersion:     reader.optionalText(columns.digestKeyVersion),
		ProtectionGeneration: reader.integer(columns.protectionGeneration),
	}
	sealedJSON := reader.optionalCanonicalJSON(columns.sealedJSON)
	if len(sealedJSON) != 0 {
		var sealed SealedPayload
		reader.decodeCanonicalJSON(columns.sealedJSON, sealedJSON, &sealed)
		payload.Sealed = &sealed
	}
	if reader.err == nil {
		if err := ValidateProtectedPayload(*payload); err != nil {
			reader.err = err
		}
	}
	return payload
}

func encodeCommunicationSubject(rec model.Record, kindCol, refCol string, value CommunicationSubjectRef) {
	rec[kindCol] = string(value.Kind)
	rec[refCol] = value.Ref
}

func decodeCommunicationSubject(
	reader *communicationRecordReader,
	kindCol, refCol string,
) CommunicationSubjectRef {
	return CommunicationSubjectRef{
		Kind: CommunicationSubjectKind(reader.text(kindCol)), Ref: reader.text(refCol),
	}
}

func encodeOptionalCommunicationSubject(
	rec model.Record,
	kindCol, refCol string,
	value *CommunicationSubjectRef,
) {
	if value == nil {
		rec[kindCol], rec[refCol] = nil, nil
		return
	}
	encodeCommunicationSubject(rec, kindCol, refCol, *value)
}

func decodeOptionalCommunicationSubject(
	reader *communicationRecordReader,
	kindCol, refCol string,
) *CommunicationSubjectRef {
	kindNull, refNull := reader.isNull(kindCol), reader.isNull(refCol)
	if kindNull != refNull {
		reader.fail(kindCol, "does not share NULL state with %q", refCol)
		return nil
	}
	if kindNull {
		return nil
	}
	value := decodeCommunicationSubject(reader, kindCol, refCol)
	return &value
}

func encodeCommunicationActor(rec model.Record, kindCol, refCol string, value CommunicationActorRef) {
	rec[kindCol] = string(value.Kind)
	rec[refCol] = value.Ref
}

func decodeCommunicationActor(
	reader *communicationRecordReader,
	kindCol, refCol string,
) CommunicationActorRef {
	return CommunicationActorRef{
		Kind: CommunicationActorKind(reader.text(kindCol)), Ref: reader.text(refCol),
	}
}

func encodeOptionalCommunicationActor(
	rec model.Record,
	kindCol, refCol string,
	value *CommunicationActorRef,
) {
	if value == nil {
		rec[kindCol], rec[refCol] = nil, nil
		return
	}
	encodeCommunicationActor(rec, kindCol, refCol, *value)
}

func decodeOptionalCommunicationActor(
	reader *communicationRecordReader,
	kindCol, refCol string,
) *CommunicationActorRef {
	kindNull, refNull := reader.isNull(kindCol), reader.isNull(refCol)
	if kindNull != refNull {
		reader.fail(kindCol, "does not share NULL state with %q", refCol)
		return nil
	}
	if kindNull {
		return nil
	}
	value := decodeCommunicationActor(reader, kindCol, refCol)
	return &value
}

func encodeRecipient(rec model.Record, kindCol, refCol string, value RecipientRef) {
	rec[kindCol] = string(value.Kind)
	rec[refCol] = value.Ref
}

func decodeRecipient(reader *communicationRecordReader, kindCol, refCol string) RecipientRef {
	return RecipientRef{Kind: RecipientKind(reader.text(kindCol)), Ref: reader.text(refCol)}
}

func encodeOptionalRecipient(rec model.Record, kindCol, refCol string, value *RecipientRef) {
	if value == nil {
		rec[kindCol], rec[refCol] = nil, nil
		return
	}
	encodeRecipient(rec, kindCol, refCol, *value)
}

func decodeOptionalRecipient(
	reader *communicationRecordReader,
	kindCol, refCol string,
) *RecipientRef {
	kindNull, refNull := reader.isNull(kindCol), reader.isNull(refCol)
	if kindNull != refNull {
		reader.fail(kindCol, "does not share NULL state with %q", refCol)
		return nil
	}
	if kindNull {
		return nil
	}
	value := decodeRecipient(reader, kindCol, refCol)
	return &value
}

func wrapCommunicationCodecError(direction string, kind model.Kind, err error) error {
	return fmt.Errorf("%s %s persistence record: %w", direction, kind, err)
}

func channelToRecord(channel Channel) (model.Record, error) {
	if err := ValidateChannel(channel); err != nil {
		return nil, wrapCommunicationCodecError("encode", channelKind, err)
	}
	rec := mutableCommunicationRecord(channel.MutableCommunicationEntity)
	rec[colCommSlug] = channel.Slug
	rec[colCommName] = channel.Name
	rec[colCommDescription] = optionalCommunicationText(channel.Description)
	rec[colCommKind] = string(channel.Kind)
	rec[colCommState] = string(channel.State)
	rec[colCommSensitivity] = string(channel.Sensitivity)
	rec[colCommContentProtection] = string(channel.ContentProtection)
	rec[colCommProtectionGeneration] = channel.ProtectionGeneration
	rec[colCommDefaultAckPolicy] = string(channel.DefaultAckPolicy)
	rec[colCommDefaultAckTimeoutMS] = channel.DefaultAckTimeoutMS
	rec[colCommDefaultWake] = string(channel.DefaultWake)
	rec[colCommRetentionPolicyRef] = optionalCommunicationText(channel.RetentionPolicyRef)
	rec[colCommMaxFanout] = channel.MaxFanout
	rec[colCommMaxAutomationDepth] = channel.MaxAutomationDepth
	rec[colCommACLRevision] = channel.ACLRevision
	rec[colCommRouteRevision] = channel.RouteRevision
	rec[colCommSubscriptionRevision] = channel.SubscriptionRevision
	return rec, nil
}

func channelFromRecord(rec model.Record) (Channel, error) {
	reader := newCommunicationRecordReader(channelKind, rec)
	channel := Channel{
		MutableCommunicationEntity: reader.mutableEntity(),
		Slug:                       reader.text(colCommSlug), Name: reader.text(colCommName),
		Description: reader.optionalText(colCommDescription),
		Kind:        ChannelKind(reader.text(colCommKind)), State: ChannelState(reader.text(colCommState)),
		Sensitivity:          ChannelSensitivity(reader.text(colCommSensitivity)),
		ContentProtection:    ContentProtection(reader.text(colCommContentProtection)),
		ProtectionGeneration: reader.integer(colCommProtectionGeneration),
		DefaultAckPolicy:     AckPolicy(reader.text(colCommDefaultAckPolicy)),
		DefaultAckTimeoutMS:  reader.integer(colCommDefaultAckTimeoutMS),
		DefaultWake:          WakePolicy(reader.text(colCommDefaultWake)),
		RetentionPolicyRef:   reader.optionalText(colCommRetentionPolicyRef),
		MaxFanout:            reader.integer(colCommMaxFanout),
		MaxAutomationDepth:   reader.integer(colCommMaxAutomationDepth),
		ACLRevision:          reader.integer(colCommACLRevision), RouteRevision: reader.integer(colCommRouteRevision),
		SubscriptionRevision: reader.integer(colCommSubscriptionRevision),
	}
	if reader.err != nil {
		return Channel{}, wrapCommunicationCodecError("decode", channelKind, reader.err)
	}
	if err := ValidateChannel(channel); err != nil {
		return Channel{}, wrapCommunicationCodecError("decode", channelKind, err)
	}
	return channel, nil
}

func channelGrantToRecord(grant ChannelGrant) (model.Record, error) {
	if err := ValidateChannelGrant(grant); err != nil {
		return nil, wrapCommunicationCodecError("encode", channelGrantKind, err)
	}
	rec := mutableCommunicationRecord(grant.MutableCommunicationEntity)
	rec[colCommChannelID] = grant.ChannelID.String()
	encodeCommunicationSubject(rec, colCommSubjectKind, colCommSubjectRef, grant.Subject)
	rec[colCommGeneration] = grant.Generation
	rec[colCommCanRead], rec[colCommCanWrite], rec[colCommCanAdmin] = grant.CanRead, grant.CanWrite, grant.CanAdmin
	rec[colCommState] = string(grant.State)
	encodeCommunicationActor(rec, colCommGrantedByKind, colCommGrantedByRef, grant.GrantedBy)
	encodeOptionalCommunicationActor(rec, colCommRevokedByKind, colCommRevokedByRef, grant.RevokedBy)
	rec[colCommExpiresAt] = optionalCommunicationTimestamp(grant.ExpiresAt)
	rec[colCommSupersedesID] = optionalCommunicationID(grant.SupersedesID)
	return rec, nil
}

func channelGrantFromRecord(rec model.Record) (ChannelGrant, error) {
	reader := newCommunicationRecordReader(channelGrantKind, rec)
	grant := ChannelGrant{
		MutableCommunicationEntity: reader.mutableEntity(), ChannelID: reader.id(colCommChannelID),
		Subject:    decodeCommunicationSubject(reader, colCommSubjectKind, colCommSubjectRef),
		Generation: reader.integer(colCommGeneration), CanRead: reader.boolean(colCommCanRead),
		CanWrite: reader.boolean(colCommCanWrite), CanAdmin: reader.boolean(colCommCanAdmin),
		State:     ChannelGrantState(reader.text(colCommState)),
		GrantedBy: decodeCommunicationActor(reader, colCommGrantedByKind, colCommGrantedByRef),
		RevokedBy: decodeOptionalCommunicationActor(reader, colCommRevokedByKind, colCommRevokedByRef),
		ExpiresAt: reader.optionalTimestamp(colCommExpiresAt), SupersedesID: reader.optionalID(colCommSupersedesID),
	}
	if reader.err != nil {
		return ChannelGrant{}, wrapCommunicationCodecError("decode", channelGrantKind, reader.err)
	}
	if err := ValidateChannelGrant(grant); err != nil {
		return ChannelGrant{}, wrapCommunicationCodecError("decode", channelGrantKind, err)
	}
	return grant, nil
}

func channelSubscriptionToRecord(subscription ChannelSubscription) (model.Record, error) {
	if err := ValidateChannelSubscription(subscription); err != nil {
		return nil, wrapCommunicationCodecError("encode", channelSubscriptionKind, err)
	}
	rec := mutableCommunicationRecord(subscription.MutableCommunicationEntity)
	rec[colCommChannelID] = subscription.ChannelID.String()
	encodeCommunicationSubject(rec, colCommSubscriberKind, colCommSubscriberRef, subscription.Subscriber)
	rec[colCommGeneration] = subscription.Generation
	rec[colCommMode] = string(subscription.Mode)
	rec[colCommWake] = string(subscription.Wake)
	rec[colCommRequiredForCritical] = subscription.RequiredForCritical
	rec[colCommState] = string(subscription.State)
	if err := setOptionalCanonicalCommunicationJSON(rec, colCommFilterJSON, subscription.FilterJSON); err != nil {
		return nil, wrapCommunicationCodecError("encode", channelSubscriptionKind, err)
	}
	rec[colCommFilterHash] = optionalCommunicationBytes(subscription.FilterHash)
	rec[colCommSupersedesID] = optionalCommunicationID(subscription.SupersedesID)
	return rec, nil
}

func channelSubscriptionFromRecord(rec model.Record) (ChannelSubscription, error) {
	reader := newCommunicationRecordReader(channelSubscriptionKind, rec)
	subscription := ChannelSubscription{
		MutableCommunicationEntity: reader.mutableEntity(), ChannelID: reader.id(colCommChannelID),
		Subscriber: decodeCommunicationSubject(reader, colCommSubscriberKind, colCommSubscriberRef),
		Generation: reader.integer(colCommGeneration), Mode: ChannelSubscriptionMode(reader.text(colCommMode)),
		Wake: WakePolicy(reader.text(colCommWake)), RequiredForCritical: reader.boolean(colCommRequiredForCritical),
		State:      ChannelSubscriptionState(reader.text(colCommState)),
		FilterJSON: reader.optionalCanonicalJSON(colCommFilterJSON),
		FilterHash: reader.optionalBytes(colCommFilterHash), SupersedesID: reader.optionalID(colCommSupersedesID),
	}
	if reader.err != nil {
		return ChannelSubscription{}, wrapCommunicationCodecError("decode", channelSubscriptionKind, reader.err)
	}
	if err := ValidateChannelSubscription(subscription); err != nil {
		return ChannelSubscription{}, wrapCommunicationCodecError("decode", channelSubscriptionKind, err)
	}
	return subscription, nil
}

func channelLabelDefinitionToRecord(label ChannelLabelDefinition) (model.Record, error) {
	if err := ValidateChannelLabelDefinition(label); err != nil {
		return nil, wrapCommunicationCodecError("encode", channelLabelDefinitionKind, err)
	}
	rec := mutableCommunicationRecord(label.MutableCommunicationEntity)
	rec[colCommChannelID] = label.ChannelID.String()
	rec[colCommLabelKey] = label.Key
	rec[colCommGeneration] = label.Generation
	if err := setCanonicalCommunicationJSON(rec, colCommAllowedValuesJSON, label.AllowedValuesJSON); err != nil {
		return nil, wrapCommunicationCodecError("encode", channelLabelDefinitionKind, err)
	}
	rec[colCommValuesHash] = cloneCommunicationBytes(label.ValuesHash)
	rec[colCommClassification] = string(label.Classification)
	rec[colCommState] = string(label.State)
	return rec, nil
}

func channelLabelDefinitionFromRecord(rec model.Record) (ChannelLabelDefinition, error) {
	reader := newCommunicationRecordReader(channelLabelDefinitionKind, rec)
	label := ChannelLabelDefinition{
		MutableCommunicationEntity: reader.mutableEntity(), ChannelID: reader.id(colCommChannelID),
		Key: reader.text(colCommLabelKey), Generation: reader.integer(colCommGeneration),
		AllowedValuesJSON: reader.canonicalJSON(colCommAllowedValuesJSON),
		ValuesHash:        reader.bytes(colCommValuesHash),
		Classification:    ChannelLabelClassification(reader.text(colCommClassification)),
		State:             ChannelLabelState(reader.text(colCommState)),
	}
	if reader.err != nil {
		return ChannelLabelDefinition{}, wrapCommunicationCodecError("decode", channelLabelDefinitionKind, reader.err)
	}
	if err := ValidateChannelLabelDefinition(label); err != nil {
		return ChannelLabelDefinition{}, wrapCommunicationCodecError("decode", channelLabelDefinitionKind, err)
	}
	return label, nil
}

func channelRouteRuleToRecord(route ChannelRouteRule) (model.Record, error) {
	if err := ValidateChannelRouteRule(route); err != nil {
		return nil, wrapCommunicationCodecError("encode", channelRouteKind, err)
	}
	rec := mutableCommunicationRecord(route.MutableCommunicationEntity)
	rec[colCommRouteKey] = route.RouteKey
	rec[colCommGeneration] = route.Generation
	rec[colCommPriority] = route.Priority
	rec[colCommSourceKind] = string(route.SourceKind)
	rec[colCommEventType] = optionalCommunicationText(route.EventType)
	rec[colCommMessageKind] = optionalCommunicationText(string(route.MessageKind))
	rec[colCommMinimumUrgency] = optionalCommunicationText(string(route.MinimumUrgency))
	if err := setOptionalCanonicalCommunicationJSON(rec, colCommLabelMatchJSON, route.LabelMatchJSON); err != nil {
		return nil, wrapCommunicationCodecError("encode", channelRouteKind, err)
	}
	rec[colCommTargetChannelID] = route.TargetChannelID.String()
	rec[colCommAudienceKind] = string(route.AudienceKind)
	if route.AudienceRef == "" {
		rec[colCommAudienceRef] = nil
	} else {
		rec[colCommAudienceRef] = route.AudienceRef
	}
	rec[colCommAckPolicy] = string(route.AckPolicy)
	rec[colCommWakePolicy] = string(route.WakePolicy)
	rec[colCommCatchAll] = route.CatchAll
	rec[colCommState] = string(route.State)
	rec[colCommSupersedesID] = optionalCommunicationID(route.SupersedesID)
	return rec, nil
}

func channelRouteRuleFromRecord(rec model.Record) (ChannelRouteRule, error) {
	reader := newCommunicationRecordReader(channelRouteKind, rec)
	route := ChannelRouteRule{
		MutableCommunicationEntity: reader.mutableEntity(), RouteKey: reader.text(colCommRouteKey),
		Generation: reader.integer(colCommGeneration), Priority: reader.integer(colCommPriority),
		SourceKind:      ChannelRouteSourceKind(reader.text(colCommSourceKind)),
		EventType:       reader.optionalText(colCommEventType),
		MessageKind:     MessageKind(reader.optionalText(colCommMessageKind)),
		MinimumUrgency:  MessageUrgency(reader.optionalText(colCommMinimumUrgency)),
		LabelMatchJSON:  reader.optionalCanonicalJSON(colCommLabelMatchJSON),
		TargetChannelID: reader.id(colCommTargetChannelID),
		AudienceKind:    ChannelRouteAudienceKind(reader.text(colCommAudienceKind)),
		AudienceRef:     reader.optionalText(colCommAudienceRef),
		AckPolicy:       AckPolicy(reader.text(colCommAckPolicy)), WakePolicy: WakePolicy(reader.text(colCommWakePolicy)),
		CatchAll: reader.boolean(colCommCatchAll), State: ChannelRouteState(reader.text(colCommState)),
		SupersedesID: reader.optionalID(colCommSupersedesID),
	}
	if reader.err != nil {
		return ChannelRouteRule{}, wrapCommunicationCodecError("decode", channelRouteKind, reader.err)
	}
	if err := ValidateChannelRouteRule(route); err != nil {
		return ChannelRouteRule{}, wrapCommunicationCodecError("decode", channelRouteKind, err)
	}
	return route, nil
}

func communicationEndpointToRecord(endpoint CommunicationEndpoint) (model.Record, error) {
	if err := ValidateCommunicationEndpoint(endpoint); err != nil {
		return nil, wrapCommunicationCodecError("encode", communicationEndpointKind, err)
	}
	rec := mutableCommunicationRecord(endpoint.MutableCommunicationEntity)
	encodeRecipient(rec, colCommOwnerKind, colCommOwnerRef, endpoint.Owner)
	rec[colCommProviderKey] = endpoint.ProviderKey
	rec[colTransport] = endpoint.Transport
	rec[colCommEndpointRef] = endpoint.EndpointRef
	rec[colCommSessionSID] = optionalCommunicationText(endpoint.SessionSID)
	if err := setCanonicalCommunicationJSON(rec, colCommCapabilitiesJSON, endpoint.CapabilitiesJSON); err != nil {
		return nil, wrapCommunicationCodecError("encode", communicationEndpointKind, err)
	}
	rec[colCommTransportFingerprint] = optionalCommunicationText(endpoint.TransportFingerprint)
	rec[colCommSupportLevel] = string(endpoint.SupportLevel)
	rec[colCommPriority] = endpoint.Priority
	rec[colCommState] = string(endpoint.State)
	rec[colCommHeartbeatExpiresAt] = optionalCommunicationTimestamp(endpoint.HeartbeatExpiresAt)
	rec[colCommGeneration] = endpoint.Generation
	rec[colCommSecretRef] = optionalCommunicationText(endpoint.SecretRef)
	return rec, nil
}

func communicationEndpointFromRecord(rec model.Record) (CommunicationEndpoint, error) {
	reader := newCommunicationRecordReader(communicationEndpointKind, rec)
	endpoint := CommunicationEndpoint{
		MutableCommunicationEntity: reader.mutableEntity(),
		Owner:                      decodeRecipient(reader, colCommOwnerKind, colCommOwnerRef),
		ProviderKey:                reader.text(colCommProviderKey), Transport: reader.text(colTransport),
		EndpointRef: reader.text(colCommEndpointRef), SessionSID: reader.optionalText(colCommSessionSID),
		CapabilitiesJSON:     reader.canonicalJSON(colCommCapabilitiesJSON),
		TransportFingerprint: reader.optionalText(colCommTransportFingerprint),
		SupportLevel:         CommunicationEndpointSupport(reader.text(colCommSupportLevel)),
		Priority:             reader.integer(colCommPriority), State: CommunicationEndpointState(reader.text(colCommState)),
		HeartbeatExpiresAt: reader.optionalTimestamp(colCommHeartbeatExpiresAt),
		Generation:         reader.integer(colCommGeneration), SecretRef: reader.optionalText(colCommSecretRef),
	}
	if reader.err != nil {
		return CommunicationEndpoint{}, wrapCommunicationCodecError("decode", communicationEndpointKind, reader.err)
	}
	if err := ValidateCommunicationEndpoint(endpoint); err != nil {
		return CommunicationEndpoint{}, wrapCommunicationCodecError("decode", communicationEndpointKind, err)
	}
	return endpoint, nil
}

func messageToRecord(message Message, requiredCount int64) (model.Record, error) {
	if err := ValidateMessage(message, requiredCount); err != nil {
		return nil, wrapCommunicationCodecError("encode", messageKind, err)
	}
	rec := mutableCommunicationRecord(message.MutableCommunicationEntity)
	rec[colCommChannelID] = message.ChannelID.String()
	rec[colWorkItemID] = optionalCommunicationID(message.WorkItemID)
	rec[colCommThreadID] = message.ThreadID.String()
	rec[colCommKind] = string(message.Kind)
	rec[colCommState] = string(message.State)
	encodeCommunicationActor(rec, colCommSenderKind, colCommSenderRef, message.Sender)
	if err := encodeProtectedPayload(rec, "payload", &message.Payload, true); err != nil {
		return nil, wrapCommunicationCodecError("encode", messageKind, err)
	}
	if err := setOptionalCanonicalCommunicationJSON(rec, colCommLabelsJSON, message.LabelsJSON); err != nil {
		return nil, wrapCommunicationCodecError("encode", messageKind, err)
	}
	rec[colCommLabelsHash] = optionalCommunicationBytes(message.LabelsHash)
	rec[colCommUrgency] = string(message.Urgency)
	rec[colCommAckPolicy] = string(message.AckPolicy)
	rec[colCommAckQuorum] = message.AckQuorum
	rec[colCommAvailableAt] = model.NewTimestamp(message.AvailableAt).String()
	rec[colCommAckDueAt] = optionalCommunicationTimestamp(message.AckDueAt)
	rec[colCommExpiresAt] = optionalCommunicationTimestamp(message.ExpiresAt)
	rec[colCommReplyToID] = optionalCommunicationID(message.ReplyToID)
	rec[colCommSupersedesID] = optionalCommunicationID(message.SupersedesID)
	rec[colCommOriginEventID] = optionalCommunicationID(message.OriginEventID)
	rec[colCommAutomationDepth] = message.AutomationDepth
	rec[colCommPublishedAt] = optionalCommunicationTimestamp(message.PublishedAt)
	rec[colCommTerminalAt] = optionalCommunicationTimestamp(message.TerminalAt)
	rec[colCommTerminalCode] = optionalCommunicationText(message.TerminalCode)
	if err := encodeProtectedPayload(rec, "terminal_reason", message.TerminalReason, false); err != nil {
		return nil, wrapCommunicationCodecError("encode", messageKind, err)
	}
	rec[colCommAudienceHash] = optionalCommunicationBytes(message.AudienceHash)
	rec[colCommLastEventSeq] = message.LastEventSeq
	return rec, nil
}

func messageFromRecord(rec model.Record, requiredCount int64) (Message, error) {
	reader := newCommunicationRecordReader(messageKind, rec)
	payload := decodeProtectedPayload(reader, "payload", true)
	terminalReason := decodeProtectedPayload(reader, "terminal_reason", false)
	message := Message{
		MutableCommunicationEntity: reader.mutableEntity(), ChannelID: reader.id(colCommChannelID),
		WorkItemID: reader.optionalID(colWorkItemID), ThreadID: reader.id(colCommThreadID),
		Kind: MessageKind(reader.text(colCommKind)), State: MessageState(reader.text(colCommState)),
		Sender:     decodeCommunicationActor(reader, colCommSenderKind, colCommSenderRef),
		LabelsJSON: reader.optionalCanonicalJSON(colCommLabelsJSON),
		LabelsHash: reader.optionalBytes(colCommLabelsHash), Urgency: MessageUrgency(reader.text(colCommUrgency)),
		AckPolicy: AckPolicy(reader.text(colCommAckPolicy)), AckQuorum: reader.integer(colCommAckQuorum),
		AvailableAt: reader.timestamp(colCommAvailableAt), AckDueAt: reader.optionalTimestamp(colCommAckDueAt),
		ExpiresAt: reader.optionalTimestamp(colCommExpiresAt), ReplyToID: reader.optionalID(colCommReplyToID),
		SupersedesID: reader.optionalID(colCommSupersedesID), OriginEventID: reader.optionalID(colCommOriginEventID),
		AutomationDepth: reader.integer(colCommAutomationDepth),
		PublishedAt:     reader.optionalTimestamp(colCommPublishedAt), TerminalAt: reader.optionalTimestamp(colCommTerminalAt),
		TerminalCode: reader.optionalText(colCommTerminalCode), TerminalReason: terminalReason,
		AudienceHash: reader.optionalBytes(colCommAudienceHash), LastEventSeq: reader.integer(colCommLastEventSeq),
	}
	if payload != nil {
		message.Payload = *payload
	}
	if reader.err != nil {
		return Message{}, wrapCommunicationCodecError("decode", messageKind, reader.err)
	}
	if err := ValidateMessage(message, requiredCount); err != nil {
		return Message{}, wrapCommunicationCodecError("decode", messageKind, err)
	}
	return message, nil
}

func messageAudienceToRecord(audience MessageAudience) (model.Record, error) {
	if err := ValidateMessageAudience(audience); err != nil {
		return nil, wrapCommunicationCodecError("encode", messageAudienceKind, err)
	}
	rec := appendOnlyCommunicationRecord(audience.AppendOnlyCommunicationEntity)
	rec[colCommMessageID] = audience.MessageID.String()
	rec[colCommOrdinal] = audience.Ordinal
	rec[colCommSelectorKind] = string(audience.Selector.Kind)
	rec[colCommSelectorRef] = optionalCommunicationText(audience.Selector.Ref)
	rec[colCommSelectorRequired] = audience.Selector.Required
	rec[colCommSelectorWakePolicy] = string(audience.Selector.WakePolicy)
	rec[colCommRouteRuleID] = optionalCommunicationID(audience.RouteRuleID)
	rec[colCommChannelACLRevision] = audience.ChannelACLRevision
	rec[colCommRouteRevision] = audience.RouteRevision
	rec[colCommSubscriptionRevision] = audience.SubscriptionRevision
	rec[colCommDirectoryEpoch] = audience.DirectoryEpoch
	rec[colCommDirectorySnapshotAt] = model.NewTimestamp(audience.DirectorySnapshotAt).String()
	rec[colCommResolvedCount] = audience.ResolvedCount
	rec[colCommSelectorHash] = cloneCommunicationBytes(audience.SelectorHash)
	rec[colCommResolvedHash] = cloneCommunicationBytes(audience.ResolvedHash)
	return rec, nil
}

func messageAudienceFromRecord(rec model.Record) (MessageAudience, error) {
	reader := newCommunicationRecordReader(messageAudienceKind, rec)
	audience := MessageAudience{
		AppendOnlyCommunicationEntity: reader.appendOnlyEntity(), MessageID: reader.id(colCommMessageID),
		Ordinal: reader.integer(colCommOrdinal),
		Selector: AudienceSelector{
			Kind: AudienceSelectorKind(reader.text(colCommSelectorKind)),
			Ref:  reader.optionalText(colCommSelectorRef), Required: reader.boolean(colCommSelectorRequired),
			WakePolicy: WakePolicy(reader.text(colCommSelectorWakePolicy)),
		},
		RouteRuleID:          reader.optionalID(colCommRouteRuleID),
		ChannelACLRevision:   reader.integer(colCommChannelACLRevision),
		RouteRevision:        reader.integer(colCommRouteRevision),
		SubscriptionRevision: reader.integer(colCommSubscriptionRevision),
		DirectoryEpoch:       reader.integer(colCommDirectoryEpoch),
		DirectorySnapshotAt:  reader.timestamp(colCommDirectorySnapshotAt),
		ResolvedCount:        reader.integer(colCommResolvedCount),
		SelectorHash:         reader.bytes(colCommSelectorHash), ResolvedHash: reader.bytes(colCommResolvedHash),
	}
	if reader.err != nil {
		return MessageAudience{}, wrapCommunicationCodecError("decode", messageAudienceKind, reader.err)
	}
	if err := ValidateMessageAudience(audience); err != nil {
		return MessageAudience{}, wrapCommunicationCodecError("decode", messageAudienceKind, err)
	}
	return audience, nil
}

func messageAudienceRecipientToRecord(contribution MessageAudienceRecipient) (model.Record, error) {
	if err := validateAudienceContribution(contribution); err != nil {
		return nil, wrapCommunicationCodecError("encode", messageAudienceRecipientKind, err)
	}
	rec := appendOnlyCommunicationRecord(contribution.AppendOnlyCommunicationEntity)
	rec[colCommMessageAudienceID] = contribution.MessageAudienceID.String()
	rec[colCommMessageDeliveryID] = contribution.MessageDeliveryID.String()
	encodeRecipient(rec, colCommRecipientKind, colCommRecipientRef, contribution.Recipient)
	rec[colCommRecipientEpoch] = contribution.RecipientEpoch
	rec[colCommRequired] = contribution.Required
	rec[colCommWakePolicy] = string(contribution.WakePolicy)
	if err := setCanonicalCommunicationJSON(rec, colCommRouteReasonsJSON, contribution.RouteReasons); err != nil {
		return nil, wrapCommunicationCodecError("encode", messageAudienceRecipientKind, err)
	}
	rec[colCommSelectorKind] = string(contribution.Selector.Kind)
	rec[colCommSelectorRef] = optionalCommunicationText(contribution.Selector.Ref)
	rec[colCommSelectorRequired] = contribution.Selector.Required
	rec[colCommSelectorWakePolicy] = string(contribution.Selector.WakePolicy)
	rec[colCommDirectoryEpoch] = contribution.DirectoryEpoch
	rec[colCommChannelACLRevision] = contribution.ChannelACLRevision
	rec[colCommRouteRevision] = contribution.RouteRevision
	rec[colCommSubscriptionRevision] = contribution.SubscriptionRevision
	rec[colCommCausalKind] = string(contribution.CausalKind)
	rec[colCommCausalRef] = contribution.CausalRef
	rec[colCommCausalFactKind] = optionalCommunicationText(string(contribution.CausalFactKind))
	rec[colCommCausalFactID] = optionalCommunicationID(contribution.CausalFactID)
	rec[colCommCausalFactVersion] = optionalCommunicationInteger(contribution.CausalFactVersion)
	rec[colCommObservedSessionSID] = optionalCommunicationText(contribution.ObservedSessionSID)
	rec[colCommObservedClaimFence] = optionalCommunicationInteger(contribution.ObservedClaimFence)
	encodeOptionalCommunicationSubject(
		rec, colCommOriginalSubscriberKind, colCommOriginalSubscriberRef, contribution.OriginalSubscriber,
	)
	rec[colCommSubscriptionID] = optionalCommunicationID(contribution.SubscriptionID)
	rec[colCommSubscriptionGeneration] = optionalCommunicationInteger(contribution.SubscriptionGeneration)
	rec[colCommRouteRuleID] = optionalCommunicationID(contribution.RouteRuleID)
	rec[colCommRouteRuleGeneration] = optionalCommunicationInteger(contribution.RouteRuleGeneration)
	rec[colCommCausalArcHash] = cloneCommunicationBytes(contribution.CausalArcHash)
	return rec, nil
}

func messageAudienceRecipientFromRecord(rec model.Record) (MessageAudienceRecipient, error) {
	reader := newCommunicationRecordReader(messageAudienceRecipientKind, rec)
	routeReasonsJSON := reader.canonicalJSON(colCommRouteReasonsJSON)
	var routeReasons []RouteReason
	reader.decodeCanonicalJSON(colCommRouteReasonsJSON, routeReasonsJSON, &routeReasons)
	contribution := MessageAudienceRecipient{
		AppendOnlyCommunicationEntity: reader.appendOnlyEntity(),
		MessageAudienceID:             reader.id(colCommMessageAudienceID),
		MessageDeliveryID:             reader.id(colCommMessageDeliveryID),
		Recipient:                     decodeRecipient(reader, colCommRecipientKind, colCommRecipientRef),
		RecipientEpoch:                reader.integer(colCommRecipientEpoch), Required: reader.boolean(colCommRequired),
		WakePolicy: WakePolicy(reader.text(colCommWakePolicy)), RouteReasons: routeReasons,
		Selector: AudienceSelector{
			Kind: AudienceSelectorKind(reader.text(colCommSelectorKind)),
			Ref:  reader.optionalText(colCommSelectorRef), Required: reader.boolean(colCommSelectorRequired),
			WakePolicy: WakePolicy(reader.text(colCommSelectorWakePolicy)),
		},
		DirectoryEpoch:       reader.integer(colCommDirectoryEpoch),
		ChannelACLRevision:   reader.integer(colCommChannelACLRevision),
		RouteRevision:        reader.integer(colCommRouteRevision),
		SubscriptionRevision: reader.integer(colCommSubscriptionRevision),
		CausalKind:           AudienceCausalKind(reader.text(colCommCausalKind)),
		CausalRef:            reader.text(colCommCausalRef),
		CausalFactKind:       model.Kind(reader.optionalText(colCommCausalFactKind)),
		CausalFactID:         reader.optionalID(colCommCausalFactID),
		CausalFactVersion:    reader.optionalPositiveInteger(colCommCausalFactVersion),
		ObservedSessionSID:   reader.optionalText(colCommObservedSessionSID),
		ObservedClaimFence:   reader.optionalPositiveInteger(colCommObservedClaimFence),
		OriginalSubscriber: decodeOptionalCommunicationSubject(
			reader, colCommOriginalSubscriberKind, colCommOriginalSubscriberRef,
		),
		SubscriptionID:         reader.optionalID(colCommSubscriptionID),
		SubscriptionGeneration: reader.optionalPositiveInteger(colCommSubscriptionGeneration),
		RouteRuleID:            reader.optionalID(colCommRouteRuleID),
		RouteRuleGeneration:    reader.optionalPositiveInteger(colCommRouteRuleGeneration),
		CausalArcHash:          reader.bytes(colCommCausalArcHash),
	}
	if reader.err != nil {
		return MessageAudienceRecipient{}, wrapCommunicationCodecError(
			"decode", messageAudienceRecipientKind, reader.err,
		)
	}
	if err := validateAudienceContribution(contribution); err != nil {
		return MessageAudienceRecipient{}, wrapCommunicationCodecError(
			"decode", messageAudienceRecipientKind, err,
		)
	}
	return contribution, nil
}

func messageDeliveryToRecord(delivery MessageDelivery) (model.Record, error) {
	if err := ValidateMessageDelivery(delivery); err != nil {
		return nil, wrapCommunicationCodecError("encode", messageDeliveryKind, err)
	}
	rec := mutableCommunicationRecord(delivery.MutableCommunicationEntity)
	rec[colCommMessageID] = delivery.MessageID.String()
	encodeRecipient(rec, colCommRecipientKind, colCommRecipientRef, delivery.Recipient)
	rec[colCommRecipientEpoch] = delivery.RecipientEpoch
	rec[colCommDeliverySeq] = delivery.DeliverySeq
	rec[colCommRequired] = delivery.Required
	if err := setCanonicalCommunicationJSON(rec, colCommRouteReasonsJSON, delivery.RouteReasons); err != nil {
		return nil, wrapCommunicationCodecError("encode", messageDeliveryKind, err)
	}
	rec[colCommWakePolicy] = string(delivery.WakePolicy)
	rec[colCommState] = string(delivery.State)
	rec[colCommAvailableAt] = model.NewTimestamp(delivery.AvailableAt).String()
	rec[colCommFirstSeenAt] = optionalCommunicationTimestamp(delivery.FirstSeenAt)
	rec[colCommAckDueAt] = optionalCommunicationTimestamp(delivery.AckDueAt)
	rec[colCommExpiresAt] = optionalCommunicationTimestamp(delivery.ExpiresAt)
	rec[colCommAckID] = optionalCommunicationID(delivery.AckID)
	rec[colCommAcknowledgedAt] = optionalCommunicationTimestamp(delivery.AcknowledgedAt)
	rec[colCommLastWakeVerdict] = optionalCommunicationText(string(delivery.LastWakeVerdict))
	rec[colCommLastWakeCode] = optionalCommunicationText(delivery.LastWakeCode)
	rec[colCommLastWakeAt] = optionalCommunicationTimestamp(delivery.LastWakeAt)
	rec[colCommRetirementTombstoneKind] = optionalCommunicationText(string(delivery.RetirementTombstoneKind))
	rec[colCommRetirementTombstoneID] = optionalCommunicationID(delivery.RetirementTombstoneID)
	rec[colCommRetirementTombstoneVersion] = optionalCommunicationInteger(delivery.RetirementTombstoneVersion)
	rec[colCommRetirementEpoch] = optionalCommunicationInteger(delivery.RetirementEpoch)
	rec[colCommUndeliverableAt] = optionalCommunicationTimestamp(delivery.UndeliverableAt)
	rec[colCommUndeliverableCode] = optionalCommunicationText(delivery.UndeliverableCode)
	return rec, nil
}

func messageDeliveryFromRecord(rec model.Record) (MessageDelivery, error) {
	reader := newCommunicationRecordReader(messageDeliveryKind, rec)
	routeReasonsJSON := reader.canonicalJSON(colCommRouteReasonsJSON)
	var routeReasons []RouteReason
	reader.decodeCanonicalJSON(colCommRouteReasonsJSON, routeReasonsJSON, &routeReasons)
	delivery := MessageDelivery{
		MutableCommunicationEntity: reader.mutableEntity(), MessageID: reader.id(colCommMessageID),
		Recipient:      decodeRecipient(reader, colCommRecipientKind, colCommRecipientRef),
		RecipientEpoch: reader.integer(colCommRecipientEpoch), DeliverySeq: reader.integer(colCommDeliverySeq),
		Required: reader.boolean(colCommRequired), RouteReasons: routeReasons,
		WakePolicy:  WakePolicy(reader.text(colCommWakePolicy)),
		State:       MessageDeliveryState(reader.text(colCommState)),
		AvailableAt: reader.timestamp(colCommAvailableAt), FirstSeenAt: reader.optionalTimestamp(colCommFirstSeenAt),
		AckDueAt: reader.optionalTimestamp(colCommAckDueAt), ExpiresAt: reader.optionalTimestamp(colCommExpiresAt),
		AckID: reader.optionalID(colCommAckID), AcknowledgedAt: reader.optionalTimestamp(colCommAcknowledgedAt),
		LastWakeVerdict: AssessmentVerdict(reader.optionalText(colCommLastWakeVerdict)),
		LastWakeCode:    reader.optionalText(colCommLastWakeCode), LastWakeAt: reader.optionalTimestamp(colCommLastWakeAt),
		RetirementTombstoneKind:    model.Kind(reader.optionalText(colCommRetirementTombstoneKind)),
		RetirementTombstoneID:      reader.optionalID(colCommRetirementTombstoneID),
		RetirementTombstoneVersion: reader.optionalPositiveInteger(colCommRetirementTombstoneVersion),
		RetirementEpoch:            reader.optionalPositiveInteger(colCommRetirementEpoch),
		UndeliverableAt:            reader.optionalTimestamp(colCommUndeliverableAt),
		UndeliverableCode:          reader.optionalText(colCommUndeliverableCode),
	}
	if reader.err != nil {
		return MessageDelivery{}, wrapCommunicationCodecError("decode", messageDeliveryKind, reader.err)
	}
	if err := ValidateMessageDelivery(delivery); err != nil {
		return MessageDelivery{}, wrapCommunicationCodecError("decode", messageDeliveryKind, err)
	}
	return delivery, nil
}

func inboxCursorToRecord(cursor InboxCursor) (model.Record, error) {
	if err := validateInboxCursor(cursor); err != nil {
		return nil, wrapCommunicationCodecError("encode", inboxCursorKind, err)
	}
	rec := mutableCommunicationRecord(cursor.MutableCommunicationEntity)
	encodeRecipient(rec, colCommReaderKind, colCommReaderRef, cursor.Reader)
	rec[colCommMailboxKind] = string(cursor.MailboxKind)
	rec[colCommMailboxRef] = cursor.MailboxRef
	rec[colCommLastSeenSeq] = cursor.LastSeenSeq
	rec[colCommLastSeenAt] = model.NewTimestamp(cursor.LastSeenAt).String()
	rec[colCommFilterHash] = cloneCommunicationBytes(cursor.FilterHash)
	return rec, nil
}

func inboxCursorFromRecord(rec model.Record) (InboxCursor, error) {
	reader := newCommunicationRecordReader(inboxCursorKind, rec)
	cursor := InboxCursor{
		MutableCommunicationEntity: reader.mutableEntity(),
		Reader:                     decodeRecipient(reader, colCommReaderKind, colCommReaderRef),
		MailboxKind:                MailboxKind(reader.text(colCommMailboxKind)), MailboxRef: reader.text(colCommMailboxRef),
		LastSeenSeq: reader.integer(colCommLastSeenSeq), LastSeenAt: reader.timestamp(colCommLastSeenAt),
		FilterHash: reader.bytes(colCommFilterHash),
	}
	if reader.err != nil {
		return InboxCursor{}, wrapCommunicationCodecError("decode", inboxCursorKind, reader.err)
	}
	if err := validateInboxCursor(cursor); err != nil {
		return InboxCursor{}, wrapCommunicationCodecError("decode", inboxCursorKind, err)
	}
	return cursor, nil
}

func inboxCursorBarrierToRecord(barrier InboxCursorBarrier, cursor InboxCursor) (model.Record, error) {
	if err := validateInboxCursor(cursor); err != nil {
		return nil, wrapCommunicationCodecError("encode context", inboxCursorKind, err)
	}
	if err := ValidateInboxCursorBarrier(cursor, barrier); err != nil {
		return nil, wrapCommunicationCodecError("encode", inboxCursorBarrierKind, err)
	}
	rec := mutableCommunicationRecord(barrier.MutableCommunicationEntity)
	encodeRecipient(rec, colCommReaderKind, colCommReaderRef, barrier.Reader)
	rec[colCommMailboxKind] = string(barrier.MailboxKind)
	rec[colCommMailboxRef] = barrier.MailboxRef
	rec[colCommFilterHash] = cloneCommunicationBytes(barrier.FilterHash)
	rec[colCommDeliveryID] = barrier.DeliveryID.String()
	rec[colCommBarrierSeq] = barrier.BarrierSeq
	rec[colCommCause] = string(barrier.Cause)
	rec[colCommState] = string(barrier.State)
	rec[colCommResolvedAt] = optionalCommunicationTimestamp(barrier.ResolvedAt)
	rec[colCommReasonCode] = barrier.ReasonCode
	return rec, nil
}

func inboxCursorBarrierFromRecord(rec model.Record, cursor InboxCursor) (InboxCursorBarrier, error) {
	if err := validateInboxCursor(cursor); err != nil {
		return InboxCursorBarrier{}, wrapCommunicationCodecError("decode context", inboxCursorKind, err)
	}
	reader := newCommunicationRecordReader(inboxCursorBarrierKind, rec)
	barrier := InboxCursorBarrier{
		MutableCommunicationEntity: reader.mutableEntity(),
		Reader:                     decodeRecipient(reader, colCommReaderKind, colCommReaderRef),
		MailboxKind:                MailboxKind(reader.text(colCommMailboxKind)), MailboxRef: reader.text(colCommMailboxRef),
		FilterHash: reader.bytes(colCommFilterHash), DeliveryID: reader.id(colCommDeliveryID),
		BarrierSeq: reader.integer(colCommBarrierSeq), Cause: CursorBarrierCause(reader.text(colCommCause)),
		State: CursorBarrierState(reader.text(colCommState)), ResolvedAt: reader.optionalTimestamp(colCommResolvedAt),
		ReasonCode: reader.text(colCommReasonCode),
	}
	if reader.err != nil {
		return InboxCursorBarrier{}, wrapCommunicationCodecError("decode", inboxCursorBarrierKind, reader.err)
	}
	if err := ValidateInboxCursorBarrier(cursor, barrier); err != nil {
		return InboxCursorBarrier{}, wrapCommunicationCodecError("decode", inboxCursorBarrierKind, err)
	}
	return barrier, nil
}

func messageAckToRecord(ack MessageAck) (model.Record, error) {
	if err := ValidateMessageAck(ack); err != nil {
		return nil, wrapCommunicationCodecError("encode", messageAckKind, err)
	}
	rec := appendOnlyCommunicationRecord(ack.AppendOnlyCommunicationEntity)
	rec[colCommDeliveryID] = ack.DeliveryID.String()
	rec[colCommAckKind] = string(ack.Kind)
	encodeCommunicationActor(rec, colCommActorKind, colCommActorRef, ack.Actor)
	encodeOptionalRecipient(rec, colCommOnBehalfOfKind, colCommOnBehalfOfRef, ack.OnBehalfOf)
	if err := encodeProtectedPayload(rec, "note", ack.Note, false); err != nil {
		return nil, wrapCommunicationCodecError("encode", messageAckKind, err)
	}
	rec[colCommAcknowledgedAt] = model.NewTimestamp(ack.AcknowledgedAt).String()
	rec[colCommLate] = ack.Late
	return rec, nil
}

func messageAckFromRecord(rec model.Record) (MessageAck, error) {
	reader := newCommunicationRecordReader(messageAckKind, rec)
	note := decodeProtectedPayload(reader, "note", false)
	ack := MessageAck{
		AppendOnlyCommunicationEntity: reader.appendOnlyEntity(), DeliveryID: reader.id(colCommDeliveryID),
		Kind:       MessageAckKind(reader.text(colCommAckKind)),
		Actor:      decodeCommunicationActor(reader, colCommActorKind, colCommActorRef),
		OnBehalfOf: decodeOptionalRecipient(reader, colCommOnBehalfOfKind, colCommOnBehalfOfRef),
		Note:       note, AcknowledgedAt: reader.timestamp(colCommAcknowledgedAt), Late: reader.boolean(colCommLate),
	}
	if reader.err != nil {
		return MessageAck{}, wrapCommunicationCodecError("decode", messageAckKind, reader.err)
	}
	if err := ValidateMessageAck(ack); err != nil {
		return MessageAck{}, wrapCommunicationCodecError("decode", messageAckKind, err)
	}
	return ack, nil
}

func communicationGuardToRecord(guard CommunicationGuard) (model.Record, error) {
	if err := ValidateCommunicationGuard(guard); err != nil {
		return nil, wrapCommunicationCodecError("encode", communicationGuardKind, err)
	}
	rec := mutableCommunicationRecord(guard.MutableCommunicationEntity)
	rec[colCommGuardKind] = string(guard.Kind)
	rec[colCommNextSeq] = guard.NextSeq
	rec[colCommLastDBTime] = model.NewTimestamp(guard.LastDBTime).String()
	return rec, nil
}

func communicationGuardFromRecord(rec model.Record) (CommunicationGuard, error) {
	reader := newCommunicationRecordReader(communicationGuardKind, rec)
	guard := CommunicationGuard{
		MutableCommunicationEntity: reader.mutableEntity(),
		Kind:                       CommunicationGuardKind(reader.text(colCommGuardKind)),
		NextSeq:                    reader.integer(colCommNextSeq), LastDBTime: reader.timestamp(colCommLastDBTime),
	}
	if reader.err != nil {
		return CommunicationGuard{}, wrapCommunicationCodecError("decode", communicationGuardKind, reader.err)
	}
	if err := ValidateCommunicationGuard(guard); err != nil {
		return CommunicationGuard{}, wrapCommunicationCodecError("decode", communicationGuardKind, err)
	}
	return guard, nil
}

func decisionRequestToRecord(request DecisionRequest) (model.Record, error) {
	if err := ValidateDecisionRequest(request); err != nil {
		return nil, wrapCommunicationCodecError("encode", decisionRequestKind, err)
	}
	rec := mutableCommunicationRecord(request.MutableCommunicationEntity)
	rec[colCommMessageID] = request.MessageID.String()
	rec[colWorkItemID] = request.WorkItemID.String()
	rec[colCommDecisionKey] = request.DecisionKey
	encodeCommunicationActor(rec, colCommRequesterKind, colCommRequesterRef, request.Requester)
	encodeCommunicationSubject(rec, colCommOwnerKind, colCommOwnerRef, request.Owner)
	rec[colCommAcceptedDeliveryID] = optionalCommunicationID(request.AcceptedDeliveryID)
	rec[colCommState] = string(request.State)
	if err := encodeProtectedPayload(rec, "request", &request.Request, true); err != nil {
		return nil, wrapCommunicationCodecError("encode", decisionRequestKind, err)
	}
	rec[colCommAuthorityRequirement] = request.AuthorityRequirement
	rec[colCommDueAt] = model.NewTimestamp(request.DueAt).String()
	rec[colCommAcceptedAt] = optionalCommunicationTimestamp(request.AcceptedAt)
	rec[colCommBlockedCode] = optionalCommunicationText(request.BlockedCode)
	rec[colCommTerminalCode] = optionalCommunicationText(request.TerminalCode)
	rec[colCommResolvedDecisionID] = optionalCommunicationID(request.ResolvedDecisionID)
	rec[colCommLastResponseSeq] = request.LastResponseSeq
	return rec, nil
}

func decisionRequestFromRecord(rec model.Record) (DecisionRequest, error) {
	reader := newCommunicationRecordReader(decisionRequestKind, rec)
	payload := decodeProtectedPayload(reader, "request", true)
	request := DecisionRequest{
		MutableCommunicationEntity: reader.mutableEntity(), MessageID: reader.id(colCommMessageID),
		WorkItemID: reader.id(colWorkItemID), DecisionKey: reader.text(colCommDecisionKey),
		Requester:            decodeCommunicationActor(reader, colCommRequesterKind, colCommRequesterRef),
		Owner:                decodeCommunicationSubject(reader, colCommOwnerKind, colCommOwnerRef),
		AcceptedDeliveryID:   reader.optionalID(colCommAcceptedDeliveryID),
		State:                DecisionRequestState(reader.text(colCommState)),
		AuthorityRequirement: reader.text(colCommAuthorityRequirement), DueAt: reader.timestamp(colCommDueAt),
		AcceptedAt: reader.optionalTimestamp(colCommAcceptedAt), BlockedCode: reader.optionalText(colCommBlockedCode),
		TerminalCode:       reader.optionalText(colCommTerminalCode),
		ResolvedDecisionID: reader.optionalID(colCommResolvedDecisionID),
		LastResponseSeq:    reader.integer(colCommLastResponseSeq),
	}
	if payload != nil {
		request.Request = *payload
	}
	if reader.err != nil {
		return DecisionRequest{}, wrapCommunicationCodecError("decode", decisionRequestKind, reader.err)
	}
	if err := ValidateDecisionRequest(request); err != nil {
		return DecisionRequest{}, wrapCommunicationCodecError("decode", decisionRequestKind, err)
	}
	return request, nil
}

func decisionResponseToRecord(
	response DecisionResponse,
	before DecisionRequest,
	after DecisionRequest,
) (model.Record, error) {
	if err := ValidateDecisionRequest(before); err != nil {
		return nil, wrapCommunicationCodecError("encode before", decisionResponseKind, err)
	}
	if err := ValidateDecisionRequest(after); err != nil {
		return nil, wrapCommunicationCodecError("encode after", decisionResponseKind, err)
	}
	if err := ValidateDecisionResponse(response, before, after); err != nil {
		return nil, wrapCommunicationCodecError("encode", decisionResponseKind, err)
	}
	rec := appendOnlyCommunicationRecord(response.AppendOnlyCommunicationEntity)
	rec[colCommRequestID] = response.RequestID.String()
	rec[colCommResponseSeq] = response.ResponseSeq
	rec[colCommFromState] = string(response.FromState)
	rec[colCommToState] = string(response.ToState)
	encodeCommunicationActor(rec, colCommActorKind, colCommActorRef, response.Actor)
	if err := encodeProtectedPayload(rec, "response", &response.Response, true); err != nil {
		return nil, wrapCommunicationCodecError("encode", decisionResponseKind, err)
	}
	rec[colCommAcceptedDeliveryID] = optionalCommunicationID(response.AcceptedDeliveryID)
	rec[colCommBlockerWorkItemID] = optionalCommunicationID(response.BlockerWorkItemID)
	rec[colCommWorkDecisionID] = optionalCommunicationID(response.WorkDecisionID)
	rec[colCommRespondedAt] = model.NewTimestamp(response.RespondedAt).String()
	return rec, nil
}

func decisionResponseFromRecord(
	rec model.Record,
	before DecisionRequest,
	after DecisionRequest,
) (DecisionResponse, error) {
	reader := newCommunicationRecordReader(decisionResponseKind, rec)
	payload := decodeProtectedPayload(reader, "response", true)
	response := DecisionResponse{
		AppendOnlyCommunicationEntity: reader.appendOnlyEntity(), RequestID: reader.id(colCommRequestID),
		ResponseSeq:        reader.integer(colCommResponseSeq),
		FromState:          DecisionRequestState(reader.text(colCommFromState)),
		ToState:            DecisionRequestState(reader.text(colCommToState)),
		Actor:              decodeCommunicationActor(reader, colCommActorKind, colCommActorRef),
		AcceptedDeliveryID: reader.optionalID(colCommAcceptedDeliveryID),
		BlockerWorkItemID:  reader.optionalID(colCommBlockerWorkItemID),
		WorkDecisionID:     reader.optionalID(colCommWorkDecisionID),
		RespondedAt:        reader.timestamp(colCommRespondedAt),
	}
	if payload != nil {
		response.Response = *payload
	}
	if reader.err != nil {
		return DecisionResponse{}, wrapCommunicationCodecError("decode", decisionResponseKind, reader.err)
	}
	if err := ValidateDecisionRequest(before); err != nil {
		return DecisionResponse{}, wrapCommunicationCodecError("decode before", decisionResponseKind, err)
	}
	if err := ValidateDecisionRequest(after); err != nil {
		return DecisionResponse{}, wrapCommunicationCodecError("decode after", decisionResponseKind, err)
	}
	if err := ValidateDecisionResponse(response, before, after); err != nil {
		return DecisionResponse{}, wrapCommunicationCodecError("decode", decisionResponseKind, err)
	}
	return response, nil
}

func handoffToRecord(handoff Handoff) (model.Record, error) {
	if err := ValidateHandoff(handoff); err != nil {
		return nil, wrapCommunicationCodecError("encode", handoffKind, err)
	}
	rec := mutableCommunicationRecord(handoff.MutableCommunicationEntity)
	rec[colWorkItemID] = handoff.WorkItemID.String()
	rec[colCommMessageID] = handoff.MessageID.String()
	rec[colCommDeliveryID] = handoff.DeliveryID.String()
	encodeRecipient(rec, colCommFromKind, colCommFromRef, handoff.From)
	rec[colCommFromOwnerEpoch] = handoff.FromOwnerEpoch
	encodeRecipient(rec, colCommToKind, colCommToRef, handoff.To)
	rec[colCommOfferedLeaseFence] = optionalCommunicationInteger(handoff.OfferedLeaseFence)
	rec[colCommContextEventSeq] = handoff.ContextEventSeq
	rec[colCommContextHash] = cloneCommunicationBytes(handoff.ContextHash)
	if err := encodeProtectedPayload(rec, "handoff", &handoff.Payload, true); err != nil {
		return nil, wrapCommunicationCodecError("encode", handoffKind, err)
	}
	rec[colCommState] = string(handoff.State)
	rec[colCommAckDeadline] = model.NewTimestamp(handoff.AckDeadline).String()
	rec[colCommAckID] = optionalCommunicationID(handoff.AckID)
	rec[colCommAcceptedAt] = optionalCommunicationTimestamp(handoff.AcceptedAt)
	rec[colCommRejectedAt] = optionalCommunicationTimestamp(handoff.RejectedAt)
	rec[colCommWithdrawnAt] = optionalCommunicationTimestamp(handoff.WithdrawnAt)
	rec[colCommExpiredAt] = optionalCommunicationTimestamp(handoff.ExpiredAt)
	rec[colCommTerminalCode] = optionalCommunicationText(handoff.TerminalCode)
	if err := encodeProtectedPayload(rec, "terminal_reason", handoff.TerminalReason, false); err != nil {
		return nil, wrapCommunicationCodecError("encode", handoffKind, err)
	}
	rec[colCommResultingLeaseFence] = optionalCommunicationInteger(handoff.ResultingLeaseFence)
	return rec, nil
}

func handoffFromRecord(rec model.Record) (Handoff, error) {
	reader := newCommunicationRecordReader(handoffKind, rec)
	payload := decodeProtectedPayload(reader, "handoff", true)
	terminalReason := decodeProtectedPayload(reader, "terminal_reason", false)
	handoff := Handoff{
		MutableCommunicationEntity: reader.mutableEntity(), WorkItemID: reader.id(colWorkItemID),
		MessageID: reader.id(colCommMessageID), DeliveryID: reader.id(colCommDeliveryID),
		From:              decodeRecipient(reader, colCommFromKind, colCommFromRef),
		FromOwnerEpoch:    reader.integer(colCommFromOwnerEpoch),
		To:                decodeRecipient(reader, colCommToKind, colCommToRef),
		OfferedLeaseFence: reader.optionalPositiveInteger(colCommOfferedLeaseFence),
		ContextEventSeq:   reader.integer(colCommContextEventSeq), ContextHash: reader.bytes(colCommContextHash),
		State: HandoffState(reader.text(colCommState)), AckDeadline: reader.timestamp(colCommAckDeadline),
		AckID: reader.optionalID(colCommAckID), AcceptedAt: reader.optionalTimestamp(colCommAcceptedAt),
		RejectedAt:  reader.optionalTimestamp(colCommRejectedAt),
		WithdrawnAt: reader.optionalTimestamp(colCommWithdrawnAt), ExpiredAt: reader.optionalTimestamp(colCommExpiredAt),
		TerminalCode: reader.optionalText(colCommTerminalCode), TerminalReason: terminalReason,
		ResultingLeaseFence: reader.optionalPositiveInteger(colCommResultingLeaseFence),
	}
	if payload != nil {
		handoff.Payload = *payload
	}
	if reader.err != nil {
		return Handoff{}, wrapCommunicationCodecError("decode", handoffKind, reader.err)
	}
	if err := ValidateHandoff(handoff); err != nil {
		return Handoff{}, wrapCommunicationCodecError("decode", handoffKind, err)
	}
	return handoff, nil
}

func deliveryDispatchToRecord(dispatch DeliveryDispatch) (model.Record, error) {
	if err := ValidateDeliveryDispatch(dispatch); err != nil {
		return nil, wrapCommunicationCodecError("encode", deliveryDispatchKind, err)
	}
	rec := mutableCommunicationRecord(dispatch.MutableCommunicationEntity)
	rec[colCommDeliveryID] = dispatch.DeliveryID.String()
	rec[colCommRootDispatchID] = dispatch.RootDispatchID.String()
	rec[colCommPredecessorID] = optionalCommunicationID(dispatch.PredecessorID)
	rec[colCommEndpointID] = dispatch.EndpointID.String()
	rec[colCommEndpointGeneration] = dispatch.EndpointGeneration
	rec[colCommRouteRuleID] = optionalCommunicationID(dispatch.RouteRuleID)
	rec[colCommRouteRuleGeneration] = optionalCommunicationInteger(dispatch.RouteRuleGeneration)
	rec[colCommDispatchGeneration] = dispatch.DispatchGeneration
	rec[colCommRerouteRung] = dispatch.RerouteRung
	rec[colCommPolicyGeneration] = dispatch.PolicyGeneration
	rec[colCommState] = string(dispatch.State)
	rec[colCommAttemptCount] = dispatch.AttemptCount
	rec[colCommNextAttemptAt] = optionalCommunicationTimestamp(dispatch.NextAttemptAt)
	rec[colCommClaimOwner] = optionalCommunicationText(dispatch.ClaimOwner)
	rec[colCommClaimUntil] = optionalCommunicationTimestamp(dispatch.ClaimUntil)
	rec[colCommIdempotencyKeyHash] = cloneCommunicationBytes(dispatch.IdempotencyKeyHash)
	rec[colCommLastVerdict] = optionalCommunicationText(string(dispatch.LastVerdict))
	rec[colCommLastCode] = optionalCommunicationText(dispatch.LastCode)
	rec[colCommResolutionDeadlineAt] = optionalCommunicationTimestamp(dispatch.ResolutionDeadlineAt)
	rec[colCommResolutionCode] = optionalCommunicationText(dispatch.ResolutionCode)
	rec[colCommReconciledAttemptID] = optionalCommunicationID(dispatch.ReconciledAttemptID)
	rec[colCommReconciledEndpointID] = optionalCommunicationID(dispatch.ReconciledEndpointID)
	rec[colCommReconciledEndpointGeneration] = optionalCommunicationInteger(dispatch.ReconciledEndpointGeneration)
	rec[colCommReconciliationVerdict] = optionalCommunicationText(string(dispatch.ReconciliationVerdict))
	rec[colCommReconciliationCode] = optionalCommunicationText(dispatch.ReconciliationCode)
	rec[colCommReconciliationEvidenceRef] = optionalCommunicationText(dispatch.ReconciliationEvidenceRef)
	rec[colCommReconciliationObservedAt] = optionalCommunicationTimestamp(dispatch.ReconciliationObservedAt)
	rec[colCommProviderAcceptanceHash] = optionalCommunicationBytes(dispatch.ProviderAcceptanceHash)
	rec[colCommSettledAt] = optionalCommunicationTimestamp(dispatch.SettledAt)
	return rec, nil
}

func deliveryDispatchFromRecord(rec model.Record) (DeliveryDispatch, error) {
	reader := newCommunicationRecordReader(deliveryDispatchKind, rec)
	dispatch := DeliveryDispatch{
		MutableCommunicationEntity: reader.mutableEntity(), DeliveryID: reader.id(colCommDeliveryID),
		RootDispatchID: reader.id(colCommRootDispatchID), PredecessorID: reader.optionalID(colCommPredecessorID),
		EndpointID: reader.id(colCommEndpointID), EndpointGeneration: reader.integer(colCommEndpointGeneration),
		RouteRuleID:         reader.optionalID(colCommRouteRuleID),
		RouteRuleGeneration: reader.optionalPositiveInteger(colCommRouteRuleGeneration),
		DispatchGeneration:  reader.integer(colCommDispatchGeneration), RerouteRung: reader.integer(colCommRerouteRung),
		PolicyGeneration: reader.integer(colCommPolicyGeneration), State: DeliveryDispatchState(reader.text(colCommState)),
		AttemptCount: reader.integer(colCommAttemptCount), NextAttemptAt: reader.optionalTimestamp(colCommNextAttemptAt),
		ClaimOwner: reader.optionalText(colCommClaimOwner), ClaimUntil: reader.optionalTimestamp(colCommClaimUntil),
		IdempotencyKeyHash:           reader.bytes(colCommIdempotencyKeyHash),
		LastVerdict:                  AssessmentVerdict(reader.optionalText(colCommLastVerdict)),
		LastCode:                     reader.optionalText(colCommLastCode),
		ResolutionDeadlineAt:         reader.optionalTimestamp(colCommResolutionDeadlineAt),
		ResolutionCode:               reader.optionalText(colCommResolutionCode),
		ReconciledAttemptID:          reader.optionalID(colCommReconciledAttemptID),
		ReconciledEndpointID:         reader.optionalID(colCommReconciledEndpointID),
		ReconciledEndpointGeneration: reader.optionalPositiveInteger(colCommReconciledEndpointGeneration),
		ReconciliationVerdict:        AssessmentVerdict(reader.optionalText(colCommReconciliationVerdict)),
		ReconciliationCode:           reader.optionalText(colCommReconciliationCode),
		ReconciliationEvidenceRef:    reader.optionalText(colCommReconciliationEvidenceRef),
		ReconciliationObservedAt:     reader.optionalTimestamp(colCommReconciliationObservedAt),
		ProviderAcceptanceHash:       reader.optionalBytes(colCommProviderAcceptanceHash),
		SettledAt:                    reader.optionalTimestamp(colCommSettledAt),
	}
	if reader.err != nil {
		return DeliveryDispatch{}, wrapCommunicationCodecError("decode", deliveryDispatchKind, reader.err)
	}
	if err := ValidateDeliveryDispatch(dispatch); err != nil {
		return DeliveryDispatch{}, wrapCommunicationCodecError("decode", deliveryDispatchKind, err)
	}
	return dispatch, nil
}

func deliveryAttemptToRecord(attempt DeliveryAttempt) (model.Record, error) {
	if err := ValidateDeliveryAttempt(attempt); err != nil {
		return nil, wrapCommunicationCodecError("encode", deliveryAttemptKind, err)
	}
	rec := mutableCommunicationRecord(attempt.MutableCommunicationEntity)
	rec[colCommDispatchID] = attempt.DispatchID.String()
	rec[colCommAttemptSeq] = attempt.AttemptSeq
	rec[colCommState] = string(attempt.State)
	rec[colCommStartedAt] = model.NewTimestamp(attempt.StartedAt).String()
	rec[colCommTransmitBoundary] = string(attempt.TransmitBoundary)
	rec[colCommFinishedAt] = optionalCommunicationTimestamp(attempt.FinishedAt)
	rec[colCommVerdict] = optionalCommunicationText(string(attempt.Verdict))
	rec[colCommCode] = optionalCommunicationText(attempt.Code)
	rec[colCommProviderReceiptHash] = optionalCommunicationBytes(attempt.ProviderReceiptHash)
	rec[colCommRequestHash] = cloneCommunicationBytes(attempt.RequestHash)
	return rec, nil
}

func deliveryAttemptFromRecord(rec model.Record) (DeliveryAttempt, error) {
	reader := newCommunicationRecordReader(deliveryAttemptKind, rec)
	attempt := DeliveryAttempt{
		MutableCommunicationEntity: reader.mutableEntity(), DispatchID: reader.id(colCommDispatchID),
		AttemptSeq: reader.integer(colCommAttemptSeq), State: DeliveryAttemptState(reader.text(colCommState)),
		StartedAt:        reader.timestamp(colCommStartedAt),
		TransmitBoundary: TransmitBoundary(reader.text(colCommTransmitBoundary)),
		FinishedAt:       reader.optionalTimestamp(colCommFinishedAt),
		Verdict:          AssessmentVerdict(reader.optionalText(colCommVerdict)), Code: reader.optionalText(colCommCode),
		ProviderReceiptHash: reader.optionalBytes(colCommProviderReceiptHash), RequestHash: reader.bytes(colCommRequestHash),
	}
	if reader.err != nil {
		return DeliveryAttempt{}, wrapCommunicationCodecError("decode", deliveryAttemptKind, reader.err)
	}
	if err := ValidateDeliveryAttempt(attempt); err != nil {
		return DeliveryAttempt{}, wrapCommunicationCodecError("decode", deliveryAttemptKind, err)
	}
	return attempt, nil
}

func communicationCommandReceiptToRecord(receipt CommunicationCommandReceipt) (model.Record, error) {
	if err := ValidateCommunicationCommandReceipt(receipt); err != nil {
		return nil, wrapCommunicationCodecError("encode", communicationCommandKind, err)
	}
	rec := appendOnlyCommunicationRecord(receipt.AppendOnlyCommunicationEntity)
	rec[colCommCommandID] = receipt.CommandID.String()
	rec[colCommActorFingerprint] = cloneCommunicationBytes(receipt.ActorFingerprint)
	rec[colCommCommandScope] = receipt.CommandScope
	rec[colCommIdempotencyKeyHash] = cloneCommunicationBytes(receipt.IdempotencyKeyHash)
	rec[colCommRequestDigest] = cloneCommunicationBytes(receipt.RequestDigest)
	rec[colCommSealKeyVersion] = optionalCommunicationText(receipt.SealKeyVersion)
	rec[colCommDigestKeyVersion] = optionalCommunicationText(receipt.DigestKeyVersion)
	rec[colCommPlanHash] = cloneCommunicationBytes(receipt.PlanHash)
	rec[colCommResultKind] = receipt.ResultKind
	rec[colCommResultID] = optionalCommunicationID(receipt.ResultID)
	rec[colCommHTTPStatus] = int64(receipt.HTTPStatus)
	if err := setCanonicalCommunicationJSON(rec, colCommResponseProjectionJSON, receipt.ResponseProjectionJSON); err != nil {
		return nil, wrapCommunicationCodecError("encode", communicationCommandKind, err)
	}
	rec[colCommResponseDigest] = cloneCommunicationBytes(receipt.ResponseDigest)
	rec[colEventID] = optionalCommunicationID(receipt.EventID)
	rec[colCommAuditSeq] = receipt.AuditSeq
	rec[colCommAuditHash] = optionalCommunicationBytes(receipt.AuditHash)
	rec[colCommCompletedAt] = model.NewTimestamp(receipt.CompletedAt).String()
	return rec, nil
}

func communicationCommandReceiptFromRecord(rec model.Record) (CommunicationCommandReceipt, error) {
	reader := newCommunicationRecordReader(communicationCommandKind, rec)
	projectionJSON := reader.canonicalJSON(colCommResponseProjectionJSON)
	var projection CommunicationCommandResponseProjection
	reader.decodeCanonicalJSON(colCommResponseProjectionJSON, projectionJSON, &projection)
	receipt := CommunicationCommandReceipt{
		AppendOnlyCommunicationEntity: reader.appendOnlyEntity(), CommandID: reader.id(colCommCommandID),
		ActorFingerprint: reader.bytes(colCommActorFingerprint), CommandScope: reader.text(colCommCommandScope),
		IdempotencyKeyHash: reader.bytes(colCommIdempotencyKeyHash), RequestDigest: reader.bytes(colCommRequestDigest),
		SealKeyVersion:   reader.optionalText(colCommSealKeyVersion),
		DigestKeyVersion: reader.optionalText(colCommDigestKeyVersion), PlanHash: reader.bytes(colCommPlanHash),
		ResultKind: reader.text(colCommResultKind), ResultID: reader.optionalID(colCommResultID),
		HTTPStatus: int(reader.integer(colCommHTTPStatus)), ResponseProjectionJSON: projection,
		ResponseDigest: reader.bytes(colCommResponseDigest), EventID: reader.optionalID(colEventID),
		AuditSeq: reader.integer(colCommAuditSeq), AuditHash: reader.optionalBytes(colCommAuditHash),
		CompletedAt: reader.timestamp(colCommCompletedAt),
	}
	if reader.err != nil {
		return CommunicationCommandReceipt{}, wrapCommunicationCodecError(
			"decode", communicationCommandKind, reader.err,
		)
	}
	if err := ValidateCommunicationCommandReceipt(receipt); err != nil {
		return CommunicationCommandReceipt{}, wrapCommunicationCodecError(
			"decode", communicationCommandKind, err,
		)
	}
	return receipt, nil
}
