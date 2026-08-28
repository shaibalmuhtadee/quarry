# Current status

- Current milestone: Milestone 6, failure suite and benchmarking
- Milestone status: in progress
- Completed milestones: Milestones 0, 1, 2, 3, 4, and 5
- Current area of work: Milestone 6 Slice 7 closed with two invalid campaigns preserved; publishable results and Workload C campaign debugging are deferred, and the Milestone 6 audit has not started
- Known blockers: Milestone 6 cannot meet its benchmark-evidence definition of done until Workload C campaign debugging and a complete replacement campaign resume
- Known architecture deviations: none
- Known implementation deviations: PowerShell provides the common command interface instead of GNU Make. The project plan permits an equivalent command interface, and this choice does not change the system architecture.
- Known limitations: The two preserved full benchmark campaigns are invalid. Quarry has no publishable benchmark results or benchmark documentation, and partial campaign measurements do not support resume claims. Cancellation, timeout, and forced-shutdown cancellation are cooperative; Quarry cannot forcibly terminate a handler that ignores its context before the worker process exits. Local Jaeger trace storage is in memory. GitHub-hosted CI has not run for the current state.
