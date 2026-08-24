# Current status

- Current milestone: Milestone 2, dispatcher and distributed workers
- Milestone status: in progress
- Completed milestones: Milestones 0 and 1
- Current area of work: Slice 2 complete; Slice 3 not started
- Known blockers: none
- Known architecture deviations: none
- Known implementation deviations: PowerShell provides the common command interface instead of GNU Make. The project plan permits an equivalent command interface, and this choice does not change the system architecture.
- Known limitations: The runnable system still leaves API-submitted jobs queued because the dispatcher service and worker runtime are not implemented yet. Direct store claims have no lease or crash recovery, so a lost acquisition response or worker crash can leave a job in `running` until Milestone 3 adds recovery. Successful completion, attempt history, retries, cancellation, idempotency, metrics, and tracing remain deferred. GitHub-hosted CI has not been run for the current milestone.
