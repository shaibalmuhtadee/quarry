# Current status

- Current milestone: Milestone 4, retries and execution controls
- Milestone status: complete
- Completed milestones: Milestones 0, 1, 2, 3, and 4
- Current area of work: Milestone 4 audit complete; Milestone 5 not started
- Known blockers: none
- Known architecture deviations: none
- Known implementation deviations: PowerShell provides the common command interface instead of GNU Make. The project plan permits an equivalent command interface, and this choice does not change the system architecture.
- Known limitations: Cancellation, timeout, and forced-shutdown cancellation are cooperative; Quarry cannot forcibly terminate a handler that ignores its context before the worker process exits. Metrics and tracing remain deferred to Milestone 5. GitHub-hosted CI has not run for the current state.
