// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package migration

// Messenger is a formatting function similar to i18n.TrString
type Messenger func(key string, args ...any)

// NilMessenger represents an empty formatting function
func NilMessenger(string, ...any) {}
