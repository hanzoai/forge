// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package web

import (
	"github.com/hanzoai/git/modules/web"
	"github.com/hanzoai/git/routers/web/repo"
)

func addOwnerRepoGitHTTPRouters(m *web.Router, middlewares ...any) {
	m.Group("/{username}/{reponame}", func() {
		m.Methods("POST,OPTIONS", "/git-upload-pack", repo.ServiceUploadPack)
		m.Methods("POST,OPTIONS", "/git-receive-pack", repo.ServiceReceivePack)
		m.Methods("POST,OPTIONS", "/git-upload-archive", repo.ServiceUploadArchive)
		m.Methods("GET,OPTIONS", "/info/refs", repo.GetInfoRefs)
		for _, dumb := range []string{
			"/HEAD",
			"/objects/info/alternates",
			"/objects/info/http-alternates",
			"/objects/info/packs",
			"/objects/info/{file:[^/]*}",
			"/objects/{head:[0-9a-f]{2}}/{hash:[0-9a-f]{38,62}}",
			"/objects/pack/pack-{file:[0-9a-f]{40,64}}.pack",
			"/objects/pack/pack-{file:[0-9a-f]{40,64}}.idx",
		} {
			m.Methods("GET,OPTIONS", dumb, repo.RefuseDumbHTTP)
		}
	}, middlewares...)
}
