// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2014 Martini Authors
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

package binding

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

var (
	emptyStr     = ""
	urlStr       = "http://example.com/"
	alphaDashStr = "aB-12"
	idInt        = 1
)

var validationTestCases = []validationTestCase{
	{
		description: "No errors",
		data: BlogPost{
			Id: 1,
			Post: Post{
				Title:   "Behold The Title!",
				Content: "And some content",
			},
			Author: Person{
				Name: "Matt Holt",
			},
		},
		expectedErrors: Errors{},
	},
	{
		description: "ID required",
		data: BlogPost{
			Post: Post{
				Title:   "Behold The Title!",
				Content: "And some content",
			},
			Author: Person{
				Name: "Matt Holt",
			},
		},
		expectedErrors: Errors{
			Error{
				FieldNames:     []string{"id"},
				Classification: ERR_REQUIRED,
				Message:        "Required",
			},
		},
	},
	{
		description: "Embedded struct field required",
		data: BlogPost{
			Id: 1,
			Post: Post{
				Content: "Content given, but title is required",
			},
			Author: Person{
				Name: "Matt Holt",
			},
		},
		expectedErrors: Errors{
			Error{
				FieldNames:     []string{"title"},
				Classification: ERR_REQUIRED,
				Message:        "Required",
			},
			Error{
				FieldNames:     []string{"title"},
				Classification: "LengthError",
				Message:        "Life is too short",
			},
		},
	},
	{
		description: "Nested struct field required",
		data: BlogPost{
			Id: 1,
			Post: Post{
				Title:   "Behold The Title!",
				Content: "And some content",
			},
		},
		expectedErrors: Errors{
			Error{
				FieldNames:     []string{"name"},
				Classification: ERR_REQUIRED,
				Message:        "Required",
			},
		},
	},
	{
		description: "Required field missing in nested struct pointer",
		data: BlogPost{
			Id: 1,
			Post: Post{
				Title:   "Behold The Title!",
				Content: "And some content",
			},
			Author: Person{
				Name: "Matt Holt",
			},
			Coauthor: &Person{},
		},
		expectedErrors: Errors{
			Error{
				FieldNames:     []string{"name"},
				Classification: ERR_REQUIRED,
				Message:        "Required",
			},
		},
	},
	{
		description: "All required fields specified in nested struct pointer",
		data: BlogPost{
			Id: 1,
			Post: Post{
				Title:   "Behold The Title!",
				Content: "And some content",
			},
			Author: Person{
				Name: "Matt Holt",
			},
			Coauthor: &Person{
				Name: "Jeremy Saenz",
			},
		},
		expectedErrors: Errors{},
	},
	{
		description: "Custom validation should put an error",
		data: BlogPost{
			Id: 1,
			Post: Post{
				Title:   "Too short",
				Content: "And some content",
			},
			Author: Person{
				Name: "Matt Holt",
			},
		},
		expectedErrors: Errors{
			Error{
				FieldNames:     []string{"title"},
				Classification: "LengthError",
				Message:        "Life is too short",
			},
		},
	},
	{
		description: "List Validation",
		data: []BlogPost{
			{
				Id: 1,
				Post: Post{
					Title:   "First Post",
					Content: "And some content",
				},
				Author: Person{
					Name: "Leeor Aharon",
				},
			},
			{
				Id: 2,
				Post: Post{
					Title:   "Second Post",
					Content: "And some content",
				},
				Author: Person{
					Name: "Leeor Aharon",
				},
			},
		},
		expectedErrors: Errors{},
	},
	{
		description: "List Validation w/ Errors",
		data: []BlogPost{
			{
				Id: 1,
				Post: Post{
					Title:   "First Post",
					Content: "And some content",
				},
				Author: Person{
					Name: "Leeor Aharon",
				},
			},
			{
				Id: 2,
				Post: Post{
					Title:   "Too Short",
					Content: "And some content",
				},
				Author: Person{
					Name: "Leeor Aharon",
				},
			},
		},
		expectedErrors: Errors{
			Error{
				FieldNames:     []string{"title"},
				Classification: "LengthError",
				Message:        "Life is too short",
			},
		},
	},
	{
		description: "List of invalid custom validations",
		data: []SadForm{
			{
				AlphaDash:    ",",
				AlphaDashDot: ",",
				Size:         "123",
				SizeSlice:    []string{"1", "2", "3"},
				MinSize:      ",",
				MinSizeSlice: []string{",", ","},
				MaxSize:      ",,",
				MaxSizeSlice: []string{",", ","},
				Range:        3,
				Email:        ",",
				Url:          ",",
				UrlEmpty:     "",
				InInvalid:    "4",
				NotIn:        "1",
				Include:      "def",
				Exclude:      "abc",
			},
		},
		expectedErrors: Errors{
			Error{
				FieldNames:     []string{"AlphaDash"},
				Classification: "AlphaDashError",
				Message:        "AlphaDash",
			},
			Error{
				FieldNames:     []string{"AlphaDashDot"},
				Classification: "AlphaDashDot",
				Message:        "AlphaDashDot",
			},
			Error{
				FieldNames:     []string{"Size"},
				Classification: "Size",
				Message:        "Size",
			},
			Error{
				FieldNames:     []string{"Size"},
				Classification: "Size",
				Message:        "Size",
			},
			Error{
				FieldNames:     []string{"MinSize"},
				Classification: "MinSize",
				Message:        "MinSize",
			},
			Error{
				FieldNames:     []string{"MinSize"},
				Classification: "MinSize",
				Message:        "MinSize",
			},
			Error{
				FieldNames:     []string{"MaxSize"},
				Classification: "MaxSize",
				Message:        "MaxSize",
			},
			Error{
				FieldNames:     []string{"MaxSize"},
				Classification: "MaxSize",
				Message:        "MaxSize",
			},
			Error{
				FieldNames:     []string{"Range"},
				Classification: "Range",
				Message:        "Range",
			},
			Error{
				FieldNames:     []string{"Email"},
				Classification: "Email",
				Message:        "Email",
			},
			Error{
				FieldNames:     []string{"Url"},
				Classification: "Url",
				Message:        "Url",
			},
			Error{
				FieldNames:     []string{"Default"},
				Classification: "Default",
				Message:        "Default",
			},
			Error{
				FieldNames:     []string{"InInvalid"},
				Classification: "In",
				Message:        "In",
			},
			Error{
				FieldNames:     []string{"NotIn"},
				Classification: "NotIn",
				Message:        "NotIn",
			},
			Error{
				FieldNames:     []string{"Include"},
				Classification: "Include",
				Message:        "Include",
			},
			Error{
				FieldNames:     []string{"Exclude"},
				Classification: "Exclude",
				Message:        "Exclude",
			},
		},
	},
	{
		description: "List of valid custom validations",
		data: []SadForm{
			{
				AlphaDash:    "123-456",
				AlphaDashDot: "123.456",
				Size:         "1",
				SizeSlice:    []string{"1"},
				MinSize:      "12345",
				MinSizeSlice: []string{"1", "2", "3", "4", "5"},
				MaxSize:      "1",
				MaxSizeSlice: []string{"1"},
				Range:        2,
				In:           "1",
				InInvalid:    "1",
				Email:        "123@456.com",
				Url:          "http://123.456",
				Include:      "abc",
			},
		},
	},
	{
		description: "slice of structs Validation",
		data: Group{
			Name: "group1",
			People: []Person{
				{Name: "anthony"},
				{Name: "awoods"},
			},
		},
		expectedErrors: Errors{},
	},
	{
		description: "slice of structs Validation failer",
		data: Group{
			Name: "group1",
			People: []Person{
				{Name: "anthony"},
				{Name: ""},
			},
		},
		expectedErrors: Errors{
			Error{
				FieldNames:     []string{"name"},
				Classification: ERR_REQUIRED,
				Message:        "Required",
			},
		},
	},
	{
		description: "email fail",
		data: struct {
			EmailValid string `binding:"Email"`
			EmailFail  string `binding:"Email"`
			EmailFail2 string `binding:"Email"`
			EmailFail3 string `binding:"Email"`
		}{
			EmailValid: "123@asd.com",
			EmailFail:  "test 123@asd.com",
			EmailFail2: "123@asd.com test",
			EmailFail3: "test 123@asd.com test",
		},
		expectedErrors: Errors{
			Error{
				FieldNames:     []string{"EmailFail"},
				Classification: ERR_EMAIL,
				Message:        "Email",
			},
			Error{
				FieldNames:     []string{"EmailFail2"},
				Classification: ERR_EMAIL,
				Message:        "Email",
			},
			Error{
				FieldNames:     []string{"EmailFail3"},
				Classification: ERR_EMAIL,
				Message:        "Email",
			},
		},
	},
	{
		description:    "pointer form empty and nil",
		data:           PointerForm{},
		expectedErrors: Errors{},
	},
	{
		description: "pointer form empty",
		data: PointerForm{
			Url:              "",
			UrlPointer:       &emptyStr,
			AlphaDash:        "",
			AlphaDashPointer: &emptyStr,
		},
		expectedErrors: Errors{},
	},
	{
		description: "no errors with not required fields",
		data: []*struct {
			AlphaDash    string   `binding:"AlphaDash"`
			AlphaDashDot string   `binding:"AlphaDashDot"`
			Size         string   `binding:"Size(1)"`
			SizeSlice    []string `binding:"Size(1)"`
			MinSize      string   `binding:"MinSize(5)"`
			MinSizeSlice []string `binding:"MinSize(5)"`
			MaxSize      string   `binding:"MaxSize(1)"`
			MaxSizeSlice []string `binding:"MaxSize(1)"`
			Range        int      `binding:"Range(1,2)"`
			Email        string   `binding:"Email"`
			Url          string   `binding:"Url"`
			In           string   `binding:"Default(0);In(1,2,3)"`
			NotIn        string   `binding:"NotIn(1,2,3)"`
		}{
			{},
		},
		expectedErrors: Errors{},
	},
	{
		description: "pointer form with valid data",
		data: PointerForm{
			Url:              urlStr,
			UrlPointer:       &urlStr,
			AlphaDash:        alphaDashStr,
			AlphaDashPointer: &alphaDashStr,
		},
		expectedErrors: Errors{},
	},
	{
		description: "pointer form with invalid data",
		data: PointerForm{
			Url:              alphaDashStr,
			UrlPointer:       &alphaDashStr,
			AlphaDash:        urlStr,
			AlphaDashPointer: &urlStr,
		},
		expectedErrors: Errors{
			Error{
				FieldNames:     []string{"Url", "UrlPointer"},
				Classification: "Url",
				Message:        "Url",
			},
			Error{
				FieldNames:     []string{"UrlPointer"},
				Classification: "Url",
				Message:        "Url",
			},
			Error{
				FieldNames:     []string{"AlphaDash"},
				Classification: "AlphaDash",
				Message:        "AlphaDash",
			},
			Error{
				FieldNames:     []string{"AlphaDashPointer"},
				Classification: "AlphaDash",
				Message:        "AlphaDash",
			},
		},
	},
	{
		description: "errors with required fields",
		data: []*struct {
			AlphaDash    string   `binding:"Required;AlphaDash"`
			AlphaDashDot string   `binding:"Required;AlphaDashDot"`
			Size         string   `binding:"Required;Size(1)"`
			SizeSlice    []string `binding:"Required;Size(1)"`
			MinSize      string   `binding:"Required;MinSize(5)"`
			MinSizeSlice []string `binding:"Required;MinSize(5)"`
			MaxSize      string   `binding:"Required;MaxSize(1)"`
			MaxSizeSlice []string `binding:"Required;MaxSize(1)"`
			Range        int      `binding:"Required;Range(1,2)"`
			Email        string   `binding:"Required;Email"`
			Url          string   `binding:"Required;Url"`
			In           string   `binding:"Required;Default(0);In(1,2,3)"`
			NotIn        string   `binding:"Required;NotIn(1,2,3)"`
		}{
			{},
		},
		expectedErrors: Errors{
			Error{
				FieldNames:     []string{"AlphaDash"},
				Classification: "Required",
				Message:        "Required",
			},
			Error{
				FieldNames:     []string{"AlphaDashDot"},
				Classification: "Required",
				Message:        "Required",
			},
			Error{
				FieldNames:     []string{"Size"},
				Classification: "Required",
				Message:        "Required",
			},
			Error{
				FieldNames:     []string{"SizeSlice"},
				Classification: "Required",
				Message:        "Required",
			},
			Error{
				FieldNames:     []string{"MinSize"},
				Classification: "Required",
				Message:        "Required",
			},
			Error{
				FieldNames:     []string{"MinSizeSlice"},
				Classification: "Required",
				Message:        "Required",
			},
			Error{
				FieldNames:     []string{"MaxSize"},
				Classification: "Required",
				Message:        "Required",
			},
			Error{
				FieldNames:     []string{"MaxSizeSlice"},
				Classification: "Required",
				Message:        "Required",
			},
			Error{
				FieldNames:     []string{"Range"},
				Classification: "Required",
				Message:        "Required",
			},
			Error{
				FieldNames:     []string{"Email"},
				Classification: "Required",
				Message:        "Required",
			},
			Error{
				FieldNames:     []string{"Url"},
				Classification: "Required",
				Message:        "Required",
			},
			Error{
				FieldNames:     []string{"In"},
				Classification: "Required",
				Message:        "Required",
			},
			Error{
				FieldNames:     []string{"NotIn"},
				Classification: "Required",
				Message:        "Required",
			},
		},
	},
}

func Test_Validation(t *testing.T) {
	for _, testCase := range validationTestCases {
		performValidationTest(t, testCase)
	}
}

func performValidationTest(t *testing.T, testCase validationTestCase) {
	httpRecorder := httptest.NewRecorder()
	m := newTestRouter()

	m.Post(testRoute, func(resp http.ResponseWriter, req *http.Request) {
		actual := Validate(req, testCase.data)
		assert.EqualValues(t, fmt.Sprintf("%+v", testCase.expectedErrors), fmt.Sprintf("%+v", actual), testCase.description)
	})

	req, err := http.NewRequest("POST", testRoute, nil)
	if err != nil {
		panic(err)
	}

	m.ServeHTTP(httpRecorder, req)

	switch httpRecorder.Code {
	case http.StatusNotFound:
		panic("Routing is messed up in test fixture (got 404): check methods and paths")
	case http.StatusInternalServerError:
		panic("Something bad happened on '" + testCase.description + "'")
	}
}

type (
	validationTestCase struct {
		description    string
		data           interface{}
		expectedErrors Errors
	}
)
