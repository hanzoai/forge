// Copyright 2026 Hanzo AI, Inc. All rights reserved.
package util

import "strings"

func ParseLineConfig(line string) map[string]string {
	ret := make(map[string]string)
	for _, it := range strings.Split(line, ",") {
		kv := strings.SplitN(strings.TrimSpace(it), "=", 2)
		if len(kv) == 2 {
			ret[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	return ret
}
