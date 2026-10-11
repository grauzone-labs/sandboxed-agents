# Issue tracker: GitHub

Issues and specs for this repo live as GitHub issues. Use the `gh` CLI for all operations.

## Conventions

- **Create an issue**: `gh issue create --title "..." --body-file <file>`. Use `--body-file <file>` for multi-line bodies. Then assign the mapped native issue type and add the issue to [Grauzone Labs – Entwicklung](https://github.com/orgs/grauzone-labs/projects/1), setting its initial Status as specified under [Project operations](#project-operations).
- **Read an issue**: `gh issue view <number> --comments`; add `--json body,labels,comments` for machine-readable output.
- **List issues**: `gh issue list --state open --json number,title,body,labels,comments --jq '[.[] | {number, title, body, labels: [.labels[].name], comments: [.comments[].body]}]'` with appropriate `--label` and `--state` filters.
- **Comment on an issue**: `gh issue comment <number> --body "..."`
- **Apply / remove labels**: `gh issue edit <number> --add-label "..."` / `--remove-label "..."`
- **Close**: `gh issue close <number> --comment "..."`

Infer the repo from `git remote -v` - `gh` does this automatically when run inside a clone.

## Issue types

Use GitHub organization issue types for issue classification. Set a type with the GitHub REST API because the installed `gh` CLI has no `issue --type` flag. The org types were verified with `gh api orgs/grauzone-labs/issue-types`:

| Work role | Native issue type |
| --- | --- |
| Overall specification or wayfinding map | `Epic` |
| Feature request | `Feature` |
| User-facing implementation | `User Story` |
| Technical work, research, prototype, or grilling decision ticket | `Task` |
| Defect | `Bug` |

These type mappings complement the seven triage labels in `docs/agents/triage-labels.md`; use the labels for category and state, and native issue types for work shape. Do not create type-lookalike labels.

- **Set an issue type**: `gh api --method PATCH repos/grauzone-labs/sandboxed-agents/issues/<n> -f type='<issue-type>' --jq '{number,type:.type.name}'`. Use the mapped type above.
- **Verify an issue type**: `gh api repos/grauzone-labs/sandboxed-agents/issues/<n> --jq '{number,type:.type.name}'` and confirm it matches the mapping. The GitHub REST endpoint accepts `type` and returns it on read ([Update an issue](https://docs.github.com/en/rest/issues/issues#update-an-issue)).

## Project operations

Issues belong in [Grauzone Labs – Entwicklung](https://github.com/orgs/grauzone-labs/projects/1), organization project **#1** (`PVT_kwDOFFIRnc4BmkBR`), linked to `grauzone-labs/sandboxed-agents`. It is open and private. The installed `gh` CLI lacks `gh project` support, so use `gh api graphql`.

The verified Status field and options are:

| Status | Option ID | Use |
| --- | --- | --- |
| Backlog | `f75ad846` | Default for new work, including untriaged or unspecified issues |
| Ready | `6dc1d98c` | Fully specified and unblocked work |
| In Progress | `47fc9ee4` | Work has started |
| In Review | `6e72a95f` | Implementation is awaiting review |
| Done | `98236657` | Completion or an explicitly closed resolution has been verified |

The Priority field exists with no options configured. Leave Priority unset until its options are configured. Project reads require `read:project` or `project` scope; enrollment and Status updates require `project` scope. Project membership and Status are set explicitly and read back; do not assume an auto-add or status automation workflow.

- **Discover project configuration** (read-only recovery lookup): `gh api graphql -f query='query($org:String!,$number:Int!){organization(login:$org){projectV2(number:$number){id title url number closed fields(first:100){nodes{... on ProjectV2SingleSelectField{id name options{id name}}}}}}}' -f org=grauzone-labs -F number=1 --jq '.data.organization.projectV2'`.
- **Get an issue's GraphQL node ID**: `gh api repos/grauzone-labs/sandboxed-agents/issues/<n> --jq .node_id`.
- **Add an issue to the project**: after retrieving the issue node ID, run `gh api graphql -f query='mutation($project:ID!,$issue:ID!){addProjectV2ItemById(input:{projectId:$project,contentId:$issue}){item{id}}}' -f project=PVT_kwDOFFIRnc4BmkBR -f issue=<issue-node-id> --jq .data.addProjectV2ItemById.item.id`. This returns `<project-item-id>`.
- **Set project Status**: `gh api graphql -f query='mutation($project:ID!,$item:ID!,$field:ID!,$option:String!){updateProjectV2ItemFieldValue(input:{projectId:$project,itemId:$item,fieldId:$field,value:{singleSelectOptionId:$option}}){projectV2Item{id}}}' -f project=PVT_kwDOFFIRnc4BmkBR -f field=PVTSSF_lADOFFIRnc4BmkBRzhlMhhg -f item=<project-item-id> -f option=<status-option-id> --jq .data.updateProjectV2ItemFieldValue.projectV2Item.id`.
- **Find an existing project item**: `gh api graphql --paginate -f query='query($n:Int!,$endCursor:String){repository(owner:"grauzone-labs",name:"sandboxed-agents"){issue(number:$n){projectItems(first:100,after:$endCursor){nodes{id project{id}}pageInfo{hasNextPage endCursor}}}}}' -F n=<n> --jq '.data.repository.issue.projectItems.nodes[] | select(.project.id == "PVT_kwDOFFIRnc4BmkBR") | .id'`. No item ID means the issue is not enrolled; add it through the enrollment command when appropriate.
- **Verify project Status**: `gh api graphql -f query='query($item:ID!){node(id:$item){... on ProjectV2Item{project{id number} fieldValueByName(name:"Status"){... on ProjectV2ItemFieldSingleSelectValue{name optionId}}}}}' -f item=<project-item-id> --jq '.data.node | {project, status:.fieldValueByName}'`. Confirm project ID `PVT_kwDOFFIRnc4BmkBR`, project number `1`, status name, and option ID.

For a new item, create the issue, set its mapped issue type, add it to the project, set Status to `Backlog` by default (or `Ready` only when fully specified and unblocked), then read back both the issue type and project Status. Move it to `In Progress` when work starts, `In Review` when implementation awaits review, and `Done` only after verifying completion or the closed resolution.

## Implementation operations

Used by the `implement-work-item` skill. The work item ID is the issue number.

- **Target branch**: `main`.
- **Allowed merge methods**: rebase, squash, and merge commit. All three are enabled for `grauzone-labs/sandboxed-agents`; the repository has no rulesets.

- **Blocking dependencies**: `gh api repos/grauzone-labs/sandboxed-agents/issues/<n>/dependencies/blocked_by --jq '[.[] | {number, state}]'` lists every blocker, including closed ones. `issue_dependencies_summary.blocked_by` on the issue counts open blockers only.
- **Parent chain**: `gh api repos/grauzone-labs/sandboxed-agents/issues/<n>/parent` returns the parent issue; repeat it on each parent until the call returns 404.
- **Merged implementation of a dependency**: read the pull requests that closed the blocker and whether each is merged into the target branch:

  ```sh
  gh api graphql -f query='query($o:String!,$r:String!,$n:Int!){repository(owner:$o,name:$r){issue(number:$n){state stateReason closedByPullRequestsReferences(first:10,includeClosedPrs:true){nodes{number merged baseRefName mergeCommit{oid}}}}}}' \
    -f o=grauzone-labs -f r=sandboxed-agents -F n=<n> --jq .data.repository.issue
  ```

  The implementation is merged when a node has `merged: true` and `baseRefName` is `main`. A closed blocker without such a pull request is unverified.
- **Commit references**: `#<n>` in a commit message links the commit to the issue.
- **Tick an acceptance criterion**: `gh issue view <n> --json body --jq .body > body.md`, change the criterion's `- [ ]` to `- [x]` in `body.md`, then `gh issue edit <n> --body-file body.md`. Edit only the checkbox; the rest of the body stays as it was.
- **List open pull requests**: `gh pr list --base main --state open`.
- **Open a pull request**: `gh pr create --base main --head <work-branch> --title "..." --body-file <file>`, with `Closes #<n>` in the body as the closing link.
- **Read checks and reviews**: `gh pr checks <pr> --required` lists the required status checks; add `--watch` to wait for them. `gh pr view <pr> --json reviewDecision,reviews` returns the review state, and `gh api repos/grauzone-labs/sandboxed-agents/pulls/<pr>/comments` the review comments.
- **Read the conflict state**: `gh pr view <pr> --json mergeable,mergeStateStatus`. `MERGEABLE` means the pull request can merge, `CONFLICTING` means the work branch needs a sync, and `UNKNOWN` means GitHub is still computing; read it again.
- **Complete with a rebase merge**: `gh pr merge <pr> --rebase --delete-branch --match-head-commit <reviewed-head-sha>`. `--match-head-commit` refuses the merge when the head moved after the review. `--delete-branch` deletes the remote branch and the local one; in a worktree that has the branch checked out, the local deletion can fail after the merge succeeded.
- **Confirm the result**: `gh pr view <pr> --json state,mergedAt` shows `MERGED`, `git ls-remote --heads origin <work-branch>` returns nothing, and the GraphQL query above shows the issue `CLOSED` with `stateReason` `COMPLETED`.

## Pull requests as a triage surface

**PRs as a request surface: no.** _(Set to `yes` if this repo treats external PRs as feature requests; the `triage` skill reads this flag.)_

When set to `yes`, PRs run through the same labels and states as issues, using the `gh pr` equivalents:

- **Read a PR**: `gh pr view <number> --comments` and `gh pr diff <number>` for the diff.
- **List external PRs for triage**: `gh api --paginate 'repos/grauzone-labs/sandboxed-agents/pulls?state=open&per_page=100'` then keep only `author_association` of `CONTRIBUTOR`, `FIRST_TIME_CONTRIBUTOR`, or `NONE` (drop `OWNER`/`MEMBER`/`COLLABORATOR`).
- **Comment / label / close**: `gh pr comment`, `gh pr edit --add-label`/`--remove-label`, `gh pr close`.

GitHub shares one number space across issues and PRs, so a bare `#42` may be either - resolve with `gh pr view 42` and fall back to `gh issue view 42`.

## When a skill says "publish to the issue tracker"

Create a GitHub issue, assign its mapped native issue type, and add it to Project #1 with the appropriate Status, following [Issue types](#issue-types) and [Project operations](#project-operations).

## When a skill says "fetch the relevant ticket"

Run `gh issue view <number> --comments` and read its native type and Project #1 Status; use the mappings and operations in [Issue types](#issue-types) and [Project operations](#project-operations).

## Wayfinding operations

Used by the `wayfinder` skill. The **map** is a single issue with **child** issues as tickets.

- **Map**: a single `Epic` issue labelled `wayfinder:map`, with the canonical map body from the `wayfinder` skill. Create it with `gh issue create --label wayfinder:map --title "..." --body-file <file>`, then set type `Epic`, add it to Project #1, and set its initial Status under [Project operations](#project-operations).
- **Child ticket**: create the issue with its mapped native type (`Task` for research, prototype, grilling, or technical decision tickets; otherwise use the role mapping under [Issue types](#issue-types)) and add it to Project #1 with an initial Status under [Project operations](#project-operations). Link it to the map as a GitHub sub-issue (`gh api` on the sub-issues endpoint). Put `Wayfinding order: <NN>` at the top of every child body, assigning consecutive numbers in breadth-first discovery order. Where sub-issues aren't enabled, put `Part of #<map>` immediately above that line; this marker is the fallback child relationship. Labels: `wayfinder:<type>` (`research`/`prototype`/`grilling`/`task`). Once claimed, the ticket is assigned to the driving dev.
- **Blocking**: GitHub's **native issue dependencies** - the canonical, UI-visible representation. Add an edge with `gh api --method POST repos/grauzone-labs/sandboxed-agents/issues/<child>/dependencies/blocked_by -F issue_id=<blocker-db-id>`, where `<blocker-db-id>` is the blocker's numeric **database id** (`gh api repos/grauzone-labs/sandboxed-agents/issues/<n> --jq .id`, _not_ the `#number` or `node_id`). GitHub reports `issue_dependencies_summary.blocked_by` (open blockers only - the live gate). Where dependencies aren't available, fall back to a `Blocked by: #<n>, #<n>` line after the child's relationship and order markers. A ticket is unblocked when every blocker is closed.
- **Frontier query**: list the map's open sub-issues with `gh api --paginate`. Where sub-issues aren't enabled, search open issues for the exact top-of-body `Part of #<map>` marker (for example, `gh issue list --state open --limit 500 --search '"Part of #<map>" in:body'`) and retain only exact matches. Drop any ticket with an open blocker (`issue_dependencies_summary.blocked_by > 0`, or an open issue in the `Blocked by` line) or an assignee, then sort by `Wayfinding order` ascending. The lowest number wins.
- **Claim**: `gh issue edit <n> --add-assignee @me` - the session's first write. Set the project Status to `In Progress` using [Project operations](#project-operations) when work starts.
- **Resolve**: `gh issue comment <n> --body "<answer>"`, then `gh issue close <n>`, set its Project #1 Status to `Done` after verifying the resolution, then append its index line (the ticket's name linked, then a one-line gist) to the map's Decisions so far.
