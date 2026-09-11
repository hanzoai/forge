// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package rpm

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParsePackage(t *testing.T) {
	base64RpmPackageContent := `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
5vDmvppPzv30YP5DCFWtXPUXwl5dQcwbAAA=`
	rpmPackageContent, err := base64.StdEncoding.DecodeString(base64RpmPackageContent)
	assert.NoError(t, err)

	zr, err := gzip.NewReader(bytes.NewReader(rpmPackageContent))
	assert.NoError(t, err)

	p, err := ParsePackage(zr)
	assert.NotNil(t, p)
	assert.NoError(t, err)

	assert.Equal(t, "forge-test", p.Name)
	assert.Equal(t, "1.0.2-1", p.Version)
	assert.NotNil(t, p.VersionMetadata)
	assert.NotNil(t, p.FileMetadata)

	assert.Equal(t, "MIT", p.VersionMetadata.License)
	assert.Equal(t, "https://ex.test", p.VersionMetadata.ProjectURL)
	assert.Equal(t, "RPM package summary", p.VersionMetadata.Summary)
	assert.Equal(t, "RPM package description", p.VersionMetadata.Description)

	assert.Equal(t, "x86_64", p.FileMetadata.Architecture)
	assert.Equal(t, "0", p.FileMetadata.Epoch)
	assert.Equal(t, "1.0.2", p.FileMetadata.Version)
	assert.Equal(t, "1", p.FileMetadata.Release)
	assert.Empty(t, p.FileMetadata.Vendor)
	assert.Equal(t, "KN4CK3R", p.FileMetadata.Packager)
	assert.Equal(t, "forge-test-1.0.2-1.src.rpm", p.FileMetadata.SourceRpm)
	assert.Equal(t, "e44b1687d04b", p.FileMetadata.BuildHost)
	assert.EqualValues(t, 1678225964, p.FileMetadata.BuildTime)
	assert.EqualValues(t, 1678225964, p.FileMetadata.FileTime)
	assert.EqualValues(t, 13, p.FileMetadata.InstalledSize)
	assert.EqualValues(t, 272, p.FileMetadata.ArchiveSize)
	assert.Empty(t, p.FileMetadata.Conflicts)
	assert.Empty(t, p.FileMetadata.Obsoletes)

	assert.ElementsMatch(
		t,
		[]*Entry{
			{
				Name:    "forge-test",
				Flags:   "EQ",
				Version: "1.0.2",
				Epoch:   "0",
				Release: "1",
			},
			{
				Name:    "forge-test(x86-64)",
				Flags:   "EQ",
				Version: "1.0.2",
				Epoch:   "0",
				Release: "1",
			},
		},
		p.FileMetadata.Provides,
	)
	assert.ElementsMatch(
		t,
		[]*Entry{
			{
				Name: "/bin/sh",
			},
			{
				Name: "/bin/sh",
			},
			{
				Name: "/bin/sh",
			},
			{
				Name:    "rpmlib(CompressedFileNames)",
				Flags:   "LE",
				Version: "3.0.4",
				Epoch:   "0",
				Release: "1",
			},
			{
				Name:    "rpmlib(FileDigests)",
				Flags:   "LE",
				Version: "4.6.0",
				Epoch:   "0",
				Release: "1",
			},
			{
				Name:    "rpmlib(PayloadFilesHavePrefix)",
				Flags:   "LE",
				Version: "4.0",
				Epoch:   "0",
				Release: "1",
			},
			{
				Name:    "rpmlib(PayloadIsXz)",
				Flags:   "LE",
				Version: "5.2",
				Epoch:   "0",
				Release: "1",
			},
		},
		p.FileMetadata.Requires,
	)
	assert.ElementsMatch(
		t,
		[]*File{
			{
				Path:         "/usr/local/bin/hello",
				IsExecutable: true,
			},
		},
		p.FileMetadata.Files,
	)
	assert.ElementsMatch(
		t,
		[]*Changelog{
			{
				Author: "KN4CK3R <dummy@ex.test>",
				Date:   1678276800,
				Text:   "- Changelog message.",
			},
		},
		p.FileMetadata.Changelogs,
	)
}
