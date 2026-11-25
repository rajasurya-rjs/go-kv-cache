#!/usr/bin/env bash
# Protect the main branch so nothing merges unless both pipeline jobs pass.
#
#   - required checks: "CI - Build & Test" and "CD - Docker & Deploy"
#   - strict mode: the branch must be up to date with main before merging
#
# Requires the GitHub CLI (`gh auth login`) and admin rights on the repo.
# Usage: scripts/setup-branch-protection.sh [owner/repo]   (default: current repo)
set -euo pipefail

REPO="${1:-$(gh repo view --json nameWithOwner --jq .nameWithOwner)}"

gh api --method PUT "repos/$REPO/branches/main/protection" \
  -H "Accept: application/vnd.github+json" \
  --input - <<'JSON'
{
  "required_status_checks": {
    "strict": true,
    "contexts": ["CI - Build & Test", "CD - Docker & Deploy"]
  },
  "enforce_admins": false,
  "required_pull_request_reviews": null,
  "restrictions": null
}
JSON

echo "Branch protection enabled on $REPO@main"
