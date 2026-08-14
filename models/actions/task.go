// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// Copyright 2022 The Gitea Authors. All rights reserved.
// SPDX-License-Identifier: MIT

package actions

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"

	runnerv1 "github.com/hanzo-git/actions-proto-go/runner/v1"
	auth_model "github.com/hanzoai/git/models/auth"
	"github.com/hanzoai/git/models/db"
	"github.com/hanzoai/git/models/unit"
	"github.com/hanzoai/git/modules/actions/jobparser"
	"github.com/hanzoai/git/modules/globallock"
	"github.com/hanzoai/git/modules/log"
	"github.com/hanzoai/git/modules/setting"
	"github.com/hanzoai/git/modules/timeutil"
	"github.com/hanzoai/git/modules/util"

	lru "github.com/hashicorp/golang-lru/v2"
	"google.golang.org/protobuf/types/known/timestamppb"
	"github.com/hanzoai/builder"
)

// ActionTask represents a distribution of job
type ActionTask struct {
	ID       int64
	JobID    int64
	Job      *ActionRunJob     `xorm:"-"`
	Steps    []*ActionTaskStep `xorm:"-"`
	Attempt  int64
	RunnerID int64              `xorm:"index"`
	Status   Status             `xorm:"index"`
	Started  timeutil.TimeStamp `xorm:"index"`
	Stopped  timeutil.TimeStamp `xorm:"index(stopped_log_expired)"`

	RepoID            int64  `xorm:"index"`
	OwnerID           int64  `xorm:"index"`
	CommitSHA         string `xorm:"index"`
	IsForkPullRequest bool

	Token          string `xorm:"-"`
	TokenHash      string `xorm:"UNIQUE"` // sha256 of token
	TokenSalt      string
	TokenLastEight string `xorm:"index token_last_eight"`

	LogFilename  string     // file name of log
	LogInStorage bool       // read log from database or from storage
	LogLength    int64      // lines count
	LogSize      int64      // blob size
	LogIndexes   LogIndexes `xorm:"LONGBLOB"`                   // line number to offset
	LogExpired   bool       `xorm:"index(stopped_log_expired)"` // files that are too old will be deleted

	Created timeutil.TimeStamp `xorm:"created"`
	Updated timeutil.TimeStamp `xorm:"updated index"`
}

var successfulTokenTaskCache *lru.Cache[string, any]

func init() {
	db.RegisterModel(new(ActionTask), func() error {
		if setting.SuccessfulTokensCacheSize > 0 {
			var err error
			successfulTokenTaskCache, err = lru.New[string, any](setting.SuccessfulTokensCacheSize)
			if err != nil {
				return fmt.Errorf("unable to allocate Task cache: %v", err)
			}
		} else {
			successfulTokenTaskCache = nil
		}
		return nil
	})
}

func (task *ActionTask) Duration() time.Duration {
	return calculateDuration(task.Started, task.Stopped, task.Status, task.Updated)
}

func (task *ActionTask) IsStopped() bool {
	return task.Stopped > 0
}

func (task *ActionTask) GetRunLink() string {
	if task.Job == nil || task.Job.Run == nil {
		return ""
	}
	return task.Job.Run.Link()
}

func (task *ActionTask) GetCommitLink() string {
	if task.Job == nil || task.Job.Run == nil || task.Job.Run.Repo == nil {
		return ""
	}
	return task.Job.Run.Repo.CommitLink(task.CommitSHA)
}

func (task *ActionTask) GetRepoName() string {
	if task.Job == nil || task.Job.Run == nil || task.Job.Run.Repo == nil {
		return ""
	}
	return task.Job.Run.Repo.FullName()
}

func (task *ActionTask) GetRepoLink() string {
	if task.Job == nil || task.Job.Run == nil || task.Job.Run.Repo == nil {
		return ""
	}
	return task.Job.Run.Repo.Link()
}

func (task *ActionTask) LoadJob(ctx context.Context) error {
	if task.Job == nil {
		job, err := GetRunJobByRepoAndID(ctx, task.RepoID, task.JobID)
		if err != nil {
			return err
		}
		task.Job = job
	}
	return nil
}

// LoadAttributes load Job Steps if not loaded
func (task *ActionTask) LoadAttributes(ctx context.Context) error {
	if err := task.LoadJob(ctx); err != nil {
		return err
	}

	if err := task.Job.LoadAttributes(ctx); err != nil {
		return err
	}

	if task.Steps == nil { // be careful, an empty slice (not nil) also means loaded
		steps, err := GetTaskStepsByTaskID(ctx, task.ID)
		if err != nil {
			return err
		}
		task.Steps = steps
	}

	return nil
}

func (task *ActionTask) GenerateAndFillToken() {
	task.Token, task.TokenSalt, task.TokenHash, task.TokenLastEight = generateSaltedToken()
}

func GetTaskByID(ctx context.Context, id int64) (*ActionTask, error) {
	var task ActionTask
	has, err := db.GetEngine(ctx).Where("id=?", id).Get(&task)
	if err != nil {
		return nil, err
	} else if !has {
		return nil, fmt.Errorf("task with id %d: %w", id, util.ErrNotExist)
	}

	return &task, nil
}

func GetRunningTaskByToken(ctx context.Context, token string) (*ActionTask, error) {
	errNotExist := fmt.Errorf("task with token %q: %w", token, util.ErrNotExist)
	if token == "" {
		return nil, errNotExist
	}
	// A token is defined as being SHA1 sum these are 40 hexadecimal bytes long
	if len(token) != 40 {
		return nil, errNotExist
	}
	for _, x := range []byte(token) {
		if x < '0' || (x > '9' && x < 'a') || x > 'f' {
			return nil, errNotExist
		}
	}

	lastEight := token[len(token)-8:]

	if id := getTaskIDFromCache(token); id > 0 {
		task := &ActionTask{
			TokenLastEight: lastEight,
		}
		// Re-get the task from the db in case it has been deleted in the intervening period
		has, err := db.GetEngine(ctx).ID(id).Get(task)
		if err != nil {
			return nil, err
		}
		if has {
			return task, nil
		}
		successfulTokenTaskCache.Remove(token)
	}

	var tasks []*ActionTask
	// Cancelling tasks are still authenticating — post-run cleanup steps need API access (artifact uploads, cache saves, etc.) before the runner finalizes the task.
	err := db.GetEngine(ctx).Where("token_last_eight = ? AND status IN (?, ?)", lastEight, StatusRunning, StatusCancelling).Find(&tasks)
	if err != nil {
		return nil, err
	} else if len(tasks) == 0 {
		return nil, errNotExist
	}

	for _, t := range tasks {
		tempHash := auth_model.HashToken(token, t.TokenSalt)
		if subtle.ConstantTimeCompare([]byte(t.TokenHash), []byte(tempHash)) == 1 {
			if successfulTokenTaskCache != nil {
				successfulTokenTaskCache.Add(token, t.ID)
			}
			return t, nil
		}
	}
	return nil, errNotExist
}

func makeTaskStepDisplayName(step *jobparser.Step, limit int) (name string) {
	if step.Name != "" {
		name = step.Name // the step has an explicit name
	} else {
		// for unnamed step, its "String()" method tries to get a display name by its "name", "uses",
		// "run" or "id" (last fallback), we add the "Run " prefix for unnamed steps for better display
		// for multi-line "run" scripts, only use the first line to match GitHub's behavior
		// https://github.com/actions/runner/blob/66800900843747f37591b077091dd2c8cf2c1796/src/Runner.Worker/Handlers/ScriptHandler.cs#L45-L58
		runStr, _, _ := strings.Cut(strings.TrimSpace(step.Run), "\n")
		name = "Run " + util.IfZero(strings.TrimSpace(runStr), step.String())
	}
	return util.EllipsisDisplayString(name, limit) // database column has a length limit
}

// errJobAlreadyClaimed is a sentinel used inside claimJobForRunner to signal that
// another runner won the optimistic-lock race; it is never returned to callers.
var errJobAlreadyClaimed = errors.New("job already claimed by another runner")

// errJobUnparsable is a sentinel used inside claimJobForRunner to signal that a
// job's stored WorkflowPayload cannot be parsed.
//
// This is PERMANENT for that job and harmless to every other one. The payload is
// a snapshot taken when the run was created and is never rewritten, so retrying
// can only fail identically. Before this was separated out, the parse error was
// returned as an ordinary error: it aborted the whole FetchTask request with a
// 500, left the job Waiting, and the next poll picked the same job again — so a
// single malformed workflow denied work to EVERY runner on the instance. One
// repository with three invalid workflow files held the entire forge down for 29
// hours that way.
//
// A job that cannot be parsed must fail on its own run, where its author can see
// it, and the scheduler must move on to the next candidate.
var errJobUnparsable = errors.New("job workflow payload cannot be parsed")

// pickTaskBatchSize bounds how many waiting jobs each CreateTaskForRunner query loads,
// so a large backlog is not fetched into memory on every runner poll.
// It is a var only so tests can shrink it to exercise pagination cheaply.
var pickTaskBatchSize = 100

// CreateTaskForRunner finds a waiting job that matches the runner's labels and
// atomically claims it. It iterates through all matching jobs so that a
// concurrent claim by another runner (which would lose the optimistic lock on
// job #1) does not leave the remaining jobs permanently unassigned.
func CreateTaskForRunner(ctx context.Context, runner *ActionRunner) (*ActionTask, bool, error) {
	if db.InTransaction(ctx) {
		return nil, false, errors.New("CreateTaskForRunner must not be called within a database transaction")
	}
	e := db.GetEngine(ctx)

	jobCond := builder.NewCond()
	if runner.RepoID != 0 {
		jobCond = builder.Eq{"repo_id": runner.RepoID}
	} else if runner.OwnerID != 0 {
		jobCond = builder.In("repo_id", builder.Select("`repository`.id").From("repository").
			Join("INNER", "repo_unit", "`repository`.id = `repo_unit`.repo_id").
			Where(builder.Eq{"`repository`.owner_id": runner.OwnerID, "`repo_unit`.type": unit.TypeActions}))
	}
	baseCond := builder.Eq{"task_id": 0, "status": StatusWaiting, "is_reusable_caller": false}.And(jobCond)

	// TODO: a more efficient way to filter labels
	log.Trace("runner labels: %v", runner.AgentLabels)

	// Page through the waiting jobs oldest-first instead of loading the whole backlog into memory on every poll.
	// Keyset pagination on (updated, id) is safe under concurrent claims:
	// updated only moves forward, so the advancing cursor never skips a still-waiting job even as claimed jobs drop out.
	var cursorUpdated timeutil.TimeStamp
	var cursorID int64
	for {
		cond := baseCond
		if cursorID > 0 {
			cond = cond.And(builder.Or(
				builder.Gt{"updated": cursorUpdated},
				builder.And(builder.Eq{"updated": cursorUpdated}, builder.Gt{"id": cursorID}),
			))
		}

		var jobs []*ActionRunJob
		if err := e.Where(cond).Asc("updated", "id").Limit(pickTaskBatchSize).Find(&jobs); err != nil {
			return nil, false, err
		}

		for _, v := range jobs {
			if !runner.CanMatchLabels(v.RunsOn) {
				continue
			}
			task, ok, err := claimJobForRunner(ctx, runner, v)
			if errors.Is(err, errJobUnparsable) {
				// One repository's malformed workflow must never deny work to
				// every runner. Fail that job and keep scanning.
				failUnparsableJob(ctx, v, err)
				continue
			}
			if err != nil {
				return nil, false, err
			}
			if ok {
				return task, true, nil
			}
			// Another runner claimed this job concurrently; try the next one.
		}

		// A short page means no waiting jobs remain beyond it.
		if len(jobs) < pickTaskBatchSize {
			return nil, false, nil
		}
		last := jobs[len(jobs)-1]
		cursorUpdated, cursorID = last.Updated, last.ID
	}
}

// claimJobForRunner attempts to atomically claim job for runner inside its own
// transaction. Returns (task, true, nil) on success, or (nil, false, nil) when
// another runner wins the optimistic-lock race (the caller should try the next
// candidate job).
func claimJobForRunner(ctx context.Context, runner *ActionRunner, job *ActionRunJob) (*ActionTask, bool, error) {
	var resultTask *ActionTask

	err := db.WithTx(ctx, func(ctx context.Context) error {
		e := db.GetEngine(ctx)

		if err := job.LoadAttributes(ctx); err != nil {
			return err
		}

		now := timeutil.TimeStampNow()
		job.Started = now
		job.Status = StatusRunning

		task := &ActionTask{
			JobID:             job.ID,
			Attempt:           job.Attempt,
			RunnerID:          runner.ID,
			Started:           now,
			Status:            StatusRunning,
			RepoID:            job.RepoID,
			OwnerID:           job.OwnerID,
			CommitSHA:         job.CommitSHA,
			IsForkPullRequest: job.IsForkPullRequest,
		}
		task.GenerateAndFillToken()

		workflowJob, err := job.ParseJob()
		if err != nil {
			// Not an infrastructure failure: this job's payload is unusable and
			// always will be. Signal the outer loop, which fails this job and
			// continues, rather than failing the runner's whole request.
			return fmt.Errorf("%w: %w", errJobUnparsable, err)
		}

		if _, err := e.Insert(task); err != nil {
			return err
		}

		task.LogFilename = logFileName(job.Run.Repo.FullName(), task.ID)
		if err := UpdateTask(ctx, task, "log_filename"); err != nil {
			return err
		}

		if len(workflowJob.Steps) > 0 {
			steps := make([]*ActionTaskStep, len(workflowJob.Steps))
			for i, v := range workflowJob.Steps {
				steps[i] = &ActionTaskStep{
					Name:   makeTaskStepDisplayName(v, 255),
					TaskID: task.ID,
					Index:  int64(i),
					RepoID: task.RepoID,
					Status: StatusWaiting,
				}
			}
			if _, err := e.Insert(steps); err != nil {
				return err
			}
			task.Steps = steps
		}

		job.TaskID = task.ID
		n, err := UpdateRunJob(ctx, job, builder.And(builder.Eq{"task_id": 0}, builder.Eq{"status": StatusWaiting}))
		if err != nil {
			return err
		}
		if n != 1 {
			// Another runner claimed this job between our scan and this update;
			// signal the outer loop to move on without treating this as an error.
			return errJobAlreadyClaimed
		}

		task.Job = job
		resultTask = task
		return nil
	})

	if errors.Is(err, errJobAlreadyClaimed) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return resultTask, true, nil
}

// failUnparsableJob marks a job whose payload cannot be parsed as failed, so it
// leaves the waiting queue instead of being re-picked forever, and its run shows
// the reason. Best-effort: if this write fails the scheduler still moves on, and
// the job is simply retried on a later poll rather than wedging the instance.
func failUnparsableJob(ctx context.Context, job *ActionRunJob, cause error) {
	log.Error("actions: job %d has an unparsable workflow payload, failing it: %v", job.ID, cause)

	job.Status = StatusFailure
	job.Stopped = timeutil.TimeStampNow()
	if _, err := UpdateRunJob(ctx, job, builder.And(
		builder.Eq{"task_id": 0},
		builder.Eq{"status": StatusWaiting},
	), "status", "stopped"); err != nil {
		log.Error("actions: failed to mark job %d failed: %v", job.ID, err)
	}
}

// ReleaseTaskForRunner reverts a freshly-claimed but undelivered task: it deletes
// the task together with its steps and returns the job to the waiting queue. It is
// used when assembling the runner response fails after the job was already claimed,
// so the job is not stranded in running state with no runner ever executing it.
func ReleaseTaskForRunner(ctx context.Context, task *ActionTask) error {
	return db.WithTx(ctx, func(ctx context.Context) error {
		e := db.GetEngine(ctx)

		job, err := GetRunJobByRepoAndID(ctx, task.RepoID, task.JobID)
		if err != nil {
			return err
		}

		job.Status = StatusWaiting
		job.Started = 0
		job.TaskID = 0
		// Guard on task_id and status so we only release while the job still
		// references this task and has not progressed past running.
		n, err := UpdateRunJob(ctx, job, builder.Eq{"task_id": task.ID, "status": StatusRunning}, "status", "started", "task_id")
		if err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("release task %d: job %d no longer references it", task.ID, task.JobID)
		}

		if _, err := e.Delete(&ActionTaskStep{TaskID: task.ID}); err != nil {
			return err
		}
		if _, err := e.ID(task.ID).Delete(&ActionTask{}); err != nil {
			return err
		}
		return nil
	})
}

// maxDrops bounds how many times one job may be handed back to the queue. Past
// it the job fails like any other, so a job that no runner can ever get through
// cannot circulate forever.
const maxDrops = 3

// RequeueDroppedTask returns task's job to the waiting queue when the task ran
// none of its steps, and reports whether it did.
//
// act_runner's graceful shutdown protects a job that is executing, not one still
// being set up, so a runner stopped in that window — a pod rolling, a node going
// away — cancels the task and reports it failed. Failing the job there is wrong
// twice over: nothing ran, and nothing tells it apart from a job whose steps ran
// and failed.
//
// The steps' own record tells them apart. A step carries a Started stamp from the
// moment it begins, so a task whose every step is unstamped executed no user
// code: nothing it could have done has been done, and another runner may take the
// job without repeating anything. One stamp vetoes — that failure is the job's
// own and it stands.
func RequeueDroppedTask(ctx context.Context, task *ActionTask) (bool, error) {
	var job *ActionRunJob
	requeued := false

	err := db.WithTx(ctx, func(ctx context.Context) error {
		steps, err := GetTaskStepsByTaskID(ctx, task.ID)
		if err != nil {
			return err
		}
		// No steps recorded is no evidence, not evidence of nothing.
		if len(steps) == 0 {
			return nil
		}
		for _, step := range steps {
			if step.Started != 0 {
				return nil
			}
		}

		if job, err = GetRunJobByRepoAndID(ctx, task.RepoID, task.JobID); err != nil {
			return err
		}
		if job.Drops >= maxDrops {
			return nil
		}

		job.Status = StatusWaiting
		job.Started = 0
		job.Stopped = 0
		job.TaskID = 0
		job.Drops++
		// Guarded on the job still being this task's running job, so one that has
		// been cancelled, or claimed by someone else, is left where it is.
		n, err := UpdateRunJob(ctx, job, builder.Eq{"task_id": task.ID, "status": StatusRunning},
			"status", "started", "stopped", "task_id", "drops")
		if err != nil {
			return err
		}
		requeued = n == 1
		return nil
	})
	if err != nil {
		return false, err
	}

	switch {
	case requeued:
		log.Info("actions: job %d re-queued after runner loss, attempt %d/%d", job.ID, job.Drops, maxDrops)
	case job != nil && job.Drops >= maxDrops:
		log.Warn("actions: job %d has been dropped %d times, failing it", job.ID, job.Drops)
	}
	return requeued, nil
}

func UpdateTask(ctx context.Context, task *ActionTask, cols ...string) error {
	sess := db.GetEngine(ctx).ID(task.ID)
	if len(cols) > 0 {
		sess.Cols(cols...)
	}
	_, err := sess.Update(task)

	// Automatically delete the ephemeral runner if the task is done
	if err == nil && task.Status.IsDone() && util.SliceContainsString(cols, "status") {
		return DeleteEphemeralRunner(ctx, task.RunnerID)
	}
	return err
}

func getRunIDByTaskID(ctx context.Context, taskID int64) (runID int64, _ error) {
	if has, err := db.GetEngine(ctx).Cols("action_run_job.run_id").
		Table("action_task").
		Join("INNER", "action_run_job", "action_run_job.id = action_task.job_id").
		Where(builder.Eq{"action_task.id": taskID}).Get(&runID); err != nil {
		return runID, err
	} else if !has {
		return runID, util.ErrNotExist
	}
	return runID, nil
}

// UpdateTaskByState updates the task by the state.
// It will always update the task if the state is not final, even there is no change.
// So it will update ActionTask.Updated to avoid the task being judged as a zombie task.
func UpdateTaskByState(ctx context.Context, runnerID int64, state *runnerv1.TaskState) (*ActionTask, error) {
	stepStates := map[int64]*runnerv1.StepState{}
	for _, v := range state.Steps {
		stepStates[v.Id] = v
	}

	// Only one request can update the task because the final state needs to be calculated with all job states.
	// Otherwise, concurrent requests with transaction will make the SQL read stale job state and result in wrong final state.
	taskID := state.Id
	runID, err := getRunIDByTaskID(ctx, taskID)
	if err != nil {
		return nil, err
	}
	task := &ActionTask{}
	err = globallock.LockAndDo(ctx, fmt.Sprintf("UpdateTaskByState-run-%d", runID), func(ctx context.Context) error {
		if has, err := db.GetEngine(ctx).ID(taskID).Get(task); err != nil {
			return err
		} else if !has {
			return util.ErrNotExist
		} else if runnerID != task.RunnerID {
			return errors.New("invalid runner for task")
		}

		if task.Status.IsDone() {
			// the state is final, do nothing
			return nil
		}

		// state.Result is not unspecified means the task is finished
		if state.Result != runnerv1.Result_RESULT_UNSPECIFIED {
			if task.Status == StatusCancelling {
				// The runner may report SUCCESS/FAILURE for the cleanup phase; preserve user intent.
				task.Status = StatusCancelled
			} else {
				task.Status = StatusFromResult(state.Result)
			}
			task.Stopped = timeutil.TimeStamp(state.StoppedAt.AsTime().Unix())
			if err := UpdateTask(ctx, task, "status", "stopped"); err != nil {
				return err
			}
		} else {
			// Force update ActionTask.Updated to avoid the task being judged as a zombie task
			task.Updated = timeutil.TimeStampNow()
			if err := UpdateTask(ctx, task, "updated"); err != nil {
				return err
			}
		}

		// Record the reported step states before settling the job below. Whether any
		// step started decides that settlement, so the steps have to already say
		// everything this report has to say about them — a step that starts and
		// fails between two reports is only ever seen here.
		steps, err := GetTaskStepsByTaskID(ctx, task.ID)
		if err != nil {
			return err
		}
		task.Steps = steps

		for _, step := range steps {
			var result runnerv1.Result
			if v, ok := stepStates[step.Index]; ok {
				result = v.Result
				step.LogIndex = v.LogIndex
				step.LogLength = v.LogLength
				step.Started = convertTimestamp(v.StartedAt)
				step.Stopped = convertTimestamp(v.StoppedAt)
			}
			if result != runnerv1.Result_RESULT_UNSPECIFIED {
				step.Status = StatusFromResult(result)
			} else if step.Started != 0 {
				step.Status = StatusRunning
			}
			if _, err := db.GetEngine(ctx).ID(step.ID).Update(step); err != nil {
				return err
			}
		}

		if state.Result != runnerv1.Result_RESULT_UNSPECIFIED {
			// A runner stopped before it started a step reports the task failed. Its
			// job ran nothing, so it goes back to the queue instead of ending red.
			requeued := false
			if task.Status == StatusFailure {
				if requeued, err = RequeueDroppedTask(ctx, task); err != nil {
					return err
				}
			}
			if !requeued {
				if _, err := UpdateRunJob(ctx, &ActionRunJob{
					ID:      task.JobID,
					RepoID:  task.RepoID,
					Status:  task.Status,
					Stopped: task.Stopped,
				}, nil, "status", "stopped"); err != nil {
					return err
				}
			}
		}

		// Load the job last: callers read the settled status off it.
		return task.LoadAttributes(ctx)
	})
	return task, err
}

func StopTask(ctx context.Context, taskID int64, status Status) error {
	if !status.IsDone() && status != StatusCancelling {
		return fmt.Errorf("cannot stop task with status %v", status)
	}
	e := db.GetEngine(ctx)

	task := &ActionTask{}
	if has, err := e.ID(taskID).Get(task); err != nil {
		return err
	} else if !has {
		return util.ErrNotExist
	}
	if task.Status.IsDone() {
		return nil
	}

	now := timeutil.TimeStampNow()
	if status == StatusCancelling {
		runner, err := GetRunnerByID(ctx, task.RunnerID)
		if err != nil {
			if !errors.Is(err, util.ErrNotExist) {
				return err
			}
			status = StatusCancelled
		} else if !runner.HasCancellingSupport {
			status = StatusCancelled
		}
	}

	// Both job writes below are guarded on the job still pointing at this task, so
	// stopping a task never settles a job that has moved on — in particular one
	// RequeueDroppedTask has just handed back to the queue.
	stillOurs := builder.Eq{"task_id": taskID}

	if status == StatusCancelling {
		task.Status = StatusCancelling

		if _, err := UpdateRunJob(ctx, &ActionRunJob{
			ID:     task.JobID,
			RepoID: task.RepoID,
			Status: StatusCancelling,
		}, stillOurs, "status"); err != nil {
			return err
		}

		return UpdateTask(ctx, task, "status")
	}

	task.Status = status
	task.Stopped = now
	if _, err := UpdateRunJob(ctx, &ActionRunJob{
		ID:      task.JobID,
		RepoID:  task.RepoID,
		Status:  task.Status,
		Stopped: task.Stopped,
	}, stillOurs); err != nil {
		return err
	}

	if err := UpdateTask(ctx, task, "status", "stopped"); err != nil {
		return err
	}

	if err := task.LoadAttributes(ctx); err != nil {
		return err
	}

	for _, step := range task.Steps {
		if !step.Status.IsDone() {
			step.Status = status
			if step.Started == 0 {
				step.Started = now
			}
			step.Stopped = now
		}
		if _, err := e.ID(step.ID).Update(step); err != nil {
			return err
		}
	}

	return nil
}

func FindOldTasksToExpire(ctx context.Context, olderThan timeutil.TimeStamp, limit int) ([]*ActionTask, error) {
	e := db.GetEngine(ctx)

	tasks := make([]*ActionTask, 0, limit)
	// Check "stopped > 0" to avoid deleting tasks that are still running
	return tasks, e.Where("stopped > 0 AND stopped < ? AND log_expired = ?", olderThan, false).
		Limit(limit).
		Find(&tasks)
}

func convertTimestamp(timestamp *timestamppb.Timestamp) timeutil.TimeStamp {
	if timestamp.GetSeconds() == 0 && timestamp.GetNanos() == 0 {
		return timeutil.TimeStamp(0)
	}
	return timeutil.TimeStamp(timestamp.AsTime().Unix())
}

func logFileName(repoFullName string, taskID int64) string {
	ret := fmt.Sprintf("%s/%02x/%d.log", repoFullName, taskID%256, taskID)

	if setting.Actions.LogCompression.IsZstd() {
		ret += ".zst"
	}

	return ret
}

func getTaskIDFromCache(token string) int64 {
	if successfulTokenTaskCache == nil {
		return 0
	}
	tInterface, ok := successfulTokenTaskCache.Get(token)
	if !ok {
		return 0
	}
	t, ok := tInterface.(int64)
	if !ok {
		return 0
	}
	return t
}
