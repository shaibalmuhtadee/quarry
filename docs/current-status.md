# Current status

- Current milestone: Milestone 1, durable HTTP job API
- Milestone status: in progress
- Completed milestones: Milestone 0
- Current area of work: Slices 1 and 2 complete; Slice 3 not started
- Known blockers: none
- Known architecture deviations: none
- Known implementation deviations: PowerShell provides the common command interface instead of GNU Make. The project plan permits an equivalent command interface, and this choice does not change the system architecture.
- Known limitations: `cmd/api` remains a one-shot PostgreSQL connection check until a later Milestone 1 slice. No dispatcher, worker, or job execution capability exists.
