// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	actions_model "github.com/hanzoai/git/models/actions"
	auth_model "github.com/hanzoai/git/models/auth"
	repo_model "github.com/hanzoai/git/models/repo"
	user_model "github.com/hanzoai/git/models/user"
	"github.com/hanzoai/git/modules/actions"
	runner_module "github.com/hanzoai/git/modules/actions/runner"
	"github.com/hanzoai/git/modules/log"
	"github.com/hanzoai/git/modules/timeutil"
	"github.com/hanzoai/git/modules/util"
	actions_service "github.com/hanzoai/git/services/actions"

	gouuid "github.com/google/uuid"
	"github.com/zap-proto/zip"
)

// The runner protocol: five typed operations under /v1/runner, declared with
// zip so the address and the operation are one registration. A runner reaches
// them over HTTP as POST /v1/runner/<name>, and over ZAP by asking for
// post_runner_<name> on the call plane; both run the same handler with the same
// input type, because zip derives the second face from the first.

// RunnerRouteBase is where the operations are addressed. It sits beside the
// "/v1" mount rather than inside it because a runner carries its own credential
// and must not meet the session and API-token middleware.
const RunnerRouteBase = "/v1/runner"

// runnerBodyLimit bounds a runner's request. A log report is the large one: a
// runner batches up to a hundred lines and a line may reach 64 KiB, so the
// ceiling has to clear ~6.4 MiB with room to spare. zip's own default is 4 MiB,
// which would refuse a full batch.
const runnerBodyLimit = 16 << 20

// RunnerOps builds the runner's operation surface. Paths are absolute because
// the forge routes to this app without stripping a prefix, so what zip declares
// is the whole address the request arrives at.
func RunnerOps() *zip.App {
	a := zip.New(zip.Config{
		AppName:               "runner",
		BodyLimit:             runnerBodyLimit,
		DisableStartupMessage: true,
	})
	zip.Post(a, RunnerRouteBase+"/register", register)
	zip.Post(a, RunnerRouteBase+"/declare", declare)
	zip.Post(a, RunnerRouteBase+"/task", task)
	zip.Post(a, RunnerRouteBase+"/state", state)
	zip.Post(a, RunnerRouteBase+"/log", appendLog)
	if err := a.Build(); err != nil {
		// Only a program that does not compose reaches here — two operations
		// claiming one address — which is a mistake in the five lines above and
		// cannot be recovered from at run time.
		panic(fmt.Errorf("actions runner: build operations: %w", err))
	}
	return a
}

// unknownRunner is the one answer to a caller the forge cannot place, whether
// its uuid is unknown or its token does not match: telling the two apart would
// let anyone probe which uuids exist.
func unknownRunner() error {
	return &zip.HTTPError{Status: http.StatusUnauthorized, Code: "unregistered", Msg: "unregistered runner"}
}

// authenticate resolves the runner behind a call from the credential it carries.
// It is the first line of every operation but register, and a return value
// rather than middleware: a handler cannot forget to hold the result, and there
// is no path that reaches a handler with no runner behind it.
//
// working says whether reaching the operation means the runner is executing a
// job; both that and plain liveness are recorded in the same write, because with
// a fleet of runners asking every few seconds this write is most of what the
// runner table costs.
func authenticate(ctx context.Context, c runner_module.Credential, working bool) (*actions_model.ActionRunner, error) {
	if c.UUID == "" || c.Token == "" {
		// Refused before the database is asked anything: a caller with no
		// credential at all should not cost a query.
		return nil, unknownRunner()
	}
	r, err := actions_model.GetRunnerByUUID(ctx, c.UUID)
	if err != nil {
		if errors.Is(err, util.ErrNotExist) {
			return nil, unknownRunner()
		}
		return nil, err
	}
	hashed := auth_model.HashToken(c.Token, r.TokenSalt)
	if subtle.ConstantTimeCompare([]byte(r.TokenHash), []byte(hashed)) != 1 {
		return nil, unknownRunner()
	}

	now := time.Now()
	var cols []string
	if working && actions_model.ShouldPersistLastActive(r.LastActive, now) {
		r.LastActive = timeutil.TimeStamp(now.Unix())
		cols = append(cols, "last_active")
	}
	if actions_model.ShouldPersistLastOnline(r.LastOnline, now) {
		r.LastOnline = timeutil.TimeStamp(now.Unix())
		cols = append(cols, "last_online")
	}
	if len(cols) > 0 {
		if err := actions_model.UpdateRunner(ctx, r, cols...); err != nil {
			log.Error("actions runner: update status of %q: %v", r.Name, err)
		}
	}
	return r, nil
}

// identity is what a runner is told about itself.
func identity(r *actions_model.ActionRunner) runner_module.Identity {
	return runner_module.Identity{
		ID:        r.ID,
		UUID:      r.UUID,
		Token:     r.Token,
		Name:      r.Name,
		Version:   r.Version,
		Labels:    r.AgentLabels,
		Ephemeral: r.Ephemeral,
	}
}

// cancelling is the capability a runner advertises when it understands the
// transitional cancelling state and will run post-step cleanup before finalizing
// its task.
const cancelling = "cancelling"

// register trades a registration token for a runner identity and the token that
// authenticates every later call. It is the one operation with no credential to
// check, because a runner has none until this answers.
func register(ctx context.Context, in *runner_module.RegisterIn) (*runner_module.RegisterOut, error) {
	if in.Token == "" || in.Name == "" {
		return nil, zip.ErrBadRequest("missing runner token or name")
	}

	token, err := actions_model.GetRunnerToken(ctx, in.Token)
	if err != nil {
		return nil, zip.ErrUnauthorized("runner registration token not found")
	}
	if !token.IsActive {
		return nil, zip.ErrUnauthorized("runner registration token has been invalidated, please use the latest one")
	}
	if token.OwnerID > 0 {
		if _, err := user_model.GetUserByID(ctx, token.OwnerID); err != nil {
			return nil, zip.ErrUnauthorized("owner of the token not found")
		}
	}
	if token.RepoID > 0 {
		if _, err := repo_model.GetRepositoryByID(ctx, token.RepoID); err != nil {
			return nil, zip.ErrUnauthorized("repository of the token not found")
		}
	}

	r := &actions_model.ActionRunner{
		UUID:                 gouuid.New().String(),
		Name:                 util.EllipsisDisplayString(in.Name, 255),
		OwnerID:              token.OwnerID,
		RepoID:               token.RepoID,
		Version:              in.Version,
		AgentLabels:          in.Labels,
		Ephemeral:            in.Ephemeral,
		HasCancellingSupport: slices.Contains(in.Capabilities, cancelling),
	}
	r.GenerateAndFillToken()
	if err := actions_model.CreateRunner(ctx, r); err != nil {
		return nil, fmt.Errorf("create runner: %w", err)
	}

	token.IsActive = true
	if err := actions_model.UpdateRunnerToken(ctx, token, "is_active"); err != nil {
		return nil, fmt.Errorf("update runner token: %w", err)
	}

	return &runner_module.RegisterOut{Runner: identity(r)}, nil
}

// declare republishes what a registered runner can do, and answers with what
// this forge understands, so the two learn about each other from one exchange.
func declare(ctx context.Context, in *runner_module.DeclareIn) (*runner_module.DeclareOut, error) {
	r, err := authenticate(ctx, in.Credential, false)
	if err != nil {
		return nil, err
	}
	if err := actions_model.UpdateRunner(ctx, r, declared(r, in)...); err != nil {
		return nil, fmt.Errorf("update runner: %w", err)
	}
	return &runner_module.DeclareOut{
		Runner:       identity(r),
		Capabilities: actions_model.RunnerCapabilities(),
	}, nil
}

// declared copies what a runner says about itself onto the stored record and
// reports which columns to write. A capability that has not changed is left out,
// so a runner declaring the same thing it declared last time costs a narrower
// update.
func declared(r *actions_model.ActionRunner, in *runner_module.DeclareIn) []string {
	r.AgentLabels = in.Labels
	r.Version = in.Version
	cols := []string{"agent_labels", "version"}
	if support := slices.Contains(in.Capabilities, cancelling); support != r.HasCancellingSupport {
		r.HasCancellingSupport = support
		cols = append(cols, "has_cancelling_support")
	}
	return cols
}

// task hands the runner a job to execute, if there is one for it, and answers
// immediately either way. A runner sends the queue version it last saw; when it
// matches, nothing has been queued since and the forge answers without opening
// an assignment transaction.
func task(ctx context.Context, in *runner_module.TaskIn) (*runner_module.TaskOut, error) {
	r, err := authenticate(ctx, in.Credential, false)
	if err != nil {
		return nil, err
	}

	latest, err := actions_model.GetTasksVersionByScope(ctx, r.OwnerID, r.RepoID)
	if err != nil {
		return nil, fmt.Errorf("query tasks version: %w", err)
	}
	if latest == 0 {
		if err := actions_model.IncreaseTaskVersion(ctx, r.OwnerID, r.RepoID); err != nil {
			return nil, fmt.Errorf("increase task version: %w", err)
		}
		// Answering zero here would tell the runner this forge is too old to
		// version its queue at all.
		latest++
	}

	var assigned *runner_module.Task
	if in.Queue != latest {
		// Re-read the runner so assignment sees its current disabled state: it may
		// have been disabled while this request was in flight.
		fresh, err := actions_model.GetRunnerByUUID(ctx, r.UUID)
		if err != nil {
			return nil, fmt.Errorf("get runner: %w", err)
		}
		t, ok, throttled, err := actions_service.TryPickTask(ctx, fresh)
		switch {
		case err != nil:
			log.Error("pick task failed: %v", err)
			return nil, fmt.Errorf("pick task: %w", err)
		case throttled:
			// Do not advance the runner's queue version, so it retries on its next
			// poll instead of sleeping until the next bump. A steady stream here
			// means MAX_CONCURRENT_TASK_PICKS is too low for the fleet.
			latest = in.Queue
			log.Debug("task pick throttled for runner %q (id %d); it will retry on its next poll", fresh.Name, fresh.ID)
		case ok:
			assigned = t
		}
	}

	return &runner_module.TaskOut{Task: assigned, Queue: latest}, nil
}

// state records a task's progress and that of its steps, and answers with the
// result the forge now holds, which is how a runner learns its task was
// cancelled from elsewhere.
func state(ctx context.Context, in *runner_module.StateIn) (*runner_module.StateOut, error) {
	r, err := authenticate(ctx, in.Credential, true)
	if err != nil {
		return nil, err
	}

	t, err := actions_model.UpdateTaskByState(ctx, r.ID, in.State)
	if err != nil {
		return nil, fmt.Errorf("update task: %w", err)
	}

	for _, out := range in.Outputs {
		if len(out.Name) > 255 {
			log.Warn("Ignore the output of task %d because the key is too long: %q", t.ID, out.Name)
			continue
		}
		// The value can be a maximum of 1 MB. GitHub also caps the total of all
		// outputs in a run at 50 MB; that one is not worth the bookkeeping.
		if l := len(out.Value); l > 1024*1024 {
			log.Warn("Ignore the output %q of task %d because the value is too long: %v", out.Name, t.ID, l)
			continue
		}
		if err := actions_model.InsertTaskOutputIfNotExist(ctx, t.ID, out.Name, out.Value); err != nil {
			// Not fatal: the runner resends outputs it has had no acknowledgement for.
			log.Warn("Failed to insert the output %q of task %d: %v", out.Name, t.ID, err)
		}
	}
	stored, err := actions_model.FindTaskOutputKeyByTaskID(ctx, t.ID)
	if err != nil {
		// Not fatal either: an unacknowledged output comes back on the next report.
		log.Warn("Failed to find the sent outputs of task %d: %v", t.ID, err)
	}

	if err := t.LoadJob(ctx); err != nil {
		return nil, fmt.Errorf("load job: %w", err)
	}
	if err := t.Job.LoadAttributes(ctx); err != nil {
		return nil, fmt.Errorf("load run: %w", err)
	}

	actions_service.CreateCommitStatusForRunJobs(ctx, t.Job.Run, t.Job)

	if t.Status.IsDone() {
		actions_service.NotifyWorkflowJobStatusUpdateWithTask(ctx, t.Job, t)
	}

	if in.State.Result != runner_module.Pending {
		if err := actions_service.EmitJobsIfReadyByRun(t.Job.RunID); err != nil {
			log.Error("Emit ready jobs of run %d: %v", t.Job.RunID, err)
		}
		if t.Job.Run.Status.IsDone() {
			actions_service.NotifyWorkflowRunStatusUpdateWithReload(ctx, t.Job.RepoID, t.Job.RunID)
		}
	}

	return &runner_module.StateOut{
		State:  runner_module.State{ID: in.State.ID, Result: t.Status.AsResult()},
		Stored: stored,
	}, nil
}

// appendLog adds console output to a task's log and answers with how far that
// log is durable, so the runner knows where to resend from.
func appendLog(ctx context.Context, in *runner_module.LogIn) (*runner_module.LogOut, error) {
	r, err := authenticate(ctx, in.Credential, true)
	if err != nil {
		return nil, err
	}

	t, err := actions_model.GetTaskByID(ctx, in.Task)
	if err != nil {
		return nil, fmt.Errorf("get task: %w", err)
	}
	if r.ID != t.RunnerID {
		return nil, zip.ErrForbidden("invalid runner for task")
	}
	ack := t.LogLength

	// Drop lines already acknowledged, keeping only what is new.
	var lines []runner_module.Line
	if in.Index <= ack && int64(len(in.Lines))+in.Index > ack {
		lines = in.Lines[ack-in.Index:]
	}

	// Acknowledge a resent seal idempotently. Appending past the seal is an error.
	if t.LogInStorage {
		if len(lines) > 0 {
			return nil, zip.ErrConflict("log file has been archived")
		}
		return &runner_module.LogOut{Ack: ack}, nil
	}

	// Nothing to do unless there are new lines or a seal to apply. Even with a
	// seal, stop when the runner has outrun the forge: archiving a log with a gap
	// in it is worse than asking the runner to retry.
	if len(lines) == 0 && (!in.Last || in.Index > ack) {
		return &runner_module.LogOut{Ack: ack}, nil
	}

	// Called even with no lines: at offset 0 it creates the empty file that
	// TransferLogs reads when a task finishes having printed nothing.
	ns, err := actions.WriteLogs(ctx, t.LogFilename, t.LogSize, lines)
	if err != nil {
		return nil, fmt.Errorf("append logs to dbfs file: %w", err)
	}
	t.LogLength += int64(len(lines))
	for _, n := range ns {
		t.LogIndexes = append(t.LogIndexes, t.LogSize)
		t.LogSize += int64(n)
	}

	var remove func()
	if in.Last {
		t.LogInStorage = true
		if remove, err = actions.TransferLogs(ctx, t.LogFilename); err != nil {
			return nil, fmt.Errorf("transfer logs: %w", err)
		}
	}
	if err := actions_model.UpdateTask(ctx, t, "log_indexes", "log_length", "log_size", "log_in_storage"); err != nil {
		return nil, fmt.Errorf("update task: %w", err)
	}
	if remove != nil {
		remove()
	}

	return &runner_module.LogOut{Ack: t.LogLength}, nil
}
