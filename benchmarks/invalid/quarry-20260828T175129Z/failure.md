# Invalid campaign record

This campaign started from clean commit `e649d16dc32f302b691fda87955fbcb6ea8b861d` and stopped during Workload C repetition 2.

- All 24 Workload A and B runs completed with generated summaries.
- Workload C repetition 1 completed with a generated recovery summary for eight affected jobs.
- Workload C repetition 2 stopped after the worker-kill procedure. The load generator reported `recovery run has no terminal jobs from the killed worker` and exited with code 1.
- Workload C repetition 2 preserves compressed job samples, stdout, stderr, resource samples, and the recovery event. It has no generated summary.
- Workload C repetition 3 did not run.
- This incomplete campaign is excluded from published benchmark results and medians.

The manifest and run directories remain as produced by the failed command. Workload C campaign debugging is deferred.
