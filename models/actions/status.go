// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"slices"

	"github.com/hanzoai/git/modules/actions/runner"
	"github.com/hanzoai/git/modules/translation"
)

// Status represents the status of ActionRun, ActionRunJob, ActionTask, or ActionTaskStep
type Status int

// A Status covers more ground than a runner Result: the four states after
// StatusSkipped describe a job the forge is still holding, which a runner never
// reports. AsResult and StatusFromResult are the whole crossing, so nothing here
// has to keep step with a number on the wire.
const (
	StatusUnknown Status = iota
	StatusSuccess
	StatusFailure
	StatusCancelled
	StatusSkipped
	StatusWaiting
	StatusRunning
	StatusBlocked
	StatusCancelling
)

var statusNames = map[Status]string{
	StatusUnknown:    "unknown",
	StatusWaiting:    "waiting",
	StatusRunning:    "running",
	StatusSuccess:    "success",
	StatusFailure:    "failure",
	StatusCancelled:  "cancelled",
	StatusCancelling: "cancelling",
	StatusSkipped:    "skipped",
	StatusBlocked:    "blocked",
}

// String returns the string name of the Status
func (s Status) String() string {
	return statusNames[s]
}

// LocaleString returns the locale string name of the Status
func (s Status) LocaleString(lang translation.Locale) string {
	return lang.TrString("actions.status." + s.String())
}

// IsDone returns whether the Status is final
func (s Status) IsDone() bool {
	return s.In(StatusSuccess, StatusFailure, StatusCancelled, StatusSkipped)
}

// HasRun returns whether the Status is a result of running
func (s Status) HasRun() bool {
	return s.In(StatusSuccess, StatusFailure)
}

func (s Status) IsUnknown() bool {
	return s == StatusUnknown
}

func (s Status) IsSuccess() bool {
	return s == StatusSuccess
}

func (s Status) IsFailure() bool {
	return s == StatusFailure
}

func (s Status) IsCancelled() bool {
	return s == StatusCancelled
}

func (s Status) IsSkipped() bool {
	return s == StatusSkipped
}

func (s Status) IsWaiting() bool {
	return s == StatusWaiting
}

func (s Status) IsRunning() bool {
	return s == StatusRunning
}

func (s Status) IsBlocked() bool {
	return s == StatusBlocked
}

func (s Status) IsCancelling() bool {
	return s == StatusCancelling
}

// In returns whether s is one of the given statuses
func (s Status) In(statuses ...Status) bool {
	return slices.Contains(statuses, s)
}

// AsResult reports how a runner should read this status. A job the forge is
// still cancelling has already been decided, so it crosses as cancelled.
func (s Status) AsResult() runner.Result {
	switch s {
	case StatusSuccess:
		return runner.Success
	case StatusFailure:
		return runner.Failure
	case StatusCancelled, StatusCancelling:
		return runner.Cancelled
	case StatusSkipped:
		return runner.Skipped
	default:
		return runner.Pending
	}
}

func StatusFromResult(r runner.Result) Status {
	switch r {
	case runner.Success:
		return StatusSuccess
	case runner.Failure:
		return StatusFailure
	case runner.Cancelled:
		return StatusCancelled
	case runner.Skipped:
		return StatusSkipped
	default:
		return StatusUnknown
	}
}
