# Current status

- Current milestone: Milestone 2, dispatcher and distributed workers
- Milestone status: in progress
- Completed milestones: Milestones 0 and 1
- Current area of work: All Milestone 2 slices complete; separate milestone audit not started
- Known blockers: none
- Known architecture deviations: none
- Known implementation deviations: PowerShell provides the common command interface instead of GNU Make. The project plan permits an equivalent command interface, and this choice does not change the system architecture.
- Known limitations: Claims have no lease or crash recovery, so a lost acquisition response or worker crash can leave a job in `running` until Milestone 3 adds recovery. Failure outcomes, durable retries, timeout enforcement, panic recovery, cancellation, graceful worker draining, idempotency, metrics, and tracing remain deferred. The separate Milestone 2 audit and GitHub-hosted CI have not been run.
