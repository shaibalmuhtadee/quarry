# Quarry project plan

## 1. Project definition

### Name

Quarry

### Description

Quarry is a distributed job execution system in Go that durably queues asynchronous work in PostgreSQL, coordinates distributed workers through gRPC, and recovers from worker failures using leases, retries, and at-least-once execution.

### Goal

The project should demonstrate how backend services behave when processes:

- execute concurrently,
- communicate over a network,
- coordinate through durable state,
- crash,
- lose connectivity,
- retry work,
- recover,
- operate under load.

The primary portfolio goal is to demonstrate backend, platform, distributed-systems, and Go engineering ability.

### Resume-ready outcome

A finished V1 should support credible technical discussion about:

- Go concurrency,
- networked services,
- PostgreSQL transactions and locking,
- durable state machines,
- at-least-once execution,
- leases,
- worker crashes,
- retries,
- idempotency,
- timeouts,
- cancellation,
- graceful shutdown,
- observability,
- integration testing,
- failure injection,
- Docker,
- Kubernetes,
- load testing.

Quarry is deliberately not intended to compete with Temporal, Celery, Sidekiq, SQS, Step Functions, Kafka, or production message brokers.

---

# 2. Design principles

Use these principles throughout implementation:

1. Working systems matter more than ambitious architecture.
2. Deep implementation matters more than feature count.
3. Failure handling matters more than frontend polish.
4. Measured performance matters more than scalability claims.
5. Use the simplest architecture that teaches the required concepts.
6. Every major component must have a clear owner and reason to exist.
7. PostgreSQL should solve coordination problems that PostgreSQL already solves well.
8. Go concurrency should be idiomatic rather than performative.
9. Do not create abstractions for hypothetical future requirements.
10. Every milestone must leave a working system.
11. V1 must stop once it becomes resume ready.
12. Future workflow support should be additive rather than require rewriting the execution engine.

---

# 3. Caller-facing usage

The primary user interaction should remain simple.

A client should be able to:

1. submit a typed asynchronous job,
2. receive a stable job ID,
3. query that job,
4. inspect its attempts,
5. request cancellation.

The client should not know:

- how workers are selected,
- how leases work,
- how PostgreSQL locking works,
- which dispatcher handled the job,
- how retries are scheduled,
- whether the worker runs in Docker or Kubernetes.

Workers should be able to:

1. register,
2. advertise available execution capacity,
3. acquire jobs,
4. heartbeat active attempts,
5. execute handlers,
6. report outcomes.

Workers should not know:

- the PostgreSQL schema,
- job claim SQL,
- retry scheduling SQL,
- future workflow dependencies,
- other workers' state.

---

# 4. Fixed V1 architecture

```text
Client / Load Generator
          |
          | HTTP/JSON
          v
     API Service
          |
          | SQL
          v
     PostgreSQL
          ^
          | SQL transactions
          |
      Dispatcher
       ^   ^   ^
       |   |   |
       +---+---+
          gRPC
       Workers
```

Observability surrounds the services:

```text
API ------------+
Dispatcher -----+----> OpenTelemetry Collector ----> Jaeger
Workers --------+

Prometheus ----scrape----> API / Dispatcher / Workers
Grafana -------query-----> Prometheus
```

## Component ownership

### API service

The API owns client-facing commands.

Responsibilities:

- validate job submissions,
- persist new jobs,
- deduplicate submissions,
- return job state,
- return attempt history,
- request cancellation,
- expose health/readiness/metrics endpoints.

It does not:

- execute jobs,
- assign jobs to workers,
- maintain an in-memory queue.

### Dispatcher

The dispatcher owns job execution-state coordination.

Responsibilities:

- register workers,
- atomically claim eligible jobs,
- create attempts,
- assign leases,
- renew leases,
- receive attempt results,
- reject stale worker updates,
- schedule retries,
- recover expired leases.

The dispatcher should remain stateless with respect to authoritative queue state.

Multiple dispatcher processes should eventually be safe because PostgreSQL provides the durable synchronization.

### PostgreSQL

PostgreSQL owns all durable application state.

There is no authoritative queue in process memory.

### Worker

The worker executes registered handlers.

Responsibilities:

- register process identity,
- advertise free capacity,
- poll for work,
- run a bounded worker pool,
- execute handlers,
- propagate timeouts and cancellation,
- heartbeat active work,
- recover handler panics,
- drain on SIGTERM.

Workers do not access PostgreSQL.

### Load generator

The load generator uses the public HTTP API.

It measures end-to-end asynchronous system behavior rather than only HTTP handler speed.

### Observability

Use:

- `log/slog` for structured logs,
- Prometheus for metrics,
- OpenTelemetry for traces,
- Jaeger for trace visualization,
- Grafana for metrics visualization.

---

# 5. Major architecture alternatives rejected

## Workers directly access PostgreSQL

Rejected for V1.

Advantages:

- less code,
- fewer processes,
- no gRPC service required.

Disadvantages:

- workers become coupled to persistence,
- no clean internal service boundary,
- harder future evolution of scheduling logic,
- less useful networking experience.

Chosen alternative:

Workers use a private gRPC dispatcher.

## API also acts as dispatcher

Rejected.

Advantages:

- one fewer binary.

Disadvantages:

- mixes external request handling with worker coordination,
- weakens responsibility boundaries,
- makes independent scaling less clear.

Chosen alternative:

Separate API and dispatcher binaries.

## Server-pushed jobs

Rejected.

Would require the dispatcher to manage:

- worker connections,
- capacity,
- stream reconnects,
- backpressure,
- connection lifecycle,
- partial failures.

Chosen alternative:

Workers pull work based on available capacity.

## Kafka, NATS, or Redis queue

Rejected for V1.

PostgreSQL is already required for:

- durable job state,
- attempts,
- idempotency,
- leases,
- cancellation,
- querying,
- retry timing.

A second stateful system would increase integration and operational work without enough portfolio benefit.

---

# 6. Domain model

## Job

A Job is one logical requested unit of work.

A job survives retries.

Important conceptual fields:

- ID
- job type
- payload
- result
- status
- creation time
- earliest execution time
- attempt count
- maximum attempts
- timeout
- active worker
- lease expiration
- cancellation request
- idempotency key

## Job attempt

A JobAttempt is one execution attempt for a job.

A job can have many attempts.

Example:

```text
Job 42
├── Attempt 1: worker crashed
├── Attempt 2: temporary failure
└── Attempt 3: succeeded
```

Workflow logic, clients, and job identity should depend on the logical Job rather than individual attempts.

## Worker

A Worker represents one running worker process.

A restarted process receives a new identity.

## Job handler

A handler implements one registered job type.

Conceptually:

```go
type Handler interface {
    Execute(ctx context.Context, payload json.RawMessage) (json.RawMessage, error)
}
```

The exact interface may evolve during implementation if evidence justifies it.

Handlers receive execution context and payload.

Handlers must not contain dispatcher, persistence, or workflow coordination logic.

---

# 7. Job state machine

Use six V1 job states:

- `queued`
- `running`
- `retry_wait`
- `succeeded`
- `cancelled`
- `dead_lettered`

Do not create a separate `leased` state.

A running job carries lease metadata.

```text
                  claim
queued --------------------------> running
                                      |
                                      | success
                                      v
                                  succeeded

running
   |
   | retryable failure / expired lease
   v
retry_wait
   |
   | eligible + claim
   v
running

running
   |
   | permanent failure / attempts exhausted
   v
dead_lettered

queued ---------> cancelled
retry_wait -----> cancelled

running -- cancellation requested --> worker cancellation
                                      |
                                      v
                                  cancelled
```

## State ownership

### Submission to `queued`

Actor:

API.

Database behavior:

Insert the job in one transaction.

Crash behavior:

The insert commits or does not commit.

### `queued` or `retry_wait` to `running`

Actor:

Dispatcher.

Transaction should:

1. lock eligible job row,
2. increment the attempt number,
3. set current worker,
4. assign lease expiration,
5. transition job to `running`,
6. insert the corresponding attempt row.

All operations must commit atomically.

### `running` to `succeeded`

Actor:

Dispatcher after a valid worker report.

Transaction should:

- verify worker ID and attempt number,
- finish attempt,
- persist result,
- clear lease fields,
- set terminal job state,
- set finish timestamp.

### `running` to `retry_wait`

Causes:

- retryable handler failure,
- timeout if retryable,
- lease expiration,
- panic if configured retryable.

Transaction should:

- close current attempt,
- calculate retry eligibility,
- clear active worker and lease,
- set `available_at`,
- transition job to `retry_wait`.

### `running` to `dead_lettered`

Causes:

- permanent error,
- maximum attempts exhausted.

### Cancellation

Queued and retry-wait jobs can cancel immediately.

Running jobs use cooperative cancellation:

1. API writes cancellation request.
2. Worker receives cancellation through heartbeat response.
3. Worker cancels the handler context.
4. Worker reports cancellation.
5. Dispatcher finishes the attempt and marks the job cancelled.

If the worker disappears, lease recovery must eventually resolve the job.

---

# 8. Delivery semantics

Quarry provides at-least-once execution.

It does not guarantee exactly-once execution.

## Why duplicates are possible

Example:

1. worker executes job,
2. external side effect succeeds,
3. worker crashes before reporting success,
4. Quarry cannot know whether the side effect occurred,
5. lease expires,
6. job executes again.

No transaction spans Quarry's PostgreSQL database and every external system used by arbitrary handlers.

Therefore exactly-once external execution cannot be guaranteed.

## Queue-state fencing

Each claim creates a monotonically increasing attempt number for that job.

Worker operations identify:

- `job_id`,
- `attempt_no`,
- `worker_id`.

The dispatcher accepts a heartbeat or result only if those values match the current active attempt.

This prevents an old worker from overwriting Quarry state after a newer attempt starts.

It does not prevent an old worker from performing external side effects.

---

# 9. Dispatch and scheduling

## Eligibility

A job is eligible when:

- status is `queued` or `retry_wait`,
- `available_at <= now()`,
- it is not terminal,
- cancellation does not make it ineligible.

## Claiming

Use PostgreSQL row locking.

Conceptual query:

```sql
SELECT id
FROM jobs
WHERE status IN ('queued', 'retry_wait')
  AND available_at <= now()
ORDER BY available_at, created_at
FOR UPDATE SKIP LOCKED
LIMIT $1;
```

The actual implementation may use a CTE or another transactional form, but it must preserve equivalent locking behavior.

The claim transaction must atomically:

- select eligible rows,
- transition jobs,
- assign workers,
- establish leases,
- create attempts.

## Ordering

Use:

1. `available_at`,
2. `created_at`.

This gives approximate FIFO behavior among currently eligible jobs.

Do not claim strict global FIFO ordering.

Concurrency and `SKIP LOCKED` intentionally permit execution reordering.

## Retry scheduling

Do not use an in-memory timer heap.

Retries remain durable through:

`available_at = calculated_retry_time`

Normal acquisition queries eventually pick them up.

## Lease recovery

A small reaper loop runs in the dispatcher.

It searches for expired running jobs in bounded batches.

Multiple dispatcher reapers must be safe through database row locking.

---

# 10. Worker protocol and design

## Worker identity

Generate a UUID when each worker process starts.

Registration metadata should include:

- worker ID,
- hostname,
- binary version,
- configured concurrency,
- process start time.

Do not persist process identity across container or pod recreation.

## Pull protocol

Workers request work according to free execution slots.

Conceptual operation:

```text
AcquireJobs(worker_id, capacity)
```

If concurrency is 8 and six handlers are active, the worker should request at most two new jobs.

A worker should not lease large batches and hold them idle locally.

## Polling

Start with short polling plus local jittered idle backoff.

Do not initially implement:

- streaming job delivery,
- PostgreSQL `LISTEN/NOTIFY`,
- bidirectional gRPC streams.

These can be considered only if benchmarks reveal a specific problem.

## Worker concurrency

Use:

- one acquisition loop,
- a bounded job channel,
- `N` executor goroutines,
- one heartbeat loop,
- shutdown coordination,
- a mutex-protected active-attempt registry.

Keep the pool bounded.

## Context propagation

Handler contexts derive from:

- worker/process lifecycle,
- job timeout,
- cancellation requests.

Handlers are expected to honor context cancellation.

Go cannot forcibly terminate an arbitrary goroutine safely.

Do not claim that a timeout can forcibly stop a handler that ignores context.

## Panic handling

Recover panics at the handler execution boundary.

A handler panic should:

- not terminate the worker process,
- create structured logs including stack information,
- produce a failed attempt,
- follow normal retry rules.

---

# 11. Worker heartbeats and leases

Recommended starting values:

- heartbeat interval: 5 seconds,
- lease duration: 20 seconds.

These are configuration defaults, not hard architectural constants.

A heartbeat identifies:

- worker,
- active job IDs,
- active attempt numbers.

Dispatcher heartbeat processing should:

- update worker `last_seen_at`,
- extend matching valid leases,
- report cancellation requests,
- ignore or reject stale attempts.

If a worker stops heartbeating:

1. lease eventually expires,
2. reaper identifies abandoned work,
3. attempt records lease-expiration outcome,
4. job retries if attempts remain,
5. another worker may claim it.

---

# 12. Retry system

Default:

- maximum attempts: 3,
- base delay: 1 second,
- maximum backoff: 60 seconds.

Use exponential backoff with full jitter.

For failed attempt `n`:

```text
cap = min(60 seconds, 1 second * 2^(n - 1))
delay = random duration in [0, cap]
```

Example caps:

| Failed attempt | Cap |
| -------------: | --: |
|              1 |  1s |
|              2 |  2s |
|              3 |  4s |
|              4 |  8s |
|              5 | 16s |
|              6 | 32s |
|             7+ | 60s |

Inject or isolate time and randomness so retry behavior can be tested deterministically.

## Failure classification

Handlers must be able to distinguish retryable and permanent errors.

Retryable examples:

- temporary network failure,
- dependency timeout,
- temporary HTTP 5xx response,
- panic,
- worker lease expiration.

Permanent examples:

- invalid payload,
- unsupported input,
- immutable missing resource,
- explicitly declared permanent handler error.

When attempts are exhausted, the job becomes `dead_lettered`.

No separate message-broker dead-letter queue is needed for V1.

---

# 13. Idempotency

Two separate forms of idempotency must be understood.

## Submission idempotency

Problem:

A client sends a job submission but loses the HTTP response and retries.

Without protection, two logical jobs could be created.

Design:

- client optionally supplies `Idempotency-Key`,
- persist key,
- persist hash of normalized submission,
- enforce uniqueness on job type and idempotency key,
- same key + same request returns existing job,
- same key + different request returns conflict.

The database uniqueness constraint must resolve concurrent duplicate submissions.

## Execution idempotency

Quarry cannot make arbitrary handler side effects exactly once.

Handler authors are responsible for making external effects idempotent where required.

Safe example:

Generate a deterministic report and write to:

```text
reports/{job_id}.json
```

Repeated execution replaces the same logical output.

Unsafe example:

```text
Charge customer $100
```

Two attempts may produce two independent charges.

A payment handler should use Quarry's stable job ID or another operation ID as the downstream provider's idempotency key if that provider supports it.

---

# 14. PostgreSQL schema

The exact migrations may evolve slightly during implementation, but preserve these concepts.

## `jobs`

Important fields:

```text
id UUID PRIMARY KEY
job_type TEXT NOT NULL
payload JSONB NOT NULL
result JSONB NULL
status TEXT NOT NULL
available_at TIMESTAMPTZ NOT NULL
attempt_count INTEGER NOT NULL DEFAULT 0
max_attempts INTEGER NOT NULL
timeout_ms BIGINT NOT NULL
current_worker_id UUID NULL
lease_expires_at TIMESTAMPTZ NULL
cancel_requested_at TIMESTAMPTZ NULL
idempotency_key TEXT NULL
request_hash BYTEA NULL
traceparent TEXT NULL
created_at TIMESTAMPTZ NOT NULL
updated_at TIMESTAMPTZ NOT NULL
finished_at TIMESTAMPTZ NULL
```

Important eligible-work index:

```sql
CREATE INDEX jobs_eligible_idx
ON jobs (available_at, created_at)
WHERE status IN ('queued', 'retry_wait');
```

Important lease-recovery index:

```sql
CREATE INDEX jobs_expired_lease_idx
ON jobs (lease_expires_at)
WHERE status = 'running';
```

Submission-idempotency uniqueness:

```sql
CREATE UNIQUE INDEX jobs_idempotency_idx
ON jobs (job_type, idempotency_key)
WHERE idempotency_key IS NOT NULL;
```

Useful browsing index:

```sql
CREATE INDEX jobs_status_created_idx
ON jobs (status, created_at DESC);
```

## `job_attempts`

Important fields:

```text
job_id UUID NOT NULL REFERENCES jobs(id)
attempt_no INTEGER NOT NULL
worker_id UUID NOT NULL REFERENCES workers(id)
status TEXT NOT NULL
started_at TIMESTAMPTZ NOT NULL
finished_at TIMESTAMPTZ NULL
error_code TEXT NULL
error_message TEXT NULL
created_at TIMESTAMPTZ NOT NULL
```

Primary key:

```text
(job_id, attempt_no)
```

The attempt number is both:

- audit identity,
- fencing information.

## `workers`

Important fields:

```text
id UUID PRIMARY KEY
hostname TEXT NOT NULL
concurrency INTEGER NOT NULL
version TEXT NOT NULL
state TEXT NOT NULL
started_at TIMESTAMPTZ NOT NULL
last_seen_at TIMESTAMPTZ NOT NULL
stopped_at TIMESTAMPTZ NULL
```

Worker state may include:

- active,
- draining,
- stopped,
- lost.

---

# 15. Public HTTP API

Use standard `net/http` unless implementation shows a concrete routing limitation.

## `POST /v1/jobs`

Request concept:

```json
{
  "type": "example",
  "payload": {},
  "max_attempts": 3,
  "timeout_ms": 30000
}
```

Optional header:

```text
Idempotency-Key
```

Response concept:

```json
{
  "id": "...",
  "status": "queued",
  "deduplicated": false,
  "created_at": "..."
}
```

## `GET /v1/jobs/{id}`

Return:

- ID,
- type,
- status,
- attempt count,
- maximum attempts,
- timestamps,
- cancellation state,
- successful result,
- latest failure summary when appropriate.

## `GET /v1/jobs/{id}/attempts`

Return attempt history.

## `POST /v1/jobs/{id}/cancel`

Behavior:

- queued job: cancel immediately,
- retry-wait job: cancel immediately,
- running job: persist cancellation request and propagate cooperatively.

## Operational endpoints

Provide:

```text
/healthz
/readyz
/metrics
```

A broad admin CRUD API is not required.

---

# 16. Internal gRPC API

Use `grpc-go` and Protocol Buffers.

The exact protobuf fields may evolve during implementation while preserving the following contracts.

## `RegisterWorker`

Request:

- worker ID,
- hostname,
- version,
- configured concurrency.

## `AcquireJobs`

Request:

- worker ID,
- available capacity.

Each returned job includes:

- job ID,
- attempt number,
- job type,
- payload,
- execution timeout,
- persisted trace context when available.

## `Heartbeat`

Request:

- worker ID,
- active `(job_id, attempt_no)` leases.

Response should convey:

- which leases remain valid,
- cancellation requests.

## `ReportAttempt`

Request identifies:

- worker ID,
- job ID,
- attempt number,
- outcome.

Possible outcomes:

- succeeded,
- retryable failure,
- permanent failure,
- cancelled,
- timed out,
- panicked.

Successful outcomes may carry a result.

Failures may carry:

- error code,
- safe diagnostic message.

Use explicit gRPC deadlines.

---

# 17. Go concurrency model

Use goroutines for actual concurrent activities:

- HTTP request handling,
- gRPC request handling,
- worker executors,
- worker acquisition loop,
- worker heartbeat loop,
- dispatcher lease-reaper loop.

## Channels

Use a bounded channel for worker execution work.

This is a real producer-consumer boundary:

```text
Acquisition loop -> bounded channel -> executor goroutines
```

Do not use channels for:

- normal synchronous function calls,
- database state transitions,
- internal event buses,
- every state change,
- attempt bookkeeping when a mutex is simpler.

## Mutexes

A mutex-protected active-attempt map is acceptable because:

- executors update it,
- heartbeat logic reads it,
- ownership is local to one worker process.

## Atomics

Do not add atomics unless implementation has a specific simple atomic-state requirement.

## Bounded concurrency

Never create one unlimited goroutine per acquired job.

Worker capacity must be explicit and measurable.

---

# 18. Failure behavior

## Worker crashes before handler begins

Lease eventually expires.

Job becomes eligible for another attempt.

No committed job is lost.

## Worker crashes during handler

Lease expires.

Another worker may retry.

Duplicate external side effects are possible if the old execution partially completed.

## Worker completes handler but dies before acknowledgement

Lease expires.

Job executes again.

This is the canonical demonstration of why Quarry is at-least-once.

## Stale worker reports completion after reassignment

Dispatcher checks:

- job ID,
- attempt number,
- worker ID.

Old report is rejected.

## Dispatcher crashes

Authoritative state remains in PostgreSQL.

Workers temporarily lose coordination if no dispatcher is available.

Another dispatcher replica or restart can continue.

## API crashes

Committed jobs remain.

New submissions and queries temporarily fail.

## PostgreSQL is temporarily unavailable

No durable state changes can be trusted until connectivity returns.

Already-running handlers may continue.

Heartbeats and acknowledgements may fail.

If outage duration exceeds leases, duplicate execution may occur after recovery.

## Network partition

Worker may continue execution while heartbeats fail.

Its lease can expire and another worker may execute the same logical job.

## Handler timeout

Cancel handler context.

Handler must honor context cancellation.

Retry/dead-letter behavior follows failure policy.

## Handler panic

Recover at handler boundary.

Record/log failure.

Apply retry policy.

## Duplicate submission

Database uniqueness constraint resolves concurrent duplicates.

## Graceful shutdown

Stop acquisition first.

Continue heartbeats while draining.

Wait for active handlers up to configured shutdown deadline.

Cancel remaining contexts.

Exit.

Any unfinished jobs eventually recover through lease expiration.

---

# 19. Observability

Build observability as part of the system rather than as cosmetic final work.

## Structured logs

Use `log/slog`.

Useful structured fields:

```text
job_id
job_type
attempt_no
worker_id
trace_id
error_code
```

Job IDs belong in logs and traces, not Prometheus labels.

## Prometheus metrics

Recommended metrics:

```text
quarry_jobs_submitted_total
quarry_job_attempts_total
quarry_job_execution_duration_seconds
quarry_job_scheduling_delay_seconds
quarry_queue_depth
quarry_oldest_queued_job_age_seconds
quarry_active_jobs
quarry_active_workers
quarry_lease_expirations_total
quarry_retries_scheduled_total
quarry_stale_reports_total
quarry_dispatch_claim_size
quarry_worker_poll_errors_total
```

Useful bounded labels include:

```text
job_type
outcome
error_code
```

Do not label metrics with:

```text
job_id
worker_id
idempotency_key
error_message
user-controlled URL
arbitrary user input
```

## Distributed tracing

Persist incoming trace context with the asynchronous job.

A representative trace should connect:

```text
HTTP POST /v1/jobs
└── db.insert_job
    ... asynchronous boundary ...
    └── dispatcher.acquire
        ├── db.claim_job
        └── worker.execute
            ├── handler
            └── dispatcher.report_attempt
                └── db.complete_attempt
```

Useful trace attributes:

```text
job.id
job.type
job.attempt
worker.id
job.outcome
```

---

# 20. Testing strategy

## Unit tests

Prioritize:

- state transition rules,
- retry calculations,
- jitter bounds,
- error classification,
- cancellation decisions,
- request hashing,
- handler validation.

Time-sensitive code should allow deterministic testing.

## PostgreSQL integration tests

Use real PostgreSQL through Testcontainers.

Highest-value cases:

1. many concurrent claimers compete for many jobs,
2. no attempt is claimed twice,
3. locked rows do not block unrelated available jobs,
4. concurrent identical idempotency requests create one job,
5. stale completion is rejected,
6. expired lease becomes retryable,
7. retry timing respects `available_at`.

Do not mock the database behaviors that the architecture depends on.

## Multi-process tests

Exercise real binaries or processes for:

- API,
- dispatcher,
- workers,
- PostgreSQL.

## Required failure tests

### Worker death during execution

1. submit long-running job,
2. verify attempt 1 starts,
3. terminate worker,
4. wait for lease expiration,
5. verify another worker runs attempt 2,
6. verify attempt 1 records lease-expired outcome,
7. verify job eventually completes.

### Acknowledgement-loss test

Add test-only fault injection capable of terminating a worker after handler success but before reporting completion.

Verify:

1. side effect occurs,
2. acknowledgement never commits,
3. lease expires,
4. another attempt executes,
5. attempt history exposes both executions.

### Stale completion test

1. hold attempt 1's completion,
2. expire lease,
3. start attempt 2,
4. send attempt 1 completion,
5. verify it cannot overwrite current job state.

This is a critical correctness test.

## Go race detector

Use:

```text
go test -race ./...
```

for relevant concurrency code and as part of CI once feasible.

---

# 21. Load testing

Create a Go program:

```text
cmd/loadgen
```

It should use the public HTTP API and observe asynchronous completion.

Do not benchmark only request acceptance.

## Workload A: queue overhead

Handler performs almost no work.

Purpose:

Measure queue, database, scheduling, RPC, and state-transition overhead.

Do not present this workload as normal application throughput.

## Workload B: simulated I/O

Handler waits roughly 25 ms while honoring context.

Purpose:

Observe worker-concurrency scaling.

## Workload C: recovery

Longer jobs.

Kill worker during measured execution.

Purpose:

Measure recovery behavior.

## Scaling matrix

Start with:

| Worker processes | Concurrency per worker |
| ---------------: | ---------------------: |
|                1 |                      8 |
|                2 |                      8 |
|                4 |                      8 |
|                8 |                      8 |

## Benchmark procedure

For each configuration:

- record machine specification,
- use 30-second warmup,
- use 120-second measured run,
- run three times,
- preserve raw results,
- report median measurements.

Record:

- submitted jobs/s,
- completed jobs/s,
- end-to-end p50,
- end-to-end p95,
- end-to-end p99,
- scheduling p50/p95/p99,
- execution duration,
- CPU,
- memory,
- database connections,
- recovery duration after worker death.

Do not invent benchmark numbers.

Do not claim that request throughput equals job throughput.

---

# 22. Local development

## Normal development

Run infrastructure in Docker:

- PostgreSQL,
- Prometheus,
- Grafana,
- OpenTelemetry Collector,
- Jaeger.

Run Go services directly when convenient:

```text
go run ./cmd/api
go run ./cmd/dispatcher
go run ./cmd/worker
```

This keeps the normal development loop fast.

## Full demonstration

Docker Compose should eventually start:

- PostgreSQL,
- migrations,
- API,
- dispatcher,
- worker,
- Prometheus,
- Grafana,
- OTel Collector,
- Jaeger.

Desired experience:

```text
docker compose up --build
```

with worker scaling available through Compose.

Use one explicit migration process rather than every service racing to migrate the schema.

---

# 23. Kubernetes

Kubernetes work exists for platform-engineering signal, not to turn Quarry into an infrastructure project.

Use:

- kind,
- Kustomize,
- plain Kubernetes resources.

Do not use Helm for V1.

## API

Provide:

- Deployment,
- Service,
- ConfigMap,
- Secret where appropriate,
- readiness probe,
- liveness probe,
- resource requests,
- resource limits.

## Dispatcher

Provide:

- Deployment,
- Service,
- multiple replicas,
- gRPC health endpoint,
- readiness/liveness probes.

## Workers

Provide:

- Deployment,
- several replicas,
- resource requests/limits,
- graceful SIGTERM handling,
- appropriate termination grace period.

Workers do not require a Service.

## PostgreSQL

For the local Kubernetes demonstration only:

- single StatefulSet,
- Service,
- persistent volume.

Do not claim that this setup represents production high availability.

## Scaling

Manual worker scaling is sufficient for V1.

Example conceptual demonstration:

```text
1 worker -> 4 workers -> 8 workers
```

Measure the result.

Queue-depth-based autoscaling is stretch work.

---

# 24. CI/CD

Use GitHub Actions.

## Pull request checks

Target:

- formatting verification,
- `go vet`,
- linting,
- unit tests,
- PostgreSQL integration tests,
- race detector,
- protobuf generation consistency,
- sqlc generation consistency,
- binary builds,
- Docker image build,
- Kustomize rendering validation.

Introduce checks as their corresponding project capabilities appear.

Do not create CI for components that do not exist yet.

## Manual or scheduled checks

Eventually consider:

- `govulncheck`,
- complete kind smoke test,
- longer failure/recovery suite,
- benchmark execution.

Benchmarks should generally not gate every pull request.

---

# 25. Repository structure

Target structure:

```text
quarry/
├── AGENTS.md
├── README.md
├── cmd/
│   ├── api/
│   │   └── main.go
│   ├── dispatcher/
│   │   └── main.go
│   ├── worker/
│   │   └── main.go
│   └── loadgen/
│       └── main.go
│
├── internal/
│   ├── domain/
│   │   ├── job.go
│   │   ├── attempt.go
│   │   └── errors.go
│   │
│   ├── api/
│   ├── dispatcher/
│   ├── worker/
│   │   └── handlers/
│   ├── store/
│   │   └── postgres/
│   │       ├── migrations/
│   │       ├── queries/
│   │       └── generated/
│   ├── rpc/
│   │   └── generated/
│   ├── telemetry/
│   └── config/
│
├── proto/
│   └── quarry/
│       └── dispatcher/
│           └── v1/
│
├── deploy/
│   ├── k8s/
│   │   ├── base/
│   │   └── overlays/
│   │       └── kind/
│   └── observability/
│
├── docs/
│   ├── project-plan.md
│   ├── current-status.md
│   ├── architecture.md
│   ├── guarantees.md
│   ├── benchmarks.md
│   └── decisions/
│
├── scripts/
├── compose.yaml
├── Dockerfile
├── Makefile
├── sqlc.yaml
├── go.mod
└── README.md
```

Do not create directories before they are useful merely to match this tree.

Do not create a public `pkg/` tree unless a genuine reusable Go library emerges.

---

# 26. Technology choices

## Go

Primary implementation language.

Use the current stable Go release selected when the repository is initialized.

Pin and document the version.

## HTTP

Use:

```text
net/http
```

unless concrete routing requirements justify another dependency.

## gRPC

Use:

```text
grpc-go
Protocol Buffers
```

for worker/dispatcher communication.

## PostgreSQL

Use:

```text
pgx/v5
pgxpool
```

Use PostgreSQL-specific behavior directly rather than hiding it behind a database-agnostic abstraction.

## SQL

Use:

```text
sqlc
```

for typed query generation while keeping SQL visible.

## Migrations

Use:

```text
goose
```

with SQL migrations.

## Logging

Use:

```text
log/slog
```

## Metrics

Use the Prometheus Go client.

## Tracing

Use OpenTelemetry Go.

## Integration tests

Use Testcontainers where it materially simplifies reliable PostgreSQL tests.

## CLI parsing

Prefer standard `flag`.

Do not add Cobra merely for keyword coverage.

---

# 27. Development milestones

Each milestone must leave a working system.

Do not implement later milestones early unless a small prerequisite is unavoidable.

## Milestone 0: repository skeleton and persistence

Estimated focused effort:

4 to 6 hours.

### Build

- initialize Go module,
- pin Go version,
- create minimal command structure,
- create PostgreSQL Compose service,
- configure migration tooling,
- create initial job/attempt schema sufficient for the milestone,
- configure pgx/sqlc,
- establish Makefile or equivalent common commands,
- add basic CI,
- add `docs/current-status.md`.

Do not build the worker or dispatcher.

### Concepts

- Go project structure,
- dependency management,
- migrations,
- PostgreSQL connectivity,
- generated database code,
- reproducible development environment.

### Tests

- migrations apply from empty database,
- migrations can be reproduced on fresh PostgreSQL,
- application can connect,
- generated code is up to date.

### Definition of done

A fresh clone can start PostgreSQL and run the repository's basic validation successfully.

---

## Milestone 1: durable HTTP job API

Estimated focused effort:

8 to 10 hours.

### Build

- core Job domain model,
- job-type validation mechanism,
- job submission,
- persistence,
- job lookup,
- initial structured logs,
- health/readiness endpoints as appropriate.

Jobs remain queued forever because no executor exists yet.

Do not implement retries, leases, or workers.

### Required behavior

`POST /v1/jobs`

creates a durable queued job.

`GET /v1/jobs/{id}`

returns it after service restart.

### Tests

- valid submission,
- invalid type,
- malformed payload,
- missing job,
- persisted job survives API restart,
- database integration coverage for submission/read behavior.

### Definition of done

The project is a durable queue with a usable HTTP control plane but no execution capability.

---

## Milestone 2: dispatcher and distributed workers

Estimated focused effort:

12 to 15 hours.

### Build

- protobuf definitions,
- dispatcher gRPC server,
- basic worker registration if required for this milestone,
- `AcquireJobs`,
- atomic PostgreSQL claim,
- attempt creation,
- worker process,
- bounded execution pool,
- at least two deterministic demonstration handlers,
- success reporting,
- attempt history API.

Do not yet implement leases or crash recovery beyond what is required to keep behavior internally consistent.

### Required concurrency behavior

Run:

- multiple worker processes,
- multiple worker goroutines,
- concurrent dispatcher calls.

PostgreSQL prevents duplicate claims for the same attempt.

### Tests

Highest priority:

Run many concurrent claimers against many queued jobs.

Verify every logical claim creates one unique attempt.

### Definition of done

Several worker processes can process a batch of queued jobs concurrently through the dispatcher.

This is the MVP boundary.

Expected cumulative effort:

24 to 31 focused hours.

---

## Milestone 3: leases and crash recovery

Estimated focused effort:

10 to 13 hours.

### Build

- complete worker registration,
- worker heartbeat,
- leases,
- lease renewal,
- lease reaper,
- expired-attempt handling,
- attempt-number fencing,
- stale completion rejection.

### Required test

Kill a worker during a long-running job.

Verify:

- attempt 1 stops heartbeating,
- its lease expires,
- attempt 1 records abandonment,
- another worker receives attempt 2,
- stale reports from attempt 1 cannot modify current state.

### Definition of done

Worker crashes are handled as a tested normal system condition rather than permanent job loss.

---

## Milestone 4: retries and execution controls

Estimated focused effort:

12 to 15 hours.

### Build

- retryable versus permanent failure model,
- maximum attempts,
- exponential backoff,
- full jitter,
- `retry_wait`,
- `dead_lettered`,
- submission idempotency,
- execution timeouts,
- cooperative cancellation,
- SIGTERM graceful worker drain.

### Tests

- retry timing,
- attempts stop at configured maximum,
- permanent errors do not retry,
- concurrent idempotent submissions create one logical job,
- same idempotency key with changed request conflicts,
- timeout propagates cancellation,
- queued cancellation,
- running cancellation,
- graceful shutdown,
- forced shutdown recovery.

### Definition of done

The main execution semantics are complete and defensible.

---

## Milestone 5: observability

Estimated focused effort:

7 to 9 hours.

### Build

- Prometheus metrics,
- Grafana dashboard,
- OpenTelemetry tracing,
- trace-context persistence across asynchronous job execution,
- Jaeger,
- structured identifiers in logs,
- telemetry configuration.

### Required demonstration

One submitted job should be traceable from:

```text
HTTP request
-> database persistence
-> dispatcher claim
-> worker execution
-> completion
```

### Definition of done

A developer can diagnose queue health and inspect one job without reading database rows manually.

---

## Milestone 6: failure suite and benchmarking

Estimated focused effort:

9 to 12 hours.

### Build

- Go load generator,
- reproducible workloads,
- benchmark output,
- fault injection,
- acknowledgement-loss failure mode,
- worker-kill recovery benchmark,
- benchmark documentation.

### Required evidence

Produce real measurements for:

- completed jobs/s,
- p50/p95/p99,
- scheduling latency,
- worker scaling,
- recovery time.

Preserve raw data.

### Definition of done

Resume performance and recovery claims can be supported by repeatable evidence.

---

## Milestone 7: packaging and portfolio polish

Estimated focused effort:

10 to 13 hours.

### Build

- complete Docker images,
- full Docker Compose environment,
- kind deployment,
- Kustomize,
- Kubernetes probes,
- resource requests/limits,
- worker scaling demonstration,
- final CI,
- architecture documentation,
- guarantees documentation,
- README,
- demonstration scripts.

### Definition of done

A technically competent stranger can:

1. understand Quarry,
2. start Quarry,
3. submit jobs,
4. kill workers,
5. observe recovery,
6. inspect metrics/traces,
7. reproduce benchmarks.

This is the resume-ready V1 boundary.

---

# 28. Estimated effort

Expected total:

72 to 93 focused development hours.

Treat roughly 95 focused hours as a scope warning.

If V1 approaches that threshold, cut optional polish before adding architecture.

---

# 29. V1 non-goals

Do not build before resume-ready V1:

- workflow DAG execution,
- workflow versioning,
- Kafka,
- NATS,
- Redis queueing,
- distributed consensus,
- leader election,
- multi-region operation,
- high-availability PostgreSQL,
- cron scheduling,
- worker sandboxing,
- arbitrary user code,
- plugin frameworks,
- React dashboard,
- cloud infrastructure,
- Terraform,
- Helm,
- Kubernetes operator,
- automatic queue-depth scaling,
- elaborate authentication or RBAC.

---

# 30. Future workflow-engine compatibility

Workflow functionality is deliberately outside V1.

However, the V1 architecture should permit a future workflow coordinator to sit above the existing job execution engine.

Future conceptual architecture:

```text
Workflow API
     |
     v
Workflow Coordinator
     |
     | makes ready steps eligible
     v
Existing Quarry Job Execution System
     |
     +--> Dispatcher
     +--> Workers
```

The future workflow layer may eventually own concepts such as:

- WorkflowDefinition,
- WorkflowRun,
- WorkflowStep,
- Dependency,
- step readiness,
- DAG validation,
- fan-out/fan-in,
- workflow-level cancellation,
- workflow-level failure propagation.

The current execution engine should continue to own:

- job attempts,
- leases,
- worker assignment,
- retries,
- crash recovery,
- execution timeout,
- handler invocation.

## Compatibility rules

V1 should preserve:

1. Workers execute jobs without knowing why they became eligible.
2. A Job remains independent from an Attempt.
3. Handlers contain no workflow dependency logic.
4. PostgreSQL owns durable coordination state.
5. The worker RPC carries execution information rather than dependency graphs.
6. Nothing assumes all jobs originate directly from the public job-submission API.
7. A future coordinator should be able to make an ordinary job eligible without modifying worker execution.

Do not create workflow code now.

Do not generalize the V1 domain into speculative workflow abstractions.

Clean ownership is the extension mechanism.

---

# 31. Resume-ready stop condition

V1 is finished when all of the following are true:

- durable job submission works,
- distributed workers execute jobs,
- PostgreSQL safely coordinates concurrent claims,
- attempts are durable,
- leases recover crashed-worker work,
- stale workers are fenced,
- retries and backoff work,
- dead-letter behavior works,
- submission idempotency works,
- timeouts work,
- cancellation works,
- graceful shutdown works,
- Prometheus metrics exist,
- OpenTelemetry traces exist,
- Docker Compose runs the complete system,
- a local Kubernetes deployment works,
- forced worker crash recovery is tested,
- acknowledgement-loss duplicate execution is demonstrated,
- load tests produce reproducible measurements,
- CI passes,
- architecture and guarantees are documented,
- the repository README provides a runnable demonstration.

At that point, stop adding V1 features.

Put Quarry on the resume.

Workflow support becomes a separate future phase rather than a condition for V1 completion.
