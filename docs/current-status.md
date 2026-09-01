# Current status

- Current milestone: Resume-ready V1
- Milestone status: complete
- Completed milestones: Milestones 0 through 7
- Current area of work: Resume-ready V1 is complete; no V2 work has started
- Known blockers: none
- Known architecture deviations: none
- Known implementation deviations: PowerShell provides the common command interface instead of GNU Make. The project plan permits an equivalent command interface, and this choice does not change the system architecture.
- Known limitations: Quarry provides at-least-once, not exactly-once, execution. Compose and kind are local demonstrations, not production high-availability deployments. Three incomplete benchmark campaigns remain preserved as invalid evidence. Published results describe one local machine under a fixed maximum of eight outstanding jobs and do not establish production capacity. Cancellation, timeout, and forced-shutdown cancellation are cooperative; Quarry cannot forcibly terminate a handler that ignores its context before the worker process exits. Local Jaeger trace storage is in memory.
