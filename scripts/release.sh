#!/usr/bin/env bash
# Cut a watts release: test, tag, publish the GitHub release, and point the
# Homebrew tap formula at it. The version is injected at build time with
# -ldflags, so no version bump commit is needed.
#
# Usage: scripts/release.sh [--dry-run] X.Y.Z
# Env:   TAP_DIR  path to the homebrew-tap checkout (default ../homebrew-tap)
set -euo pipefail

DRY_RUN=0
if [[ "${1:-}" == "--dry-run" ]]; then DRY_RUN=1; shift; fi
VERSION="${1:-}"
if [[ ! "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "usage: $0 [--dry-run] X.Y.Z" >&2
  exit 2
fi

REPO_DIR="$(cd "$(dirname "$0")/.." && pwd)"
TAP_DIR="${TAP_DIR:-$REPO_DIR/../homebrew-tap}"
TAG="v$VERSION"
URL="https://codeload.github.com/kevnnard/watts/tar.gz/refs/tags/$TAG"
FORMULA="Formula/watts.rb"

run() {
  if [[ $DRY_RUN -eq 1 ]]; then echo "[dry-run] $*"; else "$@"; fi
}

# fail stops a real release; a dry run reports it and keeps going.
fail() {
  if [[ $DRY_RUN -eq 1 ]]; then echo "[dry-run] would stop: $*" >&2; else echo "$*" >&2; exit 1; fi
}

cd "$REPO_DIR"

# Preflight: fail before changing anything.
[[ "$(git branch --show-current)" == "main" ]] || fail "release from main only"
if [[ -n "$(git status --porcelain --untracked-files=no)" ]]; then
  fail "tracked changes present; commit or stash first"
fi
if git rev-parse -q --verify "refs/tags/$TAG" >/dev/null || git ls-remote --exit-code --tags origin "refs/tags/$TAG" >/dev/null 2>&1; then
  fail "$TAG already exists"
fi
[[ -d "$TAP_DIR/Formula" ]] || fail "tap checkout not found under $TAP_DIR (set TAP_DIR)"
if [[ -n "$(git -C "$TAP_DIR" status --porcelain --untracked-files=no 2>/dev/null)" ]]; then
  fail "tap checkout has tracked changes: $TAP_DIR"
fi
gh auth status >/dev/null 2>&1 || fail "gh is not authenticated"

echo "==> tests"
go test ./... -count=1
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /dev/null ./cmd/watts

echo "==> tag and push"
run git tag -a "$TAG" -m "watts $TAG"
run git push origin main
run git push origin "$TAG"

echo "==> archive hash (the exact tarball Homebrew downloads)"
# Hash codeload's tarball for the tag. A local git archive has different bytes,
# so its hash would fail Homebrew's check.
SHA="0000000000000000000000000000000000000000000000000000000000000000"
ARCHIVE=""
if [[ $DRY_RUN -eq 0 ]]; then
  ARCHIVE="$(mktemp -d)/watts-$TAG.tar.gz"
  for attempt in 1 2 3 4 5; do
    if curl -fsSL "$URL" -o "$ARCHIVE"; then break; fi
    echo "archive not ready (attempt $attempt), retrying" >&2
    sleep 5
  done
  SHA="$(shasum -a 256 "$ARCHIVE" | cut -d' ' -f1)"
fi
echo "sha256 $SHA"

echo "==> GitHub release"
run gh release create "$TAG" ${ARCHIVE:+"$ARCHIVE"} --title "watts $TAG" --generate-notes

echo "==> tap formula"
if [[ $DRY_RUN -eq 1 ]]; then
  python3 "$REPO_DIR/scripts/release_tap.py" - "$URL" "$VERSION" "$SHA"
  echo "[dry-run] would write $TAP_DIR/$FORMULA (above), then commit and push the tap"
else
  python3 "$REPO_DIR/scripts/release_tap.py" "$TAP_DIR/$FORMULA" "$URL" "$VERSION" "$SHA"
  git -C "$TAP_DIR" add "$FORMULA"
  git -C "$TAP_DIR" commit -m "watts $TAG"
  git -C "$TAP_DIR" push origin HEAD
fi

echo "==> done: $TAG"
echo "   brew update && brew upgrade kevnnard/tap/watts"
