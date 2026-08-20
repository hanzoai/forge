// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2014 The Gogs Authors. All rights reserved.
// Copyright 2019 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package cron

import (
	"context"
	"runtime/pprof"
	"time"

	"github.com/hanzoai/git/modules/graceful"
	"github.com/hanzoai/git/modules/process"
	"github.com/hanzoai/git/modules/translation"

	"github.com/robfig/cron/v3"
)

// Init begins cron tasks
// Each cron task is run within the shutdown context as a running server
// AtShutdown the cron server is stopped
func Init(original context.Context) {
	defer pprof.SetGoroutineLabels(original)
	_, _, finished := process.GetManager().AddTypedContext(graceful.GetManager().ShutdownContext(), "Service: Cron", process.SystemProcessType, true)

	// The worker comes up BEFORE the jobs register, because registering is
	// what declares each schedule to Hanzo Tasks and a schedule with nothing
	// serving its queue is a run that times out rather than one that waits.
	startBackend()

	initBasicTasks()
	initExtendedTasks()
	initActionsTasks()

	lock.Lock()
	for _, task := range tasks {
		if task.IsEnabled() && task.DoRunAtStart() {
			go task.Run()
		}
	}

	started = true
	lock.Unlock()
	graceful.GetManager().RunAtShutdown(context.Background(), func() {
		stopBackend()
		lock.Lock()
		started = false
		lock.Unlock()
		finished()
	})
}

// TaskTableRow represents a task row in the tasks table
type TaskTableRow struct {
	Name        string
	Spec        string
	Next        time.Time
	Prev        time.Time
	Status      string
	LastMessage string
	LastDoer    string
	ExecTimes   int64
	task        *Task
}

func (t *TaskTableRow) FormatLastMessage(locale translation.Locale) string {
	if t.Status == "finished" {
		return t.task.GetConfig().FormatMessage(locale, t.Name, t.Status, t.LastDoer)
	}

	return t.task.GetConfig().FormatMessage(locale, t.Name, t.Status, t.LastDoer, t.LastMessage)
}

// TaskTable represents a table of tasks
type TaskTable []*TaskTableRow

// ListTasks returns every registered cron task for the admin table.
//
// The schedule of record is Hanzo Tasks; this reads the DECLARATION, which is
// the same string that was handed to CreateSchedule, and computes the next
// occurrence from it. That is arithmetic on a cron expression, not a second
// scheduler — nothing here decides when anything runs, and a describe call per
// row would put thirty round trips behind a page load to learn what the spec
// already says.
func ListTasks() TaskTable {
	lock.Lock()
	defer lock.Unlock()

	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	now := time.Now()

	tTable := make([]*TaskTableRow, 0, len(tasks))
	for _, task := range tasks {
		spec := task.config.GetSchedule()
		if spec == "" {
			spec = "-"
		}

		var next time.Time
		if sched, err := parser.Parse(task.config.GetSchedule()); err == nil {
			next = sched.Next(now)
		}

		task.lock.Lock()
		tTable = append(tTable, &TaskTableRow{
			Name:        task.Name,
			Spec:        spec,
			Next:        next,
			Prev:        task.LastRun,
			ExecTimes:   task.ExecTimes,
			LastMessage: task.LastMessage,
			Status:      task.Status,
			LastDoer:    task.LastDoer,
			task:        task,
		})
		task.lock.Unlock()
	}

	return tTable
}
