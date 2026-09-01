# Milestone 7 execution plan

## Milestone goal

Package Quarry as a resume-ready V1 that a technically competent stranger can understand, start, exercise, disrupt, observe, and benchmark.

Milestone 7 packages and documents the existing execution system. It does not add workflow behavior, another queue, cloud infrastructure, Helm, Terraform, autoscaling, authentication, a frontend, or image publishing.

## Existing foundation

Milestones 0 through 6 already provide:

- durable jobs and attempts in PostgreSQL,
- the public HTTP API for submission, job lookup, attempt history, and cancellation,
- gRPC worker registration, acquisition, heartbeats, and outcome reporting,
- bounded worker concurrency,
- leases, lease recovery, and stale-attempt fencing,
- retries, timeouts, cancellation, and graceful worker shutdown,
- deterministic demonstration handlers,
- Prometheus metrics, a Grafana dashboard, OpenTelemetry traces, and Jaeger,
- worker-death, acknowledgement-loss, stale-completion, and shutdown process proofs,
- a bounded Go load generator,
- a verified 27-run benchmark campaign with preserved raw data,
- a PowerShell command interface through `scripts/dev.ps1`.

The repository has no application `Dockerfile`, full-system Compose environment, Kubernetes resources, kind proof, architecture document, or guarantees document. The existing GitHub Actions workflow does not build images, validate Kustomize output, run a linter, or run the race detector. GitHub-hosted CI has not run for the current repository state.

## Milestone requirements

The slices use these requirement identifiers:

- M7-R1: complete Docker images
- M7-R2: full Docker Compose environment
- M7-R3: kind deployment and Kustomize
- M7-R4: Kubernetes probes and resource requests and limits
- M7-R5: measured worker scaling demonstration
- M7-R6: final CI
- M7-R7: architecture documentation
- M7-R8: guarantees documentation
- M7-R9: final README
- M7-R10: demonstration scripts
- M7-D1: a stranger can understand Quarry
- M7-D2: a stranger can start Quarry
- M7-D3: a stranger can submit jobs
- M7-D4: a stranger can kill workers
- M7-D5: a stranger can observe recovery
- M7-D6: a stranger can inspect metrics and traces
- M7-D7: a stranger can reproduce benchmarks

## Approved decisions

### Container images

- Use one multi-stage `Dockerfile` with separate named runtime targets for the API, dispatcher, worker, and migration process.
- Build separate service images from the named targets rather than use one generic image with command overrides.
- Run application images as a non-root user.
- Build the pinned Goose command and copy the SQL migrations into the migration image.
- Keep load generation and benchmark verification as development commands. Do not add them to the runtime images.

### Dispatcher health

- Implement the standard gRPC health protocol on the dispatcher port.
- Use distinct liveness and readiness service names.
- Make liveness report whether the gRPC process can serve the health request.
- Make readiness perform a bounded PostgreSQL ping.
- Keep health logic in the dispatcher process boundary. Do not move operational state into the dispatcher domain service.
- Use native Kubernetes gRPC probes. Do not add a separate probe executable.

### Docker Compose

- Make `docker compose up --build` start PostgreSQL, one migration process, API, dispatcher, worker, Prometheus, Grafana, the OpenTelemetry Collector, and Jaeger.
- Use one explicit migration service. API and dispatcher replicas do not run migrations.
- Do not set a fixed container name for workers. Compose must support worker replicas.
- Use service DNS for the full Compose Prometheus configuration.
- Preserve the existing host-run development path with a separate Prometheus scrape configuration selected by `scripts/dev.ps1`.
- Keep telemetry failure non-authoritative. Application startup and job execution do not depend on the Collector, Prometheus, Grafana, or Jaeger.

### Recovery demonstration

- Add one user-facing Compose demonstration that starts the stack, submits work, force-kills one validated worker container, observes lease recovery, and leaves the stack running for inspection.
- Use an explicit short lease and heartbeat configuration for the demonstration so recovery finishes promptly.
- Validate the target container through its exact Compose project and service labels before killing it.
- Prove recovery through the public HTTP job and attempt APIs.
- Require attempt 1 to be `abandoned` with `lease_expired` and attempt 2 to succeed under a new worker identity.
- Keep an automated recovery test separate from the user-facing command so test cleanup remains deterministic.

### Kubernetes shape

- Use kind, Kustomize, and plain Kubernetes resources.
- Do not add Helm, an operator, cloud infrastructure, or automatic scaling.
- Keep the kind deployment limited to PostgreSQL and Quarry services. The full metrics and traces demonstration remains in Compose.
- Split deployment into ordered PostgreSQL, migration, and application stages.
- Use one Kubernetes Job for migrations.
- Deploy the API behind a Service with HTTP liveness and readiness probes.
- Deploy two dispatcher replicas behind a Service with native gRPC liveness and readiness probes.
- Deploy several worker replicas without a Service.
- Set worker `terminationGracePeriodSeconds` longer than `QUARRY_WORKER_SHUTDOWN_TIMEOUT`.
- Give API, dispatcher, worker, migration, and PostgreSQL containers explicit resource requests and limits.
- Use a single PostgreSQL StatefulSet, Service, and persistent volume for the local demonstration.
- Generate local-only credentials through the kind overlay. State plainly that this PostgreSQL deployment is not highly available or production-ready.

### kind orchestration

- Build and load local Quarry images into kind.
- Apply PostgreSQL, wait for readiness, run the migration Job, then apply the applications.
- Use uniquely named clusters for automated tests and always remove them.
- Use a managed `kubectl port-forward` process for the HTTP proof.
- Keep the user-facing kind cluster available until an explicit down command removes it.

### Worker scaling

- Demonstrate manual scaling at 1, 4, and 8 worker replicas.
- Use Workload B with worker concurrency 1 and a fixed maximum of eight outstanding jobs.
- Require the requested worker count to be Ready before each measurement.
- Use short warmup and measurement windows.
- Label the results as a deployment demonstration. Do not publish or commit them as benchmark evidence.
- Keep the Milestone 6 campaign and `docs/benchmarks.md` claims unchanged unless direct verification finds an error.

### CI

- Keep GitHub Actions as the CI system.
- Pin Staticcheck as the repository linter. Do not add `golangci-lint`.
- Split Go validation, race tests, and packaging validation into clear jobs.
- Run formatting, dependency consistency, Staticcheck, `go vet`, unit tests, PostgreSQL integration tests, generated-code consistency, and binary builds on pull requests.
- Build every Docker image target and validate the Compose configuration on pull requests.
- Render and validate the kind Kustomize configuration on pull requests.
- Run the Linux race detector on pull requests.
- Keep complete kind tests, long failure suites, and benchmarks outside mandatory pull-request checks.
- Add a manually triggered extended workflow only if it gives useful evidence without duplicating local commands.
- Require an observed green GitHub-hosted workflow before the milestone audit can pass.

### Documentation ownership

- Make `README.md` the short portfolio entry point and runnable demonstration guide.
- Make `docs/architecture.md` an explanation of components, ownership, state transitions, failure paths, and deployment topology.
- Make `docs/guarantees.md` the reference for guarantees, boundaries, and explicit non-guarantees.
- Keep measurements, methodology, and benchmark limits in `docs/benchmarks.md`.
- Link between documents instead of copying the same material.
- Make only claims supported by the implementation and recorded evidence.

## Slice 1: runtime health and container images

Status: complete

### Goal

Produce reproducible non-root images for API, dispatcher, worker, and migrations. Add the dispatcher health contract required by Kubernetes.

### Expected files and areas

- `Dockerfile`
- `.dockerignore`
- `cmd/dispatcher/main.go`
- focused dispatcher health code and tests
- `scripts/dev.ps1`
- this execution plan
- `docs/current-status.md`

### Dependencies

- Go 1.27
- the existing Goose tool dependency and SQL migrations
- Docker BuildKit
- the existing API, dispatcher, and worker commands

### Important decisions

- Use the approved named Docker targets and one shared build stage.
- Copy only the required binary and runtime files into each final image.
- Run application images as a non-root user.
- Use Goose environment variables in the migration image instead of shell interpolation.
- Implement distinct standard gRPC liveness and readiness service names.
- Bound the readiness PostgreSQL ping by the health-request context.
- Do not change the worker-dispatcher application protocol.

### Validation required

- focused dispatcher health tests cover live, ready, and PostgreSQL-unavailable behavior
- `go test -count=1 ./cmd/dispatcher`
- `go vet ./cmd/dispatcher`
- every Docker target builds successfully
- image inspection confirms the expected entry point and non-root user
- the migration image applies all migrations to fresh PostgreSQL
- application images start their intended binary
- `pwsh ./scripts/dev.ps1 build`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- M7-R1
- dispatcher-health foundation for M7-R4

### Decisions and deviations discovered during implementation

- The Dockerfile pins the Dockerfile frontend, Go builder, and distroless runtime base by digest while retaining readable version tags.
- The dispatcher exposes `quarry.dispatcher.liveness` and `quarry.dispatcher.readiness` through the standard gRPC health service. Readiness passes the health request context directly to PostgreSQL `Ping`.
- `pwsh ./scripts/dev.ps1 image-test` is the rerunnable image proof. It owns unique containers and a Docker network, and it verifies their removal after each run.
- No architecture or project-plan deviations were required.

### Validation evidence

- 2026-08-29: `go test -count=1 ./cmd/dispatcher` passed, including direct standard gRPC health calls and live, ready, PostgreSQL-unavailable, request-context, and unknown-service cases.
- 2026-08-29: `go vet ./cmd/dispatcher` passed.
- 2026-08-29: `pwsh ./scripts/dev.ps1 image-test` passed twice. All four named targets built; image inspection found the expected entrypoint and a non-root user; the migration image applied migrations 1 through 8 to fresh PostgreSQL; API, dispatcher, and worker startup logs came from their intended binaries; cleanup removed every owned container and network.
- 2026-08-29: `pwsh ./scripts/dev.ps1 build` passed.
- 2026-08-29: `pwsh ./scripts/dev.ps1 check` passed. The canonical run included dependency, formatting, pinned-tool, generated-code, vet, unit, PostgreSQL integration, build, image, observability, distributed-process, recovery, acknowledgement-loss, and shutdown-semantics validation with cleanup.
- 2026-08-29: `git diff --check` passed.
- GitHub-hosted CI remains unverified and is deferred to Slice 7.

## Slice 2: full Docker Compose environment

Status: complete

### Goal

Make `docker compose up --build` start the complete local system with one explicit migration process and scalable workers.

### Expected files and areas

- `compose.yaml`
- Compose-specific configuration under `deploy/observability/`
- `scripts/dev.ps1`
- focused Compose configuration tests
- this execution plan
- `docs/current-status.md`

### Dependencies

- Slice 1 images
- existing PostgreSQL and observability services
- existing API readiness endpoint and dispatcher gRPC health endpoint
- Docker Compose dependency conditions

### Important decisions

- Keep PostgreSQL data in the existing named volume.
- Run one migration service after PostgreSQL becomes healthy.
- Start API, dispatcher, and worker only after migrations succeed.
- Preserve direct host-run development and observability tests.
- Use DNS discovery for scaled worker metrics inside Compose.
- Do not publish worker ports to the host.
- Do not make job execution depend on telemetry services.

### Validation required

- `docker compose config`
- an isolated `compose-test` starts from a fresh volume
- the migration service exits successfully exactly once
- API readiness succeeds
- a submitted job completes and exposes one successful attempt
- Prometheus reports the expected Quarry targets
- Jaeger receives a job trace
- the worker service scales without naming or port conflicts
- existing `pwsh ./scripts/dev.ps1 observability-test` passes
- cleanup removes test processes, containers, networks, and volumes
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- M7-R2
- Compose scaling portion of M7-R5
- foundation for M7-R10
- M7-D2
- M7-D3
- M7-D6

### Decisions and deviations discovered during implementation

- The default Compose stack uses `deploy/observability/prometheus-compose.yml`, which scrapes the API and dispatcher through service DNS and discovers every worker replica through DNS service discovery. The existing host-run observability commands select `compose.host-observability.yaml` so they continue to use `host.docker.internal` targets.
- Worker-only scale operations use `docker compose up --no-deps --scale` so Compose does not restart the completed one-shot migration container.
- The Compose test allocates unique host ports and a unique project name, then verifies cleanup through Docker labels. No architecture or project-plan deviations were required.

### Validation evidence

- 2026-08-29: `docker compose config --quiet` passed for the complete stack. Promtool accepted both the Compose and host-run Prometheus configurations, the Collector accepted its configuration, and `go test -count=1 ./deploy/observability` passed.
- 2026-08-29: `pwsh ./scripts/dev.ps1 compose-test` passed twice, once directly and once inside the canonical check. Each fresh-volume run built the four Quarry images, ran one migration container successfully without restarting it during scaling, reached API readiness, completed a `demo.echo` job with one successful attempt, found healthy API, dispatcher, and worker Prometheus targets, retrieved the provisioned Grafana dashboard, found the complete job trace in Jaeger, scaled the worker service from two to three containers, and removed its containers, network, volume, and temporary directory.
- 2026-08-29: `pwsh ./scripts/dev.ps1 observability-test` passed twice, once directly and once inside the canonical check. The existing host-run path retained its success and retry traces, Collector-unavailable execution proof, structured-log assertions, Prometheus metrics, Grafana dashboard, Jaeger traces, and cleanup.
- 2026-08-29: `pwsh ./scripts/dev.ps1 check` passed. The canonical run included dependency and formatting checks, pinned tools, sqlc and protobuf consistency, vet, all Go tests, all binary builds, image validation, the Compose proof, observability validation, API smoke, distributed execution, failure recovery, acknowledgement-loss, stale-report, and worker-shutdown semantics.
- 2026-08-29: `git diff --check` passed.
- GitHub-hosted CI was not run and remains unverified.

## Slice 3: Compose recovery demonstration

Status: complete

### Goal

Provide a stranger-oriented command that demonstrates worker death, lease recovery, replacement execution, and observability in the full Compose environment.

### Expected files and areas

- `scripts/dev.ps1`
- an optional Compose demonstration override
- focused recovery-demonstration assertions
- this execution plan
- `docs/current-status.md`

### Dependencies

- Slice 2 full Compose environment
- the existing public job and attempt APIs
- existing lease recovery and `demo.sleep` behavior
- Docker Compose project and service labels

### Important decisions

- Keep the automated recovery test isolated and self-cleaning.
- Let the user-facing demonstration leave the stack running for Grafana and Jaeger inspection.
- Use one worker before the forced kill so ownership is unambiguous.
- Use a short explicit lease configuration only for this demonstration.
- Kill only a container whose project and service labels match the expected demonstration target.
- Prove attempt state through HTTP rather than direct PostgreSQL queries.
- Do not claim exactly-once execution or side-effect deduplication.

### Validation required

- an automated `compose-recovery-test` passes twice
- attempt 1 is `abandoned` with `lease_expired`
- attempt 2 succeeds under a distinct worker identity
- the final job status is `succeeded`
- Grafana and Jaeger remain reachable after recovery
- the user-facing demonstration prints the job ID, attempt evidence, and inspection URLs
- the automated test removes all owned processes, containers, networks, volumes, and temporary files
- `pwsh ./scripts/dev.ps1 failure-test`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- M7-R10
- M7-D3
- M7-D4
- M7-D5
- M7-D6

### Decisions and deviations discovered during implementation

- The demonstration uses `deploy/compose.recovery.yaml` to limit the stack to one worker and shorten only its lease, liveness, reaper, retry, and heartbeat timings. The default Compose environment is unchanged.
- The script validates the exact Compose project and `worker` service labels, plus running state, immediately before killing the target container. It starts the replacement with `docker compose up --no-deps worker`; relying on the restart policy was not deterministic when the first worker was killed before Docker's restart-success window, and `--no-deps` avoids rerunning the completed migration service.
- Both commands share one HTTP-driven proof. `compose-recovery-test` uses a unique project and host ports and always removes its stack and temporary directory. `compose-recovery` uses the fixed `quarry-recovery-demo` project and leaves a successful stack running until `compose-recovery-down` removes it.
- Job and attempt state comes only from the public HTTP API. Grafana and Jaeger confirm that the recovered execution remains observable; telemetry does not determine success.
- No architecture or project-plan deviations were required. The demonstration proves at-least-once recovery and does not claim exactly-once execution or side-effect deduplication.

### Validation evidence

- 2026-08-30: `docker compose -f compose.yaml -f deploy/compose.recovery.yaml config --quiet` and `go test -count=1 ./deploy/observability` passed, including assertions for every recovery-only timing and concurrency setting.
- 2026-08-30: `pwsh ./scripts/dev.ps1 compose-recovery-test` passed twice directly. Job `ab92b143-edbb-47a3-a388-e2288ea7bfb3` moved from worker `f0e86f80-ae9b-43da-9edc-422412ffdc91` on abandoned attempt 1 to worker `8d28571a-821b-46d3-8931-c8c542e4d496` on successful attempt 2. Job `ed18ff94-7156-47ef-8eca-1c1066422737` moved from worker `5428853e-d59b-4b96-8b9d-a26846bd625c` to worker `cc386529-c526-4ad9-a223-85667041160c`. Both runs observed `lease_expired`, final `succeeded` state, a provisioned Grafana dashboard, a job trace in Jaeger, printed evidence and inspection URLs, and verified complete cleanup.
- 2026-08-30: The first live recovery attempt was not counted as evidence because Docker's restart policy did not replace a worker killed before its restart-success window. The implementation now starts the replacement explicitly, and all three subsequent recovery runs passed.
- 2026-08-30: `pwsh ./scripts/dev.ps1 failure-test` passed, including recovery, acknowledgement-loss, stale-completion rejection, and process and Docker cleanup.
- 2026-08-30: `pwsh ./scripts/dev.ps1 check` passed. Its recovery run moved job `473bcd3c-1a4c-49ad-b45c-5ee48547f21d` from worker `9fb7dfc0-ea68-49d8-8ccc-eedb99198389` to worker `4b4d23b3-aa87-42b4-9a2f-b31bf7cb36d6`, found Jaeger trace `090f19c48de15900b25b8d8bb08551a4`, and cleaned the isolated stack. The canonical run also passed dependency and formatting checks, pinned tools, generation consistency, vet, all Go tests and builds, image and Compose validation, observability, API smoke, distributed execution, failure recovery, acknowledgement loss, stale-report handling, shutdown semantics, and race checks.
- 2026-08-30: `git diff --check` passed after the tracking updates.
- GitHub-hosted CI was not run and remains unverified.

## Slice 4: Kubernetes and Kustomize resources

Status: complete

### Goal

Define the local Kubernetes deployment with plain resources and an ordered Kustomize structure.

### Expected files and areas

- `deploy/k8s/base/`
- `deploy/k8s/overlays/kind/`
- `scripts/dev.ps1`
- focused Kubernetes configuration checks
- this execution plan
- `docs/current-status.md`

### Dependencies

- Slice 1 images and dispatcher health endpoint
- `kubectl` with built-in Kustomize
- Kubernetes native HTTP and gRPC probes
- the existing worker shutdown timeout

### Important decisions

- Keep PostgreSQL, migration, and application resources in explicit deployment stages.
- Use a Kustomize kind overlay for local image names and local-only credentials.
- Use two dispatcher replicas and several worker replicas by default.
- Give workers no Service.
- Set application and PostgreSQL resource requests and limits.
- Use a worker termination grace period longer than its configured shutdown timeout.
- Keep the kind deployment free of Helm, autoscaling, and the observability stack.
- State that the single PostgreSQL StatefulSet is a local demonstration, not a high-availability design.

### Validation required

- `kubectl kustomize` renders every stage and the complete kind configuration
- `kubectl apply --dry-run=client` accepts the rendered resources
- focused checks inspect image references, replica counts, Services, probes, resources, migration ownership, persistent storage, and worker termination grace
- no worker Service exists
- no Helm, autoscaling, operator, or cloud resource exists
- `pwsh ./scripts/dev.ps1 k8s-config-test`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- M7-R3
- M7-R4

### Decisions and deviations discovered during implementation

- The base and kind overlay each expose three explicit Kustomize entry points in deployment order: PostgreSQL, migration, and applications. Their parent Kustomizations render the complete configuration without removing the stage boundaries that Slice 5 will orchestrate.
- The PostgreSQL stage owns the `quarry` Namespace, headless Service, one-replica StatefulSet, 1 GiB persistent volume claim, and local credential Secret. The base refers only to the Secret contract; the kind overlay generates the fixed-name Secret from plainly marked local-only values so later stages can consume it independently.
- The application stage deploys one API replica behind an HTTP Service, two dispatcher replicas behind a gRPC Service, and three worker replicas without a Service. API probes use `/healthz` and `/readyz`; dispatcher probes use the native gRPC `quarry.dispatcher.liveness` and `quarry.dispatcher.readiness` services.
- Every PostgreSQL, migration, API, dispatcher, and worker container has explicit CPU and memory requests and limits. Worker pods use a 20-second termination grace period with `QUARRY_WORKER_SHUTDOWN_TIMEOUT=10s`.
- `kubectl apply --dry-run=client` still requires API discovery when schema validation is disabled. `k8s-config-test` therefore starts a temporary loopback discovery fixture that advertises only the built-in resource types used here, renders and client-dry-runs all eight Kustomize entry points, then removes the process and temporary directory. It does not create a cluster or perform Slice 5's runtime proof.
- The Kubernetes configuration contains no observability stack, worker Service, Helm, autoscaling, operator, Terraform, cloud load balancer, or custom resource. The single PostgreSQL replica is marked as a local demonstration, not a highly available or production-ready database.
- No architecture or project-plan deviations were required.

### Validation evidence

- 2026-08-30: `kubectl version --client --output=yaml` reported kubectl v1.32.2 with Kustomize v5.5.0.
- 2026-08-30: `go test -count=1 ./deploy/k8s/...` passed. Focused assertions covered stage order, kind image replacements and credentials, replica counts, API and dispatcher Services and probes, resource requests and limits, migration Job ownership, PostgreSQL persistence, worker shutdown grace, the absence of a worker Service, and excluded resource types.
- 2026-08-30: `pwsh ./scripts/dev.ps1 k8s-config-test` passed. `kubectl kustomize` rendered the PostgreSQL, migration, application, and complete roots for both the base and kind overlay. `kubectl apply --dry-run=client --validate=false` accepted every rendered root through the temporary built-in-resource discovery fixture, and cleanup removed the fixture process, binary, rendered files, cache, and kubeconfig.
- 2026-08-30: `pwsh ./scripts/dev.ps1 check` passed. The canonical run repeated the complete Kubernetes configuration proof and also passed dependency and formatting checks, pinned tools, generation consistency, vet, all Go tests and builds, image and Compose validation, observability, API smoke, distributed execution, failure recovery, acknowledgement loss, stale-report handling, shutdown semantics, and race checks.
- 2026-08-30: `git diff --check` passed after the tracking updates.
- A live Kubernetes deployment was not run because that is Slice 5 and `kind` is not installed. GitHub-hosted CI was not run and remains unverified.

## Slice 5: kind deployment proof

Status: complete

### Goal

Prove that a fresh kind cluster can run locally built Quarry images and complete a job.

### Expected files and areas

- `scripts/dev.ps1`
- optional kind cluster configuration under `deploy/k8s/`
- focused kind orchestration helpers
- this execution plan
- `docs/current-status.md`

### Dependencies

- Slices 1 and 4
- Docker daemon access
- `kind`
- `kubectl`
- locally built Quarry images

### Important decisions

- Use a unique cluster name for automated validation.
- Load local images into the cluster rather than publish them.
- Apply PostgreSQL, migration, and application stages in order.
- Wait for each required condition before advancing.
- Manage the API port-forward as an owned child process.
- Keep user-facing and automated cluster lifecycles separate.
- Delete every automated cluster even when validation fails.

### Validation required

- a fresh cluster starts from no prior Quarry resources
- all local images load into kind
- PostgreSQL becomes ready before migration starts
- the migration Job completes before applications deploy
- API, two dispatcher replicas, and the configured worker replicas become Ready
- API and dispatcher probes report success
- a submitted job succeeds and returns attempt history
- the automated port-forward stops
- the automated cluster is deleted and cleanup is verified
- `pwsh ./scripts/dev.ps1 kind-test`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- M7-R3
- runtime proof for M7-R4
- M7-D2
- M7-D3

### Decisions and deviations discovered during implementation

- Automated validation creates a unique cluster, always deletes it, and verifies removal of the cluster, Docker node container, kubectl context, owned API port-forward, and temporary files. The user-facing `kind-up` command uses the fixed `quarry-demo` name and leaves a successful cluster available until `kind-down` removes it.
- The proof requires kind v0.32.0 or newer and pins Kubernetes v1.33.12 by the kind node image digest. It builds and loads the four local Quarry images, then checks the node's container runtime for every exact tag. PostgreSQL remains an upstream image pulled by Kubernetes; importing Docker Desktop's multi-platform PostgreSQL image into kind was unnecessary and failed because its content store did not contain every referenced platform blob.
- PostgreSQL, migration, and applications are applied as separate Kustomize stages. The proof checks that the fresh cluster has no prior Quarry namespace, records PostgreSQL's Ready transition, requires the migration to start afterward and complete once, confirms no application Deployment predates migration completion, and then checks the exact configured replica counts.
- The distroless images declare the named user `nonroot:nonroot`, but Kubernetes cannot prove a named image user satisfies `runAsNonRoot`. The migration and application pod security contexts therefore set the distroless numeric UID and GID 65532 explicitly. This is a Kubernetes boundary fix, not an architecture change.
- The application stage creates dispatcher and worker Deployments together, so Kubernetes can restart workers while dispatcher service endpoints become available. The proof requires the final exact Ready state and a successful public-API job rather than rejecting recovered startup restarts.
- API liveness and readiness are checked through an owned loopback port-forward, dispatcher liveness and readiness are checked through the configured gRPC probes, and one `demo.echo` job must return its expected result with one succeeded attempt and a worker ID.
- No architecture or project-plan deviations were required. Slice 6 scaling work did not begin.

### Validation evidence

- 2026-08-30: kind v0.32.0 and Kubernetes v1.33.12 were used. `go test -count=1 ./deploy/k8s/...` and `pwsh ./scripts/dev.ps1 k8s-config-test` passed after the numeric UID/GID fix.
- 2026-08-30: `pwsh ./scripts/dev.ps1 kind-test` passed on fresh cluster `quarry-m7-ffd6360034fe`. PostgreSQL became Ready, all eight migrations completed before application creation, API 1/1, dispatcher 2/2, and worker 3/3 became Ready, job `ef086ed0-a862-4303-9623-94e078aa7ae3` succeeded through worker `0e558c31-8044-4abf-beb5-d0d10a2b5d17`, and cleanup removed the port-forward, temporary directory, cluster, node container, and kubectl context.
- 2026-08-30: `pwsh ./scripts/dev.ps1 check` passed. Its independent fresh cluster `quarry-m7-7631fc5a6a79` completed job `1c95cb27-79fb-456d-ab5f-cbca6d71b079` through worker `2de33f03-e568-4dc7-b2e6-dc43f225433d`, verified the same ordered deployment and cleanup, and the canonical run also passed tools, formatting and generation consistency, vet, all Go tests and builds, image validation, Compose and observability proofs, distributed execution, failure recovery, acknowledgement loss, stale-report handling, shutdown semantics, and race checks.
- 2026-08-30: Four earlier live attempts were not counted as evidence. They exposed an unnecessary PostgreSQL image import, the named distroless user incompatibility, an acceptance check stricter than the slice requirements, and leaked PowerShell command output that obscured the structured result. Every failed automated cluster was deleted and its node container and kubectl context were removed.
- 2026-08-30: A final cleanup inspection found zero kind clusters, kind node containers, Quarry kind contexts, and kind port-forward temporary directories.
- 2026-08-30: `git diff --check` passed after the tracking updates.
- GitHub-hosted CI was not run and remains unverified.

## Slice 6: Kubernetes worker scaling demonstration

Status: complete

### Goal

Measure manual worker scaling at 1, 4, and 8 kind replicas without changing the publishable Milestone 6 benchmark evidence.

### Expected files and areas

- `scripts/dev.ps1`
- focused scaling orchestration and output
- optional kind demonstration documentation
- this execution plan
- `docs/current-status.md`

### Dependencies

- Slice 5 kind deployment
- existing `cmd/loadgen`
- Workload B
- enough local kind capacity for eight bounded worker pods

### Important decisions

- Fix worker concurrency at 1 for this replica-scaling demonstration.
- Keep the maximum number of outstanding jobs at 8 for every measurement.
- Scale to 1, 4, and 8 worker replicas.
- Require the requested number of Ready replicas before each run.
- Use short warmup and measurement windows.
- Print the results but do not commit them as benchmark claims.
- Restore the documented default replica count after the demonstration.

### Validation required

- each scale operation reaches exactly the requested Ready count
- Workload B completes successfully at 1, 4, and 8 replicas
- each result reports the worker count, fixed load settings, phase durations, and completed jobs per second
- all configurations use the same load limit
- the output labels the measurements as non-publishable
- the documented default replica count is restored
- `pwsh ./scripts/dev.ps1 kind-scaling-test`
- `pwsh ./scripts/dev.ps1 benchmark-verify`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- M7-R5
- scaling portion of M7-R10

### Decisions and deviations discovered during implementation

- `kind-scaling-test` creates a fresh isolated cluster, runs the existing deployment proof, and then measures Workload B in the same cluster at 1, 4, and 8 worker replicas. Reusing the cluster and one owned API port-forward keeps each comparison on the same control plane while retaining full test ownership and cleanup.
- Every configuration fixes worker concurrency at 1, maximum outstanding jobs and HTTP concurrency at 8, warmup at 2 seconds, measurement at 8 seconds, and drain timeout at 10 seconds. The proof requires all completed jobs to succeed with no terminal, submission, or incomplete failures.
- Exact worker readiness counts only active, non-terminating pods. Kubernetes can briefly retain terminal or terminating pods from the prior ReplicaSet after a successful rollout, so counting every historical pod record would reject a correct exact Ready state.
- The command prints completed jobs and completed jobs per second with an explicit non-publishable warning. It does not write to `benchmarks/results/` or change the Milestone 6 campaign.
- Cleanup reapplies the kind application manifests, restores 3 worker replicas with concurrency 4, validates that state, and then removes the port-forward, temporary output, cluster, node container, and kubectl context.
- The canonical `check` runs `kind-scaling-test` in place of the deployment-only kind proof because the scaling command includes that complete baseline proof before measuring. No architecture or project-plan deviations were required. Slice 7 did not begin.

### Validation evidence

- 2026-08-30: the first focused run exposed that completed rollout pods can remain visible briefly after `kubectl rollout status`; it restored the 3-replica, concurrency-4 default and removed all resources but is not completion evidence. The readiness assertion was corrected to count only active, non-terminating pods.
- 2026-08-30: `pwsh ./scripts/dev.ps1 kind-scaling-test` passed on a fresh kind v0.32.0 / Kubernetes v1.33.12 cluster. The baseline public job succeeded, Workload B completed with no failures at exactly 1, 4, and 8 Ready workers, and the non-publishable output reported 31.38, 64.38, and 83.00 completed jobs per second under the fixed load and phase settings. The command restored 3 replicas with concurrency 4 before deleting the cluster and verified cleanup.
- 2026-08-30: `pwsh ./scripts/dev.ps1 benchmark-verify` passed after the implementation and verified campaign `quarry-20260829T002429Z`; no committed benchmark evidence changed.
- 2026-08-30: `pwsh ./scripts/dev.ps1 check` passed. Its independent fresh kind run repeated the baseline deployment proof and measured 30.75, 82.88, and 84.12 completed jobs per second at 1, 4, and 8 workers, then restored defaults and removed the cluster. The canonical run also passed formatting, pinned-tool, generated-code, vet, unit, PostgreSQL integration, build, image, Kubernetes configuration, Compose, recovery, observability, distributed-process, acknowledgement-loss, shutdown-semantics, and race-sensitive validation with cleanup.
- 2026-08-30: an independent post-validation residue check found zero Slice 6 clusters, kubectl contexts, node containers, Quarry Compose containers, and kind temporary directories.
- 2026-08-30: `git diff --check` passed after the implementation and tracking updates.
- GitHub-hosted CI was not run and remains deferred to Slice 7.

## Slice 7: final CI

Status: complete

### Goal

Make pull-request CI validate the completed Go, Docker, Compose, and Kustomize artifacts, then obtain direct hosted evidence.

### Expected files and areas

- `.github/workflows/ci.yml`
- an optional manually triggered extended workflow
- `go.mod`
- `go.sum`
- `scripts/dev.ps1`
- this execution plan
- `docs/current-status.md`

### Dependencies

- Slices 1, 2, and 4
- GitHub Actions
- pinned Staticcheck
- Linux race-detector support
- Docker on the GitHub-hosted runner
- a pinned kubectl setup for Kustomize rendering

### Important decisions

- Keep Staticcheck pinned through the repository's Go tool mechanism.
- Separate Go validation, race, and packaging jobs so failures identify their owner.
- Keep PostgreSQL integration tests in mandatory CI.
- Build every named Docker target.
- Validate Compose configuration and Kustomize rendering.
- Keep long failure, complete kind, and benchmark runs outside mandatory pull-request checks.
- Do not represent local workflow inspection as hosted CI success.

### Validation required

- formatting verification
- `go mod tidy -diff`
- pinned-tool checks
- Staticcheck
- `go vet ./...`
- unit and PostgreSQL integration tests
- protobuf and sqlc generation consistency
- all binary builds
- Linux race detector
- every Docker image target builds
- Docker Compose configuration validates
- kind Kustomize output renders and validates
- workflow YAML passes local syntax checks
- an actual GitHub-hosted push or pull-request run completes successfully
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- M7-R6

### Decisions and deviations discovered during implementation

- Pull-request and push CI now uses three jobs named `go-validation`, `race`, and `packaging`. The jobs call `ci-go`, `ci-race`, and `ci-packaging` in `scripts/dev.ps1` so contributors can rerun each hosted check locally.
- Staticcheck 2026.2.1 (`v0.8.1`) and actionlint `v1.7.12` are pinned through the Go tool mechanism. Staticcheck 2026.2.1 supports the repository's Go 1.27 language version. actionlint provides the required local GitHub Actions syntax check.
- The kubectl setup action installs Kubernetes `v1.33.12`, which matches the kind node version used by the repository.
- Staticcheck adoption required behavior-preserving cleanup of deprecated OpenTelemetry and Prometheus APIs, two error strings, and one direct struct conversion. No domain behavior or architecture changed.
- No extended workflow was added. The long failure, complete kind, and benchmark commands remain explicit local checks because a second workflow would duplicate existing commands without adding evidence required by this slice.

### Validation evidence

- `pwsh -NoProfile ./scripts/dev.ps1 workflow-check`: passed locally; actionlint accepted `.github/workflows/ci.yml`.
- `pwsh -NoProfile ./scripts/dev.ps1 ci-go`: passed locally. It verified `go mod tidy -diff`, formatting, pinned tools, sqlc and Protocol Buffer generation, actionlint, Staticcheck, `go vet`, unit and PostgreSQL integration tests, and all Go builds.
- `pwsh -NoProfile ./scripts/dev.ps1 ci-packaging`: passed locally. All four named Docker targets built, `docker compose config --quiet` passed, and every base and kind Kustomize stage rendered and passed client dry-run validation.
- Linux `go test -count=1 -race ./...`: passed locally in the pinned `golang:1.27.0-bookworm` image with PostgreSQL testcontainers connected through Docker.
- `pwsh -NoProfile ./scripts/dev.ps1 check`: passed locally. The canonical check also completed the image runtime, kind scaling, full Compose, Compose recovery, observability, distributed-process, acknowledgement-loss, failure, and shutdown suites with cleanup verified.
- `git diff --check`: passed.
- GitHub Actions run [33356794797](https://github.com/shaibalmuhtadee/quarry/actions/runs/33356794797) passed on 2026-08-31 for commit `e8a067fe58c4b7747dcb7b11fd4bcc724a3ba6df`. The hosted `go-validation`, `race`, and `packaging` jobs all succeeded.

## Slice 8: architecture, guarantees, and portfolio entry point

Status: complete

### Goal

Make the finished repository understandable and runnable without requiring the project plan or source code as the first reference.

### Expected files and areas

- `docs/architecture.md`
- `docs/guarantees.md`
- `README.md`
- `docs/benchmarks.md` only if links or reproduction commands need correction
- demonstration command help in `scripts/dev.ps1`
- this execution plan
- `docs/current-status.md`

### Dependencies

- Slices 2, 3, 5, 6, and 7
- current implementation and recorded validation evidence
- the verified Milestone 6 benchmark campaign

### Important decisions

- Keep `README.md` short enough to act as the repository entry point.
- Put component ownership, request flow, execution flow, recovery, and deployment topology in `docs/architecture.md`.
- Put guarantees, failure boundaries, and non-guarantees in `docs/guarantees.md`.
- Keep benchmark methodology, results, and limits in `docs/benchmarks.md`.
- Document the Compose path as the complete metrics and traces demonstration.
- Document kind as a local platform demonstration without production availability claims.
- State at-least-once execution and cooperative cancellation limits directly.
- Link to evidence instead of copying benchmark values across files.

### Validation required

- follow the README Compose path from a clean checkout
- submit and complete a job through the documented commands
- run the documented worker-kill recovery demonstration
- inspect Grafana metrics and the Jaeger trace through the documented URLs
- follow the kind quickstart and scaling demonstration
- run every documented validation and benchmark-verification command that is part of the primary walkthrough
- verify every local documentation link
- compare all guarantee statements with code and tests
- `pwsh ./scripts/dev.ps1 benchmark-verify`
- `pwsh ./scripts/dev.ps1 check`
- `git diff --check`

### Milestone requirements satisfied

- M7-R7
- M7-R8
- M7-R9
- M7-R10
- M7-D1
- M7-D2
- M7-D3
- M7-D4
- M7-D5
- M7-D6
- M7-D7

### Decisions and deviations discovered during implementation

- `README.md` is the short runnable entry point. Architecture, guarantees, and benchmark detail remain in their single-purpose documents instead of being copied into the README.
- `scripts/dev.ps1 help` lists the primary demonstrations, and `docs-test` verifies inline local Markdown links without requiring Go. The canonical `check` command runs the same link check first.
- `docs/benchmarks.md` did not change because its committed campaign links and reproduction commands remained correct.
- The clean Compose walkthrough and canonical check used isolated Compose project names because the default `quarry_postgres-data` volume contained pre-existing development data. The demonstrations removed their isolated resources and preserved that volume.
- The implementation matched the planned architecture and guarantee boundaries. No architecture or project-plan deviation was required.

### Validation evidence

- On 2026-08-31, `pwsh -NoProfile ./scripts/dev.ps1 help` printed the primary Compose recovery, kind, benchmark-verification, documentation, and canonical-check commands. `pwsh -NoProfile ./scripts/dev.ps1 docs-test` passed with 9 local links across 16 Markdown files.
- The README Compose walkthrough passed from a fresh `quarry-m7-docs` project and volume. Job `ae7b792d-42f3-455f-a95d-6fd07af052f1` succeeded in one attempt with the documented result. Grafana rendered queue depth 0 and two active workers; a Jaeger search by that exact job ID returned one trace with nine spans across three services. The isolated containers, network, and volume were removed.
- `pwsh -NoProfile ./scripts/dev.ps1 compose-recovery` passed for job `70422e8d-e361-4f38-a02a-ef87f6c503c7`: attempt 1 was abandoned with `lease_expired`, and attempt 2 succeeded under a different worker. Grafana showed the abandoned and succeeded outcomes, and Jaeger trace `a512334098ee261ed465cdf1e26bd72c` showed both execution paths. `compose-recovery-down` removed the demonstration resources.
- `pwsh -NoProfile ./scripts/dev.ps1 kind-up` passed with kind v0.32.0 and Kubernetes v1.33.12. The documented `kubectl` command showed one API, two dispatcher, and three worker pods Ready, and job `966eb720-baf0-4314-a9da-f696d08e3d53` succeeded. `kind-down` removed the cluster.
- A separate `kind-scaling-test` passed at 1, 4, and 8 Ready workers, restored the committed three-worker default, and removed the cluster and temporary output. Its 31.88, 80.25, and 89.25 jobs/s observations remain explicitly non-publishable deployment evidence.
- Guarantee statements were compared with the API cancellation path, dispatcher claim/report/reaper transitions, worker executor and shutdown behavior, PostgreSQL integration tests, and the recovery, acknowledgement-loss, and semantics process tests. Incorrect drafts about expired cancellation and universal forced handler cancellation were corrected before validation.
- `pwsh -NoProfile ./scripts/dev.ps1 benchmark-verify` passed, including deterministic fixtures and committed campaign `quarry-20260829T002429Z`.
- With `COMPOSE_PROJECT_NAME=quarry-m7-slice8-check` to isolate the retained development volume, `pwsh -NoProfile ./scripts/dev.ps1 check` passed. This covered documentation links, formatting and generation, pinned tools, static analysis, Go tests and builds, four images, Compose, Kustomize, kind scaling, observability, distributed execution, worker recovery, acknowledgement loss, shutdown semantics, PostgreSQL integration, and race-sensitive packages.
- Final cleanup inspection found no Slice 8 containers, networks, kind clusters, or kind contexts. The one outer isolated-check volume was removed explicitly; the pre-existing `quarry_postgres-data` development volume remained. `git diff --check` passed.

## Milestone audit

Status: complete

The audit inspected the implementation and reran the required proofs independently of the slice statuses. Milestone 7 requirements M7-R1 through M7-R10 and definition-of-done items M7-D1 through M7-D7 are satisfied. No implementation defect or omission required a code change.

### Required audit checks

- compare the implementation with Milestone 7 and the resume-ready stop condition in `docs/project-plan.md`
- inspect the public API, runtime behavior, images, Compose environment, Kubernetes resources, scripts, CI, and documentation directly
- run the focused image, Compose, recovery, Kustomize, kind, scaling, and benchmark-verification commands
- run `pwsh ./scripts/dev.ps1 check`
- run required Linux race validation
- observe a successful GitHub-hosted CI run
- follow the README demonstration as a stranger would
- inspect metrics and traces from the full Compose environment
- verify worker-kill recovery through public attempt history
- verify all test processes, containers, networks, volumes, port-forwards, and kind clusters are cleaned up
- inspect the diff for excluded V1 features, unsupported claims, and unnecessary dependencies
- run `git diff --check`

Only after the audit passes:

- mark Milestone 7 complete in `docs/current-status.md`,
- move this plan to `docs/exec-plans/completed/milestone-7.md`,
- record any remaining limitations,
- stop adding V1 features.

### Audit evidence

- Direct inspection covered the public HTTP API, dispatcher and worker runtime paths, PostgreSQL migrations and state transitions, all four Docker image targets, the complete Compose topology, Kustomize base and kind overlay, health probes, resource controls, scaling scripts, CI workflow, Go dependencies, README, architecture, guarantees, and benchmark documentation. The implementation preserves the planned PostgreSQL authority and at-least-once boundary and contains no excluded V1 feature, unsupported availability or exactly-once claim, or unnecessary runtime dependency.
- The README Compose walkthrough passed from a fresh isolated project. Job `1b693475-91f6-4847-8ff5-dbb5d96a5003` succeeded; Prometheus reported the submitted and succeeded work plus two active workers; Grafana loaded the provisioned 13-panel dashboard; and Jaeger trace `bc17cf64f4fee76a3c8d685678671607` contained nine spans across the API, dispatcher, worker, handler, and database operations.
- The documented worker-kill recovery demonstration passed for job `591b9d1b-516b-4c1c-aca6-7009bb97a349`. Public attempt history and direct PostgreSQL inspection agreed: attempt 1 was abandoned with `lease_expired`, and attempt 2 succeeded under a distinct worker. Prometheus exposed both outcomes, and Jaeger returned an 11-span trace containing both execution paths.
- The README kind walkthrough passed with one API, two dispatcher, and three worker pods Ready. A submitted job succeeded, and direct PostgreSQL inspection confirmed migration version 8 plus the durable succeeded job and attempt rows. The cluster was removed afterward.
- Focused `image-test`, `compose-test`, `compose-recovery-test`, `k8s-config-test`, `kind-test`, `kind-scaling-test`, and `benchmark-verify` commands passed. The standalone scaling proof observed 32.00, 95.12, and 98.88 jobs/s at 1, 4, and 8 workers, restored the committed three-worker and concurrency-four defaults, and remains explicitly non-publishable. Benchmark verification accepted the deterministic fixtures and committed campaign `quarry-20260829T002429Z`.
- Linux `go test -count=1 -race ./...` passed in the pinned `golang:1.27.0-bookworm` image, including real PostgreSQL testcontainers. `go mod tidy -diff`, focused dispatcher, Kustomize, and observability tests, documentation links, and `git diff --check` also passed.
- A fresh canonical `pwsh -NoProfile ./scripts/dev.ps1 check` passed with `COMPOSE_PROJECT_NAME=quarry-m7-audit-check2`. It covered documentation, formatting, generation, pinned tools, workflow linting, static analysis, all Go tests and builds, image runtime, Kustomize validation, kind scaling, full Compose, public recovery, observability failure isolation, distributed execution, process-crash recovery, acknowledgement loss, shutdown semantics, PostgreSQL state, and owned-resource cleanup.
- GitHub Actions run [33423791505](https://github.com/shaibalmuhtadee/quarry/actions/runs/33423791505) passed for audited commit `f64e3f93780422dc0dd5265e53eecc624cdda8c2`; its `go-validation`, `race`, and `packaging` jobs all succeeded.
- Final cleanup inspection found no audit containers, networks, kind clusters, kind contexts, or port-forwards. The exact isolated canonical-check volume was removed after its Compose project label and lack of attached containers were verified. The pre-existing `quarry_postgres-data` development volume was preserved.
- Remaining limitations are intentional and documented: execution is at least once, cancellation depends on handler cooperation until process exit, Compose and kind are local demonstrations rather than production high-availability deployments, local Jaeger storage is in memory, and benchmark results describe one bounded local environment.
