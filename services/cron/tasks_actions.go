// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

package cron

import (
	"context"

	user_model "github.com/hanzoai/git/models/user"
	"github.com/hanzoai/git/modules/log"
	"github.com/hanzoai/git/modules/setting"
	actions_service "github.com/hanzoai/git/services/actions"
)

func initActionsTasks() {
	if !setting.Actions.Enabled {
		return
	}
	registerStopZombieTasks()
	registerStopEndlessTasks()
	registerCancelAbandonedJobs()
	registerFailUnsatisfiableJobs()
	registerWakeRunners()
	registerScheduleTasks()
	registerActionsCleanup()
}

func registerStopZombieTasks() {
	RegisterTaskFatal("stop_zombie_tasks", &BaseConfig{
		Enabled:    true,
		RunAtStart: true,
		Schedule:   "@every 5m",
	}, func(ctx context.Context, _ *user_model.User, cfg Config) error {
		return actions_service.StopZombieTasks(ctx)
	})
}

func registerStopEndlessTasks() {
	RegisterTaskFatal("stop_endless_tasks", &BaseConfig{
		Enabled:    true,
		RunAtStart: true,
		Schedule:   "@every 30m",
	}, func(ctx context.Context, _ *user_model.User, cfg Config) error {
		return actions_service.StopEndlessTasks(ctx)
	})
}

func registerCancelAbandonedJobs() {
	RegisterTaskFatal("cancel_abandoned_jobs", &BaseConfig{
		Enabled:    true,
		RunAtStart: true,
		Schedule:   "@every 6h",
	}, func(ctx context.Context, _ *user_model.User, cfg Config) error {
		return actions_service.CancelAbandonedJobs(ctx)
	})
}

// registerFailUnsatisfiableJobs sweeps often, because the jobs it fails are ones
// no amount of waiting can help, and every one of them sits in the queue that
// real work is scheduled from.
func registerFailUnsatisfiableJobs() {
	RegisterTaskFatal("fail_unsatisfiable_jobs", &BaseConfig{
		Enabled:    true,
		RunAtStart: true,
		Schedule:   "@every 5m",
	}, func(ctx context.Context, _ *user_model.User, cfg Config) error {
		return actions_service.FailUnsatisfiableJobs(ctx)
	})
}

// registerWakeRunners runs every minute: a job that has waited past the grace with no runner is
// the sign that a runner is asleep on a version that should have moved.
func registerWakeRunners() {
	warnWakeRunnersUnscheduled()
	RegisterTaskFatal("wake_runners", &BaseConfig{
		Enabled:    true,
		RunAtStart: false,
		Schedule:   "@every 1m",
	}, func(ctx context.Context, _ *user_model.User, cfg Config) error {
		return actions_service.WakeRunners(ctx)
	})
}

// warnWakeRunnersUnscheduled says at error level that wake_runners will never fire when Hanzo Tasks is unavailable:
// it is the only repair for a runner wakeup lost to a failed version bump.
func warnWakeRunnersUnscheduled() {
	if tasksClient == nil {
		log.Error("cron: wake_runners cannot be scheduled because Hanzo Tasks is unavailable: " +
			"a runner wakeup lost to a failed version bump will not be repaired, and idle runners can sit on work they could take")
	}
}

// registerScheduleTasks registers a scheduled task that runs every minute to start any due schedule tasks.
func registerScheduleTasks() {
	// Register the task with a unique name, enabled status, and schedule for every minute.
	RegisterTaskFatal("start_schedule_tasks", &BaseConfig{
		Enabled:    true,
		RunAtStart: false,
		Schedule:   "@every 1m",
	}, func(ctx context.Context, _ *user_model.User, cfg Config) error {
		// Call the function to start schedule tasks and pass the context.
		return actions_service.StartScheduleTasks(ctx)
	})
}

func registerActionsCleanup() {
	RegisterTaskFatal("cleanup_actions", &BaseConfig{
		Enabled:    true,
		RunAtStart: false,
		Schedule:   "@midnight",
	}, func(ctx context.Context, _ *user_model.User, _ Config) error {
		return actions_service.Cleanup(ctx)
	})
}
