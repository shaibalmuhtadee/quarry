# Benchmarks

Quarry's publishable local benchmark campaign is `quarry-20260829T002429Z`. The campaign contains 27 valid runs from clean commit `9067502e6445cf191ea316b1790c4d323fb8ab42`. Each configuration ran three times with a 30-second warmup and a 120-second measurement window. The tables report the median run for each metric.

These results describe one machine and one bounded workload. They are not production capacity claims. The load generator held `max_outstanding` at 8 for every configuration, so the scaling results show how quickly Quarry processed that fixed amount of concurrent work.

## Test environment

- Windows 10.0.26200, x64
- AMD Ryzen 7 5700X, 8 cores and 16 logical CPUs
- 32 GiB system memory
- Go 1.27.0
- Docker Engine and client 28.3.2
- PostgreSQL 18.6
- Worker concurrency 8 per worker process
- Maximum outstanding jobs 8

## Throughput and latency

Workload A runs `demo.echo` with deterministic small payloads. Workload B runs `demo.sleep` with a 25 ms handler delay. Latencies use durable server timestamps. The percentile calculation uses nearest rank.

| Workload | Workers | Completed jobs/s | End-to-end p50 | End-to-end p95 | End-to-end p99 | Scheduling p95 | Attempt p95 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| A | 1 | 13.94 | 589.66 ms | 967.88 ms | 996.17 ms | 960.87 ms | 10.95 ms |
| A | 2 | 27.83 | 223.04 ms | 677.05 ms | 801.35 ms | 671.87 ms | 7.02 ms |
| A | 4 | 54.29 | 108.42 ms | 359.38 ms | 493.76 ms | 354.68 ms | 6.72 ms |
| A | 8 | 101.21 | 52.19 ms | 184.39 ms | 266.77 ms | 179.40 ms | 6.31 ms |
| B | 1 | 14.40 | 552.91 ms | 980.63 ms | 1,016.52 ms | 949.74 ms | 37.15 ms |
| B | 2 | 25.47 | 254.72 ms | 699.09 ms | 834.82 ms | 667.59 ms | 33.29 ms |
| B | 4 | 45.55 | 139.74 ms | 386.32 ms | 498.32 ms | 354.79 ms | 32.30 ms |
| B | 8 | 72.72 | 80.71 ms | 221.42 ms | 305.83 ms | 190.88 ms | 31.94 ms |

Under this fixed in-flight limit, Workload A completed 7.26 times as many jobs per second with eight workers as with one. Workload B completed 5.05 times as many. These ratios do not establish scaling beyond eight workers or under a larger queue.

## Recovery

Workload C ran with two workers. The command waited until one target worker owned the measured jobs, started and verified a replacement worker, then force-killed the target. Every measured job's first attempt became `abandoned` with `lease_expired`. A distinct worker completed attempt 2.

| Measurement | p50 | p95 | p99 |
| --- | ---: | ---: | ---: |
| Kill to replacement-attempt start | 20.53 s | 20.83 s | 20.83 s |
| Kill to final success | 26.54 s | 26.83 s | 26.83 s |

The campaign used a 20-second lease, a 5-second heartbeat interval, and a 1-second reaper interval. The lease duration therefore dominates the recovery measurement.

## Resource observations

Across the throughput medians, Quarry used 0.35 to 0.60 average CPU cores for Workload A and 0.37 to 0.53 for Workload B. Peak summed Quarry resident memory rose from about 213 MiB with one worker to about 433 MiB with eight workers for Workload A, and from about 216 MiB to about 436 MiB for Workload B. Peak PostgreSQL connections were between 18 and 21.

The raw resource samples also contain PostgreSQL CPU and memory. Host and container measurements are useful for comparing these runs on this machine, but they do not predict another deployment's resource use.

## Reproduce and verify

Run a new full campaign only from a clean worktree:

```powershell
pwsh ./scripts/dev.ps1 benchmark
```

Verify all committed results and regenerate summaries from the preserved compressed job samples and resource JSON Lines:

```powershell
pwsh ./scripts/dev.ps1 benchmark-verify
```

The campaign manifest records the Git commit, machine, software, durations, workload configuration, and each run directory. Three earlier incomplete campaigns remain under `benchmarks/invalid/` with failure records. They do not contribute to the published medians.
