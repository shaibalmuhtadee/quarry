# Current status

- Current milestone: Milestone 5, observability
- Milestone status: in progress
- Completed milestones: Milestones 0, 1, 2, 3, and 4
- Current area of work: Milestone 5 Slices 1 through 5 complete; Slice 6 not started
- Known blockers: none
- Known architecture deviations: none
- Known implementation deviations: PowerShell provides the common command interface instead of GNU Make. The project plan permits an equivalent command interface, and this choice does not change the system architecture.
- Known limitations: Cancellation, timeout, and forced-shutdown cancellation are cooperative; Quarry cannot forcibly terminate a handler that ignores its context before the worker process exits. The telemetry runtime, Prometheus metrics, and durable trace-context transport exist, but trace extraction into worker execution, execution spans, dashboards, and local observability infrastructure remain deferred to later Milestone 5 slices. GitHub-hosted CI has not run for the current state.
