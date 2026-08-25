# Current status

- Current milestone: Milestone 3, leases and crash recovery
- Milestone status: in progress
- Completed milestones: Milestones 0, 1, and 2
- Current area of work: Slice 4 complete; Slice 5 not started
- Known blockers: none
- Known architecture deviations: none
- Known implementation deviations: PowerShell provides the common command interface instead of GNU Make. The project plan permits an equivalent command interface, and this choice does not change the system architecture.
- Known limitations: Workers now heartbeat every acquired but unfinished attempt and cancel stale attempt contexts, but no lease reaper or crash recovery transition exists. An expired lease still leaves its job in `running`, and a handler must observe context cancellation to stop promptly. Failure outcomes, general retry policy and backoff, timeout enforcement, panic recovery, cancellation, graceful worker draining, idempotency, metrics, and tracing remain deferred. GitHub-hosted CI has not run for the current Milestone 3 state.
