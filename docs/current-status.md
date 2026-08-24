# Current status

- Current milestone: Milestone 2, dispatcher and distributed workers
- Milestone status: in progress
- Completed milestones: Milestones 0 and 1
- Current area of work: Slice 4 complete; Slice 5 not started
- Known blockers: none
- Known architecture deviations: none
- Known implementation deviations: PowerShell provides the common command interface instead of GNU Make. The project plan permits an equivalent command interface, and this choice does not change the system architecture.
- Known limitations: The API and dispatcher processes are runnable, but API-submitted jobs remain queued because the worker runtime is not implemented yet. A gRPC client can register, claim, and report successful attempts through the dispatcher. Claims have no lease or crash recovery, so a lost acquisition response or worker crash can leave a job in `running` until Milestone 3 adds recovery. Failure outcomes, retries, cancellation, idempotency, metrics, and tracing remain deferred. GitHub-hosted CI has not been run for the current milestone.
