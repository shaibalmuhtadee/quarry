# Current status

- Current milestone: Milestone 3, leases and crash recovery
- Milestone status: in progress
- Completed milestones: Milestones 0, 1, and 2
- Current area of work: Slice 5 complete; Slice 6 not started
- Known blockers: none
- Known architecture deviations: none
- Known implementation deviations: PowerShell provides the common command interface instead of GNU Make. The project plan permits an equivalent command interface, and this choice does not change the system architecture.
- Known limitations: The dispatcher now recovers expired leases in bounded PostgreSQL batches, fences expired and stale success reports, and marks stale workers lost. The end-to-end worker-process crash acceptance test and developer flow remain for Slice 6, and a handler must observe context cancellation to stop promptly. Failure outcomes, general retry policy and backoff, timeout enforcement, panic recovery, cancellation, graceful worker draining, idempotency, metrics, and tracing remain deferred. GitHub-hosted CI has not run for the current Milestone 3 state.
