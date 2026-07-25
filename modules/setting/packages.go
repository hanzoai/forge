// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

import (
	"fmt"
	"math"
	"strings"

	"github.com/dustin/go-humanize"
)

// Package registry settings
var (
	Packages = struct {
		Storage *Storage
		Enabled bool

		// BaseURL is where package clients are told to fetch from. Package
		// metadata carries absolute URLs -- an npm tarball, a Cargo download,
		// a NuGet page -- and by default those name this server. Set it when
		// clients reach the registry through a different host than the web UI,
		// so the URLs handed out point at the host the client can actually
		// use. Expects the base that owner/ecosystem hangs off, e.g.
		// "https://api.example.com/v1/packages". Empty means "use my own URL".
		BaseURL string

		LimitTotalOwnerCount    int64
		LimitTotalOwnerSize     int64
		LimitSizeAlpine         int64
		LimitSizeArch           int64
		LimitSizeCargo          int64
		LimitSizeChef           int64
		LimitSizeComposer       int64
		LimitSizeConan          int64
		LimitSizeConda          int64
		LimitSizeContainer      int64
		LimitSizeCran           int64
		LimitSizeDebian         int64
		LimitSizeGeneric        int64
		LimitSizeGo             int64
		LimitSizeHelm           int64
		LimitSizeMaven          int64
		LimitSizeNpm            int64
		LimitSizeNuGet          int64
		LimitSizePub            int64
		LimitSizePyPI           int64
		LimitSizeRpm            int64
		LimitSizeRubyGems       int64
		LimitSizeSwift          int64
		LimitSizeTerraformState int64
		LimitSizeVagrant        int64

		DefaultRPMSignEnabled bool
	}{
		Enabled:              true,
		LimitTotalOwnerCount: -1,
	}
)

func loadPackagesFrom(rootCfg ConfigProvider) (err error) {
	sec, _ := rootCfg.GetSection("packages")
	if sec == nil {
		Packages.Storage, err = getStorage(rootCfg, "packages", "", nil)
		return err
	}

	if err = sec.MapTo(&Packages); err != nil {
		return fmt.Errorf("failed to map Packages settings: %v", err)
	}

	Packages.Storage, err = getStorage(rootCfg, "packages", "", sec)
	if err != nil {
		return err
	}

	Packages.LimitTotalOwnerSize = mustBytes(sec, "LIMIT_TOTAL_OWNER_SIZE")
	Packages.LimitSizeAlpine = mustBytes(sec, "LIMIT_SIZE_ALPINE")
	Packages.LimitSizeArch = mustBytes(sec, "LIMIT_SIZE_ARCH")
	Packages.LimitSizeCargo = mustBytes(sec, "LIMIT_SIZE_CARGO")
	Packages.LimitSizeChef = mustBytes(sec, "LIMIT_SIZE_CHEF")
	Packages.LimitSizeComposer = mustBytes(sec, "LIMIT_SIZE_COMPOSER")
	Packages.LimitSizeConan = mustBytes(sec, "LIMIT_SIZE_CONAN")
	Packages.LimitSizeConda = mustBytes(sec, "LIMIT_SIZE_CONDA")
	Packages.LimitSizeContainer = mustBytes(sec, "LIMIT_SIZE_CONTAINER")
	Packages.LimitSizeCran = mustBytes(sec, "LIMIT_SIZE_CRAN")
	Packages.LimitSizeDebian = mustBytes(sec, "LIMIT_SIZE_DEBIAN")
	Packages.LimitSizeGeneric = mustBytes(sec, "LIMIT_SIZE_GENERIC")
	Packages.LimitSizeGo = mustBytes(sec, "LIMIT_SIZE_GO")
	Packages.LimitSizeHelm = mustBytes(sec, "LIMIT_SIZE_HELM")
	Packages.LimitSizeMaven = mustBytes(sec, "LIMIT_SIZE_MAVEN")
	Packages.LimitSizeNpm = mustBytes(sec, "LIMIT_SIZE_NPM")
	Packages.LimitSizeNuGet = mustBytes(sec, "LIMIT_SIZE_NUGET")
	Packages.LimitSizePub = mustBytes(sec, "LIMIT_SIZE_PUB")
	Packages.LimitSizePyPI = mustBytes(sec, "LIMIT_SIZE_PYPI")
	Packages.LimitSizeRpm = mustBytes(sec, "LIMIT_SIZE_RPM")
	Packages.LimitSizeRubyGems = mustBytes(sec, "LIMIT_SIZE_RUBYGEMS")
	Packages.LimitSizeSwift = mustBytes(sec, "LIMIT_SIZE_SWIFT")
	Packages.LimitSizeTerraformState = mustBytes(sec, "LIMIT_SIZE_TERRAFORM_STATE")
	Packages.LimitSizeVagrant = mustBytes(sec, "LIMIT_SIZE_VAGRANT")
	Packages.DefaultRPMSignEnabled = sec.Key("DEFAULT_RPM_SIGN_ENABLED").MustBool(false)
	Packages.BaseURL = strings.TrimSuffix(sec.Key("BASE_URL").MustString(""), "/")
	return nil
}

// PackageRegistryURL is the base URL a client should use for owner's ecosystem
// registry. Every package handler builds its absolute URLs from this, so the
// host clients are pointed at is decided in exactly one place.
//
// Callers pass owner already escaped or lower-cased as their ecosystem
// requires; this only joins.
func PackageRegistryURL(owner, ecosystem string) string {
	if Packages.BaseURL != "" {
		return Packages.BaseURL + "/" + owner + "/" + ecosystem
	}
	return AppURL + "v1/packages/" + owner + "/" + ecosystem
}

func mustBytes(section ConfigSection, key string) int64 {
	const noLimit = "-1"

	value := section.Key(key).MustString(noLimit)
	if value == noLimit {
		return -1
	}
	bytes, err := humanize.ParseBytes(value)
	if err != nil || bytes > math.MaxInt64 {
		return -1
	}
	return int64(bytes)
}
