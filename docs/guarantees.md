# Quarry guarantees and limits

This document states the V1 behavior contract. These guarantees assume that PostgreSQL remains available and durable, processes use compatible configuration, and handlers follow the worker context contract.

## Execution contract

| Area | Guarantee | Boundary |
| --- | --- | --- |
| Accepted jobs | A successful submission commits the job to PostgreSQL before the API responds. Process restarts do not remove it. | A lost HTTP response is ambiguous unless the client uses an idempotency key. |
| Delivery | Quarry provides at-least-once execution through durable attempts and lease-based recovery. | A job or external side effect can execute more than once. Quarry does not provide exactly-once execution. |
| Claims | One PostgreSQL transaction claims eligible jobs and inserts their attempt rows with `FOR UPDATE SKIP LOCKED`. | Concurrent claims do not provide a global FIFO order. Locked or delayed rows can be skipped. |
| Attempt fencing | Completion changes state only for the current worker ID, attempt number, running job, and unexpired lease. | Fencing protects Quarry state. It cannot undo an external side effect from a stale worker. |
| Retries | Retry eligibility and `available_at` survive process restarts. Exponential backoff uses full jitter and a configured cap. | Retries stop at `max_attempts`. Permanent failures and exhausted jobs become `dead_lettered`. |
| Worker capacity | Each worker has a fixed executor count. It requests only free capacity and holds a slot until the report is acknowledged or rejected as stale. | Quarry does not automatically scale worker replicas from queue depth. |
| Cancellation | Queued and retry-wait jobs cancel atomically. Running workers receive cancellation through heartbeats and cancel the handler context. | Running cancellation is cooperative. A handler can finish before it observes cancellation or can ignore the context. |
| Timeouts | Every handler receives a context with the submitted execution timeout. A cooperative overrun records `timed_out` and follows retry policy. | Quarry cannot forcibly stop a handler goroutine that ignores its context. |
| Shutdown | On SIGTERM, a worker stops acquiring work, keeps active leases alive, and drains until its configured deadline. | At the deadline, the process exits and unfinished work waits for lease recovery. Duplicate execution remains possible. |
| Observability | Metrics, logs, and traces identify jobs, attempts, workers, outcomes, and recovery behavior. | Observability data is diagnostic. It is not authoritative state and does not control transitions. |

## Submission idempotency

Clients can send one `Idempotency-Key` header with `POST /v1/jobs`. The key must contain non-whitespace UTF-8 text and cannot exceed 255 bytes.

The key is scoped to the job type. Quarry hashes the canonical JSON payload, `max_attempts`, and `timeout_ms` with that type. A replay with the same normalized input returns the original job and marks the response as deduplicated. Reusing the key for different input under the same job type returns HTTP 409.

Idempotent submission does not make handler side effects idempotent. Handlers must use their own domain key or deduplication transaction when repeated execution could repeat an external change.

## Attempt outcomes and retry rules

The worker reports one of these outcomes:

- `succeeded` finishes the job and stores its JSON result.
- `retryable_failure`, `timed_out`, and `panicked` schedule another attempt when the attempt budget remains.
- `permanent_failure` moves the job directly to `dead_lettered`.
- `cancelled` moves the job to `cancelled`.

An expired lease normally records the running attempt as `abandoned` with `lease_expired`. Quarry then schedules a retry or dead-letters the job when no attempt remains. If cancellation is already pending, the expired attempt and job become `cancelled` instead.

The dispatcher accepts a repeated completion report only when it matches the stored terminal transition. It rejects a report that conflicts with stored state, targets an old attempt, comes from another worker, or arrives after lease expiry.

## Failure boundaries

Quarry handles these failures as tested system behavior:

- API, dispatcher, and worker process restarts do not erase PostgreSQL state.
- A worker crash stops heartbeats; lease expiry makes its job eligible for a replacement attempt.
- A worker that loses the success acknowledgement retries the report while its lease remains valid.
- A worker that exits after a side effect but before reporting success causes a later duplicate attempt.
- Multiple dispatchers can claim and recover work concurrently through PostgreSQL row locks.
- A stale worker cannot overwrite the current Quarry job or attempt state.

Quarry does not mask PostgreSQL failure. The API readiness endpoint and dispatcher readiness service fail when PostgreSQL cannot answer a bounded ping. Job submission and state transitions require the database.

## Non-guarantees

V1 does not provide:

- exactly-once execution or exactly-once external side effects,
- strict job ordering, fairness, priority queues, or deadlines for when work starts,
- successful completion for invalid handlers, permanent failures, or exhausted attempts,
- forced termination of a handler inside a live worker process,
- high availability for the local PostgreSQL deployment,
- multi-region coordination, distributed consensus, or leader election,
- automatic worker scaling,
- workflows or dependency graphs,
- authentication, authorization, TLS termination, tenant isolation, quotas, or untrusted-code sandboxing,
- production backup, restore, disaster recovery, or capacity guarantees.

The Compose and kind credentials are local demonstration credentials. Do not expose either deployment to an untrusted network.

The local Jaeger deployment stores traces in memory. Stopping its container removes those traces. Prometheus retains only the local demonstration window.

## Evidence map

| Behavior | Rerunnable evidence |
| --- | --- |
| Concurrent claims and durable state transitions | `pwsh ./scripts/dev.ps1 test` |
| Worker crash, lease expiry, replacement, and stale fencing | `pwsh ./scripts/dev.ps1 recovery-test` |
| Side effect followed by lost acknowledgement and duplicate execution | `pwsh ./scripts/dev.ps1 ack-loss-test` |
| Retry, timeout, cancellation, panic, and graceful shutdown semantics | `pwsh ./scripts/dev.ps1 semantics-test` |
| Full Compose recovery with Grafana and Jaeger evidence | `pwsh ./scripts/dev.ps1 compose-recovery-test` |
| Verified benchmark aggregation and preserved raw evidence | `pwsh ./scripts/dev.ps1 benchmark-verify` |
| Complete local repository validation | `pwsh ./scripts/dev.ps1 check` |

See the [architecture](architecture.md) for ownership and state flow. See the [benchmark report](benchmarks.md) for measured throughput, latency, recovery, resource use, and limits.
