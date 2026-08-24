# Current status

- Current milestone: Milestone 2, dispatcher and distributed workers
- Milestone status: complete
- Completed milestones: Milestones 0, 1, and 2
- Current area of work: Milestone 2 audit complete; Milestone 3 not started
- Known blockers: none
- Known architecture deviations: none
- Known implementation deviations: PowerShell provides the common command interface instead of GNU Make. The project plan permits an equivalent command interface, and this choice does not change the system architecture.
- Known limitations: Claims have no lease or crash recovery, so a lost acquisition response or worker crash can leave a job in `running` until Milestone 3 adds recovery. Failure outcomes, durable retries, timeout enforcement, panic recovery, cancellation, graceful worker draining, idempotency, metrics, and tracing remain deferred. GitHub-hosted CI has not run for the Milestone 2 completion state.
