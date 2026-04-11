# Master UPS Observability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a master-side UPS status view to `/status` and a config-controlled log line for successful UPS polls.

**Architecture:** Extend master config with a `log_ups_status` switch, keep the latest UPS poll snapshot in memory on the server, and surface that snapshot through the existing `/status` response. Successful polls optionally emit a concise log line, while failures keep using the existing error path.

**Tech Stack:** Go, YAML config loading, existing HTTP status endpoint, Go tests

---

### Task 1: Add config and API shape

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/protocol/messages.go`

- [ ] Step 1: Write failing tests for config defaults and parsing
- [ ] Step 2: Run the focused config tests and confirm the new assertions fail
- [ ] Step 3: Add the new master config field and the `/status` response struct for UPS data
- [ ] Step 4: Re-run the focused config tests and confirm they pass

### Task 2: Track UPS status in memory and expose it via `/status`

**Files:**
- Modify: `internal/master/server.go`
- Modify: `internal/master/server_test.go`

- [ ] Step 1: Write a failing test proving `Status()` returns the latest UPS values and latest error metadata
- [ ] Step 2: Run the focused master tests and confirm they fail for the new expectations
- [ ] Step 3: Add in-memory UPS snapshot storage and update it from the polling path
- [ ] Step 4: Re-run the focused master tests and confirm they pass

### Task 3: Add the optional UPS success log line

**Files:**
- Modify: `internal/master/server.go`
- Modify: `internal/master/server_test.go`

- [ ] Step 1: Write a failing test proving successful UPS polls log only when `log_ups_status` is enabled
- [ ] Step 2: Run the focused master test and confirm it fails
- [ ] Step 3: Add the config-controlled UPS success log line
- [ ] Step 4: Re-run the focused master test and confirm it passes

### Task 4: Update examples and verify end to end

**Files:**
- Modify: `configs/master.example.yaml`
- Modify: `scripts/install-master.sh`
- Modify: `README.md`

- [ ] Step 1: Add the new config field to generated and example master configs
- [ ] Step 2: Document how to enable UPS status logging and where to view it
- [ ] Step 3: Run `go test ./internal/config ./internal/master -v`
- [ ] Step 4: Run `go test ./...`
