#!/bin/bash
set -euo pipefail

# Usage: ./scripts/bench.sh [mode]
# Modes:
#   main  (default) - compare current branch vs main
#   prev            - compare vs the previous commit (HEAD^1)
#   local           - compare vs a previously saved local baseline (.benchmarks/local.txt)

MODE="${1:-main}"
mkdir -p .benchmarks

ORIG_REF="$(git rev-parse --abbrev-ref HEAD)"
if [ "$ORIG_REF" = "HEAD" ]; then
  ORIG_REF="$(git rev-parse HEAD)" # detached: fall back to the SHA
fi
cleanup() {
  git checkout --quiet "$ORIG_REF" 2>/dev/null || true
}
trap cleanup EXIT INT TERM


if ! git diff-index --quiet HEAD --; then
  echo "error: working tree is dirty; commit or stash before benchmarking." >&2
  exit 1
fi

run_bench() {
  go test -bench=. -benchmem -run='^$' -count=5 ./... > ".benchmarks/$1.txt"
}

case "$MODE" in
  main)
    echo "Comparing current branch vs main..."
    run_bench "current"
    git checkout --quiet main
    run_bench "main"
    git checkout --quiet "$ORIG_REF"
    benchstat .benchmarks/main.txt .benchmarks/current.txt
    ;;

  prev)
    echo "Comparing vs previous commit (HEAD^1)..."
    run_bench "current"
    git checkout --quiet HEAD^1
    run_bench "prev"
    git checkout --quiet "$ORIG_REF"
    benchstat .benchmarks/prev.txt .benchmarks/current.txt
    ;;

  local)
    echo "Comparing vs existing local baseline..."
    if [ ! -f .benchmarks/local.txt ]; then
      echo "error: no baseline found. Run 'make bench-init' first." >&2
      exit 1
    fi
    run_bench "current"
    benchstat .benchmarks/local.txt .benchmarks/current.txt
    ;;

  *)
    echo "unknown mode: $MODE (expected: main | prev | local)" >&2
    exit 1
    ;;
esac