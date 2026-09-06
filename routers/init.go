// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2016 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package routers

import (
	"context"
	"net/http"
	"reflect"
	"runtime"

	"github.com/hanzoai/git/models"
	authmodel "github.com/hanzoai/git/models/auth"
	"github.com/hanzoai/git/modules/cache"
	"github.com/hanzoai/git/modules/eventsource"
	"github.com/hanzoai/git/modules/git"
	"github.com/hanzoai/git/modules/git/gitcmd"
	"github.com/hanzoai/git/modules/log"
	"github.com/hanzoai/git/modules/markup"
	"github.com/hanzoai/git/modules/markup/external"
	private_module "github.com/hanzoai/git/modules/private"
	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/modules/ssh"
	"github.com/hanzoai/git/modules/storage"
	"github.com/hanzoai/git/modules/svg"
	"github.com/hanzoai/git/modules/system"
	"github.com/hanzoai/git/modules/translation"
	"github.com/hanzoai/git/modules/util"
	"github.com/hanzoai/git/modules/web"
	"github.com/hanzoai/git/modules/web/routing"
	actions_router "github.com/hanzoai/git/routers/api/actions"
	packages_router "github.com/hanzoai/git/routers/api/packages"
	apiv1 "github.com/hanzoai/git/routers/api/v1"
	"github.com/hanzoai/git/routers/common"
	"github.com/hanzoai/git/routers/private"
	web_routers "github.com/hanzoai/git/routers/web"
	"github.com/hanzoai/git/routers/web/healthcheck"
	"github.com/hanzoai/git/routers/web/misc"
	actions_service "github.com/hanzoai/git/services/actions"
	asymkey_service "github.com/hanzoai/git/services/asymkey"
	"github.com/hanzoai/git/services/auth"
	"github.com/hanzoai/git/services/auth/source/oauth2"
	"github.com/hanzoai/git/services/automerge"
	"github.com/hanzoai/git/services/cron"
	feed_service "github.com/hanzoai/git/services/feed"
	indexer_service "github.com/hanzoai/git/services/indexer"
	"github.com/hanzoai/git/services/mailer"
	mailer_incoming "github.com/hanzoai/git/services/mailer/incoming"
	markup_service "github.com/hanzoai/git/services/markup"
	repo_migrations "github.com/hanzoai/git/services/migrations"
	mirror_service "github.com/hanzoai/git/services/mirror"
	"github.com/hanzoai/git/services/oauth2_provider"
	packages_spec "github.com/hanzoai/git/services/packages/pkgspec"
	pull_service "github.com/hanzoai/git/services/pull"
	release_service "github.com/hanzoai/git/services/release"
	repo_service "github.com/hanzoai/git/services/repository"
	"github.com/hanzoai/git/services/repository/archiver"
	"github.com/hanzoai/git/services/task"
	"github.com/hanzoai/git/services/uinotification"
	"github.com/hanzoai/git/services/webhook"

	"github.com/zap-proto/fiber/v3/middleware/adaptor"
	"github.com/zap-proto/zip"
)

func mustInit(fn func() error) {
	err := fn()
	if err != nil {
		ptr := reflect.ValueOf(fn).Pointer()
		fi := runtime.FuncForPC(ptr)
		log.Fatal("%s failed: %v", fi.Name(), err)
	}
}

func mustInitCtx(ctx context.Context, fn func(ctx context.Context) error) {
	err := fn(ctx)
	if err != nil {
		ptr := reflect.ValueOf(fn).Pointer()
		fi := runtime.FuncForPC(ptr)
		log.Fatal("%s(ctx) failed: %v", fi.Name(), err)
	}
}

func syncAppConfForGit(ctx context.Context) error {
	runtimeState := new(system.RuntimeState)
	if err := system.AppState.Get(ctx, runtimeState); err != nil {
		return err
	}

	updated := false
	if runtimeState.LastAppPath != setting.AppPath {
		log.Info("AppPath changed from '%s' to '%s'", runtimeState.LastAppPath, setting.AppPath)
		runtimeState.LastAppPath = setting.AppPath
		updated = true
	}
	if runtimeState.LastCustomConf != setting.CustomConf {
		log.Info("CustomConf changed from '%s' to '%s'", runtimeState.LastCustomConf, setting.CustomConf)
		runtimeState.LastCustomConf = setting.CustomConf
		updated = true
	}

	if updated {
		log.Info("re-sync repository hooks ...")
		mustInitCtx(ctx, repo_service.SyncRepositoryHooks)

		log.Info("re-write ssh public keys ...")
		mustInitCtx(ctx, asymkey_service.RewriteAllPublicKeys)

		return system.AppState.Set(ctx, runtimeState)
	}
	return nil
}

func InitWebInstallPage(ctx context.Context) {
	translation.InitLocales(ctx)
	setting.LoadSettingsForInstall()
	mustInit(svg.Init)
}

// InitWebInstalled is for the global configuration of an installed instance
func InitWebInstalled(ctx context.Context) {
	mustInit(git.InitFull)
	log.Info("Git version: %s (home: %s)", git.DefaultFeatures().VersionInfo(), gitcmd.HomeDir())
	if !git.DefaultFeatures().SupportHashSha256 {
		log.Warn("sha256 hash support is disabled - requires Git >= 2.42." + util.Iif(git.DefaultFeatures().UsingGogit, " Gogit is currently unsupported.", ""))
	}

	// Setup i18n
	translation.InitLocales(ctx)

	setting.LoadSettings()
	mustInit(storage.Init)

	mailer.NewContext(ctx)
	mustInit(cache.Init)
	mustInit(feed_service.Init)
	mustInit(uinotification.Init)
	mustInitCtx(ctx, archiver.Init)

	external.RegisterRenderers()
	markup.Init(markup_service.FormalRenderHelperFuncs())

	mustInitCtx(ctx, common.InitDBEngine)
	log.Info("ORM engine initialization successful!")
	mustInit(system.Init)
	mustInitCtx(ctx, oauth2.Init)
	mustInitCtx(ctx, oauth2_provider.Init)
	mustInit(release_service.Init)

	mustInitCtx(ctx, models.Init)
	mustInitCtx(ctx, authmodel.Init)
	mustInitCtx(ctx, repo_service.Init)
	mustInit(packages_spec.InitManager)

	// Booting long running goroutines.
	mustInit(indexer_service.Init)

	mirror_service.InitSyncMirrors()
	mustInit(webhook.Init)
	mustInit(pull_service.Init)
	mustInit(automerge.Init)
	mustInit(task.Init)
	mustInit(repo_migrations.Init)
	eventsource.GetManager().Init()
	mustInitCtx(ctx, mailer_incoming.Init)

	mustInitCtx(ctx, syncAppConfForGit)

	mustInit(ssh.Init)

	auth.Init()
	mustInit(svg.Init)

	mustInitCtx(ctx, actions_service.Init)

	mustInit(repo_service.InitLicenseClassifier)

	// Finally start up the cron
	cron.Init(ctx)
}

// artifactRouteBase is what the runner passes to a job as ACTIONS_RUNTIME_URL.
const artifactRouteBase = "/v1/artifact"

// NormalRoutes represents non install routes
func NormalRoutes() *web.Router {
	r := web.NewRouter()
	r.BeforeRouting(common.ProtocolMiddlewares()...)

	r.AfterRouting(common.MaintenanceModeHandler())

	// The whole top-level URL namespace is allocated here and nowhere else:
	// "/" is the browser, "/v1" is every machine surface, "/v2" is the OCI spec,
	// "/.well-known/zip/op/" is ZAP's call plane and "/twirp" is what
	// third-party artifact JavaScript insists on (see the actions mounts below).
	// chi routes the most specific mount first, so a "/v1/…" route registered on
	// the web router below would be swallowed by the "/v1" mount — /v1/sync and
	// /v1/healthz therefore live here, not in routers/web.
	r.Mount("/", web_routers.Routes())
	r.Mount("/v1", apiv1.Routes())
	r.Mount(private_module.RoutePrefix, private.Routes())

	// Machine endpoints that authenticate themselves and must skip the session
	// and API-token middleware: sync's credential is the payload HMAC, and a
	// health probe must not touch auth or the database.
	r.Post("/v1/sync", misc.Sync)
	r.Get("/v1/healthz", healthcheck.Check)

	r.Post("/-/fetch-redirect", common.FetchRedirectDelegate)

	if setting.Packages.Enabled {
		// This implements package support for most package managers
		r.Mount("/v1/packages", packages_router.CommonRoutes())
		// This implements the OCI API, this container registry "/v2" endpoint must be in the root of the site.
		// If site admin deploys Gitea in a sub-path, they must configure their reverse proxy to map the "https://host/v2" endpoint to Gitea.
		r.Mount("/v2", packages_router.ContainerRoutes())
	}

	if setting.Actions.Enabled {
		// The runner protocol: five typed zip operations, addressed as
		// POST /v1/runner/<name> over HTTP and as post_runner_<name> on ZAP's own
		// call plane. Both faces are the same app on the same listener, so this
		// needs no second port and no change at the edge.
		//
		// Registered with Post rather than Mount because Mount is chi's
		// prefix-stripping form: the operations declare absolute paths, and an app
		// that receives "/register" where it declared "/v1/runner/register"
		// answers nothing. Post reaches chi's Method, which leaves URL.Path whole.
		//
		// It sits beside the "/v1" mount rather than inside it because a runner
		// carries its own credential and must not meet the session and API-token
		// middleware.
		runnerOps := adaptor.FiberApp(actions_router.RunnerOps().Fiber())
		r.Post(actions_router.RunnerRouteBase+"/*", runnerOps)
		r.Post(zip.CallPath+"*", runnerOps)

		// Artifact upload and download for actions/upload-artifact@v3 and its
		// download counterpart. The runner hands a job this whole base as
		// ACTIONS_RUNTIME_URL and their JavaScript concatenates
		// "_apis/pipelines/…" onto it, so the base is ours to place and the suffix
		// is theirs.
		r.Mount(artifactRouteBase, actions_router.ArtifactsRoutes(artifactRouteBase))
		// The v4 artifact protocol keeps the root: @actions/artifact v2 reduces
		// ACTIONS_RESULTS_URL to its origin, so this path is not ours to place.
		r.Mount(actions_router.ArtifactV4RouteBase, actions_router.ArtifactsV4Routes(actions_router.ArtifactV4RouteBase))
	}

	r.NotFound(func(w http.ResponseWriter, req *http.Request) {
		defer routing.RecordFuncInfo(req.Context(), routing.GetFuncInfo(http.NotFound, "GlobalNotFound"))()
		http.NotFound(w, req)
	})
	return r
}
