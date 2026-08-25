# Current status

- Current milestone: Milestone 3, leases and crash recovery
- Milestone status: in progress
- Completed milestones: Milestones 0, 1, and 2
- Current area of work: Slice 6 complete; Milestone 3 audit not started
- Known blockers: none
- Known architecture deviations: none
- Known implementation deviations: PowerShell provides the common command interface instead of GNU Make. The project plan permits an equivalent command interface, and this choice does not change the system architecture.
- Known limitations: The worker-crash acceptance path now passes with real API, dispatcher, worker, and PostgreSQL processes, but Milestone 3 remains in progress until its separate audit passes. A handler must observe context cancellation to stop promptly. Failure outcomes, general retry policy and backoff, timeout enforcement, panic recovery, cancellation, graceful worker draining, idempotency, metrics, and tracing remain deferred. GitHub-hosted CI has not run for the current Milestone 3 state.
