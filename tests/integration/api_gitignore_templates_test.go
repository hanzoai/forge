// Copyright 2023 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package integration

import (
	"net/http"
	"testing"

	"github.com/hanzoai/git/modules/options"
	repo_module "github.com/hanzoai/git/modules/repository"
	api "github.com/hanzoai/git/modules/structs"
	"github.com/hanzoai/git/tests"

	"github.com/stretchr/testify/assert"
)

func TestAPIListGitignoresTemplates(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	req := NewRequest(t, "GET", "/api/v1/gitignore/templates")
	resp := MakeRequest(t, req, http.StatusOK)

	templateList := DecodeJSON(t, resp, []string{}) // this is a very long list
	assert.Contains(t, templateList, "C++")
	assert.Contains(t, templateList, "Go")
}

func TestAPIGetGitignoreTemplateInfo(t *testing.T) {
	defer tests.PrepareTestEnv(t)()

	// If Gitea has for some reason no Gitignore templates, we need to skip this test
	if len(repo_module.Gitignores) == 0 {
		return
	}

	// Use the first template for the test
	templateName := repo_module.Gitignores[0]

	urlStr := "/api/v1/gitignore/templates/" + templateName
	req := NewRequest(t, "GET", urlStr)
	resp := MakeRequest(t, req, http.StatusOK)

	templateInfo := DecodeJSON(t, resp, &api.GitignoreTemplateInfo{})

	// We get the text of the template here
	text, _ := options.Gitignore(templateName)

	assert.Equal(t, templateInfo.Name, templateName)
	assert.Equal(t, templateInfo.Source, string(text))
}
