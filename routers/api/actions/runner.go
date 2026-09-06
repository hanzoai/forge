// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"context"
	"crypto/subtle"
	"encoding/json"
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
	"github.com/hanzoai/git/modules/web"
	actions_service "github.com/hanzoai/git/services/actions"

	gouuid "github.com/google/uuid"
)

// The runner protocol: five operations, each a POST carrying JSON in and JSON
// out, mounted at /v1/runner. The address is the operation and the body is the
// whole input, so there is nothing to negotiate and nothing to generate.

// The credential a registered runner presents on every call but register.
const (
	uuidHeader  = "x-runner-uuid"
	tokenHeader = "x-runner-token"
)

// RunnerRoutes returns the operation surface a runner talks to. Register stands
// outside the credential check because a runner has no credential until it
// answers; the other four are split by whether reaching them means the runner is
// executing a job.
func RunnerRoutes() *web.Router {
	m := web.NewRouter()
	m.Post("/register", op(register))
	m.Group("", func() {
		m.Post("/declare", op(declare))
		m.Post("/task", op(task))
	}, credential(false))
	m.Group("", func() {
		m.Post("/state", op(state))
		m.Post("/logs", op(logs))
	}, credential(true))
	return m
}

// op serves one operation: decode the body into In, run fn, write Out as JSON.
// It is the only code here that touches the wire, so every operation answers in
// the same shapes and the handlers below never see an http.ResponseWriter.
func op[In, Out any](fn func(context.Context, *In) (*Out, error)) http.HandlerFunc {
	return func(resp http.ResponseWriter, req *http.Request) {
		var in In
		if err := json.NewDecoder(req.Body).Decode(&in); err != nil {
			answerFault(resp, faultf(http.StatusBadRequest, "decode request: %v", err))
			return
		}
		out, err := fn(req.Context(), &in)
		if err != nil {
			answerFault(resp, err)
			return
		}
		// Encoded whole before anything is written, so a value that will not
		// marshal is an error the caller sees rather than a truncated 200 it
		// reads as success.
		body, err := json.Marshal(out)
		if err != nil {
			answerFault(resp, fmt.Errorf("encode reply: %w", err))
			return
		}
		resp.Header().Set("Content-Type", "application/json")
		if _, err := resp.Write(body); err != nil {
			log.Error("actions runner: write reply for %s: %v", req.URL.Path, err)
		}
	}
}

// fault is the one error body every operation answers with: the status, a short
// code a client can branch on, and a message for a human.
type fault struct {
	Status int    `json:"status"`
	Code   string `json:"code,omitempty"`
	Msg    string `json:"error"`
}

func (f *fault) Error() string { return f.Msg }

func faultf(status int, format string, a ...any) *fault {
	return &fault{Status: status, Msg: fmt.Sprintf(format, a...)}
}

// unknownRunner is the one answer to a caller the forge cannot place, whether
// its uuid is unknown or its token does not match: telling the two apart would
// let anyone probe which uuids exist.
func unknownRunner() *fault {
	return &fault{Status: http.StatusUnauthorized, Code: "unregistered", Msg: "unregistered runner"}
}

func answerFault(resp http.ResponseWriter, err error) {
	var f *fault
	if !errors.As(err, &f) {
		f = faultf(http.StatusInternalServerError, "%v", err)
	}
	resp.Header().Set("Content-Type", "application/json")
	resp.WriteHeader(f.Status)
	if err := json.NewEncoder(resp).Encode(f); err != nil {
		log.Error("actions runner: encode fault: %v", err)
	}
}

type callerKey struct{}

// caller is the runner that made this call, placed by the credential check.
func caller(ctx context.Context) *actions_model.ActionRunner {
	r, _ := ctx.Value(callerKey{}).(*actions_model.ActionRunner)
	return r
}

// credential resolves the runner behind a call from the uuid and token it
// carries. working says whether reaching the operation means the runner is
// executing a job; both that and plain liveness are recorded in the same write,
// because with a fleet of runners polling, this write is most of what the runner
// table costs.
func credential(working bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(resp http.ResponseWriter, req *http.Request) {
			ctx := req.Context()
			r, err := actions_model.GetRunnerByUUID(ctx, req.Header.Get(uuidHeader))
			if err != nil {
				if errors.Is(err, util.ErrNotExist) {
					answerFault(resp, unknownRunner())
				} else {
					answerFault(resp, err)
				}
				return
			}
			hashed := auth_model.HashToken(req.Header.Get(tokenHeader), r.TokenSalt)
			if subtle.ConstantTimeCompare([]byte(r.TokenHash), []byte(hashed)) != 1 {
				answerFault(resp, unknownRunner())
				return
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

			next.ServeHTTP(resp, req.WithContext(context.WithValue(ctx, callerKey{}, r)))
		})
	}
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
// authenticates every later call.
func register(ctx context.Context, in *runner_module.RegisterIn) (*runner_module.RegisterOut, error) {
	if in.Token == "" || in.Name == "" {
		return nil, faultf(http.StatusBadRequest, "missing runner token or name")
	}

	token, err := actions_model.GetRunnerToken(ctx, in.Token)
	if err != nil {
		return nil, faultf(http.StatusUnauthorized, "runner registration token not found")
	}
	if !token.IsActive {
		return nil, faultf(http.StatusUnauthorized, "runner registration token has been invalidated, please use the latest one")
	}
	if token.OwnerID > 0 {
		if _, err := user_model.GetUserByID(ctx, token.OwnerID); err != nil {
			return nil, faultf(http.StatusUnauthorized, "owner of the token not found")
		}
	}
	if token.RepoID > 0 {
		if _, err := repo_model.GetRepositoryByID(ctx, token.RepoID); err != nil {
			return nil, faultf(http.StatusUnauthorized, "repository of the token not found")
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
	r := caller(ctx)
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

// task hands the runner a job to execute, if there is one for it. A runner sends
// the queue version it last saw; when it matches, nothing has been queued since
// and the forge answers without opening an assignment transaction.
func task(ctx context.Context, in *runner_module.TaskIn) (*runner_module.TaskOut, error) {
	r := caller(ctx)

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
	if in.TasksVersion != latest {
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
			latest = in.TasksVersion
			log.Debug("task pick throttled for runner %q (id %d); it will retry on its next poll", fresh.Name, fresh.ID)
		case ok:
			assigned = t
		}
	}

	return &runner_module.TaskOut{Task: assigned, TasksVersion: latest}, nil
}

// state records a task's progress and that of its steps, and answers with the
// result the forge now holds, which is how a runner learns its task was
// cancelled from elsewhere.
func state(ctx context.Context, in *runner_module.StateIn) (*runner_module.StateOut, error) {
	r := caller(ctx)

	t, err := actions_model.UpdateTaskByState(ctx, r.ID, in.State)
	if err != nil {
		return nil, fmt.Errorf("update task: %w", err)
	}

	for k, v := range in.Outputs {
		if len(k) > 255 {
			log.Warn("Ignore the output of task %d because the key is too long: %q", t.ID, k)
			continue
		}
		// The value can be a maximum of 1 MB. GitHub also caps the total of all
		// outputs in a run at 50 MB; that one is not worth the bookkeeping.
		if l := len(v); l > 1024*1024 {
			log.Warn("Ignore the output %q of task %d because the value is too long: %v", k, t.ID, l)
			continue
		}
		if err := actions_model.InsertTaskOutputIfNotExist(ctx, t.ID, k, v); err != nil {
			// Not fatal: the runner resends outputs it has had no acknowledgement for.
			log.Warn("Failed to insert the output %q of task %d: %v", k, t.ID, err)
		}
	}
	sent, err := actions_model.FindTaskOutputKeyByTaskID(ctx, t.ID)
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
		State:       runner_module.State{ID: in.State.ID, Result: t.Status.AsResult()},
		SentOutputs: sent,
	}, nil
}

// logs appends console output to a task's log and answers with how far that log
// is durable, so the runner knows where to resend from.
func logs(ctx context.Context, in *runner_module.LogsIn) (*runner_module.LogsOut, error) {
	r := caller(ctx)

	t, err := actions_model.GetTaskByID(ctx, in.TaskID)
	if err != nil {
		return nil, fmt.Errorf("get task: %w", err)
	}
	if r.ID != t.RunnerID {
		return nil, faultf(http.StatusForbidden, "invalid runner for task")
	}
	ack := t.LogLength

	// Drop rows already acknowledged, keeping only what is new.
	var rows []runner_module.Row
	if in.Index <= ack && int64(len(in.Rows))+in.Index > ack {
		rows = in.Rows[ack-in.Index:]
	}

	// Acknowledge a resent seal idempotently. Appending past the seal is an error.
	if t.LogInStorage {
		if len(rows) > 0 {
			return nil, faultf(http.StatusConflict, "log file has been archived")
		}
		return &runner_module.LogsOut{Ack: ack}, nil
	}

	// Nothing to do unless there are new rows or a seal to apply. Even with a
	// seal, stop when the runner has outrun the forge: archiving a log with a gap
	// in it is worse than asking the runner to retry.
	if len(rows) == 0 && (!in.NoMore || in.Index > ack) {
		return &runner_module.LogsOut{Ack: ack}, nil
	}

	// Called even with no rows: at offset 0 it creates the empty file that
	// TransferLogs reads when a task finishes having printed nothing.
	ns, err := actions.WriteLogs(ctx, t.LogFilename, t.LogSize, rows)
	if err != nil {
		return nil, fmt.Errorf("append logs to dbfs file: %w", err)
	}
	t.LogLength += int64(len(rows))
	for _, n := range ns {
		t.LogIndexes = append(t.LogIndexes, t.LogSize)
		t.LogSize += int64(n)
	}

	var remove func()
	if in.NoMore {
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

	return &runner_module.LogsOut{Ack: t.LogLength}, nil
}
