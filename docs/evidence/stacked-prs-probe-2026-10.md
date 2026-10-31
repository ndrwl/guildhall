# Stacked pull requests on ndrwl/guildhall: documented facts and an unrun probe kit (d113, 2026-10)

Task d113, research-2, 2026-10-31, kept by build-1 for the decision [Slices stay sequential](../decisions/stacked-prs-not-yet.md).
It carries the research's reading of GitHub's documentation, the questions a live probe would settle, a checklist for running it by hand, and the probe script.

**Nothing here about ndrwl/guildhall's live behaviour is confirmed.**
No task grant can allow `gh`, so no Artisan could run the probe, and no probe output exists.

**`probe.sh` has never been run, not even `bash -n`.**
It opens, labels and merges pull requests, creates and deletes branches and changes branch protection on the live repository, on a scratch base branch and never on main.
It is kept as a Markdown code block, not as an executable file in the tree, so nothing runs it by accident.
Run it only by hand, following section 3, and expect small fixes on first run.

## 1. What GitHub's documentation says (read 2026-10-31)

Sources:

- [About stacked PRs](https://docs.github.com/en/pull-requests/get-started/about-stacked-prs)
- [Stacked PR APIs and webhooks](https://docs.github.com/en/pull-requests/reference/stacked-pull-requests-apis-and-webhooks)
- [REST stacks endpoints](https://docs.github.com/en/rest/pulls/stacks)
- [REST pulls (merge-async)](https://docs.github.com/en/rest/pulls/pulls?apiVersion=2026-03-10)
- [gh stack CLI reference](https://docs.github.com/en/pull-requests/reference/stacked-prs-cli-commands)

These were read through a summarising fetch tool. Quoted text is as that tool reported it.

### Facts the docs state

- **D1. Merge order and partial merges.** PRs "must merge from the bottom up". Merging a mid-stack PR merges everything beneath it. After a partial merge, "the pull requests above stay open and automatically re-target the stack's base branch" and "the remaining branches are automatically rebased".
- **D2. Merge methods.** "Stacks support merge commit, squash, and rebase merge methods." The resulting history "is the same as merging each pull request individually, starting from the bottom".
- **D3. Branch protection.** Rules "are enforced on every pull request in the stack". The requirements "are determined by the bottom pull request's base branch". The up-to-date ("strict") requirement is **not mentioned**.
- **D4. Legacy merge is refused for stacks.** "A stack cannot be merged with the legacy synchronous merge endpoints or mutations." Stacks need the asynchronous merge, `PUT /repos/{o}/{r}/pulls/{n}/merge-async`.
  - Body fields: `sha`, `merge_method` (`merge`, `squash` or `rebase`), `merge_action` (`default`, `direct_merge` or `merge_queue`) and `bypass_rules` (default false).
  - Status codes: 202 accepted, 200 already merged or queued, 409 already enqueued, plus 400, 403, 404 and 422.
  - The result is polled with `GET …/merge-async/{uuid}` and kept for 24 h.
  - Protection and rulesets are evaluated only when the merge runs, and a failure appears only in the polled result.
  - The docs say a stack merge request is atomic.
- **D5. Stacks REST API.** These endpoints are documented under API version 2022-11-28:
  - `GET /repos/{o}/{r}/stacks?pull_request=N`
  - `POST /repos/{o}/{r}/stacks` with `{"pull_requests":[bottom…top]}`. Each PR's base ref must equal the previous PR's head ref. It returns 201, or 422 if the PRs cannot form a stack.
  - `GET /stacks/{n}`
  - `POST /stacks/{n}/add`, which can return 409 while the stack is being modified.
  - `POST /stacks/{n}/unstack`. It returns 204 when the stack is dissolved, and PRs queued for merge stay.

  The REST PR object gains an optional `stack` field: `{id, number, size, position, base{ref,sha}}`. GraphQL is read-only for stacks.
- **D6. What the docs leave out.** No token or permission requirements are documented for the stacks or merge-async endpoints. No 404 or 422 for "feature not enabled" is documented. The CLI reference does give an exit code (9) for "stacked pull requests must be enabled for the repository", so per-repository enablement exists.
- **D7. `gh stack link`.** Syntax: `gh stack link [--base B] <stack|branch-or-pr> <branch-or-pr>…`.
  - Branch arguments are pushed, and PRs are created for branches that have none.
  - It "corrects" mismatched bases.
  - So the probe passes **PR numbers only**, never branch names.
  - `gh stack merge` merges everything up to the chosen PR, all or nothing. Guildhall must never use it (d88).
- **D8. CI.** "CI checks triggered by pull requests on your default branch run for all pull requests in the stack." Guildhall's CI triggers on every `pull_request`, with no base filter (`.github/workflows/ci.yml:3-7`), so CI runs on PRs into `probe/stack-base` and into lower layers.
- **D9. Not supported.** Cross-fork stacks and GitHub Desktop. All branches must be in one repository.

### Status of the feature

The d88 research recorded a GA changelog of 2026-10-06 "available on all github.com plans". The CLI and API pages still call the feature preview and "subject to change".

## 2. Open questions the probe settles

Each question names the `probe-out/` files that answer it.

- **Q1. Is the feature on for ndrwl/guildhall, a personal-account repository?**
  - Files: `read-stacks-list.*` (200 vs 404) and `stack-create.*` (201 vs 404/422).
- **Q2. Does Guildhall's gh token reach the Stacks API?**
  - Files: `read-auth.err` (token type prefix and scopes, masked by gh), `read-stacks-list-headers.out` (`X-OAuth-Scopes`, `X-Accepted-GitHub-Permissions`) and `stack-create.*`.
  - If REST refuses, the result of the fallback, `stack-link-gh.*`.
- **Q3. How does up-to-date protection treat a stack?**
  - Bottom layer behind its protected base: `snap-behind-*` should show `mergeStateStatus` BEHIND for L1. It also shows what L2 and L3 report.
  - Does GitHub auto-update layers when the base moves without a stack merge? Compare `snap-behind-*` with `snap-behind-later-*`.
  - Does the legacy merge of a behind L1 get refused? `merge-l1-behind.*`
- **Q4. Can a bottom layer be merged with Guildhall's current command,** `gh pr merge --squash --match-head-commit`?
  - File: `merge-l1-legacy.*`. A refusal citing stacks means a merge-async path is mandatory.
  - The async fallback, its status code and its polled result: `merge-l1-async*`.
- **Q5. After Guildhall force-pushes an up-to-date L1, as the d23 rebaser does, does GitHub rebase L2 onto it?**
  - Files: `snap-after-l1-push-*` and `snap-after-l1-push-later-*`.
- **Q6. After L1 squash-merges, what happens to L2 and L3?**
  - Retargeted base: `baseRefName` in `poll-merge1-*`.
  - New head, and how many seconds after the merge: `poll-merge1.tsv`.
  - Whether L1's original commits are gone from L2: `analysis-merge1.txt` (`is-ancestor`, file list, behind count).
  - Whether L2's own change is patch-identical before and after: `analysis-merge1.txt` (patch-id of the whole change against its base).
  - Whether CI re-runs on the new head, and whether the PR becomes mergeable without help: `snap-merge1-end-*`.
- **Q7. Does GitHub's rebase race with Guildhall's?** The probe merges L2 and immediately force-pushes a Guildhall-style rebase of L3, as the merge queue would.
  - Files: `poll-race.tsv` and `analysis-race.txt`.
  - It shows whose head L3 ends at, whether GitHub overwrites the push or gives up, and whether `--match-head-commit` with Guildhall's head then succeeds (`merge-l3-match.*`).
- **Q8. Is a mid-stack legacy merge refused?** Optional `hazard` phase: `hazard-merge-mid.*`.
  - If it is not refused, it merges a layer into the branch below. Guildhall would never do this, but it would show the forge gives no guard.
- **Q9. Merge-commit method.** An optional second run with `METHOD=merge` answers Q4, Q6 and Q7 again for merge commits.
- **Q10. What happens on unstack and cleanup?** File: `cleanup-*`. Probe PRs are closed, branches deleted and the label removed if the probe created it, with nothing left on main (`cleanup-main-check.txt`).

## 3. Checklist for the Guildmaster (run by hand)

1. **Prerequisites.**
   - `gh` signed in as the account Guildhall uses (`gh auth status`), plus `git`, `jq` and bash.
   - A clean clone of ndrwl/guildhall with remote `origin`.
   - Optional: `gh extension install github/gh-stack`, used only as the fallback for `gh stack link`.
2. **Save the script.** Copy section 4 to `probe.sh` outside the clone, then `chmod +x probe.sh`. Run `bash -n probe.sh` first. Run every phase from the clone's root, with `OUT` pointing outside the clone, for example `OUT=~/d113-probe-out`.
3. **`./probe.sh read`.** Read-only: versions, auth (masked), repository settings, main's protection and rulesets, and the stacks list. Stop here if `read-main-protection.rc` is not 0, which means main is protected by rulesets rather than classic protection. Send the output instead, and the analysis adjusts setup.
4. **`./probe.sh setup`.** Creates `probe/stack-base` at main's tip, copies main's classic protection to it (strict up-to-date, the same required checks and the same admin enforcement, with no required reviews), and creates the `probe` label if missing.
5. **`./probe.sh stack`.** Pushes `probe/stack-1..3`, each with two commits adding only files under `probe-d113/`, plus `probe/stack-bump`. It opens four `probe`-labelled PRs titled "[probe d113] … do not merge" and links L1..L3 with `POST /stacks`, falling back to `gh stack link <numbers>`.
6. **`./probe.sh wait`.** Waits until CI on every probe PR finishes, which takes about one CI run. Run this again before each later phase if CI is still running.
7. **`./probe.sh behind`.** Merges the bump PR into `probe/stack-base`, so L1 is behind. It snapshots, tries a legacy merge of the behind L1 (expected refused), force-pushes L1 rebased onto the new base as Guildhall's rebaser would, and snapshots again. Then run `./probe.sh wait`.
8. **`./probe.sh merge1`.** Squash-merges L1 at its head, first with Guildhall's own command and with merge-async only if that is refused. It then polls L2 and L3 every 5 s for 3 min and writes `analysis-merge1.txt`. Then run `./probe.sh wait`.
9. **`./probe.sh race`.** Merges L2, the new bottom, and pushes a Guildhall-style rebase of L3 within about a second. It polls for 3 min, then tries `gh pr merge L3 --match-head-commit <Guildhall's head>` once checks pass. Run `./probe.sh wait` first if L2's checks are pending.
10. **Optional: `./probe.sh hazard`.** Run only if a fresh stack is wanted for Q8. It rebuilds a two-layer stack and tries a legacy merge of the top layer while the bottom is open.
11. **`./probe.sh cleanup`.** Always run this, even after a failure. It unstacks, closes every open probe PR with a comment, removes the protection, deletes every `probe/stack-*` branch and `probe/stack-base`, deletes the label if setup created it, and removes the temporary worktree. Check that `cleanup-leftovers.txt` is empty.
12. **Optional second run.** For the merge-commit method, run steps 4 to 11 again with `METHOD=merge` and a different `OUT`.
13. **Hand back.** Run `./probe.sh bundle` and attach `$OUT.tar.gz`, or the `OUT` directory, to the task that re-opens the question.

### Safety properties of the script

- Every push uses an explicit refspec onto `refs/heads/probe/stack-*`.
- `guard` refuses any ref equal to the default branch or `main`.
- Every PR is created with an explicit `--base` of `probe/stack-base` or a probe layer.
- No call passes `--admin`, `--auto`, `bypass_rules=true` or `merge_action=merge_queue`.
- `gh stack link` gets PR numbers only, so it pushes nothing. gh-stack's merge, rebase, sync and submit commands are never called.
- Main's tip is recorded at setup and compared at cleanup. The probe never writes to main. A difference means something else merged meanwhile, and the probe's PRs are listed so this can be checked.

## 4. probe.sh

```bash
#!/usr/bin/env bash
# d113 stacked-PR probe for ndrwl/guildhall. Run by hand, phase by phase, from a clean clone's root.
# Phases: read setup stack wait behind merge1 race hazard cleanup bundle.  Never touches main.
set -euo pipefail
REPO=${REPO:-ndrwl/guildhall}
REMOTE=${REMOTE:-origin}
METHOD=${METHOD:-squash}            # squash (Guildhall's method) or merge
OUT=${OUT:-$HOME/d113-probe-out}
BASE=probe/stack-base
LABEL=probe
WT=${WT:-$(dirname "$OUT")/d113-probe-wt}
STATE=$OUT/state.env
mkdir -p "$OUT"
touch "$STATE"
# shellcheck disable=SC1090
. "$STATE"

log() { printf '%s %s\n' "$(date -u +%FT%TZ)" "$*" | tee -a "$OUT/log.txt" >&2; }
save() { printf '%s=%q\n' "$1" "$2" >>"$STATE"; eval "$1=\$2"; }
# rec NAME CMD...: run CMD, keep stdout/stderr/exit code under $OUT, never abort.
rec() {
  local name=$1; shift
  log "RUN $name: $*"
  set +e; "$@" >"$OUT/$name.out" 2>"$OUT/$name.err"; local rc=$?; set -e
  echo "$rc" >"$OUT/$name.rc"; log "  rc=$rc"; return 0
}
rc() { cat "$OUT/$1.rc"; }
default_branch() { gh repo view "$REPO" --json defaultBranchRef -q .defaultBranchRef.name; }
guard() {
  local d; d=$(default_branch)
  for b in "$@"; do
    if [[ $b == "$d" || $b == main || $b == refs/heads/main || $b != probe/* ]]; then
      echo "refusing to write ref $b" >&2; exit 3
    fi
  done
}
push() { # push COMMIT BRANCH (force, as Guildhall's Publish does)
  guard "$2"; git -C "$WT" push --quiet --porcelain "$REMOTE" "+$1:refs/heads/$2"
}
pr_create() { # pr_create HEAD BASE TITLE -> prints number
  guard "$1" "$2"
  local url; url=$(gh pr create --repo "$REPO" --head "$1" --base "$2" --label "$LABEL" \
    --title "[probe d113] $3 - do not merge" --body "Throwaway d113 stacked-PR probe. Merges only into $BASE, never main.")
  echo "${url##*/}"
}
view() { gh pr view "$1" --repo "$REPO" --json number,state,baseRefName,headRefName,headRefOid,mergeable,mergeStateStatus,mergeCommit; }
snap() { # snap TAG PR...
  local tag=$1; shift
  for n in "$@"; do
    rec "snap-$tag-$n" view "$n"
    rec "snap-$tag-$n-rest" gh api "repos/$REPO/pulls/$n" --jq '{stack, base: .base.ref, base_sha: .base.sha, head: .head.sha, mergeable_state}'
  done
}
poll() { # poll NAME SECONDS PR...: one TSV line per PR every 5 s
  local name=$1 secs=$2; shift 2
  local start; start=$(date +%s)
  printf 't\tpr\tstate\tbase\thead\tmergeStateStatus\n' >"$OUT/$name.tsv"
  while (( $(date +%s) - start < secs )); do
    for n in "$@"; do
      view "$n" | jq -r --arg t "$(( $(date +%s) - start ))" \
        '[$t, .number, .state, .baseRefName, .headRefOid, .mergeStateStatus] | @tsv' >>"$OUT/$name.tsv" || true
    done
    sleep 5
  done
}
fetch() { git -C "$WT" fetch --quiet --no-tags "$REMOTE" "+refs/heads/probe/*:refs/remotes/$REMOTE/probe/*" 2>/dev/null || true; }
change_patch() { git -C "$WT" diff "$1" "$2" | git -C "$WT" patch-id --stable | cut -d' ' -f1; }
layer_commits() { # layer_commits K: two commits adding probe-d113/lK-*.txt on the current HEAD
  for i in a b; do
    echo "d113 probe layer $1 $i" >"$WT/probe-d113/l$1-$i.txt"
    git -C "$WT" add "probe-d113/l$1-$i.txt"
    git -C "$WT" commit --quiet -m "probe d113: layer $1 commit $i"
  done
}
ensure_wt() {
  git fetch --quiet --no-tags "$REMOTE" "+refs/heads/$BASE:refs/remotes/$REMOTE/$BASE"
  [[ -d $WT ]] || git worktree add --quiet --detach "$WT" "$REMOTE/$BASE"
  mkdir -p "$WT/probe-d113"
}
merge_legacy() { # merge_legacy NAME PR HEAD: Guildhall's exact command, with METHOD
  rec "$1" gh pr merge "$2" --repo "$REPO" "--$METHOD" --match-head-commit "$3"
}
merge_async() { # merge_async NAME PR HEAD: PUT merge-async then poll its result
  rec "$1" gh api -X PUT -H "X-GitHub-Api-Version: 2026-03-10" "repos/$REPO/pulls/$2/merge-async" \
    -f sha="$3" -f merge_method="$METHOD" -f merge_action=direct_merge -i
  local uuid; uuid=$(grep -o '"[a-z_]*id"[^,}]*' "$OUT/$1.out" | head -1 | grep -oE '[0-9a-f-]{36}' || true)
  echo "$uuid" >"$OUT/$1.uuid"
  [[ -n $uuid ]] || return 0
  for i in $(seq 1 36); do
    rec "$1-poll-$i" gh api -H "X-GitHub-Api-Version: 2026-03-10" "repos/$REPO/pulls/$2/merge-async/$uuid"
    grep -qiE '"(state|status)": *"(merged|succeeded|completed|failed|error)' "$OUT/$1-poll-$i.out" && break
    sleep 5
  done
}
merge_bottom() { # merge_bottom NAME PR HEAD: legacy first; async only if refused
  merge_legacy "$1-legacy" "$2" "$3"
  if [[ $(view "$2" | jq -r .state) != MERGED ]]; then merge_async "$1-async" "$2" "$3"; fi
  snap "$1-done" "$2"
}

phase_read() {
  rec read-gh-version gh --version
  rec read-auth gh auth status            # gh masks the token; shows its type prefix and scopes
  rec read-extensions gh extension list
  rec read-repo gh api "repos/$REPO" --jq '{owner_type: .owner.type, visibility, default_branch,
    allow_squash_merge, allow_merge_commit, allow_rebase_merge, allow_auto_merge, delete_branch_on_merge, allow_update_branch}'
  rec read-main-protection gh api "repos/$REPO/branches/$(default_branch)/protection"
  rec read-rulesets gh api "repos/$REPO/rulesets"
  rec read-main-rules gh api "repos/$REPO/rules/branches/$(default_branch)"
  rec read-stacks-list gh api "repos/$REPO/stacks"
  rec read-stacks-list-headers gh api -i "repos/$REPO/stacks"
  rec read-stacks-list-v2026 gh api -H "X-GitHub-Api-Version: 2026-03-10" "repos/$REPO/stacks"
  rec read-ci gh api "repos/$REPO/contents/.github/workflows/ci.yml" --jq .sha
}

phase_setup() {
  local d main_sha; d=$(default_branch); main_sha=$(gh api "repos/$REPO/git/ref/heads/$d" --jq .object.sha)
  save MAIN_AT_SETUP "$main_sha"
  guard "$BASE"
  rec setup-base gh api -X POST "repos/$REPO/git/refs" -f ref="refs/heads/$BASE" -f sha="$main_sha"
  gh api "repos/$REPO/branches/$d/protection" >"$OUT/setup-main-protection.json"
  jq '{required_status_checks: (if .required_status_checks then
         {strict: true, checks: [.required_status_checks.checks[] | {context, app_id}]} else null end),
       enforce_admins: (.enforce_admins.enabled // false),
       required_pull_request_reviews: null, restrictions: null,
       required_linear_history: (.required_linear_history.enabled // false),
       allow_force_pushes: false, allow_deletions: true}' \
     "$OUT/setup-main-protection.json" >"$OUT/setup-probe-protection.json"
  if [[ $(jq -r .required_status_checks "$OUT/setup-probe-protection.json") == null ]]; then
    echo "main has no classic required status checks; send $OUT for the analysis attempt" >&2; exit 4
  fi
  rec setup-protection gh api -X PUT "repos/$REPO/branches/$BASE/protection" --input "$OUT/setup-probe-protection.json"
  if gh label list --repo "$REPO" --json name -q '.[].name' | grep -qx "$LABEL"; then save LABEL_CREATED 0
  else rec setup-label gh label create "$LABEL" --repo "$REPO" --description "Throwaway probe PRs"; save LABEL_CREATED 1; fi
}

phase_stack() {
  ensure_wt
  local base_sha; base_sha=$(git -C "$WT" rev-parse "$REMOTE/$BASE")
  git -C "$WT" checkout --quiet --detach "$base_sha"
  local prev=$BASE
  for k in 1 2 3; do
    layer_commits "$k"
    local h; h=$(git -C "$WT" rev-parse HEAD)
    push "$h" "probe/stack-$k"; save "L${k}_ORIG" "$h"
    local n; n=$(pr_create "probe/stack-$k" "$prev" "layer $k"); save "N$k" "$n"
    prev=probe/stack-$k
  done
  git -C "$WT" checkout --quiet --detach "$base_sha"
  echo "d113 probe bump" >"$WT/probe-d113/bump.txt"; git -C "$WT" add probe-d113/bump.txt
  git -C "$WT" commit --quiet -m "probe d113: bump the scratch base"
  push "$(git -C "$WT" rev-parse HEAD)" probe/stack-bump
  save NB "$(pr_create probe/stack-bump "$BASE" "base bump")"
  rec stack-create gh api -X POST "repos/$REPO/stacks" -F "pull_requests[]=$N1" -F "pull_requests[]=$N2" -F "pull_requests[]=$N3"
  if [[ $(rc stack-create) != 0 ]] && gh extension list | grep -q gh-stack; then
    rec stack-link-gh gh stack link --base "$BASE" "$N1" "$N2" "$N3"   # PR numbers only: pushes nothing
  fi
  rec stack-read gh api "repos/$REPO/stacks?pull_request=$N1"
  snap stacked "$N1" "$N2" "$N3" "$NB"
}

phase_wait() {
  for n in ${N1:-} ${N2:-} ${N3:-} ${NB:-} ${H1:-} ${H2:-}; do
    [[ $(view "$n" | jq -r .state) == OPEN ]] || continue
    log "waiting for checks on #$n"; gh pr checks "$n" --repo "$REPO" --watch --interval 30 >/dev/null 2>&1 || true
    rec "wait-checks-$n" gh pr checks "$n" --repo "$REPO"
  done
}

phase_behind() {
  ensure_wt
  merge_legacy behind-merge-bump "$NB" "$(view "$NB" | jq -r .headRefOid)"
  sleep 10; snap behind "$N1" "$N2" "$N3"
  sleep 60; snap behind-later "$N1" "$N2" "$N3"
  merge_legacy merge-l1-behind "$N1" "$(view "$N1" | jq -r .headRefOid)"   # expected: refused (behind)
  # Bring L1 up to date as Guildhall's rebaser does: replay its change onto the base tip and force-push.
  fetch
  local tip h1; tip=$(git -C "$WT" rev-parse "$REMOTE/$BASE")
  git -C "$WT" checkout --quiet --detach "$tip"
  git -C "$WT" cherry-pick --quiet "$REMOTE/$BASE..$L1_ORIG" >/dev/null 2>&1 || git -C "$WT" cherry-pick "$tip..$L1_ORIG"
  h1=$(git -C "$WT" rev-parse HEAD); push "$h1" probe/stack-1; save L1_UP "$h1"
  sleep 10; snap after-l1-push "$N1" "$N2" "$N3"
  sleep 60; snap after-l1-push-later "$N1" "$N2" "$N3"
}

phase_merge1() {
  fetch
  local h1 l2_before; h1=$(view "$N1" | jq -r .headRefOid); l2_before=$(view "$N2" | jq -r .headRefOid)
  save L2_BEFORE_MERGE1 "$l2_before"
  snap merge1-start "$N1" "$N2" "$N3"
  merge_bottom merge-l1 "$N1" "$h1"
  poll poll-merge1 180 "$N2" "$N3"
  snap merge1-end "$N2" "$N3"
  fetch
  local l2_after base_tip; l2_after=$(view "$N2" | jq -r .headRefOid); base_tip=$(git -C "$WT" rev-parse "$REMOTE/$BASE")
  git -C "$WT" fetch --quiet "$REMOTE" "$l2_after" 2>/dev/null || true
  {
    echo "method=$METHOD l1_merged_head=$h1 base_tip=$base_tip"
    echo "l2_before=$l2_before l2_after=$l2_after base_after=$(view "$N2" | jq -r .baseRefName)"
    echo "l1_orig_is_ancestor_of_l2_after: $(git -C "$WT" merge-base --is-ancestor "$L1_ORIG" "$l2_after" && echo yes || echo no)"
    echo "files base_tip..l2_after:"; git -C "$WT" diff --name-only "$base_tip" "$l2_after"
    echo "commits base_tip..l2_after: $(git -C "$WT" rev-list --count "$base_tip..$l2_after")"
    echo "l2 own change patch-id before (l1 head..l2_before): $(change_patch "$h1" "$l2_before")"
    echo "l2 own change patch-id after  (base_tip..l2_after): $(change_patch "$base_tip" "$l2_after")"
  } >"$OUT/analysis-merge1.txt" 2>&1
}

phase_race() {
  ensure_wt; fetch
  local h2 h3 tip ours; h2=$(view "$N2" | jq -r .headRefOid); h3=$(view "$N3" | jq -r .headRefOid)
  git -C "$WT" fetch --quiet "$REMOTE" "$h2" "$h3" 2>/dev/null || true
  snap race-start "$N2" "$N3"
  merge_bottom merge-l2 "$N2" "$h2"
  # Immediately push a Guildhall-style rebase of L3: its own change replayed onto the new base tip.
  git -C "$WT" fetch --quiet --no-tags "$REMOTE" "+refs/heads/$BASE:refs/remotes/$REMOTE/$BASE"
  tip=$(git -C "$WT" rev-parse "$REMOTE/$BASE")
  git -C "$WT" checkout --quiet --detach "$tip"
  git -C "$WT" cherry-pick --quiet "$h2..$h3" >/dev/null 2>&1 || true
  ours=$(git -C "$WT" rev-parse HEAD); save L3_OURS "$ours"
  rec race-push push "$ours" probe/stack-3
  log "pushed ours=$ours for L3"
  poll poll-race 180 "$N3"
  snap race-end "$N3"
  local final; final=$(view "$N3" | jq -r .headRefOid)
  { echo "method=$METHOD l3_before=$h3 ours=$ours final=$final base_tip=$tip"
    echo "final_is_ours: $([[ $final == "$ours" ]] && echo yes || echo no)"
    echo "head changes seen:"; cut -f5 "$OUT/poll-race.tsv" | uniq; } >"$OUT/analysis-race.txt"
  gh pr checks "$N3" --repo "$REPO" --watch --interval 30 >/dev/null 2>&1 || true
  merge_legacy merge-l3-match "$N3" "$ours"
  [[ $(view "$N3" | jq -r .state) == MERGED ]] || merge_async merge-l3-match-async "$N3" "$ours"
  snap race-done "$N3"
}

phase_hazard() {
  ensure_wt; fetch
  local tip; tip=$(git -C "$WT" rev-parse "$REMOTE/$BASE"); git -C "$WT" checkout --quiet --detach "$tip"
  layer_commits 4; push "$(git -C "$WT" rev-parse HEAD)" probe/stack-4; save H1 "$(pr_create probe/stack-4 "$BASE" "hazard bottom")"
  layer_commits 5; push "$(git -C "$WT" rev-parse HEAD)" probe/stack-5; save H2 "$(pr_create probe/stack-5 probe/stack-4 "hazard top")"
  rec hazard-stack gh api -X POST "repos/$REPO/stacks" -F "pull_requests[]=$H1" -F "pull_requests[]=$H2"
  phase_wait
  merge_legacy hazard-merge-mid "$H2" "$(view "$H2" | jq -r .headRefOid)"   # top layer while bottom is open
  snap hazard-end "$H1" "$H2"
}

phase_cleanup() {
  local nums; nums=$(gh pr list --repo "$REPO" --state open --label "$LABEL" --json number,headRefName \
    -q '.[] | select(.headRefName | startswith("probe/stack-")) | .number')
  for s in $(gh api "repos/$REPO/stacks" --jq '.[].number' 2>/dev/null || true); do
    rec "cleanup-unstack-$s" gh api -X POST "repos/$REPO/stacks/$s/unstack"
  done
  for n in $nums; do rec "cleanup-close-$n" gh pr close "$n" --repo "$REPO" --comment "d113 probe finished; closing."; done
  rec cleanup-protection gh api -X DELETE "repos/$REPO/branches/$BASE/protection"
  for b in $(gh api "repos/$REPO/git/matching-refs/heads/probe/stack-" --jq '.[].ref | sub("refs/heads/"; "")'); do
    guard "$b"; rec "cleanup-del-${b//\//_}" gh api -X DELETE "repos/$REPO/git/refs/heads/$b"
  done
  guard "$BASE"; rec cleanup-del-base gh api -X DELETE "repos/$REPO/git/refs/heads/$BASE"
  [[ ${LABEL_CREATED:-0} == 1 ]] && rec cleanup-label gh label delete "$LABEL" --repo "$REPO" --yes
  [[ -d $WT ]] && git worktree remove --force "$WT"
  git for-each-ref --format='%(refname)' "refs/remotes/$REMOTE/probe/" | xargs -r -n1 git update-ref -d
  { echo "main_at_setup=${MAIN_AT_SETUP:-unknown} main_now=$(gh api "repos/$REPO/git/ref/heads/$(default_branch)" --jq .object.sha)"
    echo "probe PRs ever opened: ${N1:-} ${N2:-} ${N3:-} ${NB:-} ${H1:-} ${H2:-}"
    for n in ${N1:-} ${N2:-} ${N3:-} ${NB:-} ${H1:-} ${H2:-}; do view "$n" | jq -c '{number, state, baseRefName}'; done
  } >"$OUT/cleanup-main-check.txt" 2>&1
  { gh api "repos/$REPO/git/matching-refs/heads/probe/" --jq '.[].ref'
    gh pr list --repo "$REPO" --state open --label "$LABEL" --json number -q '.[].number'; } >"$OUT/cleanup-leftovers.txt" 2>&1 || true
}

case ${1:-} in
  read) phase_read ;; setup) phase_setup ;; stack) phase_stack ;; wait) phase_wait ;;
  behind) phase_behind ;; merge1) phase_merge1 ;; race) phase_race ;; hazard) phase_hazard ;;
  cleanup) phase_cleanup ;; bundle) tar -czf "$OUT.tar.gz" -C "$(dirname "$OUT")" "$(basename "$OUT")"; echo "$OUT.tar.gz" ;;
  *) echo "usage: $0 read|setup|stack|wait|behind|merge1|race|hazard|cleanup|bundle" >&2; exit 2 ;;
esac
```

### Notes on the script

- **Not run, only reviewed.** The script was not executed in d113's research or build, because no `gh` access is allowed. It has been reviewed by reading only, and even `bash -n` and shellcheck were not permitted in this session, so run `bash -n probe.sh` first. Expect small fixes on first run. Each phase records every call's exit code and output rather than aborting, so a failed step is itself evidence.
- **The merge-async result id is guessed.** The docs did not show the 202 body's field names. The script greps the first UUID in the response, and keeps the raw response with headers (`merge-l1-async.out`) either way.
- **`behind` uses a different replay.** It cherry-picks L1's commits onto the new base rather than doing Guildhall's one-commit `merge-tree` replay. For the forge's view, a forced new head on the bottom branch, the two are equivalent.
- **Phases chain through state.** The phases depend on `state.env`. Re-running a phase is safe for read-only steps. `stack` should not be re-run without `cleanup` first.
