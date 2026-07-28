// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2014 The Macaron Authors
//
// Licensed under the Apache License, Version 2.0 (the "License"): you may
// not use this file except in compliance with the License. You may obtain
// a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
// WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
// License for the specific language governing permissions and limitations
// under the License.

package captcha

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/git/modules/cache/chi"
	. "github.com/smartystreets/goconvey/convey"
)

func Test_Captcha(t *testing.T) {
	Convey("Captch service", t, func() {
		c, err := cache.NewCacher(cache.Options{})
		So(err, ShouldBeNil)

		r := newTestRouter()
		captcha := NewCaptcha(Options{})
		captcha.Store = c
		r.Use(Captchaer(captcha))

		r.Get("/", func(w http.ResponseWriter, req *http.Request) {

		})

		resp := httptest.NewRecorder()
		req, err := http.NewRequest("GET", "/", nil)
		So(err, ShouldBeNil)
		r.ServeHTTP(resp, req)
	})
}
