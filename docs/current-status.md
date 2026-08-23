# Current status

- Current milestone: Milestone 1, durable HTTP job API
- Milestone status: complete
- Completed milestones: Milestones 0 and 1
- Current area of work: Milestone 1 audit complete; Milestone 2 not started
- Known blockers: none
- Known architecture deviations: none
- Known implementation deviations: PowerShell provides the common command interface instead of GNU Make. The project plan permits an equivalent command interface, and this choice does not change the system architecture.
- Known limitations: Jobs remain queued because no dispatcher, worker, or execution capability exists. Retries, leases, cancellation, idempotency, metrics, and tracing remain deferred to later milestones. The audit verified local validation but did not run GitHub-hosted CI.
