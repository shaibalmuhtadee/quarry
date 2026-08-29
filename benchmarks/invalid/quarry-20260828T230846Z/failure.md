# Invalid campaign record

This campaign started from clean commit `51a6cbd6211fa58e989c5def605c3dac8a3e5929` and stopped during Workload C repetition 3.

- All 24 Workload A and B runs completed with generated summaries.
- Workload C repetitions 1 and 2 completed. Each summary contains eight recovered jobs.
- Workload C repetition 3 stopped after the worker-kill procedure. The load generator reported `recovery run has no terminal jobs from the killed worker` and exited with code 1.
- The killed worker's measured jobs finished at 00:16:00 UTC. Resource sampling completed at 00:16:02 UTC, and the harness killed the worker at 00:16:02 UTC. The resource sample reopened the ownership race after the PostgreSQL check.
- Workload C repetition 3 preserves compressed job samples, stdout, stderr, resource samples, and the recovery event. It has no generated summary.
- This incomplete campaign is excluded from published benchmark results and medians.

The manifest and run directories remain as produced by the failed command. The harness now samples resources before it selects the current batch owner.
