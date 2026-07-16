// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"encoding/json"
	"testing"
	"time"
)

// Golden wire vectors: the Actions Artifacts v4 messages must serialize to the
// SAME proto3-JSON bytes the act_runner exchanges — lowerCamelCase field names,
// int64 as a JSON STRING, google.protobuf wrappers as bare primitives, Timestamp
// as RFC 3339. This is the contract the removed protobuf/protojson code enforced;
// these tests keep it after the rip so the runner protocol cannot silently drift.

func TestWire_FinalizeArtifactRequest_int64IsString(t *testing.T) {
	// Incoming from the runner: size is a JSON STRING, hash is a bare string.
	const in = `{"workflowRunBackendId":"wr","workflowJobRunBackendId":"wj","name":"a","size":"12345","hash":"sha256:abc"}`
	var req FinalizeArtifactRequest
	if err := json.Unmarshal([]byte(in), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.Size != 12345 {
		t.Errorf("size = %d, want 12345 (int64 decoded from a JSON string)", req.Size)
	}
	if req.GetHash().GetValue() != "sha256:abc" {
		t.Errorf("hash = %q, want sha256:abc", req.GetHash().GetValue())
	}
}

func TestWire_FinalizeArtifactResponse_marshal(t *testing.T) {
	b, _ := json.Marshal(&FinalizeArtifactResponse{Ok: true, ArtifactId: 42})
	const want = `{"ok":true,"artifactId":"42"}` // artifactId is a STRING
	if string(b) != want {
		t.Fatalf("marshal = %s, want %s", b, want)
	}
}

func TestWire_MonolithArtifact_marshal(t *testing.T) {
	created := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	b, _ := json.Marshal(&ListArtifactsResponse_MonolithArtifact{
		WorkflowRunBackendId: "wr",
		DatabaseId:           7,
		Name:                 "art",
		Size:                 100,
		CreatedAt:            New(created),
	})
	const want = `{"workflowRunBackendId":"wr","databaseId":"7","name":"art","size":"100","createdAt":"2024-01-02T03:04:05Z"}`
	if string(b) != want {
		t.Fatalf("marshal = %s\nwant     %s", b, want)
	}
}

func TestWire_CreateArtifactResponse_marshal(t *testing.T) {
	b, _ := json.Marshal(&CreateArtifactResponse{Ok: true, SignedUploadUrl: "http://x/y"})
	const want = `{"ok":true,"signedUploadUrl":"http://x/y"}`
	if string(b) != want {
		t.Fatalf("marshal = %s, want %s", b, want)
	}
}

func TestWire_CreateArtifactRequest_unmarshal(t *testing.T) {
	const in = `{"workflowRunBackendId":"wr","name":"a","version":4,"expiresAt":"2030-01-01T00:00:00Z","mimeType":"application/zip"}`
	var req CreateArtifactRequest
	if err := json.Unmarshal([]byte(in), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if req.Version != 4 { // int32 stays a JSON number
		t.Errorf("version = %d, want 4", req.Version)
	}
	if req.GetMimeType().GetValue() != "application/zip" {
		t.Errorf("mimeType = %q", req.GetMimeType().GetValue())
	}
	if req.ExpiresAt == nil || req.ExpiresAt.AsTime().Year() != 2030 {
		t.Errorf("expiresAt not parsed: %+v", req.ExpiresAt)
	}
}

func TestWire_ListArtifactsRequest_idFilterStringOrNumber(t *testing.T) {
	// proto3-JSON sends Int64Value as a string; tolerate a number too.
	for _, in := range []string{
		`{"workflowRunBackendId":"wr","idFilter":"99","nameFilter":"art"}`,
		`{"workflowRunBackendId":"wr","idFilter":99,"nameFilter":"art"}`,
	} {
		var req ListArtifactsRequest
		if err := json.Unmarshal([]byte(in), &req); err != nil {
			t.Fatalf("unmarshal %s: %v", in, err)
		}
		if req.IdFilter.GetValue() != 99 {
			t.Errorf("idFilter = %d, want 99 (from %s)", req.IdFilter.GetValue(), in)
		}
		if req.NameFilter.GetValue() != "art" {
			t.Errorf("nameFilter = %q", req.NameFilter.GetValue())
		}
	}
}

// A nil wrapper getter is safe (the generated getter contract the handler relies
// on: req.GetHash().GetValue() when hash is absent → "").
func TestWire_nilWrapperGettersSafe(t *testing.T) {
	var req FinalizeArtifactRequest // Hash nil
	if req.GetHash().GetValue() != "" {
		t.Fatal("nil hash GetValue must be empty")
	}
	var lreq ListArtifactsRequest // IdFilter nil
	if lreq.IdFilter.GetValue() != 0 {
		t.Fatal("nil idFilter GetValue must be 0")
	}
}
