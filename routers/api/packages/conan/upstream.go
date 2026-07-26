// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package conan

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	packages_model "github.com/hanzoai/git/models/packages"
	"github.com/hanzoai/git/modules/json"
	"github.com/hanzoai/git/modules/log"
	packages_module "github.com/hanzoai/git/modules/packages"
	conan_module "github.com/hanzoai/git/modules/packages/conan"
	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/services/context"
)

// Read-through caching for the public Conan (C++) ecosystem.
//
// Without this the registry serves ONLY what was published to it, so every CI
// run re-fetches the same recipes from ConanCenter. That is slow and it makes
// every build depend on someone else's uptime.
//
// With `[packages] CONAN_UPSTREAM` set, a miss fetches the recipe ONCE and
// stores it through `storeFile` -- the same writer the upload handler uses -- so
// there is exactly one storage shape and everything downstream (listing, quota,
// cleanup, backup) works on cached recipes for free.
//
// Fail-soft by construction: if upstream is disabled, unreachable or 404s, the
// handler falls back to the ordinary not-found it would have returned anyway.
//
// SCOPE, stated plainly. A Conan reference only names one immutable thing once
// it carries a RECIPE REVISION, and a recipe additionally has BINARY packages
// under their own revisions. This caches exactly one case:
//
//	recipe files of name/version@user/channel#recipe_revision
//
// Anything less specific -- `/latest`, the revision listing, the whole v1 API
// (which has no revision in its URLs and so arrives as the "0" sentinel) -- is
// refused, not guessed. Resolving a revision ourselves would pin whatever
// upstream happened to publish first into a name that promises never to change,
// and serving the wrong bits under an immutable name is worse than no cache.
// Binary packages are not cached at all; see cacheRecipeIfMissing.

const upstreamTimeout = 60 * time.Second

// upstreamEnabled reports whether read-through caching is configured. Empty
// means publish-only.
func upstreamEnabled() bool {
	return strings.TrimSpace(setting.Packages.ConanUpstream) != ""
}

// isPrivateRecipe reports whether a recipe name must NEVER be fetched upstream.
//
// This is what makes caching-by-default safe. A private recipe is not published
// publicly, so a local miss would otherwise send its name -- e.g. lux-crypto --
// to a public remote, which logs it. The name alone leaks an internal library
// that may not be public yet. For these, a miss stays a miss.
//
// Enforced server-side rather than trusting every client's remotes to be ordered
// correctly: one machine with a stale config would otherwise leak for everyone.
func isPrivateRecipe(name string) bool {
	for _, p := range strings.Split(setting.Packages.ConanPrivate, ",") {
		p = strings.TrimSpace(p)
		if p != "" && strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// hasConcreteRevision reports whether the reference names ONE immutable recipe
// revision.
//
// DefaultRevision ("0") is this registry's stand-in for "the caller did not say",
// which is how every v1 URL and every revision-less v2 URL arrives. It is a
// question, not an answer, so it can never be fetched.
func hasConcreteRevision(rref *conan_module.RecipeReference) bool {
	return rref.Revision != "" && rref.Revision != conan_module.DefaultRevision
}

// upstreamRecipePath builds the Conan v2 REST path of one recipe revision, or
// reports false when the reference cannot be expressed as literal path segments.
//
// The version field is the reason for the check: NewRecipeReference validates
// name, user, channel and revision against a pattern but accepts any non-empty
// version, so ".." would otherwise walk the upstream host's paths.
func upstreamRecipePath(rref *conan_module.RecipeReference) (string, bool) {
	user, channel := rref.User, rref.Channel
	// ConanCenter recipes have no user/channel; "_" is the placeholder the v2
	// API uses for both.
	if user == "" {
		user = "_"
	}
	if channel == "" {
		channel = "_"
	}

	segments := []string{"v2", "conans", rref.Name, rref.Version, user, channel, "revisions", rref.Revision}
	for _, s := range segments {
		if !safeSegment(s) {
			return "", false
		}
	}
	return strings.Join(segments, "/"), true
}

// safeSegment reports whether s stays one literal path segment upstream.
func safeSegment(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, "/\\?#%")
}

// upstreamFetch GETs one file from the configured remote. `elem` is the
// remote-relative path, e.g. "v2/conans/zlib/1.3.1/_/_/revisions/abc/files".
func upstreamFetch(elem string) (io.ReadCloser, int64, error) {
	base := strings.TrimRight(strings.TrimSpace(setting.Packages.ConanUpstream), "/")
	req, err := http.NewRequest(http.MethodGet, base+"/"+elem, nil)
	if err != nil {
		return nil, 0, err
	}
	client := &http.Client{Timeout: upstreamTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, 0, fmt.Errorf("upstream %s: %s", elem, resp.Status)
	}
	return resp.Body, resp.ContentLength, nil
}

// upstreamRecipeFileList reads the file names of one recipe revision. The v2 API
// answers {"files": {"conanfile.py": {}, ...}} -- the same shape this registry
// serves from ListRecipeRevisionFiles.
//
// Names the upload path would reject are dropped here rather than failing the
// whole mirror, so the cache can only ever hold files the registry accepts.
func upstreamRecipeFileList(elem string) ([]string, error) {
	body, _, err := upstreamFetch(elem + "/files")
	if err != nil {
		return nil, err
	}
	defer body.Close()

	var list struct {
		Files map[string]any `json:"files"`
	}
	if err := json.NewDecoder(io.LimitReader(body, 1<<20)).Decode(&list); err != nil {
		return nil, err
	}

	names := make([]string, 0, len(list.Files))
	for name := range list.Files {
		if recipeFileList.Contains(name) {
			names = append(names, name)
		}
	}
	slices.Sort(names) // map order is random; the set is tiny and callers want it stable
	return names, nil
}

// haveRecipeRevision reports whether this exact revision is already stored.
func haveRecipeRevision(ctx *context.Context, rref *conan_module.RecipeReference) bool {
	pv, err := packages_model.GetVersionByNameAndVersion(ctx, ctx.Package.Owner.ID, packages_model.TypeConan, rref.Name, rref.Version)
	if err != nil {
		return false
	}
	pfs, _, err := packages_model.SearchFiles(ctx, &packages_model.PackageFileSearchOptions{
		VersionID:    pv.ID,
		CompositeKey: rref.AsKey(),
	})
	return err == nil && len(pfs) > 0
}

// cacheRecipeFromUpstream mirrors ONE recipe revision: resolve its file set
// upstream, fetch every file this registry accepts, store them through the
// upload handler's writer. Mechanism only -- the caller decides whether fetching
// is allowed at all.
//
// Concurrency: two requests for the same missing revision race, and the loser
// either rewrites an identical blob or gets a duplicate error. Both are a
// SUCCESS, not a failure -- the revision is in the cache either way.
func cacheRecipeFromUpstream(ctx *context.Context, rref *conan_module.RecipeReference) error {
	elem, ok := upstreamRecipePath(rref)
	if !ok {
		return packages_model.ErrPackageNotExist
	}

	names, err := upstreamRecipeFileList(elem)
	if err != nil {
		return err
	}
	if !slices.Contains(names, conanfileFile) {
		// Without conanfile.py there is no recipe, only loose files. Storing
		// them would leave a husk under a name that promises to be complete.
		return packages_model.ErrPackageNotExist
	}

	// Fetch everything before storing anything, so a network failure part way
	// through leaves no partial revision behind.
	bufs := make(map[string]*packages_module.HashedBuffer, len(names))
	defer func() {
		for _, buf := range bufs {
			buf.Close()
		}
	}()
	for _, name := range names {
		body, _, err := upstreamFetch(elem + "/files/" + url.PathEscape(name))
		if err != nil {
			return err
		}
		buf, err := packages_module.CreateHashedBufferFromReader(body)
		body.Close()
		if err != nil {
			return err
		}
		bufs[name] = buf
	}

	// Creator: the requesting user when there is one, else the owner of the
	// registry being read. An anonymous read must still be able to warm the
	// cache -- a cache that only fills for logged-in callers would miss most CI.
	creator := ctx.Doer
	if creator == nil {
		creator = ctx.Package.Owner
	}

	for _, name := range names {
		err := storeFile(ctx, creator, rref, nil, rref.AsKey(), name, bufs[name])
		if err != nil && !errors.Is(err, packages_model.ErrDuplicatePackageFile) && !errors.Is(err, packages_model.ErrDuplicatePackageVersion) {
			return err
		}
	}
	return nil
}

// cacheRecipeIfMissing is the ONE seam the recipe read endpoints go through:
// serve what we have, otherwise fetch this exact revision once and serve that.
// Callers keep their existing not-found handling -- an upstream that is
// disabled, unreachable or 404s simply leaves the original miss.
//
// Recipes only. A binary package is selected by a settings hash the caller
// computed against ITS OWN dependency graph, so mirroring one on demand would
// need the recipe's options resolved first; that is a different job.
func cacheRecipeIfMissing(ctx *context.Context, rref *conan_module.RecipeReference) {
	// Cheap refusals first: with no upstream configured a read pays one string
	// check and never touches the database.
	if !upstreamEnabled() || !hasConcreteRevision(rref) || isPrivateRecipe(rref.Name) {
		return
	}
	if haveRecipeRevision(ctx, rref) {
		return
	}
	if err := cacheRecipeFromUpstream(ctx, rref); err != nil {
		log.Trace("Conan upstream cache did not fill %v: %v", rref, err)
	}
}
