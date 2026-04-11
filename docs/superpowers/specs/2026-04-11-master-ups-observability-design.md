# Master UPS Observability Design

## Goal

Make `nut-master` expose the latest UPS polling result in `/status`, and optionally print successful UPS poll results into the service log for troubleshooting.

## Scope

- Add a master config switch to enable UPS status logging.
- Track the latest successful UPS reading and the latest polling error in memory.
- Extend `/status` so operators can see recent UPS values and recent polling errors.
- Keep the new data runtime-only. Do not store it in the existing master state file.

## Approach

### Config

Add a top-level boolean field to the master config:

- `log_ups_status: false`

Default is `false` so existing installs do not start emitting a log line every poll cycle.

### Runtime State

Store the latest UPS observation on the master server instance:

- Target host
- Last successful values:
  - `on_battery`
  - `battery_charge`
  - `runtime_minutes`
  - `last_success_at`
- Last polling failure:
  - `last_error`
  - `last_error_at`

Successful polls update the values and clear the error message. Failed polls keep the last successful values, while updating the latest error fields.

### Status API

Extend `/status` with a new `ups` object that reports the latest runtime view of UPS polling.

If the master has not yet completed a successful poll, value fields remain empty while error fields can still be present.

### Logging

When `log_ups_status` is `true`, each successful poll writes one concise line like:

`ups status target=10.0.0.31 on_battery=false charge=95 runtime_minutes=42`

Polling failures keep the existing error log path.

## Non-Goals

- No persistence of UPS poll snapshots across restarts
- No new endpoint beyond extending `/status`
- No change to shutdown decision rules

## Testing

- Config load test for the new logging switch
- Server status test proving `/status` includes recent UPS state
- Logging test proving successful UPS polls only log when the switch is enabled
