#!/usr/bin/env bash
set -euo pipefail

# merge-pr.sh -- Watch a pull request through CI, bugbot, and merge.
#
# Auto-merge must be armed on the PR (gh pr merge --auto --squash --delete-branch);
# this script polls checks with fail-fast, surfaces bugbot threads, and confirms
# the PR reaches MERGED state.
#
# The last line of stdout is always a STATUS line:
#
#   STATUS: MERGED          -- PR merged, done
#   STATUS: CHECK_FAILED    -- CI check failed, output has details
#   STATUS: BUGBOT_THREADS  -- unresolved review threads, output has details
#   STATUS: TIMEOUT         -- timed out waiting
#   STATUS: ERROR           -- PR closed, not found, or unexpected state
#
# Usage: scripts/merge-pr.sh <pr-number>

PR_NUMBER="${1:-}"

if [[ -z "$PR_NUMBER" ]]; then
    echo "Usage: scripts/merge-pr.sh <pr-number>"
    echo "STATUS: ERROR"
    exit 1
fi

REPO=$(gh repo view --json nameWithOwner --jq '.nameWithOwner' 2>/dev/null) || {
    echo "Could not detect repository. Run from inside a git checkout."
    echo "STATUS: ERROR"
    exit 1
}

OWNER="${REPO%/*}"
REPO_NAME="${REPO#*/}"

POLL_INTERVAL=15
CHECK_TIMEOUT=900
MERGE_TIMEOUT=120

# ── Helpers ──────────────────────────────────────────────────────────

pr_state() {
    gh pr view "$PR_NUMBER" --json state --jq '.state' 2>/dev/null || echo "UNKNOWN"
}

# ── Validate PR ──────────────────────────────────────────────────────

state=$(pr_state)
case "$state" in
    MERGED)
        echo "PR #${PR_NUMBER} already merged."
        echo "STATUS: MERGED"
        exit 0
        ;;
    CLOSED)
        echo "PR #${PR_NUMBER} is CLOSED."
        echo "STATUS: ERROR"
        exit 1
        ;;
    OPEN) ;;
    *)
        echo "PR #${PR_NUMBER} has unexpected state: ${state}"
        echo "STATUS: ERROR"
        exit 1
        ;;
esac

echo "Watching PR #${PR_NUMBER} (${REPO})..."

# ── Fetch required checks from rulesets ─────────────────────────────

target_branch=$(gh pr view "$PR_NUMBER" --json baseRefName --jq '.baseRefName' 2>/dev/null) || target_branch="main"

required_checks=$(gh api "repos/${REPO}/rules/branches/${target_branch}" \
    --jq '[.[] | select(.type == "required_status_checks") | .parameters.required_status_checks[].context] | unique' 2>/dev/null) || required_checks="[]"

required_count=$(echo "$required_checks" | jq 'length')
if (( required_count > 0 )); then
    echo "Required checks (${required_count}): $(echo "$required_checks" | jq -r 'join(", ")')"
else
    echo "WARNING: Could not determine required checks. Treating all checks as required."
fi

# ── Phase 1: Poll checks, fail on required failures ────────────────

deadline=$((SECONDS + CHECK_TIMEOUT))
checks=""
checks_passed=false
prev_total=-1
# Default so the timeout branch is safe under `set -u` even if the loop only ever
# saw "no checks yet" and never assigned it.
required_pending="[]"

while (( SECONDS < deadline )); do
    checks=$(gh pr checks "$PR_NUMBER" --json name,bucket,link 2>/dev/null) || {
        sleep "$POLL_INTERVAL"
        continue
    }

    total=$(echo "$checks" | jq 'length')

    if (( total == 0 )); then
        echo "Waiting for checks to start..."
        sleep "$POLL_INTERVAL"
        continue
    fi

    if (( required_count > 0 )); then
        # Only consider checks that match required contexts
        required_failed=$(echo "$checks" | jq --argjson req "$required_checks" \
            '[.[] | select(.bucket == "fail" and (.name as $n | $req | index($n)))]')
        required_pending=$(echo "$checks" | jq --argjson req "$required_checks" \
            '[.[] | select(.bucket == "pending" and (.name as $n | $req | index($n)))]')
        required_passed=$(echo "$checks" | jq --argjson req "$required_checks" \
            '[.[] | select(.bucket == "pass" and (.name as $n | $req | index($n)))]')

        req_fail_count=$(echo "$required_failed" | jq 'length')
        req_pending_count=$(echo "$required_pending" | jq 'length')
        req_pass_count=$(echo "$required_passed" | jq 'length')
    else
        # Fallback: treat all checks as required
        required_failed=$(echo "$checks" | jq '[.[] | select(.bucket == "fail")]')
        required_pending=$(echo "$checks" | jq '[.[] | select(.bucket == "pending")]')
        required_passed=$(echo "$checks" | jq '[.[] | select(.bucket == "pass")]')

        req_fail_count=$(echo "$required_failed" | jq 'length')
        req_pending_count=$(echo "$required_pending" | jq 'length')
        req_pass_count=$(echo "$required_passed" | jq 'length')
    fi

    if (( req_fail_count > 0 )); then
        echo ""
        echo "Required CI check(s) failed on PR #${PR_NUMBER}:"
        echo ""
        echo "$required_failed" | jq -r '.[] | "  \(.name)\n    \(.link)\n"'

        if (( required_count > 0 )); then
            non_req_failed=$(echo "$checks" | jq --argjson req "$required_checks" \
                '[.[] | select(.bucket == "fail" and (.name as $n | $req | index($n) | not))]')
            non_req_fail_count=$(echo "$non_req_failed" | jq 'length')
            if (( non_req_fail_count > 0 )); then
                echo "Non-required checks also failing (informational):"
                echo "$non_req_failed" | jq -r '.[] | "  \(.name)"'
                echo ""
            fi
        fi

        echo "ACTION: Fix the failing required check(s), commit, push, and re-run this script."
        echo "STATUS: CHECK_FAILED"
        exit 1
    fi

    # "No pending" is not enough: a required context that has not started yet is
    # absent from `gh pr checks` entirely. When the required set is known, require
    # every required context to have a passing entry; when it is not (fallback),
    # require the check set to have stopped growing across two polls so a
    # not-yet-started check cannot let the watcher advance early.
    if (( req_pending_count == 0 )) && { (( required_count == 0 && total == prev_total )) || (( required_count > 0 && req_pass_count >= required_count )); }; then
        echo "All ${req_pass_count} required checks passed."

        if (( required_count > 0 )); then
            non_req_failed=$(echo "$checks" | jq --argjson req "$required_checks" \
                '[.[] | select(.bucket == "fail" and (.name as $n | $req | index($n) | not))]')
            non_req_fail_count=$(echo "$non_req_failed" | jq 'length')
            if (( non_req_fail_count > 0 )); then
                echo "Non-required checks failing (informational):"
                echo "$non_req_failed" | jq -r '.[] | "  \(.name)"'
            fi
        fi

        checks_passed=true
        break
    fi

    pass_count=$(echo "$checks" | jq '[.[] | select(.bucket == "pass")] | length')
    pending_count=$(echo "$checks" | jq '[.[] | select(.bucket == "pending")] | length')
    echo "Checks: ${pass_count} passed, ${pending_count} pending (${req_pending_count} required pending)"
    prev_total=$total
    sleep "$POLL_INTERVAL"
done

if [[ "$checks_passed" != true ]]; then
    echo "Timed out after ${CHECK_TIMEOUT}s waiting for required checks."
    echo "$required_pending" | jq -r '.[] | "  Pending: \(.name)"'
    echo "STATUS: TIMEOUT"
    exit 1
fi

# ── Phase 2: Check for unresolved bugbot threads ────────────────────

echo "Checking for unresolved review threads..."

# shellcheck disable=SC2016
threads=$(gh api graphql -f query='
query($owner: String!, $repo: String!, $pr: Int!) {
  repository(owner: $owner, name: $repo) {
    pullRequest(number: $pr) {
      reviewThreads(first: 100) {
        nodes {
          id
          isResolved
          path
          line
          comments(first: 10) {
            nodes {
              databaseId
              author { login }
              body
            }
          }
        }
      }
    }
  }
}' -f owner="$OWNER" -f repo="$REPO_NAME" -F pr="$PR_NUMBER" \
  --jq '[.data.repository.pullRequest.reviewThreads.nodes[] | select(.isResolved == false and .comments.nodes[0].author.login == "cursor")]' 2>/dev/null) || threads="[]"

thread_count=$(echo "$threads" | jq 'length')

if (( thread_count > 0 )); then
    echo ""
    echo "Unresolved Bugbot threads (${thread_count}):"
    echo ""
    echo "$threads" | jq -r '
        to_entries[] |
        "--- Thread \(.key + 1) ---" +
        "\nFile: \(.value.path)" +
        "\nLine: \(.value.line)" +
        "\nThread ID: \(.value.id)" +
        "\nComment ID: \(.value.comments.nodes[0].databaseId)" +
        "\n\n\(.value.comments.nodes[0].body)\n"
    '
    echo "ACTION: For each thread -- investigate the code, reply with analysis, resolve the thread, fix if valid."
    echo "  Reply:   gh api repos/${REPO}/pulls/${PR_NUMBER}/comments/{comment-id}/replies -f body='...'"
    echo "  Resolve: gh api graphql -f query='mutation(\$id:ID!){resolveReviewThread(input:{threadId:\$id}){thread{isResolved}}}' -f id='{thread-id}'"
    echo "  Then re-run this script."
    echo "STATUS: BUGBOT_THREADS"
    exit 1
fi

echo "No unresolved review threads."

# ── Phase 3: Wait for auto-merge ────────────────────────────────────

echo "All required checks green, no blocking threads. Waiting for auto-merge..."

merge_deadline=$((SECONDS + MERGE_TIMEOUT))

while (( SECONDS < merge_deadline )); do
    state=$(pr_state)
    case "$state" in
        MERGED)
            echo "PR #${PR_NUMBER} merged."
            echo "STATUS: MERGED"
            exit 0
            ;;
        CLOSED)
            echo "PR #${PR_NUMBER} was closed unexpectedly."
            echo "STATUS: ERROR"
            exit 1
            ;;
        OPEN|*)
            sleep 5
            ;;
    esac
done

# ── Diagnose why merge did not happen ────────────────────────────────

# Final check: PR may have merged during the last sleep interval.
state=$(pr_state)
if [[ "$state" == "MERGED" ]]; then
    echo "PR #${PR_NUMBER} merged."
    echo "STATUS: MERGED"
    exit 0
fi

diag=$(gh pr view "$PR_NUMBER" --json autoMergeRequest,mergeStateStatus,mergeable 2>/dev/null) || diag="{}"

auto_merge=$(echo "$diag" | jq -r '.autoMergeRequest // empty')
merge_status=$(echo "$diag" | jq -r '.mergeStateStatus // "UNKNOWN"')
mergeable=$(echo "$diag" | jq -r '.mergeable // "UNKNOWN"')

echo ""
echo "PR #${PR_NUMBER} did not merge after ${MERGE_TIMEOUT}s."

if [[ -z "$auto_merge" ]]; then
    echo "Auto-merge is NOT armed on this PR."
    echo "ACTION: Run 'gh pr merge ${PR_NUMBER} --auto --squash --delete-branch' then re-run this script."
elif [[ "$mergeable" == "CONFLICTING" ]]; then
    echo "PR has merge conflicts."
    echo "ACTION: Rebase on main (git fetch origin main && git rebase origin/main), force-push, and re-run this script."
elif [[ "$merge_status" == "BLOCKED" ]]; then
    echo "Merge is BLOCKED by branch protection."
    echo "ACTION: Check for unresolved non-Bugbot review threads or other branch protection requirements."
    echo "  Diagnose: gh pr view ${PR_NUMBER} --json reviewDecision,reviewRequests"
else
    echo "Merge state: ${merge_status}, Mergeable: ${mergeable}"
    echo "ACTION: Investigate PR state manually."
    echo "  Diagnose: gh pr view ${PR_NUMBER} --json autoMergeRequest,mergeStateStatus,mergeable"
fi

echo "STATUS: ERROR"
exit 1
