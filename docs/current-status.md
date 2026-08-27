# Current status

- Current milestone: Milestone 5, observability
- Milestone status: complete
- Completed milestones: Milestones 0, 1, 2, 3, 4, and 5
- Current area of work: Milestone 5 audit complete; Milestone 6 not started
- Known blockers: none
- Known architecture deviations: none
- Known implementation deviations: PowerShell provides the common command interface instead of GNU Make. The project plan permits an equivalent command interface, and this choice does not change the system architecture.
- Known limitations: Cancellation, timeout, and forced-shutdown cancellation are cooperative; Quarry cannot forcibly terminate a handler that ignores its context before the worker process exits. Local Jaeger trace storage is in memory. GitHub-hosted CI has not run for the current state.
