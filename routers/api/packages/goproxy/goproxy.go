// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package goproxy

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	packages_model "github.com/hanzoai/git/models/packages"
	"github.com/hanzoai/git/modules/optional"
	packages_module "github.com/hanzoai/git/modules/packages"
	goproxy_module "github.com/hanzoai/git/modules/packages/goproxy"
	"github.com/hanzoai/git/modules/util"
	"github.com/hanzoai/git/routers/api/packages/helper"
	"github.com/hanzoai/git/services/context"
	packages_service "github.com/hanzoai/git/services/packages"
)

func apiError(ctx *context.Context, status int, obj any) {
	message := helper.ProcessErrorForUser(ctx, status, obj)
	ctx.PlainText(status, message)
}

func EnumeratePackageVersions(ctx *context.Context) {
	name := ctx.PathParam("name")
	pvs, err := packages_model.GetVersionsByPackageName(ctx, ctx.Package.Owner.ID, packages_model.TypeGo, name)
	if err != nil {
		apiError(ctx, http.StatusInternalServerError, err)
		return
	}

	sort.Slice(pvs, func(i, j int) bool {
		return pvs[i].CreatedUnix < pvs[j].CreatedUnix
	})

	// The union of what we hold and what upstream knows. Cached-only would make
	// `go get mod@latest` resolve to the newest version WE happen to have, which
	// is a silently wrong answer rather than a slow one. Ordering is left to the
	// client: the go command selects by semver itself, so sorting here would be
	// a second, weaker implementation of a decision already made correctly
	// elsewhere.
	seen := make(map[string]struct{}, len(pvs))
	versions := make([]string, 0, len(pvs))
	for _, pv := range pvs {
		if _, dup := seen[pv.Version]; !dup {
			seen[pv.Version] = struct{}{}
			versions = append(versions, pv.Version)
		}
	}
	// Fail-soft: an unreachable upstream degrades to the cached list, never to
	// an error — the same contract the rest of the read path keeps.
	if up, uerr := upstreamListVersions(name); uerr == nil {
		for _, v := range up {
			if _, dup := seen[v]; !dup {
				seen[v] = struct{}{}
				versions = append(versions, v)
			}
		}
	}

	if len(versions) == 0 {
		apiError(ctx, http.StatusNotFound, err)
		return
	}

	ctx.Resp.Header().Set("Content-Type", "text/plain;charset=utf-8")

	for _, v := range versions {
		fmt.Fprintln(ctx.Resp, v)
	}
}

func PackageVersionMetadata(ctx *context.Context) {
	// resolveOrCache: serve what we have, else fetch it once from the configured
	// upstream and serve that. With no upstream set this is exactly resolvePackage.
	pv, err := resolveOrCache(ctx, ctx.PathParam("name"), ctx.PathParam("version"))
	if err != nil {
		if errors.Is(err, util.ErrNotExist) {
			apiError(ctx, http.StatusNotFound, err)
		} else {
			apiError(ctx, http.StatusInternalServerError, err)
		}
		return
	}

	ctx.JSON(http.StatusOK, struct {
		Version string    `json:"Version"`
		Time    time.Time `json:"Time"`
	}{
		Version: pv.Version,
		Time:    pv.CreatedUnix.AsLocalTime(),
	})
}

func PackageVersionGoModContent(ctx *context.Context) {
	// resolveOrCache: serve what we have, else fetch it once from the configured
	// upstream and serve that. With no upstream set this is exactly resolvePackage.
	pv, err := resolveOrCache(ctx, ctx.PathParam("name"), ctx.PathParam("version"))
	if err != nil {
		if errors.Is(err, util.ErrNotExist) {
			apiError(ctx, http.StatusNotFound, err)
		} else {
			apiError(ctx, http.StatusInternalServerError, err)
		}
		return
	}

	pps, err := packages_model.GetPropertiesByName(ctx, packages_model.PropertyTypeVersion, pv.ID, goproxy_module.PropertyGoMod)
	if err != nil || len(pps) != 1 {
		apiError(ctx, http.StatusInternalServerError, err)
		return
	}

	ctx.PlainText(http.StatusOK, pps[0].Value)
}

func DownloadPackageFile(ctx *context.Context) {
	// resolveOrCache: serve what we have, else fetch it once from the configured
	// upstream and serve that. With no upstream set this is exactly resolvePackage.
	pv, err := resolveOrCache(ctx, ctx.PathParam("name"), ctx.PathParam("version"))
	if err != nil {
		if errors.Is(err, util.ErrNotExist) {
			apiError(ctx, http.StatusNotFound, err)
		} else {
			apiError(ctx, http.StatusInternalServerError, err)
		}
		return
	}

	pfs, err := packages_model.GetFilesByVersionID(ctx, pv.ID)
	if err != nil || len(pfs) != 1 {
		apiError(ctx, http.StatusInternalServerError, err)
		return
	}

	s, u, _, err := packages_service.OpenFileForDownload(ctx, pfs[0], ctx.Req.Method)
	if err != nil {
		if errors.Is(err, util.ErrNotExist) {
			apiError(ctx, http.StatusNotFound, err)
		} else {
			apiError(ctx, http.StatusInternalServerError, err)
		}
		return
	}

	helper.ServePackageFile(ctx, s, u, pfs[0])
}

func resolvePackage(ctx *context.Context, ownerID int64, name, version string) (*packages_model.PackageVersion, error) {
	var pv *packages_model.PackageVersion

	if version == "latest" {
		pvs, _, err := packages_model.SearchLatestVersions(ctx, &packages_model.PackageSearchOptions{
			OwnerID: ownerID,
			Type:    packages_model.TypeGo,
			Name: packages_model.SearchValue{
				Value:      name,
				ExactMatch: true,
			},
			IsInternal: optional.Some(false),
			Sort:       packages_model.SortCreatedDesc,
		})
		if err != nil {
			return nil, err
		}

		if len(pvs) != 1 {
			return nil, packages_model.ErrPackageNotExist
		}

		pv = pvs[0]
	} else {
		var err error
		pv, err = packages_model.GetVersionByNameAndVersion(ctx, ownerID, packages_model.TypeGo, name, version)
		if err != nil {
			return nil, err
		}
	}

	return pv, nil
}

func UploadPackage(ctx *context.Context) {
	upload, needToClose, err := ctx.UploadStream()
	if err != nil {
		apiError(ctx, http.StatusInternalServerError, err)
		return
	}
	if needToClose {
		defer upload.Close()
	}

	buf, err := packages_module.CreateHashedBufferFromReader(upload)
	if err != nil {
		apiError(ctx, http.StatusInternalServerError, err)
		return
	}
	defer buf.Close()

	pck, err := goproxy_module.ParsePackage(buf, buf.Size())
	if err != nil {
		if errors.Is(err, util.ErrInvalidArgument) {
			apiError(ctx, http.StatusBadRequest, err)
		} else {
			apiError(ctx, http.StatusInternalServerError, err)
		}
		return
	}

	if _, err := buf.Seek(0, io.SeekStart); err != nil {
		apiError(ctx, http.StatusInternalServerError, err)
		return
	}

	_, _, err = packages_service.CreatePackageAndAddFile(
		ctx,
		&packages_service.PackageCreationInfo{
			PackageInfo: packages_service.PackageInfo{
				Owner:       ctx.Package.Owner,
				PackageType: packages_model.TypeGo,
				Name:        pck.Name,
				Version:     pck.Version,
			},
			Creator: ctx.Doer,
			VersionProperties: map[string]string{
				goproxy_module.PropertyGoMod: pck.GoMod,
			},
		},
		&packages_service.PackageFileCreationInfo{
			PackageFileInfo: packages_service.PackageFileInfo{
				Filename: fmt.Sprintf("%v.zip", pck.Version),
			},
			Creator: ctx.Doer,
			Data:    buf,
			IsLead:  true,
		},
	)
	if err != nil {
		switch err {
		case packages_model.ErrDuplicatePackageVersion:
			apiError(ctx, http.StatusConflict, err)
		case packages_service.ErrQuotaTotalCount, packages_service.ErrQuotaTypeSize, packages_service.ErrQuotaTotalSize:
			apiError(ctx, http.StatusForbidden, err)
		default:
			apiError(ctx, http.StatusInternalServerError, err)
		}
		return
	}

	ctx.Status(http.StatusCreated)
}
