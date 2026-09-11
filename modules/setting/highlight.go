// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package setting

func GetHighlightMapping() map[string]string {
	highlightMapping := map[string]string{}
	if CfgProvider == nil {
		return highlightMapping
	}

	keys := CfgProvider.Section("highlight.mapping").Keys()
	for _, key := range keys {
		highlightMapping[key.Name()] = key.Value()
	}
	return highlightMapping
}
