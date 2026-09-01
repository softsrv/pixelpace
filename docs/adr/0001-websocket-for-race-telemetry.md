# WebSocket for race telemetry, not SSE+POST

Live races need each participant's browser to both stream its own ~1Hz Telemetry Sample and receive every other participant's samples in real time. We chose a single persistent WebSocket connection per Race over Server-Sent-Events-downstream-plus-POST-upstream, even though it requires a third-party library — a deliberate exception to this repo's stdlib-first default.

## Considered Options

- **SSE (downstream) + POST (upstream)**: plain HTTP, reuses the existing middleware chain for the upstream leg, and gets free browser auto-reconnect. Rejected because SSE is unidirectional by spec, so it always needs pairing with a separate upstream channel — splitting one symmetrical, same-Race, real-time problem into two independently-failing ones, for no real benefit at this traffic volume (a handful to dozens of concurrent racers per Race).
