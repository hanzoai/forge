// Copyright 2026 Hanzo AI, Inc. All rights reserved.
// SPDX-License-Identifier: MIT

// Package runner holds the values a runner and the forge exchange: the input
// and output of each of the five ops the forge serves under /v1/runner.
//
// It is a leaf on purpose. The runner is a separate module and imports this
// package to get the very types the handlers declare, so there is one
// definition of the protocol rather than a server copy and a client copy that
// drift. Nothing here may import the rest of the forge.
//
// Two rules govern every field, and both come from the wire rather than from
// taste. ZAP gives a field an offset and a width, so:
//
//   - No map. A map has neither, and it is refused outright when the layout is
//     derived. A set of names and values crosses as an ordered []Pair.
//   - No time.Time. Every one of its fields is unexported, so it derives an
//     empty layout: it is accepted, carries nothing, and arrives as the zero
//     time with no error anywhere. Every instant here is an int64 of unix
//     nanoseconds, and 0 means unset. Nanoseconds rather than milliseconds
//     because the stored log format keeps 100ns ticks.
//
// Field ORDER is part of the wire: a field is found at its offset, not by its
// name. Append at the end, and only at the end.
package runner

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

// Credential is what a registered runner presents on every op but register.
//
// It is embedded rather than repeated so it is declared once and serves both
// faces of the same op. Over HTTP the two values arrive as headers, which is
// what the `header:` tags say and what `json:"-"` makes the ONLY way to present
// them: a body carrying a uuid and a token reaches the handler with both fields
// empty. Over the call plane there are no headers, so they travel as ordinary
// arguments in their own slots.
type Credential struct {
	UUID  string `json:"-" header:"x-runner-uuid"`
	Token string `json:"-" header:"x-runner-token"`
}

// Pair is one name and the value under it, and it is how every set of named
// values on this protocol crosses.
type Pair struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

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

// RegisterIn trades a registration token for a runner identity. It is the one
// input with no Credential, because a runner has none until this answers; the
// token here is the REGISTRATION token, a different secret from the runner
// token the reply carries.
type RegisterIn struct {
	Name         string   `json:"name"`
	Token        string   `json:"token"`
	Version      string   `json:"version"`
	Labels       []string `json:"labels"`
	Ephemeral    bool     `json:"ephemeral"`
	Capabilities []string `json:"capabilities"`
}

// RegisterOut carries the minted runner credential in cleartext, exactly once.
type RegisterOut struct {
	Runner Identity `json:"runner"`
}

// DeclareIn republishes what a registered runner can do. A runner sends it on
// start and after its configuration changes, so labels and version follow the
// runner without re-registering it.
type DeclareIn struct {
	Credential
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

// TaskIn asks for work. Queue is the queue version the runner last saw; when it
// equals the forge's, nothing has been queued since and the forge answers
// without opening the assignment transaction.
type TaskIn struct {
	Credential
	Queue int64 `json:"queue"`
}

// TaskOut carries an assigned task, or no task and the current queue version
// for the runner to ask against next time. A nil Task is the "no work" answer
// and the only one: a present task always has a nonzero ID.
type TaskOut struct {
	Task  *Task `json:"task,omitempty"`
	Queue int64 `json:"queue"`
}

// Task is one job, expanded and ready to execute. ID is unique for all time —
// unlike a run or job id, it is never reused.
type Task struct {
	ID       int64   `json:"id"`
	Workflow []byte  `json:"workflow"`
	Context  Context `json:"context"`
	Secrets  []Pair  `json:"secrets,omitempty"`
	Vars     []Pair  `json:"vars,omitempty"`
	Needs    []Need  `json:"needs,omitempty"`
}

// Need is what one job this task depends on produced. Job is the key the
// forge's needs map held, promoted to a field so the set crosses as a list.
type Need struct {
	Job     string `json:"job"`
	Result  Result `json:"result"`
	Outputs []Pair `json:"outputs,omitempty"`
}

// Context is what a job evaluates its expressions against, and what the runner
// hands to act as the github context.
//
// It is a struct, and that is the change with the most behaviour behind it. As
// a map the two sides could disagree in silence, and did: the forge wrote
// git_runtime_token while the runner read gitea_runtime_token and quietly fell
// back to the task token, which nothing anywhere reported. A field is a compile
// error instead.
//
// It carries what the forge computes AND the runner reads, and nothing else.
// The map shipped about thirteen keys that were always the empty string —
// action, action_path, env, path, job's siblings — because act fills those in
// itself; they crossed the wire to be discarded.
type Context struct {
	// Event is the webhook payload that triggered the run, as opaque JSON. It
	// stays bytes because its shape belongs to the event and not to us: the
	// runner hands it to act, which evaluates github.event.* against it.
	Event           []byte `json:"event"`
	EventName       string `json:"event_name"`
	Job             string `json:"job"`
	RunID           string `json:"run_id"`
	RunNumber       string `json:"run_number"`
	RunAttempt      string `json:"run_attempt"`
	Actor           string `json:"actor"`
	Repository      string `json:"repository"`
	RepositoryOwner string `json:"repository_owner"`
	Ref             string `json:"ref"`
	RefName         string `json:"ref_name"`
	RefType         string `json:"ref_type"`
	HeadRef         string `json:"head_ref"`
	BaseRef         string `json:"base_ref"`
	Sha             string `json:"sha"`
	ServerURL       string `json:"server_url"`
	APIURL          string `json:"api_url"`
	RetentionDays   string `json:"retention_days"`
	// Token is the job's credential against the forge's own API — github.token
	// in an expression.
	Token string `json:"token"`
	// RuntimeToken authorizes the job against the actions runtime: artifacts,
	// the cache. The runner passes it as ACTIONS_RUNTIME_TOKEN and masks it out
	// of the log.
	RuntimeToken string `json:"runtime_token"`
	// ActionsURL is where act fetches an action repository from when a step
	// names one. A runner that receives nothing here composes an unusable URL
	// and every job dies before its first step, so the runner supplies its own
	// fallback rather than trusting this to be set.
	ActionsURL string `json:"actions_url"`
}

// StateIn reports progress. Outputs are sent incrementally: the reply names
// every key the forge has stored, so the runner stops resending them.
type StateIn struct {
	Credential
	State   State  `json:"state"`
	Outputs []Pair `json:"outputs,omitempty"`
}

// StateOut echoes the task's result as the forge now holds it, which is how a
// runner learns its task was cancelled from elsewhere. Stored names the outputs
// the forge has written down.
type StateOut struct {
	State  State    `json:"state"`
	Stored []string `json:"stored,omitempty"`
}

// State is a task's progress and that of each of its steps. Started and Stopped
// are unix nanoseconds, 0 for unset.
type State struct {
	ID      int64  `json:"id"`
	Result  Result `json:"result"`
	Started int64  `json:"started,omitempty"`
	Stopped int64  `json:"stopped,omitempty"`
	Steps   []Step `json:"steps,omitempty"`
}

// Step is one step's progress and the slice of the task log it wrote. Started
// and Stopped are unix nanoseconds, 0 for unset.
type Step struct {
	ID        int64  `json:"id"`
	Result    Result `json:"result"`
	Started   int64  `json:"started,omitempty"`
	Stopped   int64  `json:"stopped,omitempty"`
	LogIndex  int64  `json:"log_index"`
	LogLength int64  `json:"log_length"`
}

// LogIn appends console output. Index is the position of the first line in the
// task's log, so a resent batch overlaps rather than duplicates. Last seals the
// log and moves it to storage.
type LogIn struct {
	Credential
	Task  int64  `json:"task"`
	Index int64  `json:"index"`
	Lines []Line `json:"lines,omitempty"`
	Last  bool   `json:"last,omitempty"`
}

// LogOut reports how far the log is durable: Ack is index + lines accepted, and
// the runner resends from there.
type LogOut struct {
	Ack int64 `json:"ack"`
}

// Line is one line of console output. Time is unix nanoseconds.
type Line struct {
	Time    int64  `json:"time"`
	Content string `json:"content"`
}
