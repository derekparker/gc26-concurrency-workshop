#!/bin/bash

echo "✅ Exercise Solution Checker"
echo "============================"

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# Run a command with a timeout (portable: macOS has no `timeout` by default).
run_with_timeout() {
    local secs=$1; shift
    "$@" > /dev/null 2>&1 &
    local pid=$!
    (
        sleep "$secs"
        kill -9 "$pid" 2>/dev/null
    ) &
    local watcher=$!
    wait "$pid" 2>/dev/null
    local status=$?
    kill "$watcher" 2>/dev/null
    wait "$watcher" 2>/dev/null
    return $status
}

check_race_detector() {
    echo "🔍 Race Detector exercises (pass = clean under -race)..."
    for dir in "$REPO_ROOT"/01-race-detector/exercises/ex*/; do
        [ -d "$dir" ] || continue
        local_name=$(basename "$dir")
        if ls "$dir"/*_test.go > /dev/null 2>&1; then
            cmd="go test -race -count=1 ."
        else
            cmd="go run -race ."
        fi
        if (cd "$dir" && run_with_timeout 90 $cmd); then
            echo "  ✅ $local_name: no race detected — looks solved"
        else
            echo "  ❌ $local_name: race detected (or program failed/hung) — keep going"
        fi
    done
}

check_execution_tracer() {
    echo "📊 Execution Tracer exercises (pass = program builds and runs)..."
    for dir in "$REPO_ROOT"/02-execution-tracer/exercises/ex*/; do
        [ -d "$dir" ] || continue
        local_name=$(basename "$dir")
        if [ -f "$dir/main.go" ]; then
            if (cd "$dir" && run_with_timeout 120 go run .); then
                echo "  ✅ $local_name: runs successfully"
            else
                echo "  ❌ $local_name: build/runtime error or hang"
            fi
        elif [ -f "$dir/go.mod" ]; then
            if (cd "$dir" && go build ./... > /dev/null 2>&1); then
                echo "  ✅ $local_name: builds (run its cmd/ tools per the README)"
            else
                echo "  ❌ $local_name: build error"
            fi
        fi
    done
}

check_delve() {
    echo "🐛 Delve exercises build check (debugging skill is checked by you!)..."
    for dir in "$REPO_ROOT"/03-delve/exercises/ex*/; do
        [ -f "$dir/go.mod" ] || continue
        local_name=$(basename "$dir")
        if (cd "$dir" && go build -o /dev/null . > /dev/null 2>&1); then
            echo "  ✅ $local_name: builds"
        else
            echo "  ❌ $local_name: build error"
        fi
    done
    echo "  ℹ️  Delve exercises are verified by the walkthroughs in each README."
}

check_race_detector
echo ""
check_execution_tracer
echo ""
check_delve

echo ""
echo "💡 Solutions are in each exercise's README.md (behind the fold)."
