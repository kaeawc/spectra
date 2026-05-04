#!/usr/bin/env bash
set -euo pipefail

# Validates GitHub Actions workflow files, referenced scripts, and shell scripts.
# Requires shellcheck for shell linting; YAML is parsed via python (stdlib).

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
EXIT_CODE=0

echo "=== Validating YAML files ==="
yaml_files=()
while IFS= read -r f; do
  yaml_files+=("$f")
done < <(find "$REPO_ROOT/.github" -name '*.yml' -o -name '*.yaml' 2>/dev/null)

if [ ${#yaml_files[@]} -eq 0 ]; then
  echo "  WARNING: no YAML files found under .github/"
elif command -v python3 >/dev/null 2>&1; then
  for f in "${yaml_files[@]}"; do
    if python3 -c "import sys, yaml; yaml.safe_load(open(sys.argv[1]))" "$f" >/dev/null 2>&1; then
      echo "  OK: ${f#"$REPO_ROOT"/}"
    else
      echo "  FAIL: ${f#"$REPO_ROOT"/}"
      EXIT_CODE=1
    fi
  done
else
  echo "  WARNING: python3 not available, skipping YAML parse check"
fi

echo ""
echo "=== Checking referenced scripts exist ==="
script_refs=()
while IFS= read -r ref; do
  script_refs+=("$ref")
done < <(grep -roh 'scripts/[^ "]*\.sh' "$REPO_ROOT/.github" 2>/dev/null | sort -u)

if [ ${#script_refs[@]} -eq 0 ]; then
  echo "  No script references found in workflows."
else
  for ref in "${script_refs[@]}"; do
    if [ -f "$REPO_ROOT/$ref" ]; then
      echo "  OK: $ref"
    else
      echo "  MISSING: $ref"
      EXIT_CODE=1
    fi
  done
fi

echo ""
echo "=== Running shellcheck ==="
sh_files=()
while IFS= read -r f; do
  sh_files+=("$f")
done < <(find "$REPO_ROOT/scripts" -name '*.sh' 2>/dev/null)

if [ ${#sh_files[@]} -eq 0 ]; then
  echo "  No .sh files found in scripts/"
elif command -v shellcheck >/dev/null 2>&1; then
  for f in "${sh_files[@]}"; do
    if shellcheck "$f"; then
      echo "  OK: ${f#"$REPO_ROOT"/}"
    else
      echo "  ISSUES: ${f#"$REPO_ROOT"/}"
      EXIT_CODE=1
    fi
  done
else
  echo "  WARNING: shellcheck not installed, skipping"
fi

echo ""
if [ "$EXIT_CODE" -eq 0 ]; then
  echo "All checks passed."
else
  echo "Some checks failed."
fi
exit $EXIT_CODE
