// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package cargo

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	packages_model "github.com/hanzoai/git/models/packages"

	"github.com/hanzoai/git/modules/json"
	"github.com/hanzoai/git/modules/log"
	packages_module "github.com/hanzoai/git/modules/packages"
	cargo_module "github.com/hanzoai/git/modules/packages/cargo"
	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/services/context"
	packages_service "github.com/hanzoai/git/services/packages"
	cargo_service "github.com/hanzoai/git/services/packages/cargo"
)

// Read-through caching for the public Rust crate ecosystem.
//
// Without this the registry serves ONLY what was published to it, so every CI
// run and every developer re-fetches the same crates from crates.io. That is
// slow, it makes every build depend on someone else's uptime, and a crate
// yanked upstream silently changes what we build.
//
// With `[packages] CARGO_UPSTREAM` set, a miss fetches the crate ONCE and
// stores it as an ordinary package version — the same rows, files and quota the
// publish path creates, so there is exactly one storage shape and cached crates
// are not a special case for listing, quota, cleanup, backup or the index we
// serve.
//
// Fail-soft by construction: if upstream is disabled, unreachable, slow or
// 404s, the handler falls back to the ordinary not-found it would have returned
// anyway. A cache that is down degrades to the behaviour of having no cache —
// it never turns a working registry into a broken one.
//
// Where this differs from the Go proxy: proxy.golang.org serves exactly what
// the publish path parses, crates.io does not. The cargo PUBLISH wire format is
// a length-prefixed metadata JSON followed by the .crate bytes; crates.io
// serves a web API instead. So the fetched pieces are re-assembled INTO that
// wire format and handed to the publish parser — see cacheFromUpstream.

const upstreamTimeout = 60 * time.Second

// upstreamEnabled reports whether read-through caching is configured. Empty
// means publish-only.
func upstreamEnabled() bool {
	return strings.TrimSpace(setting.Packages.CargoUpstream) != ""
}

// normalizeCrateName applies the crate-name equivalence crates.io itself uses —
// case and `-`/`_` do not distinguish two crates — so `Hanzo_Secret` cannot walk
// past a `hanzo-` guard.
func normalizeCrateName(name string) string {
	return strings.ReplaceAll(strings.ToLower(name), "_", "-")
}

// isPrivateCrate reports whether a crate name must NEVER be fetched upstream.
//
// This is what makes caching-by-default safe. A private crate is not published
// publicly, so a local miss would otherwise send its name to crates.io, which
// logs it. The name alone leaks a library that may not be public yet. For
// these, a miss stays a miss.
//
// Enforced server-side rather than trusting every client to order its registry
// sources correctly: one machine with a stale config would otherwise leak for
// everyone.
func isPrivateCrate(name string) bool {
	name = normalizeCrateName(name)
	for _, p := range strings.Split(setting.Packages.CargoPrivate, ",") {
		if p = normalizeCrateName(strings.TrimSpace(p)); p != "" && strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// upstreamGet GETs one path from the configured registry. `elem` is relative to
// it, e.g. "api/v1/crates/rand/0.8.5/download".
//
// A User-Agent is mandatory, not politeness: crates.io answers 403 to requests
// that do not identify themselves. The download endpoint answers a redirect to
// the CDN, which the client follows — so the CDN host stays an implementation
// detail of whatever upstream is configured, rather than a second hardcoded
// hostname that an internal mirror could not override.
func upstreamGet(elem string) (io.ReadCloser, error) {
	base := strings.TrimRight(strings.TrimSpace(setting.Packages.CargoUpstream), "/")
	req, err := http.NewRequest(http.MethodGet, base+"/"+elem, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "hanzo-git/"+setting.AppVer)
	client := &http.Client{Timeout: upstreamTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("upstream %s: %s", elem, resp.Status)
	}
	return resp.Body, nil
}

// upstreamJSON GETs and decodes one API document.
func upstreamJSON(elem string, v any) error {
	body, err := upstreamGet(elem)
	if err != nil {
		return err
	}
	defer body.Close()

	return json.NewDecoder(body).Decode(v)
}

// publishMetadata is the cargo publish metadata document — the JSON half of the
// wire format `cargo publish` sends. Field names are the PUBLISH format's, not
// the API's, because this is fed straight back into the publish parser.
//
// https://doc.rust-lang.org/cargo/reference/registry-web-api.html#publish
type publishMetadata struct {
	Name          string              `json:"name"`
	Vers          string              `json:"vers"`
	Deps          []publishDependency `json:"deps"`
	Features      map[string][]string `json:"features"`
	Description   string              `json:"description"`
	Documentation string              `json:"documentation"`
	Homepage      string              `json:"homepage"`
	License       string              `json:"license"`
	Repository    string              `json:"repository"`
	Links         string              `json:"links"`
}

type publishDependency struct {
	Name            string   `json:"name"`
	VersionReq      string   `json:"version_req"`
	Features        []string `json:"features"`
	Optional        bool     `json:"optional"`
	DefaultFeatures bool     `json:"default_features"`
	Target          *string  `json:"target"`
	Kind            string   `json:"kind"`
}

// upstreamCrate is everything the upstream API knows about one crate version:
// the publish document to re-parse, plus the two facts that live outside it.
type upstreamCrate struct {
	meta     *publishMetadata
	checksum string // SHA256 of the .crate, as published upstream
	yanked   bool
}

// fetchUpstreamMetadata asks the registry about one crate version and renders
// the answer back into the publish document.
//
// It takes two requests because the API splits them, and the field names are
// its own (`req`, `crate_id`) rather than the publish format's (`version_req`,
// `name`). Dependencies are fetched rather than left empty: an index entry that
// claims a crate has no dependencies is a WRONG answer, which is worse than the
// missing one we would otherwise have served.
func fetchUpstreamMetadata(name, version string) (*upstreamCrate, error) {
	base := "api/v1/crates/" + url.PathEscape(name) + "/" + url.PathEscape(version)

	var vr struct {
		Version struct {
			Crate         string              `json:"crate"`
			Num           string              `json:"num"`
			Features      map[string][]string `json:"features"`
			Yanked        bool                `json:"yanked"`
			Checksum      string              `json:"checksum"`
			License       string              `json:"license"`
			Description   string              `json:"description"`
			Documentation string              `json:"documentation"`
			Homepage      string              `json:"homepage"`
			Repository    string              `json:"repository"`
			LibLinks      string              `json:"lib_links"` // the manifest `links` key, not the API's link map
		} `json:"version"`
	}
	if err := upstreamJSON(base, &vr); err != nil {
		return nil, err
	}

	var dr struct {
		Dependencies []struct {
			CrateID         string   `json:"crate_id"`
			Req             string   `json:"req"`
			Features        []string `json:"features"`
			Optional        bool     `json:"optional"`
			DefaultFeatures bool     `json:"default_features"`
			Target          *string  `json:"target"`
			Kind            string   `json:"kind"`
		} `json:"dependencies"`
	}
	if err := upstreamJSON(base+"/dependencies", &dr); err != nil {
		return nil, err
	}

	deps := make([]publishDependency, 0, len(dr.Dependencies))
	for _, d := range dr.Dependencies {
		// The API exposes neither the `package = "..."` rename nor a per-dep
		// alternate registry, so both are dropped and a renamed dependency is
		// recorded under its real crate name. The cached .crate is exact; only
		// the index entry we serve for it can differ from the upstream one.
		deps = append(deps, publishDependency{
			Name:            d.CrateID,
			VersionReq:      d.Req,
			Features:        d.Features,
			Optional:        d.Optional,
			DefaultFeatures: d.DefaultFeatures,
			Target:          d.Target,
			Kind:            d.Kind,
		})
	}

	v := vr.Version
	return &upstreamCrate{
		meta: &publishMetadata{
			Name:          v.Crate,
			Vers:          v.Num,
			Deps:          deps,
			Features:      v.Features,
			Description:   v.Description,
			Documentation: v.Documentation,
			Homepage:      v.Homepage,
			License:       v.License,
			Repository:    v.Repository,
			Links:         v.LibLinks,
		},
		checksum: v.Checksum,
		yanked:   v.Yanked,
	}, nil
}

// wireHeader renders the framing of the cargo publish wire format: a
// little-endian uint32 length, the metadata JSON, then the length of the
// .crate bytes that follow.
func wireHeader(meta []byte, contentSize int64) []byte {
	b := binary.LittleEndian.AppendUint32(make([]byte, 0, 8+len(meta)), uint32(len(meta)))
	b = append(b, meta...)
	return binary.LittleEndian.AppendUint32(b, uint32(contentSize))
}

// cacheFromUpstream fetches one crate@version and stores it exactly as
// UploadPackage would.
//
// Concurrency: two requests for the same missing crate race, and the loser gets
// ErrDuplicatePackageVersion. That is a SUCCESS, not a failure — the crate is
// in the cache either way — so it returns nil.
func cacheFromUpstream(ctx *context.Context, name, version string) error {
	if !upstreamEnabled() {
		return packages_model.ErrPackageNotExist
	}
	// Only a concrete crate@version is a fetchable artifact.
	if name == "" || version == "" {
		return packages_model.ErrPackageNotExist
	}
	// Never hand a private crate name to a public registry.
	if isPrivateCrate(name) {
		return packages_model.ErrPackageNotExist
	}

	uc, err := fetchUpstreamMetadata(name, version)
	if err != nil {
		return err
	}

	body, err := upstreamGet("api/v1/crates/" + url.PathEscape(name) + "/" + url.PathEscape(version) + "/download")
	if err != nil {
		return err
	}
	defer body.Close()

	buf, err := packages_module.CreateHashedBufferFromReader(body)
	if err != nil {
		return err
	}
	defer buf.Close()

	// The registry publishes a SHA256 for every crate file and cargo verifies it
	// against the index; check it here too, so a cache fill cannot be the place
	// a tampered crate enters. An upstream that will not say what the bytes
	// should hash to does not get cached.
	_, _, sha256, _ := buf.Sums()
	if !strings.EqualFold(uc.checksum, hex.EncodeToString(sha256)) {
		return fmt.Errorf("upstream checksum mismatch for %s@%s", name, version)
	}

	meta, err := json.Marshal(uc.meta)
	if err != nil {
		return err
	}

	// Synthesise the wire format and hand it to the SAME parser the publish path
	// uses, instead of building a cargo_module.Package here. Two builders of one
	// struct is two places to drift, and the parser owns the name, semver and
	// URL validation — so the cache can never hold something upload would
	// refuse. The framing costs ~10 lines; a second parser would cost the whole
	// of parsePackage.
	cp, err := cargo_module.ParsePackage(io.MultiReader(bytes.NewReader(wireHeader(meta, buf.Size())), buf))
	if err != nil {
		return err
	}

	// cp.Content is a view of buf, which already holds the crate and its hashes,
	// so buf itself is what gets stored — rewound because the parser read the
	// framing from in front of it.
	if _, err := buf.Seek(0, io.SeekStart); err != nil {
		return err
	}

	// Creator: the requesting user when there is one, else the owner of the
	// registry being read. An anonymous read must still be able to warm the
	// cache — a cache that only fills for logged-in callers would miss most CI.
	creator := ctx.Doer
	if creator == nil {
		creator = ctx.Package.Owner
	}

	pv, _, err := packages_service.CreatePackageAndAddFile(
		ctx,
		&packages_service.PackageCreationInfo{
			PackageInfo: packages_service.PackageInfo{
				Owner:       ctx.Package.Owner,
				PackageType: packages_model.TypeCargo,
				Name:        cp.Name,
				Version:     cp.Version,
			},
			SemverCompatible: true,
			Creator:          creator,
			Metadata:         cp.Metadata,
			VersionProperties: map[string]string{
				// Upstream's yank state, not a hardcoded false: a lockfile
				// pinned to a yanked version must still resolve, and our index
				// must keep telling clients what crates.io tells them.
				cargo_module.PropertyYanked: strconv.FormatBool(uc.yanked),
			},
		},
		&packages_service.PackageFileCreationInfo{
			PackageFileInfo: packages_service.PackageFileInfo{
				Filename: crateFilename(cp.Name, cp.Version),
			},
			Creator: creator,
			Data:    buf,
			IsLead:  true,
		},
	)
	if err != nil {
		if errors.Is(err, packages_model.ErrDuplicatePackageVersion) {
			return nil
		}
		return err
	}

	// Keep the git-protocol index in step, as publishing does. Best effort, and
	// deliberately without UploadPackage's rollback: the crate is stored and
	// servable, and the HTTP sparse index is built from the database anyway, so
	// a failed index commit must not turn a warm cache back into a 404.
	if err := cargo_service.UpdatePackageIndexIfExists(ctx, creator, ctx.Package.Owner, pv.PackageID); err != nil {
		log.Error("Update Cargo index for cached %s@%s: %v", cp.Name, cp.Version, err)
	}

	return nil
}

// crateFilename is the ONE name a stored .crate has, so the publish path and
// the cache cannot disagree about what to look up.
func crateFilename(name, version string) string {
	return strings.ToLower(fmt.Sprintf("%s-%s.crate", name, version))
}

func openLocalCrate(ctx *context.Context, name, version string) (io.ReadSeekCloser, *url.URL, *packages_model.PackageFile, error) {
	return packages_service.OpenFileForDownloadByPackageNameAndVersion(
		ctx,
		&packages_service.PackageInfo{
			Owner:       ctx.Package.Owner,
			PackageType: packages_model.TypeCargo,
			Name:        name,
			Version:     version,
		},
		&packages_service.PackageFileInfo{
			Filename: crateFilename(name, version),
		},
		ctx.Req.Method,
	)
}

// openOrCache is the ONE seam the download endpoint goes through: serve what we
// have, otherwise fetch it once and serve that. The caller keeps its existing
// not-found handling — an upstream that is disabled, unreachable or 404s simply
// yields the original miss.
func openOrCache(ctx *context.Context, name, version string) (io.ReadSeekCloser, *url.URL, *packages_model.PackageFile, error) {
	s, u, pf, err := openLocalCrate(ctx, name, version)
	if err == nil {
		return s, u, pf, nil
	}
	if !errors.Is(err, packages_model.ErrPackageNotExist) && !errors.Is(err, packages_model.ErrPackageFileNotExist) {
		return nil, nil, nil, err
	}
	if cerr := cacheFromUpstream(ctx, name, version); cerr != nil {
		return nil, nil, nil, err // the original miss; upstream's problem is not the caller's
	}
	return openLocalCrate(ctx, name, version)
}
