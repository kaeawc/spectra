#!/usr/bin/env bash
set -euo pipefail

# Moves the checks listed in CHECKS (today only `docs`) between GitHub Actions and
# Buildkite. Sets the repository variable CI_ON_BUILDKITE (read by the buildkite
# job and the moved jobs' `if:` in .github/workflows/ci.yml) and repoints those
# required checks in the green-main ruleset to the app that now reports them. A
# GitHub job skipped by `if:` reports success, so the ruleset has to stop
# accepting GitHub Actions for a check while Buildkite runs it.
#
# Usage: scripts/ci-backend.sh <buildkite|github|status>
#
# Needs gh with admin on the repository. Environment:
#   REPO     owner/name (default kaeawc/spectra)
#   RULESET  ruleset name (default green-main)

REPO="${REPO:-kaeawc/spectra}"
RULESET="${RULESET:-green-main}"
CHECKS='["docs"]'
GITHUB_ACTIONS_APP=15368
BUILDKITE_APP=805657

usage() {
  echo "usage: $0 <buildkite|github|status>" >&2
  exit 2
}

[ "$#" -eq 1 ] || usage
case "$1" in
  buildkite) value=true; app=$BUILDKITE_APP ;;
  github) value=false; app=$GITHUB_ACTIONS_APP ;;
  status) ;;
  *) usage ;;
esac

ruleset_id="$(gh api "repos/$REPO/rulesets" --jq ".[] | select(.name == \"$RULESET\") | .id")"
if [ -z "$ruleset_id" ]; then
  echo "no ruleset named $RULESET on $REPO" >&2
  exit 1
fi

if [ "$1" = status ]; then
  echo "CI_ON_BUILDKITE=$(gh variable get CI_ON_BUILDKITE --repo "$REPO" 2>/dev/null || echo unset)"
  gh api "repos/$REPO/rulesets/$ruleset_id" --jq \
    ".rules[] | select(.type == \"required_status_checks\") | .parameters.required_status_checks[]
     | select(.context as \$c | $CHECKS | index(\$c)) | \"\(.context): app \(.integration_id)\""
  exit 0
fi

# Switching to Buildkite repoints the checks last and switching back repoints
# them first, so a PR never waits on a status the new backend is not posting.
set_variable() { gh variable set CI_ON_BUILDKITE --repo "$REPO" --body "$value"; }
repoint_checks() {
  gh api "repos/$REPO/rulesets/$ruleset_id" |
    jq --argjson app "$app" --argjson checks "$CHECKS" '{
      name, target, enforcement, conditions, bypass_actors,
      rules: [.rules[] | if .type == "required_status_checks" then
        .parameters.required_status_checks |= map(
          if (.context as $c | $checks | index($c)) then .integration_id = $app else . end)
        else . end]
    }' |
    gh api --method PUT "repos/$REPO/rulesets/$ruleset_id" --input - --silent
}

if [ "$1" = buildkite ]; then
  set_variable
  repoint_checks
else
  repoint_checks
  set_variable
fi
echo "$(jq -r 'join(", ")' <<<"$CHECKS") now run on $1"
