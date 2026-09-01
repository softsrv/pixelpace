# Reconnect via client-side buffer-and-replay, not a server-side pause/grace-period

Typical multiplayer designs pause a match, or hold a live "waiting to reconnect" state, when a player's connection drops. We rejected that here because rowing is a continuous physical activity: a racer mid-stroke on a physical machine has no reason to stop and fix their device, and a Race can't meaningfully pause for a real-time physical action anyway.

Instead, a dropped Connection never pauses the Race. The client buffers Telemetry Samples locally while disconnected — each still carrying its true elapsed-time/timestamp — and replays them as a batch once reconnected, so only the server's *view* of that racer is delayed, never the racer's actual rowing. A second tab/device attempting to join the same Race while a Connection is already active is rejected outright, never allowed to take over.
