# Community governance and review process

This document describes maintainer expectations, project governance, and the detailed pull request review workflow (labels, merge queue, commit message format for mergers). For what contributors should do when opening and updating a PR, see [CONTRIBUTING.md](../CONTRIBUTING.md).

## Table of contents

- [Community governance and review process](#community-governance-and-review-process)
  - [Table of contents](#table-of-contents)
  - [Code review](#code-review)
    - [Milestone](#milestone)
    - [Labels](#labels)
    - [Reviewing PRs](#reviewing-prs)
      - [For reviewers](#for-reviewers)
    - [Getting PRs merged](#getting-prs-merged)
    - [Final call](#final-call)
    - [Commit messages](#commit-messages)
      - [PR Co-authors](#pr-co-authors)
      - [PRs targeting `main`](#prs-targeting-main)
      - [Backport PRs](#backport-prs)

## Code review

### Milestone

A PR should only be assigned to a milestone if it will likely be merged into the given version. \
PRs without a milestone may not be merged.

### Labels

Almost all labels used inside Hanzo Forge can be classified as one of the following:

- `modifies/…`: Determines which parts of the codebase are affected. These labels will be set through the CI.
- `topic/…`:  Determines the conceptual component of Hanzo Forge that is affected, i.e. issues, projects, or authentication. At best, PRs should only target one component but there might be overlap. Must be set manually.
- `type/…`: Determines the type of an issue or PR (feature, refactoring, docs, bug, …). If GitHub supported scoped labels, these labels would be exclusive, so you should set **exactly** one, not more or less (every PR should fall into one of the provided categories, and only one).
- `issue/…` / `lgtm/…`: Labels that are specific to issues or PRs respectively and that are only necessary in a given context, i.e. `issue/not-a-bug` or `lgtm/need 2`

Every PR should be labeled correctly with every label that applies.

There are also some labels that will be managed automatically.\
In particular, these are

- the amount of pending required approvals
- has all `backport`s or needs a manual backport

### Reviewing PRs

Maintainers are encouraged to review pull requests in areas where they have expertise or particular interest.

#### For reviewers

- **Verification**: Verify that the PR accurately reflects the changes, and verify that the tests and documentation are complete and aligned with the implementation.
- **Actionable feedback**: Say what should change and why, and distinguish required changes from optional suggestions.
- **Feedback**: Focus feedback on the issue itself and avoid comments about the contributor's abilities.
- **Request changes**: If you request changes (i.e., block a PR), give a clear rationale and, whenever possible, a concrete path to resolution.
- **Approval**: Only approve a PR when you are fully satisfied with its current state - "rubber-stamp" approvals need to be highlighted as such.

### Getting PRs merged

Changes to Hanzo Forge must be reviewed before they are accepted, including changes from owners and maintainers. The exception is critical bugs that prevent Hanzo Forge from compiling or starting.

We require two maintainer approvals for every PR. When that is satisfied, your PR gets the `lgtm/done` label. After that, you mainly fix merge conflicts and respond to or implement maintainer requests; maintainers drive getting the PR merged.

If a PR has `lgtm/done`, no open discussions, and no merge conflicts, any maintainer may add `reviewed/wait-merge`. That puts the PR in the merge queue. PRs are merged from the queue in the order of this list:


The merge queue is maintained by hand: a maintainer removes the `reviewed/wait-merge` label after a merge, and opens a backport PR when one is needed. Upstream automates this with a bot; this fork does not run one.

- Creates a backport PR when needed after the initial PR merges.
- Removes the PR from the merge queue after it merges.
- Keeps the oldest branch in the merge queue up to date with merges.

### Final call

If a PR has been ignored for more than 7 days with no comments or reviews, and the author or any maintainer believes it will not survive a long wait (such as a refactoring PR), they can send "final call" to the TOC by mentioning them in a comment.

After another 7 days, if there is still zero approval, this is considered a polite refusal, and the PR will be closed to avoid wasting further time. Therefore, the "final call" has a cost, and should be used cautiously.

However, if there are no objections from maintainers, the PR can be merged with only one approval from the TOC (not the author).

### Commit messages

Mergers are required to rewrite the PR title and the first comment (the summary) when necessary so the squash commit message is clear.
Usually the Pull Request description and commit message body should not be empty, unless the title is already clear enough or the description would be a copy of the comments in code.

The final commit message:

- should match the code changes.
- should only keep true co-authors, false-positive co-authors should be removed.
- should not hedge: replace phrases like `hopefully, <x> won't happen anymore` with definite wording.
- should not contain hidden information like `<!-- -->` or extra information after the description's divider `----`.
- should not contain unrelated contents (e.g.: Release Notes, Configuration, etc.) from a Renovate update PR.

#### PR Co-authors

A person counts as a PR co-author once they (co-)authored a commit that is not simply a `Merge base branch into branch` commit. Mergers must remove such false-positive co-authors when writing the squash message. Every true co-author must remain in the commit message.

#### PRs targeting `main`

The commit message of PRs targeting `main` is always

```bash
$PR_TITLE ($PR_INDEX)

$REWRITTEN_PR_SUMMARY
```

#### Backport PRs

The commit message of backport PRs is always

```bash
$PR_TITLE ($INITIAL_PR_INDEX) ($BACKPORT_PR_INDEX)

$REWRITTEN_PR_SUMMARY
```
