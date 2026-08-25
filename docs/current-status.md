# Current status

- Current milestone: Milestone 3, leases and crash recovery
- Milestone status: complete
- Completed milestones: Milestones 0, 1, 2, and 3
- Current area of work: Milestone 3 audit complete; Milestone 4 not started
- Known blockers: none
- Known architecture deviations: none
- Known implementation deviations: PowerShell provides the common command interface instead of GNU Make. The project plan permits an equivalent command interface, and this choice does not change the system architecture.
- Known limitations: A handler must observe context cancellation to stop promptly. Failure outcomes, general retry policy and backoff, timeout enforcement, panic recovery, cancellation, graceful worker draining, idempotency, metrics, and tracing remain deferred. GitHub-hosted CI has not run for the completed Milestone 3 state.
