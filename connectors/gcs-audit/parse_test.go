// SPDX-FileCopyrightText: 2026 Olivares.AI
// SPDX-License-Identifier: Apache-2.0

package gcsaudit

import (
	"testing"
	"time"

	"github.com/olivaresai/olivares/sdk/model"
)

func TestClassifyMethod(t *testing.T) {
	cases := []struct {
		method string
		want   model.AccessMode
	}{
		{"storage.objects.get", model.ModeRead},
		{"storage.objects.list", model.ModeRead},
		{"storage.buckets.get", model.ModeRead},
		{"storage.buckets.list", model.ModeRead},
		{"storage.objects.create", model.ModeWrite},
		{"storage.objects.delete", model.ModeWrite},
		{"storage.objects.update", model.ModeWrite},
		{"storage.objects.getIamPolicy", model.ModeUnknown},
		{"storage.buckets.setIamPolicy", model.ModeUnknown},
		{"", model.ModeUnknown},
	}
	for _, c := range cases {
		if got := classifyMethod(c.method); got != c.want {
			t.Errorf("classifyMethod(%q) = %q, want %q", c.method, got, c.want)
		}
	}
}

func TestResolveResource(t *testing.T) {
	cases := []struct {
		name           string
		resourceName   string
		wantKind, wRef string
		wantOK         bool
	}{
		{"object", "projects/_/buckets/my-bucket/objects/dir/file.csv", "gcs.object", "gs://my-bucket/dir/file.csv", true},
		{"object simple", "projects/_/buckets/b/objects/o", "gcs.object", "gs://b/o", true},
		{"bucket", "projects/_/buckets/my-bucket", "gcs.bucket", "gs://my-bucket", true},
		{"bucket trailing slash", "projects/_/buckets/my-bucket/", "gcs.bucket", "gs://my-bucket", true},
		{"no buckets segment", "projects/my-project/datasets/analytics", "", "", false},
		{"empty bucket", "projects/_/buckets/", "", "", false},
		{"object with empty name", "projects/_/buckets/b/objects/", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kind, ref, ok := resolveResource(c.resourceName)
			if ok != c.wantOK || kind != c.wantKind || ref != c.wRef {
				t.Errorf("resolveResource(%q) = (%q, %q, %v), want (%q, %q, %v)",
					c.resourceName, kind, ref, ok, c.wantKind, c.wRef, c.wantOK)
			}
		})
	}
}

func TestParseTime(t *testing.T) {
	want := time.Date(2026, 6, 3, 10, 23, 45, 123000000, time.UTC)
	for _, s := range []string{"2026-06-03T10:23:45.123Z", " 2026-06-03T10:23:45.123Z "} {
		got, ok := parseTime(s)
		if !ok || !got.Equal(want) {
			t.Errorf("parseTime(%q) = (%v, %v), want (%v, true)", s, got, ok, want)
		}
	}
	if got, ok := parseTime("2026-06-03T10:23:45Z"); !ok || got.Location() != time.UTC {
		t.Errorf("parseTime no-fraction = (%v, %v), want UTC", got, ok)
	}
	if _, ok := parseTime("not-a-time"); ok {
		t.Error("parseTime(garbage) should fail")
	}
}
