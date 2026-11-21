#!/usr/bin/env bash
set -euo pipefail

# Validates GitHub Actions workflow files, referenced scripts, and shell scripts.
# Requires shellcheck for shell linting; YAML is parsed via python (stdlib).

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# WORKFLOWS_DIR lets tests point the checks at a fixture tree; referenced
# scripts are still resolved against REPO_ROOT. REFS_ONLY=1 skips the YAML
# parse and shellcheck stages.
WORKFLOWS_DIR="${WORKFLOWS_DIR:-$REPO_ROOT/.github}"
REFS_ONLY="${REFS_ONLY:-0}"
EXIT_CODE=0

# Prints scripts/*.sh references found in executable positions only: run:
# blocks and `uses: ./...` lines. YAML comments are ignored, as are paths that
# belong to another repository (e.g. skills/x/scripts/y.sh), recognised by a
# path component before scripts/.
extract_script_refs() {
  awk '
    function pathch(c) { return c ~ /[A-Za-z0-9_.\/-]/ }
    function emit(line,   rest, prev, prev2, ok) {
      rest = line
      while (match(rest, /scripts\/[^ "\047]*\.sh/)) {
        prev = (RSTART > 1) ? substr(rest, RSTART - 1, 1) : " "
        ok = !pathch(prev)
        if (prev == "/" && RSTART > 2 && substr(rest, RSTART - 2, 1) == ".") {
          prev2 = (RSTART > 3) ? substr(rest, RSTART - 3, 1) : " "
          ok = !pathch(prev2)
        }
        if (ok) print substr(rest, RSTART, RLENGTH)
        rest = substr(rest, RSTART + RLENGTH)
      }
    }
    {
      line = $0
      match(line, /^[ \t]*/); indent = RLENGTH
      body = substr(line, indent + 1)
      if (body == "") next
      if (inblock) {
        if (indent > runindent) { if (body !~ /^#/) emit(body); next }
        inblock = 0
      }
      if (body ~ /^#/) next
      sub(/^- +/, "", body)
      if (body ~ /^run:/) {
        val = body; sub(/^run:[ \t]*/, "", val)
        if (val ~ /^[|>][-+0-9]*[ \t]*(#.*)?$/) { inblock = 1; runindent = indent }
        else { sub(/[ \t]+#.*$/, "", val); emit(val) }
      } else if (body ~ /^uses:[ \t]*\.\//) {
        emit(body)
      }
    }
  ' "$@"
}

echo "=== Validating YAML files ==="
yaml_files=()
while IFS= read -r f; do
  yaml_files+=("$f")
done < <(find "$WORKFLOWS_DIR" -name '*.yml' -o -name '*.yaml' 2>/dev/null)

if [ "$REFS_ONLY" = 1 ]; then
  echo "  skipped (REFS_ONLY=1)"
elif [ ${#yaml_files[@]} -eq 0 ]; then
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
if [ ${#yaml_files[@]} -gt 0 ]; then
  while IFS= read -r ref; do
    script_refs+=("$ref")
  done < <(extract_script_refs "${yaml_files[@]}" | sort -u)
fi

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

if [ "$REFS_ONLY" = 1 ]; then
  echo "  skipped (REFS_ONLY=1)"
elif [ ${#sh_files[@]} -eq 0 ]; then
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
