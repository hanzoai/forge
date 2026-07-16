// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

// GitHub Actions Artifacts v4 protocol messages — plain Go structs, no protobuf.
//
// This replaces the generated artifact.pb.go. That file registered a protobuf
// FileDescriptor at package init from an embedded rawDesc; a version skew between
// the generator and the linked google.golang.org/protobuf runtime made the
// descriptor unparseable and the binary panicked at startup ("slice bounds out of
// range" in unmarshalSeed) before it could serve anything. The Actions artifact
// wire format is already JSON (the handler used protojson, Content-Type
// application/json), so protobuf bought nothing here but a fragile init.
//
// These structs serialize with encoding/json to the SAME proto3-JSON bytes the
// act_runner exchanges: lowerCamelCase field names, int64 as a JSON string, a
// Timestamp as an RFC 3339 string, and the well-known wrappers (StringValue,
// Int64Value) as their bare primitive. artifact_wire_test.go locks that byte
// contract with golden vectors so the runner protocol cannot silently drift.
package actions

import (
	"encoding/json"
	"strconv"
	"time"
)

// Timestamp mirrors google.protobuf.Timestamp's proto3-JSON encoding: an RFC 3339
// string. It keeps the AsTime()/New() surface the handler used.
type Timestamp struct{ time.Time }

// New wraps a time.Time as a *Timestamp (drop-in for timestamppb.New).
func New(t time.Time) *Timestamp { return &Timestamp{t} }

// AsTime returns the wrapped time (zero value for a nil receiver).
func (t *Timestamp) AsTime() time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.Time
}

func (t Timestamp) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.Time.UTC().Format(time.RFC3339Nano))
}

func (t *Timestamp) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if s == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return err
	}
	t.Time = parsed
	return nil
}

// StringValue mirrors google.protobuf.StringValue: a nullable string encoded as a
// bare JSON string. GetValue is nil-safe (the generated getter's contract).
type StringValue struct{ Value string }

func (s *StringValue) GetValue() string {
	if s == nil {
		return ""
	}
	return s.Value
}

func (s StringValue) MarshalJSON() ([]byte, error)  { return json.Marshal(s.Value) }
func (s *StringValue) UnmarshalJSON(b []byte) error { return json.Unmarshal(b, &s.Value) }

// Int64Value mirrors google.protobuf.Int64Value: a nullable int64 encoded (per
// proto3-JSON) as a JSON string. Unmarshal also tolerates a bare JSON number.
type Int64Value struct{ Value int64 }

func (v *Int64Value) GetValue() int64 {
	if v == nil {
		return 0
	}
	return v.Value
}

func (v Int64Value) MarshalJSON() ([]byte, error) {
	return json.Marshal(strconv.FormatInt(v.Value, 10))
}

func (v *Int64Value) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		if s == "" {
			return nil
		}
		n, perr := strconv.ParseInt(s, 10, 64)
		if perr != nil {
			return perr
		}
		v.Value = n
		return nil
	}
	return json.Unmarshal(b, &v.Value)
}

// ---- messages (github.actions.results.api.v1) ----
// int64 fields carry `,string` to match proto3-JSON (int64 encoded as a string).

type CreateArtifactRequest struct {
	WorkflowRunBackendId    string       `json:"workflowRunBackendId,omitempty"`
	WorkflowJobRunBackendId string       `json:"workflowJobRunBackendId,omitempty"`
	Name                    string       `json:"name,omitempty"`
	ExpiresAt               *Timestamp   `json:"expiresAt,omitempty"`
	Version                 int32        `json:"version,omitempty"`
	MimeType                *StringValue `json:"mimeType,omitempty"`
}

func (x *CreateArtifactRequest) GetMimeType() *StringValue {
	if x == nil {
		return nil
	}
	return x.MimeType
}

type CreateArtifactResponse struct {
	Ok              bool   `json:"ok,omitempty"`
	SignedUploadUrl string `json:"signedUploadUrl,omitempty"`
}

type FinalizeArtifactRequest struct {
	WorkflowRunBackendId    string       `json:"workflowRunBackendId,omitempty"`
	WorkflowJobRunBackendId string       `json:"workflowJobRunBackendId,omitempty"`
	Name                    string       `json:"name,omitempty"`
	Size                    int64        `json:"size,string,omitempty"`
	Hash                    *StringValue `json:"hash,omitempty"`
}

func (x *FinalizeArtifactRequest) GetHash() *StringValue {
	if x == nil {
		return nil
	}
	return x.Hash
}

type FinalizeArtifactResponse struct {
	Ok         bool  `json:"ok,omitempty"`
	ArtifactId int64 `json:"artifactId,string,omitempty"`
}

type ListArtifactsRequest struct {
	WorkflowRunBackendId    string       `json:"workflowRunBackendId,omitempty"`
	WorkflowJobRunBackendId string       `json:"workflowJobRunBackendId,omitempty"`
	NameFilter              *StringValue `json:"nameFilter,omitempty"`
	IdFilter                *Int64Value  `json:"idFilter,omitempty"`
}

type ListArtifactsResponse struct {
	Artifacts []*ListArtifactsResponse_MonolithArtifact `json:"artifacts,omitempty"`
}

type ListArtifactsResponse_MonolithArtifact struct {
	WorkflowRunBackendId    string     `json:"workflowRunBackendId,omitempty"`
	WorkflowJobRunBackendId string     `json:"workflowJobRunBackendId,omitempty"`
	DatabaseId              int64      `json:"databaseId,string,omitempty"`
	Name                    string     `json:"name,omitempty"`
	Size                    int64      `json:"size,string,omitempty"`
	CreatedAt               *Timestamp `json:"createdAt,omitempty"`
}

type GetSignedArtifactURLRequest struct {
	WorkflowRunBackendId    string `json:"workflowRunBackendId,omitempty"`
	WorkflowJobRunBackendId string `json:"workflowJobRunBackendId,omitempty"`
	Name                    string `json:"name,omitempty"`
}

type GetSignedArtifactURLResponse struct {
	SignedUrl string `json:"signedUrl,omitempty"`
}

type DeleteArtifactRequest struct {
	WorkflowRunBackendId    string `json:"workflowRunBackendId,omitempty"`
	WorkflowJobRunBackendId string `json:"workflowJobRunBackendId,omitempty"`
	Name                    string `json:"name,omitempty"`
}

type DeleteArtifactResponse struct {
	Ok         bool  `json:"ok,omitempty"`
	ArtifactId int64 `json:"artifactId,string,omitempty"`
}
