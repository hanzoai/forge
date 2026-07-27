// Copyright 2026 Hanzo AI, Inc. All rights reserved.
package util

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestParseLineConfig(t *testing.T) {
	Convey("test parse line config", t, func() {
		line := " key1 = value1 , key2=value2,key3= value3 "
		cfg := ParseLineConfig(line)
		So(cfg["key1"], ShouldEqual, "value1")
		So(cfg["key2"], ShouldEqual, "value2")
		So(cfg["key3"], ShouldEqual, "value3")
	})
}
