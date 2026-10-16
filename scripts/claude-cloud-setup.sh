#!/bin/bash
# Environment setup script for Spectra cloud sessions (Claude Code on the web).
# Paste into the environment's "Setup script" field, or run it from the repo.
# Idempotent: every step skips work that is already done.
set -eo pipefail
step() { echo "==> $*"; }
warn() { echo "WARN: $*" >&2; }

SUDO=""; [ "$(id -u)" -ne 0 ] && command -v sudo >/dev/null && SUDO="sudo"
export DEBIAN_FRONTEND=noninteractive
export LANG=C.UTF-8 LC_ALL=C.UTF-8
REPO="${CLAUDE_PROJECT_DIR:-$PWD}"
ARCH="$(dpkg --print-architecture 2>/dev/null || echo amd64)"

step "apt packages"
# iproute2 provides ip/ss, which the Linux netstate backend shells out to;
# procps provides ps/pgrep for the process collectors' smoke tests.
# python3-yaml and shellcheck back scripts/validate-workflows.sh.
$SUDO apt-get update -qq
$SUDO apt-get install -y -qq --no-install-recommends \
  ca-certificates curl git jq make gcc libc6-dev unzip xz-utils \
  iproute2 procps sqlite3 python3 python3-venv python3-yaml shellcheck >/dev/null
# Only needed for `make build-agent` (javac --add-modules jdk.attach); the jar
# is checked in, so a missing JDK is not fatal.
command -v javac >/dev/null \
  || $SUDO apt-get install -y -qq --no-install-recommends openjdk-21-jdk-headless >/dev/null \
  || warn "openjdk-21 install failed; make build-agent will not work"

step "go (version from go.mod)"
# Read the toolchain go.mod asks for, so the container does not re-download it
# through GOTOOLCHAIN=auto on every go invocation in a fresh module cache.
GO_WANT=""
if [ -f "$REPO/go.mod" ]; then
  GO_WANT="$(awk '$1=="toolchain"{sub(/^go/,"",$2); print $2; exit}' "$REPO/go.mod")"
  [ -n "$GO_WANT" ] || GO_WANT="$(awk '$1=="go"{print $2; exit}' "$REPO/go.mod")"
fi
GO_HAVE="$(GOTOOLCHAIN=local /usr/local/go/bin/go env GOVERSION 2>/dev/null | sed 's/^go//' || true)"
if [ -n "$GO_WANT" ] && [ "$GO_HAVE" != "$GO_WANT" ]; then
  echo "installing go$GO_WANT (found: ${GO_HAVE:-none})"
  curl -fsSL --retry 3 -o /tmp/go.tgz "https://go.dev/dl/go${GO_WANT}.linux-${ARCH}.tar.gz"
  $SUDO rm -rf /usr/local/go
  $SUDO tar -C /usr/local -xzf /tmp/go.tgz
  rm -f /tmp/go.tgz
fi
export PATH="/usr/local/go/bin:$PATH"
GOBIN_DIR="$(go env GOPATH)/bin"
export PATH="$GOBIN_DIR:$PATH"
go version

step "go tools (match .github/workflows/ci.yml)"
# Built with the repo's toolchain: a golangci-lint/gosec built with an older Go
# refuses to analyze a module whose go directive is newer than its own.
for pkg in \
  gotest.tools/gotestsum@latest \
  github.com/fzipp/gocyclo/cmd/gocyclo@latest \
  github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest \
  github.com/securego/gosec/v2/cmd/gosec@latest \
  github.com/google/go-licenses@latest; do
  go install "$pkg" || warn "go install $pkg failed"
done
# Symlink into /usr/local/bin so these win over any image-provided copies
# (e.g. an older golangci-lint) even in shells that skip ~/.bashrc.
for bin in gotestsum gocyclo golangci-lint gosec go-licenses; do
  [ -x "$GOBIN_DIR/$bin" ] && $SUDO ln -sf "$GOBIN_DIR/$bin" "/usr/local/bin/$bin"
done
$SUDO ln -sf /usr/local/go/bin/go /usr/local/bin/go
$SUDO ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt

step "docs tooling (mkdocs + lychee)"
# Ubuntu 24.04's Python is externally managed (PEP 668), so mkdocs lives in a venv.
DOCS_VENV="$HOME/.venvs/spectra-docs"
if [ ! -x "$DOCS_VENV/bin/mkdocs" ]; then
  if ! { python3 -m venv "$DOCS_VENV" && "$DOCS_VENV/bin/pip" install -q mkdocs mkdocs-material; }; then
    warn "mkdocs install failed; make docs-build will not work"
  fi
fi
[ -x "$DOCS_VENV/bin/mkdocs" ] && $SUDO ln -sf "$DOCS_VENV/bin/mkdocs" /usr/local/bin/mkdocs
if ! command -v lychee >/dev/null; then
  if [ -f "$REPO/scripts/lychee/install_lychee.sh" ]; then
    bash "$REPO/scripts/lychee/install_lychee.sh" >/tmp/lychee-install.log 2>&1 \
      || warn "lychee install failed (see /tmp/lychee-install.log)"
  fi
fi
[ -x "$HOME/.local/bin/lychee" ] && $SUDO ln -sf "$HOME/.local/bin/lychee" /usr/local/bin/lychee

step "shell env"
if ! grep -q '# >>> spectra env >>>' "$HOME/.bashrc" 2>/dev/null; then
  cat >> "$HOME/.bashrc" <<'EOF'
# >>> spectra env >>>
export LANG=C.UTF-8 LC_ALL=C.UTF-8
export PATH="/usr/local/go/bin:$HOME/go/bin:$HOME/.local/bin:$HOME/.venvs/spectra-docs/bin:$PATH"
# <<< spectra env <<<
EOF
fi

step "repo deps"
cd "$REPO"
if [ -f go.mod ]; then
  go mod download
  # Warm the build cache for both CGO modes: `make test` (-race) needs CGO,
  # while the Linux CI lane builds with CGO_ENABLED=0.
  go build ./... || warn "go build ./... failed"
  CGO_ENABLED=0 go build ./... || warn "CGO_ENABLED=0 go build ./... failed"
  go vet ./... >/dev/null 2>&1 || warn "go vet ./... reported issues"
else
  step "go.mod not found in $PWD; skipping repo deps"
fi
step "done"
