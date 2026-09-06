// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

// Package runner holds the values a runner and the forge exchange: the input
// and output of each of the five ops the forge serves under /v1/runner.
//
// It is a leaf on purpose. The runner is a separate module and imports this
// package to get the very types the handlers declare, so there is one
// definition of the protocol rather than a server copy and a client copy that
// drift. Nothing here may import the rest of the forge.
package runner

import "time"

// Result is how a job, a step or a task finished. The empty value means it has
// not finished, so a zero Result is "still going" rather than a fabricated
// outcome. The four names are the same words the forge's Status uses, so the
// wire needs no numbering to stay in step with the database.
type Result string

const (
	Pending   Result = ""
	Success   Result = "success"
	Failure   Result = "failure"
	Cancelled Result = "cancelled"
	Skipped   Result = "skipped"
)

// Identity is what the forge knows about a runner. The token is minted at
// registration and is the runner's credential for every later op.
type Identity struct {
	ID        int64    `json:"id"`
	UUID      string   `json:"uuid"`
	Token     string   `json:"token"`
	Name      string   `json:"name"`
	Version   string   `json:"version"`
	Labels    []string `json:"labels"`
	Ephemeral bool     `json:"ephemeral"`
}

// RegisterIn trades a registration token for a runner identity.
type RegisterIn struct {
	Name         string   `json:"name"`
	Token        string   `json:"token"`
	Version      string   `json:"version"`
	Labels       []string `json:"labels"`
	Ephemeral    bool     `json:"ephemeral"`
	Capabilities []string `json:"capabilities"`
}

type RegisterOut struct {
	Runner Identity `json:"runner"`
}

// DeclareIn republishes what a registered runner can do. A runner sends it on
// start and after its configuration changes, so labels and version follow the
// runner without re-registering it.
type DeclareIn struct {
	Version      string   `json:"version"`
	Labels       []string `json:"labels"`
	Capabilities []string `json:"capabilities"`
}

// DeclareOut answers with the stored identity and the capabilities this forge
// understands, so a runner learns what the other side supports from the reply
// body rather than from a header.
type DeclareOut struct {
	Runner       Identity `json:"runner"`
	Capabilities []string `json:"capabilities"`
}

// TaskIn asks for work. TasksVersion is the queue version the runner last saw;
// when it equals the forge's, nothing has been queued since and the forge skips
// the assignment transaction entirely.
type TaskIn struct {
	TasksVersion int64 `json:"tasks_version"`
}

// TaskOut carries an assigned task, or no task and the current queue version
// for the runner to poll against next time.
type TaskOut struct {
	Task         *Task `json:"task,omitempty"`
	TasksVersion int64 `json:"tasks_version"`
}

// Task is one job, expanded and ready to execute. ID is unique for all time —
// unlike a run or job id, it is never reused.
type Task struct {
	ID       int64             `json:"id"`
	Workflow []byte            `json:"workflow"`
	Context  map[string]any    `json:"context"`
	Secrets  map[string]string `json:"secrets,omitempty"`
	Vars     map[string]string `json:"vars,omitempty"`
	Needs    map[string]Need   `json:"needs,omitempty"`
}

// Need is what one job this task depends on produced.
type Need struct {
	Outputs map[string]string `json:"outputs,omitempty"`
	Result  Result            `json:"result"`
}

// StateIn reports progress. Outputs are sent incrementally: the reply names
// every key the forge has stored, so the runner stops resending them.
type StateIn struct {
	State   State             `json:"state"`
	Outputs map[string]string `json:"outputs,omitempty"`
}

// StateOut echoes the task's result as the forge now holds it, which is how a
// runner learns its task was cancelled from elsewhere.
type StateOut struct {
	State       State    `json:"state"`
	SentOutputs []string `json:"sent_outputs,omitempty"`
}

// State is a task's progress and that of each of its steps.
type State struct {
	ID      int64     `json:"id"`
	Result  Result    `json:"result"`
	Started time.Time `json:"started,omitzero"`
	Stopped time.Time `json:"stopped,omitzero"`
	Steps   []Step    `json:"steps,omitempty"`
}

// Step is one step's progress and the slice of the task log it wrote.
type Step struct {
	ID        int64     `json:"id"`
	Result    Result    `json:"result"`
	Started   time.Time `json:"started,omitzero"`
	Stopped   time.Time `json:"stopped,omitzero"`
	LogIndex  int64     `json:"log_index"`
	LogLength int64     `json:"log_length"`
}

// LogsIn appends console output. Index is the position of the first row in the
// task's log, so a resent batch overlaps rather than duplicates. NoMore seals
// the log and moves it to storage.
type LogsIn struct {
	TaskID int64 `json:"task_id"`
	Index  int64 `json:"index"`
	Rows   []Row `json:"rows,omitempty"`
	NoMore bool  `json:"no_more,omitempty"`
}

// LogsOut reports how far the log is durable: Ack is index + rows accepted, and
// the runner resends from there.
type LogsOut struct {
	Ack int64 `json:"ack"`
}

// Row is one line of console output.
type Row struct {
	Time    time.Time `json:"time"`
	Content string    `json:"content"`
}
