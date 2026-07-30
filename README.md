# Go Concurrency: Debugging Goroutines and Channels

## GopherCon 2026 Workshop

A 4-hour, hands-on workshop covering Go's concurrency debugging tools: the **Race Detector**, the **Execution Tracer** (including the Flight Recorder), and the **Delve debugger**.

## Why This Workshop, Now

AI coding tools are changing what engineers spend their time on. As agents write more of the code, your time shifts toward architecture, review, deployment, and above all, **debugging code you didn't write**. That makes diagnostic tooling *more* important, not less:

- These tools give you **ground truth** about running programs, schedules, data races, blocked goroutines, that no amount of reading source code (by you *or* an AI) can provide.
- The artifacts they produce (race reports, execution traces, debugger sessions) are exactly the high-signal evidence that makes AI assistants effective. An agent with a race report or a trace fixes the real bug; an agent without one guesses.

Mastering these tools is how you stay the engineer who can say *what the program actually did*.

## Quick Start

```bash
# Verify Go + Delve are installed and ready
./setup.sh
```

## Workshop Structure

| Part | Topic | Directory |
|------|-------|-----------|
| I | Race Detector | [`01-race-detector/`](01-race-detector/) |
| II | Execution Tracer & Flight Recorder | [`02-execution-tracer/`](02-execution-tracer/) |
| III | Delve Debugger | [`03-delve/`](03-delve/) |

Each part contains:

- `demo/`, a guided walkthrough you work through yourself
- `exercises/`, buggy programs **you** debug yourself using the tool just covered

## Prerequisites

- **Go 1.26+** (`go version` to check)
- **Delve** (installed by `./setup.sh` if missing)
- Basic Go experience (6+ months)
- Familiarity with goroutines and channels

All demos and exercises run fully offline, no conference-WiFi dependency.
The only network access needed is a one-time `go mod download` for the
Part II capstone (pre-fetch it before the workshop).

## Repository Structure

```
01-race-detector/    # Race detection demo and exercises
02-execution-tracer/ # Execution tracing + flight recorder demo and exercises
03-delve/            # Delve debugging demo and exercises
scripts/             # Utility scripts (exercise checker)
```

## Getting Help

- Run `./scripts/exercise_checker.sh` to validate your exercise solutions
- Run `./cleanup.sh` to reset the repo to a pristine state between attempts

Happy debugging! 🐛➡️✅
