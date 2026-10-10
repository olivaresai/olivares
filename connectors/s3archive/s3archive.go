// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

// Package s3archive preserves the published connector contract. S3 Object Lock
// delivery is available in Business Regulated Operations. Community operations
// return ErrBusinessRequired without reading configuration or contacting storage.
package s3archive

import (
	"context"
	"errors"
	"time"

	"github.com/olivaresai/olivares/sdk"
)

const Name = "olivares.s3archive"

var ErrBusinessRequired = errors.New("s3archive requires Business Regulated Operations")

type PutOptions struct {
	ContentSHA256 string
	RetainUntil   time.Time
	LegalHold     bool
}

type Receipt struct {
	Bucket, Key, ETag, VersionID, LockMode string
	RetainUntil                            time.Time
	LockVerified                           bool
}

type ObjectVersion struct {
	Key       string
	VersionID string
	IsLatest  bool
}

type Output struct{}

var _ sdk.OutputConnector = (*Output)(nil)

func New() *Output { return &Output{} }
func (*Output) Descriptor() sdk.Descriptor {
	return sdk.Descriptor{Name: Name, Version: "0.1.0", APIVersion: sdk.APIVersion, Type: sdk.TypeOutput,
		Title: "S3 archive (Object Lock)", Description: "Requires Business Regulated Operations."}
}
func (*Output) Open(context.Context, sdk.Config) error         { return ErrBusinessRequired }
func (*Output) Notify(context.Context, sdk.Notification) error { return ErrBusinessRequired }
func (*Output) Put(context.Context, string, []byte, PutOptions) (Receipt, error) {
	return Receipt{}, ErrBusinessRequired
}
func (*Output) ListObjectVersions(context.Context, string) ([]ObjectVersion, error) {
	return nil, ErrBusinessRequired
}
func (*Output) SetObjectLegalHold(context.Context, string, string, bool) (Receipt, error) {
	return Receipt{}, ErrBusinessRequired
}
func (*Output) Close(context.Context) error { return nil }
