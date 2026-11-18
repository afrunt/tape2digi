#!/usr/bin/env bash
# Builds the release artifacts of all supported platforms into dist/.
# Usage: build-dist.sh [--skip-doctor] [--clean] [target ...]
# Targets: darwin-amd64 darwin-arm64 windows-amd64 windows-arm64 linux-amd64 linux-arm64
# (all of them when none is given). VERSION=<v> overrides the git-derived version.
set -euo pipefail

cd "$(dirname "$0")"

all=(darwin-amd64 darwin-arm64 windows-amd64 windows-arm64 linux-amd64 linux-arm64)
doctor=1
clean=0
targets=()

for arg in "$@"; do
  case "$arg" in
    --skip-doctor) doctor=0 ;;
    --clean) clean=1 ;;
    -h|--help) sed -n '2,5p' "$0"; exit 0 ;;
    *)
      if [[ " ${all[*]} " != *" $arg "* ]]; then
        echo "unknown target: $arg (expected one of: ${all[*]})" >&2
        exit 2
      fi
      targets+=("$arg")
      ;;
  esac
done
[[ ${#targets[@]} -gt 0 ]] || targets=("${all[@]}")

[[ $clean -eq 0 ]] || make clean
[[ $doctor -eq 0 ]] || make doctor

mkdir -p dist
for t in "${targets[@]}"; do
  echo "==> dist-$t"
  make "dist-$t" ${VERSION:+VERSION="$VERSION"}
done

echo "Artifacts in dist/:"
ls -1 dist
