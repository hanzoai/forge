// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

// Scheduling belongs to Hanzo Tasks, and this file is the only place that
// knows it.
//
// What stood here was a second async system: gocron driving thirty jobs in
// process, each wrapped by hand in a global lock, a panic recover, and its own
// bookkeeping. Every one of those is something Tasks already does, and doing it
// twice is how the two versions drift.
//
// The cost of the second system was measured rather than argued. One schedule
// naming a reusable workflow this instance does not host held 108 of the 116
// specs below it overdue across 60 repositories — for eleven days, unnoticed,
// because a ticker loop has no failure surface. Under Tasks that run fails on
// its own, retries under its policy, and lands in
// `tasks_workflow_runs_failed_total` where it pages.
//
// The declaration surface does not move. `RegisterTaskFatal(name, config, fn)`
// still says what a job is and when it runs; the thirty callers are untouched.
// Only the thing that fires them changed.
package cron

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	user_model "github.com/hanzoai/git/models/user"
	"github.com/hanzoai/git/modules/log"

	"github.com/hanzoai/tasks/pkg/sdk/client"
	"github.com/hanzoai/tasks/pkg/sdk/worker"
	"github.com/hanzoai/tasks/pkg/sdk/workflow"
)

const (
	// One queue for every cron job this server owns. They are the same kind of
	// work — short, periodic, in-process — and splitting them would buy a
	// dimension nobody queries.
	taskQueue = "hanzo-git-cron"

	// One workflow type, dispatched by job name. Thirty registrations would be
	// thirty copies of the same three lines, free to drift; the name is data,
	// so it travels as data.
	workflowType = "GitCronJob"
)

var (
	backendOnce sync.Once
	tasksClient client.Client
	tasksWorker worker.Worker
)

// tasksAddress is the Hanzo Tasks frontend. Empty disables the backend, which
// is what a test binary and a laptop want: jobs stay registered and runnable by
// hand, nothing dials out.
func tasksAddress() string { return os.Getenv("HANZO_TASKS_HOSTPORT") }

func tasksNamespace() string {
	if ns := os.Getenv("HANZO_TASKS_NAMESPACE"); ns != "" {
		return ns
	}
	return "git"
}

// GitCronJobWorkflow is what every schedule fires. It carries the job's name
// and nothing else — the work itself is an activity, so a panic inside it is
// caught, counted as an attempt, and retried by the server rather than
// swallowed by a recover() we maintain.
func GitCronJobWorkflow(ctx workflow.Context, name string) error {
	return workflow.ExecuteActivity(ctx, RunCronJobActivity, name).Get(ctx, nil)
}

// RunCronJobActivity executes one registered job in this process.
//
// It calls the job function directly. The lock, the recover and the run
// counter that used to wrap this are gone: a schedule does not overlap itself,
// an activity panic is the server's to catch, and the count lives in the
// visibility store where it can be queried rather than in a mutex-guarded int
// that dies with the pod.
func RunCronJobActivity(ctx context.Context, name string) error {
	task := GetTask(name)
	if task == nil {
		// A schedule naming a job this build does not register. Fail the run
		// rather than the scan — that distinction is the whole reason this
		// file exists.
		return fmt.Errorf("cron: no job registered under %q", name)
	}
	return task.fun(ctx, &user_model.User{ID: -1, Name: "(Cron)"}, task.GetConfig())
}

// startBackend dials Tasks and serves the one workflow. Idempotent, and a
// failure to reach Tasks is logged rather than fatal: a git server that cannot
// schedule is still a git server, and refusing to boot over it would turn a
// degraded cron into an outage.
func startBackend() {
	backendOnce.Do(func() {
		addr := tasksAddress()
		if addr == "" {
			log.Info("cron: HANZO_TASKS_HOSTPORT is unset — jobs are registered but nothing will fire them")
			return
		}

		cli, err := client.Dial(client.Options{HostPort: addr, Namespace: tasksNamespace()})
		if err != nil {
			log.Error("cron: cannot reach Hanzo Tasks at %s: %v", addr, err)
			return
		}
		tasksClient = cli

		wk := worker.New(cli, taskQueue, worker.Options{})
		wk.RegisterWorkflowWithOptions(GitCronJobWorkflow, worker.RegisterWorkflowOptions{Name: workflowType})
		wk.RegisterActivity(RunCronJobActivity)
		if err := wk.Start(); err != nil {
			log.Error("cron: cannot start the Hanzo Tasks worker: %v", err)
			tasksClient = nil
			return
		}
		tasksWorker = wk
		log.Info("cron: serving %s on queue %s in namespace %s", workflowType, taskQueue, tasksNamespace())
	})
}

// scheduleTask declares one job's cadence to Tasks. Called once per registered
// job, after the backend is up.
func scheduleTask(t *Task) {
	if tasksClient == nil {
		return
	}
	spec := t.config.GetSchedule()
	if spec == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := tasksClient.CreateSchedule(ctx, scheduleOptionsFor(t)); err != nil {
		log.Error("cron: cannot schedule %q (%s): %v", t.Name, spec, err)
	}
}

// scheduleOptionsFor says what a job's schedule IS, separately from declaring
// it. Deciding and sending are different jobs: this one is pure, so the shape
// that reaches Tasks can be asserted without a server to send it to.
//
// The job's name is the schedule id. One job, one schedule, and a re-register
// after a restart addresses the same row rather than growing a second one.
func scheduleOptionsFor(t *Task) client.CreateScheduleOptions {
	return client.CreateScheduleOptions{
		ID:              "git-cron-" + t.Name,
		CronExpressions: []string{t.config.GetSchedule()},
		WorkflowType:    workflowType,
		TaskQueue:       taskQueue,
		Input:           []any{t.Name},
	}
}

// stopBackend releases the worker at shutdown.
func stopBackend() {
	if tasksWorker != nil {
		tasksWorker.Stop()
		tasksWorker = nil
	}
	if tasksClient != nil {
		tasksClient.Close()
		tasksClient = nil
	}
}
