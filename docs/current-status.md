# Current status

- Current milestone: Milestone 0, repository skeleton and persistence
- Milestone status: complete
- Completed milestones: Milestone 0
- Current area of work: Milestone 0 audit complete; Milestone 1 not started
- Known blockers: none
- Known architecture deviations: none
- Known implementation deviations: PowerShell provides the common command interface instead of GNU Make. The project plan permits an equivalent command interface, and this choice does not change the system architecture.
- Known limitations: `cmd/api` is a one-shot PostgreSQL connection check, and the schema contains only the job and attempt fields needed by Milestone 0. Later milestones add the HTTP API and execution-state fields.
