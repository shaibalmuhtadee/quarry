# Current status

- Current milestone: Milestone 3, leases and crash recovery
- Milestone status: in progress
- Completed milestones: Milestones 0, 1, and 2
- Current area of work: Slice 3 complete; Slice 4 not started
- Known blockers: none
- Known architecture deviations: none
- Known implementation deviations: PowerShell provides the common command interface instead of GNU Make. The project plan permits an equivalent command interface, and this choice does not change the system architecture.
- Known limitations: The dispatcher persists worker heartbeats and renews valid leases, but workers do not send periodic heartbeats yet. No lease reaper or crash recovery transition exists, so an expired lease still leaves its job in `running`. Failure outcomes, general retry policy and backoff, timeout enforcement, panic recovery, cancellation, graceful worker draining, idempotency, metrics, and tracing remain deferred. GitHub-hosted CI has not run for the current Milestone 3 state.
