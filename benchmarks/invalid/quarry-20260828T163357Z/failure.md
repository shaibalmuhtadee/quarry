# Invalid campaign record

This campaign started from clean commit `57bfff19343a03c795216f75c82394aae4ed80fb` and stopped during Workload C repetition 1.

- All 24 Workload A and B runs completed and regenerated successfully.
- Workload C repetition 1 stopped after the worker-kill procedure because the load generator exited with code 1.
- The original harness did not capture the load generator's stderr and wrote no job samples before recovery filtering, so the preserved evidence cannot establish the deeper cause.
- Workload C repetitions 2 and 3 did not run.
- This incomplete campaign is excluded from published benchmark results and medians.

The manifest, 24 valid run directories, incomplete recovery directory, resource samples, and recovery event remain as produced by the failed command.
