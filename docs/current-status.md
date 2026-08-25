# Current status

- Current milestone: Milestone 3, leases and crash recovery
- Milestone status: in progress
- Completed milestones: Milestones 0, 1, and 2
- Current area of work: Slice 2 complete; Slice 3 not started
- Known blockers: none
- Known architecture deviations: none
- Known implementation deviations: PowerShell provides the common command interface instead of GNU Make. The project plan permits an equivalent command interface, and this choice does not change the system architecture.
- Known limitations: The heartbeat and abandoned-attempt contracts exist, but no runtime heartbeat, lease, renewal, reaper, or crash recovery behavior exists yet. A lost acquisition response or worker crash can still leave a job in `running`. Failure outcomes, general retry policy and backoff, timeout enforcement, panic recovery, cancellation, graceful worker draining, idempotency, metrics, and tracing remain deferred. GitHub-hosted CI has not run for the current Milestone 3 state.
