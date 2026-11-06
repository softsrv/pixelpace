# PixelPace

An online rowing application where users connect a rowing machine (or a dev-mode mock) and race other users in real time.

## Language

**Activity**:
A rowing session in the application undertaken by one or more participants: a Race, a Time Trial, or a Warmup.
_Avoid_: Session, match, workout

**Activity Type**:
The category of activity: **Race**, **Time Trial**, or **Warmup**. Only a race has a lobby (Room) and matchmaking (Quick Match); Time Trial and Warmup start immediately as solo activities.
_Avoid_: Mode, game mode, session mode

**Kind**:
The measurement category of a Preset: **Fixed Distance**, **Fixed Time**, or **Interval**. Race and Time Trial activities have a Kind (determined by their Preset); Warmup has none (it is always a timed 1–20 minute solo session).
_Avoid_: Format, workout type, activity type

**Preset**:
Defines an activity's win condition and target, chosen from a **fixed preset catalog** — never a freeform value — so results stay comparable within a Leaderboard. Starts with **Fixed Distance** (500m / 1000m / 2000m / 5000m / 10000m — first to reach the target wins, ranked by elapsed time). Designed to extend to **Fixed Time** (5 / 10 / 20 minutes — most distance covered wins) and **Interval** (a sequence of Segments) without a schema redesign.
_Note_: Wire names for the preset stay unchanged from the earlier Race Type naming: form field `race_type_id`, URL query parameter `race_type`, and endpoint `/rooms/{id}/race-type` (decision U1).
_Avoid_: Race Type, race format, game mode

**Race**:
A live, synchronous, multi-participant rowing event where all participants start at the same instant and are ranked by outcome when it ends.
_Avoid_: Session, match, workout

**Time Trial**:
An activity type chosen on the dashboard, not an entrant count: a racer rowing alone against the clock to set a personal best. A begun race stays a race even if others drop out; a room created as a race never silently turns into a time trial. A Time Trial is an independent activity type, not a kind of Race.
A Time Trial whose racer has an earlier finished Time Trial at that Preset shows a **Pacer**: a boat that rows the distance at the racer's personal best (their fastest such time). The Pacer is a lane only — never an entrant, never ranked, never on a Leaderboard. A first Time Trial at a Preset has no Pacer.
_Avoid_: Solo race, practice, workout

**Warmup**:
A timed 1–20 minute solo activity with no pacer, chosen on the dashboard, ending in a summary only. Has no Kind or Preset. Nothing is saved beyond the activity itself: never on a leaderboard, never used as a pacer time, never used in matchmaking, and never posted to Concept2.
_Avoid_: Practice, free row, cooldown

**Leaderboard**:
A ranking of each racer's own **personal-best** result (not every result they've ever posted) for a single Preset and **activity type** — Race or Time Trial. The two activity types never mix: the 500m Race Leaderboard and the 500m Time Trial Leaderboard are separate rankings. Warmup activities never appear on a leaderboard. Further filtered by a **time window** (All-Time / Month / Week — each its own ranking, not a combined one) and a **scope** (Global, or Friends-only). An "in your area" geography-based scope is a deferred future idea, not yet designed.
_Avoid_: Rankings, standings

**Telemetry Sample**:
A hardware-agnostic snapshot of a participant's rowing performance at a point in time (elapsed time, distance, stroke rate, power), translated from whatever device produced it before it reaches the server. The server never speaks a device-specific protocol.
_Avoid_: PM5 data, raw metrics, BLE notification

**Server Instance**:
A running copy of the app process. Multiple Server Instances exist only for horizontal scaling or zero-downtime rolling deploys; a Race's real-time broadcast must reach every participant's Connection regardless of which Server Instance holds it.
_Avoid_: Node, replica, pod, instance (when the meaning is ambiguous — always say "Server Instance" or "Connection")

**Connection**:
A single browser tab or device's live link to a Race. A racer holds at most one active Connection to a given Race at a time — a second tab/device attempting to join is rejected outright, never a takeover. A Race is never paused for a dropped Connection: the client buffers Telemetry Samples locally while disconnected and replays them, each with its original timestamp, once reconnected.
_Avoid_: Session (already used for auth device sessions), socket, instance

**Room**:
The private space where racers gather before a Race starts, entered via a shareable code or a direct invite to a Friend. Only Race activities use a Room; Time Trial and Warmup activities bypass it. Has a Host, and starts once every participant is Ready.
_Avoid_: Lobby, race room

**Host**:
The Room participant with control — configures the Race, can kick a non-Ready participant, and triggers the Countdown once everyone is Ready. The Room's creator is Host by default. If the Host leaves, a random remaining participant becomes Host — the same rule for both a manually-formed Room and a Quick Match Room, no best-average preference. If every participant leaves, the Room is dissolved.
_Avoid_: Owner, organizer

**Ready**:
A per-participant status in a Room signaling they're prepared to race. The Host can only start once every participant is Ready, and a Room needs at least 2 participants (max 8) to start. Once started, a begun race stays a race.
_Avoid_: Ready-up (as a verb, fine informally, but the status itself is "Ready")

**Countdown**:
The fixed 10-second delay between the Host starting a Race and the Race actually beginning.
_Avoid_: Start delay

**Quick Match**:
An alternative to a private Room: a racer joins a queue and is automatically paired, via Matchmaking, against other queued racers for the same Preset. Every room created by Quick Match is a Race activity.
_Avoid_: Ranked queue, auto-match

**Matchmaking**:
Pairing Quick Match queue entrants using a rolling average of each racer's own recent finish times (from Race and Time Trial finishes, never Warmup) for the Preset they're queuing for. No cross-player skill rating (e.g. ELO) — each racer is compared only against their own history.
_Avoid_: Skill rating, ELO, ranking

**Friend**:
A mutual relationship between two Users, established when one sends a Friend Request and the other accepts. Enables direct Room invites.
_Avoid_: Follower, contact

**Friend Request**:
An invitation from one User to another to become Friends. If rejected, the sender may not re-send to the same recipient for a 10-day cooldown; the recipient may still send one to the original sender at any time during that window, in case the rejection was accidental.
_Avoid_: Friend invite (fine informally; "Friend Request" is canonical)

**DNF**:
A Race outcome for a participant whose Connection was lost for good (not a brief, buffered drop) before finishing. Excluded from the Leaderboard, but its Telemetry Samples are kept — e.g. as future input to Matchmaking via completion rate.
_Avoid_: Abandoned, incomplete

**Segment**:
One distance-or-time chunk of an Interval Preset (e.g. the "500m" in a 500m/1000m/500m pyramid). An Interval Preset is an ordered sequence of Segments, not a single scalar target — segments are not assumed uniform.
_Avoid_: Interval (that's the Preset; "Segment" is one piece of it), split
