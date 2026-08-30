# Current status

- Current milestone: Milestone 7, packaging and portfolio polish
- Milestone status: in progress
- Completed milestones: Milestones 0, 1, 2, 3, 4, 5, and 6
- Current area of work: Slice 4 is complete; Slice 5 has not started
- Known blockers: `kind` is not installed on the current development machine and is required before the kind deployment proof can run
- Known architecture deviations: none
- Known implementation deviations: PowerShell provides the common command interface instead of GNU Make. The project plan permits an equivalent command interface, and this choice does not change the system architecture.
- Known limitations: Three incomplete benchmark campaigns remain preserved as invalid evidence. Published results describe one local machine under a fixed maximum of eight outstanding jobs and do not establish production capacity. Cancellation, timeout, and forced-shutdown cancellation are cooperative; Quarry cannot forcibly terminate a handler that ignores its context before the worker process exits. Local Jaeger trace storage is in memory. GitHub-hosted CI has not run for the current state.
