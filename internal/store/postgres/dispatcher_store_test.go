package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shaibalmuhtadee/quarry/internal/domain"
	"github.com/shaibalmuhtadee/quarry/internal/store/postgres"
)

func TestDispatcherStoreRegistersWorkersIdempotently(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	store := postgres.NewDispatcherStore(pool)

	registration := postgres.WorkerRegistration{
		ID:          domain.NewWorkerID(),
		Hostname:    "worker-a",
		Version:     "v0.1.0",
		Concurrency: 4,
		StartedAt:   time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC),
	}
	if err := store.RegisterWorker(ctx, registration); err != nil {
		t.Fatalf("register worker: %v", err)
	}
	if err := store.RegisterWorker(ctx, registration); err != nil {
		t.Fatalf("repeat identical registration: %v", err)
	}

	conflicts := []struct {
		name   string
		change func(*postgres.WorkerRegistration)
	}{
		{name: "hostname", change: func(value *postgres.WorkerRegistration) { value.Hostname = "worker-b" }},
		{name: "version", change: func(value *postgres.WorkerRegistration) { value.Version = "v0.2.0" }},
		{name: "concurrency", change: func(value *postgres.WorkerRegistration) { value.Concurrency = 5 }},
		{name: "start time", change: func(value *postgres.WorkerRegistration) { value.StartedAt = value.StartedAt.Add(time.Second) }},
	}
	for _, test := range conflicts {
		t.Run(test.name, func(t *testing.T) {
			conflict := registration
			test.change(&conflict)
			err := store.RegisterWorker(ctx, conflict)
			if !errors.Is(err, postgres.ErrWorkerRegistrationConflict) {
				t.Fatalf("conflicting registration error = %v, want ErrWorkerRegistrationConflict", err)
			}
		})
	}

	var workerCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workers WHERE id = $1`, registration.ID.UUID()).Scan(&workerCount); err != nil {
		t.Fatalf("count registered workers: %v", err)
	}
	if workerCount != 1 {
		t.Fatalf("registered worker count = %d, want 1", workerCount)
	}

	_, err := store.AcquireJobs(ctx, domain.NewWorkerID(), 1, []domain.JobType{mustJobType(t, "demo.echo")})
	if !errors.Is(err, postgres.ErrWorkerNotRegistered) {
		t.Fatalf("unregistered worker acquisition error = %v, want ErrWorkerNotRegistered", err)
	}
}

func TestDispatcherStoreClaimsEligibleSupportedJobsInOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	dispatcherStore := postgres.NewDispatcherStore(pool)
	jobStore := postgres.NewJobStore(pool)

	worker := registerTestWorker(t, ctx, dispatcherStore, 5)
	baseTime := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	first := createTestJob(t, ctx, jobStore, "demo.echo", `{"order":1}`)
	second := createTestJob(t, ctx, jobStore, "demo.echo", `{"order":2}`)
	capacityLimited := createTestJob(t, ctx, jobStore, "demo.echo", `{"order":3}`)
	future := createTestJob(t, ctx, jobStore, "demo.echo", `{"future":true}`)
	unsupported := createTestJob(t, ctx, jobStore, "demo.payload_size", `{"size":3}`)

	setJobTimes(t, ctx, pool, first.ID, baseTime, baseTime)
	setJobTimes(t, ctx, pool, second.ID, baseTime.Add(time.Second), baseTime.Add(time.Second))
	setJobTimes(t, ctx, pool, capacityLimited.ID, baseTime.Add(2*time.Second), baseTime.Add(2*time.Second))
	setJobTimes(t, ctx, pool, unsupported.ID, baseTime.Add(3*time.Second), baseTime.Add(3*time.Second))
	setJobTimes(t, ctx, pool, future.ID, time.Now().Add(time.Hour), baseTime.Add(4*time.Second))

	jobs, err := dispatcherStore.AcquireJobs(
		ctx,
		worker,
		2,
		[]domain.JobType{mustJobType(t, "demo.echo")},
	)
	if err != nil {
		t.Fatalf("acquire jobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("acquired job count = %d, want 2", len(jobs))
	}
	if jobs[0].ID != first.ID || jobs[1].ID != second.ID {
		t.Fatalf("acquired order = [%s, %s], want [%s, %s]", jobs[0].ID, jobs[1].ID, first.ID, second.ID)
	}
	for i, job := range jobs {
		if job.AttemptNumber.Int32() != 1 {
			t.Fatalf("job %d attempt number = %d, want 1", i, job.AttemptNumber.Int32())
		}
		if job.Type.String() != "demo.echo" {
			t.Fatalf("job %d type = %q, want demo.echo", i, job.Type)
		}
		if job.Timeout != 30*time.Second {
			t.Fatalf("job %d timeout = %s, want 30s", i, job.Timeout)
		}
	}

	for _, claimed := range []domain.Job{first, second} {
		stored, err := jobStore.GetJob(ctx, claimed.ID)
		if err != nil {
			t.Fatalf("get claimed job %s: %v", claimed.ID, err)
		}
		if stored.Status != domain.JobStatusRunning || stored.AttemptCount != 1 {
			t.Fatalf("claimed job %s state = (%q, %d), want (running, 1)", stored.ID, stored.Status, stored.AttemptCount)
		}
	}
	for _, unclaimed := range []domain.Job{capacityLimited, future, unsupported} {
		stored, err := jobStore.GetJob(ctx, unclaimed.ID)
		if err != nil {
			t.Fatalf("get unclaimed job %s: %v", unclaimed.ID, err)
		}
		if stored.Status != domain.JobStatusQueued || stored.AttemptCount != 0 {
			t.Fatalf("unclaimed job %s state = (%q, %d), want (queued, 0)", stored.ID, stored.Status, stored.AttemptCount)
		}
	}

	var attempts int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM job_attempts
		WHERE worker_id = $1 AND status = 'running'
	`, worker.UUID()).Scan(&attempts); err != nil {
		t.Fatalf("count created attempts: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("created attempt count = %d, want 2", attempts)
	}
}

func TestDispatcherStoreEnforcesConcurrencyAcrossConcurrentAcquisitions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	dispatcherStore := postgres.NewDispatcherStore(pool)
	jobStore := postgres.NewJobStore(pool)
	worker := registerTestWorker(t, ctx, dispatcherStore, 3)
	jobType := mustJobType(t, "capacity.test")

	for i := 0; i < 20; i++ {
		createTestJob(t, ctx, jobStore, jobType.String(), fmt.Sprintf(`{"job":%d}`, i))
	}

	const callers = 8
	start := make(chan struct{})
	results := make(chan []postgres.AcquiredJob, callers)
	errorsByCaller := make(chan error, callers)
	var group sync.WaitGroup
	for i := 0; i < callers; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			jobs, err := dispatcherStore.AcquireJobs(ctx, worker, 3, []domain.JobType{jobType})
			if err != nil {
				errorsByCaller <- err
				return
			}
			results <- jobs
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errorsByCaller)

	for err := range errorsByCaller {
		t.Errorf("concurrent acquisition: %v", err)
	}
	claimed := 0
	for jobs := range results {
		claimed += len(jobs)
	}
	if claimed != 3 {
		t.Fatalf("concurrently claimed jobs = %d, want registered concurrency 3", claimed)
	}

	var running int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE current_worker_id = $1 AND status = 'running'`, worker.UUID()).Scan(&running); err != nil {
		t.Fatalf("count running jobs: %v", err)
	}
	if running != 3 {
		t.Fatalf("stored running jobs = %d, want 3", running)
	}
}

func TestDispatcherStoreConcurrentClaimersCreateOneAttemptPerJob(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	dispatcherStore := postgres.NewDispatcherStore(pool)
	jobStore := postgres.NewJobStore(pool)
	jobType := mustJobType(t, "claim.test")

	const jobCount = 100
	for i := 0; i < jobCount; i++ {
		createTestJob(t, ctx, jobStore, jobType.String(), fmt.Sprintf(`{"job":%d}`, i))
	}

	const workerCount = 8
	workers := make([]domain.WorkerID, workerCount)
	for i := range workers {
		workers[i] = registerTestWorker(t, ctx, dispatcherStore, 20)
	}

	start := make(chan struct{})
	results := make(chan []postgres.AcquiredJob, workerCount)
	errorsByWorker := make(chan error, workerCount)
	var group sync.WaitGroup
	for _, worker := range workers {
		group.Add(1)
		go func(worker domain.WorkerID) {
			defer group.Done()
			<-start
			jobs, err := dispatcherStore.AcquireJobs(ctx, worker, 20, []domain.JobType{jobType})
			if err != nil {
				errorsByWorker <- err
				return
			}
			results <- jobs
		}(worker)
	}
	close(start)
	group.Wait()
	close(results)
	close(errorsByWorker)

	for err := range errorsByWorker {
		t.Errorf("concurrent worker acquisition: %v", err)
	}
	claimedIDs := make(map[string]struct{}, jobCount)
	for jobs := range results {
		for _, job := range jobs {
			if _, exists := claimedIDs[job.ID.String()]; exists {
				t.Fatalf("job %s was returned to more than one claimer", job.ID)
			}
			claimedIDs[job.ID.String()] = struct{}{}
		}
	}
	if len(claimedIDs) != jobCount {
		t.Fatalf("unique claimed jobs = %d, want %d", len(claimedIDs), jobCount)
	}

	var runningJobs, attempts, duplicateAttempts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE status = 'running' AND attempt_count = 1`).Scan(&runningJobs); err != nil {
		t.Fatalf("count running jobs: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM job_attempts WHERE status = 'running'`).Scan(&attempts); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM (
			SELECT job_id
			FROM job_attempts
			GROUP BY job_id
			HAVING count(*) <> 1
		) AS duplicates
	`).Scan(&duplicateAttempts); err != nil {
		t.Fatalf("count duplicate attempts: %v", err)
	}
	if runningJobs != jobCount || attempts != jobCount || duplicateAttempts != 0 {
		t.Fatalf("stored claim state = jobs %d, attempts %d, duplicate groups %d; want %d, %d, 0", runningJobs, attempts, duplicateAttempts, jobCount, jobCount)
	}
}

func TestDispatcherStoreReportsSuccessAtomicallyAndIdempotently(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	dispatcherStore := postgres.NewDispatcherStore(pool)
	jobStore := postgres.NewJobStore(pool)
	worker := registerTestWorker(t, ctx, dispatcherStore, 1)
	job := createTestJob(t, ctx, jobStore, "success.test", `{"input":true}`)
	attempt := acquireOneTestJob(t, ctx, dispatcherStore, worker, "success.test")
	result := mustResult(t, `{"ok":true,"count":2}`)

	if err := dispatcherStore.ReportSuccess(ctx, worker, job.ID, attempt.AttemptNumber, result); err != nil {
		t.Fatalf("report success: %v", err)
	}
	semanticallyIdenticalResult := mustResult(t, `{ "count": 2, "ok": true }`)
	if err := dispatcherStore.ReportSuccess(ctx, worker, job.ID, attempt.AttemptNumber, semanticallyIdenticalResult); err != nil {
		t.Fatalf("repeat identical success: %v", err)
	}
	conflictingResult := mustResult(t, `{"ok":false,"count":2}`)
	if err := dispatcherStore.ReportSuccess(ctx, worker, job.ID, attempt.AttemptNumber, conflictingResult); !errors.Is(err, postgres.ErrAttemptReportConflict) {
		t.Fatalf("conflicting repeated success error = %v, want ErrAttemptReportConflict", err)
	}

	storedJob, err := jobStore.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get successful job: %v", err)
	}
	if storedJob.Status != domain.JobStatusSucceeded || storedJob.Result == nil || storedJob.FinishedAt == nil {
		t.Fatalf("successful job state = status %q, result %v, finished_at %v", storedJob.Status, storedJob.Result, storedJob.FinishedAt)
	}
	assertJSONEqual(t, storedJob.Result.JSON(), result.JSON())
	if !storedJob.UpdatedAt.Equal(*storedJob.FinishedAt) {
		t.Fatalf("job updated timestamp = %s, want finish timestamp %s", storedJob.UpdatedAt, *storedJob.FinishedAt)
	}

	attempts, err := jobStore.ListJobAttempts(ctx, job.ID)
	if err != nil {
		t.Fatalf("list successful attempts: %v", err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempt count = %d, want 1", len(attempts))
	}
	storedAttempt := attempts[0]
	if storedAttempt.Number != attempt.AttemptNumber || storedAttempt.WorkerID != worker ||
		storedAttempt.Status != domain.AttemptStatusSucceeded || storedAttempt.FinishedAt == nil {
		t.Fatalf("stored attempt = %#v", storedAttempt)
	}
	if !storedAttempt.FinishedAt.Equal(*storedJob.FinishedAt) {
		t.Fatalf("attempt finish timestamp = %s, want job finish timestamp %s", *storedAttempt.FinishedAt, *storedJob.FinishedAt)
	}

	var currentWorkerIsNull bool
	if err := pool.QueryRow(ctx, `SELECT current_worker_id IS NULL FROM jobs WHERE id = $1`, job.ID.UUID()).Scan(&currentWorkerIsNull); err != nil {
		t.Fatalf("read successful job worker assignment: %v", err)
	}
	if !currentWorkerIsNull {
		t.Fatal("successful job retained its active worker assignment")
	}
}

func TestDispatcherStoreRejectsMismatchedSuccessWithoutChangingState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	dispatcherStore := postgres.NewDispatcherStore(pool)
	jobStore := postgres.NewJobStore(pool)
	worker := registerTestWorker(t, ctx, dispatcherStore, 1)
	otherWorker := registerTestWorker(t, ctx, dispatcherStore, 1)
	job := createTestJob(t, ctx, jobStore, "mismatch.test", `{}`)
	attempt := acquireOneTestJob(t, ctx, dispatcherStore, worker, "mismatch.test")
	otherAttemptNumber, err := domain.NewAttemptNumber(2)
	if err != nil {
		t.Fatalf("create mismatched attempt number: %v", err)
	}
	result := mustResult(t, `{"ok":true}`)

	tests := []struct {
		name          string
		workerID      domain.WorkerID
		jobID         domain.JobID
		attemptNumber domain.AttemptNumber
	}{
		{name: "worker", workerID: otherWorker, jobID: job.ID, attemptNumber: attempt.AttemptNumber},
		{name: "job", workerID: worker, jobID: domain.NewJobID(), attemptNumber: attempt.AttemptNumber},
		{name: "attempt", workerID: worker, jobID: job.ID, attemptNumber: otherAttemptNumber},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := dispatcherStore.ReportSuccess(ctx, test.workerID, test.jobID, test.attemptNumber, result)
			if !errors.Is(err, postgres.ErrAttemptReportConflict) {
				t.Fatalf("mismatched success error = %v, want ErrAttemptReportConflict", err)
			}
		})
	}

	storedJob, err := jobStore.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job after mismatched reports: %v", err)
	}
	if storedJob.Status != domain.JobStatusRunning || storedJob.Result != nil || storedJob.FinishedAt != nil {
		t.Fatalf("job changed after mismatched reports: %#v", storedJob)
	}
	attempts, err := jobStore.ListJobAttempts(ctx, job.ID)
	if err != nil {
		t.Fatalf("list attempts after mismatched reports: %v", err)
	}
	if len(attempts) != 1 || attempts[0].Status != domain.AttemptStatusRunning || attempts[0].FinishedAt != nil {
		t.Fatalf("attempt changed after mismatched reports: %#v", attempts)
	}
}

func TestDispatcherStoreRollsBackAttemptWhenJobCompletionFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	dispatcherStore := postgres.NewDispatcherStore(pool)
	jobStore := postgres.NewJobStore(pool)
	worker := registerTestWorker(t, ctx, dispatcherStore, 1)
	job := createTestJob(t, ctx, jobStore, "atomic.test", `{}`)
	attempt := acquireOneTestJob(t, ctx, dispatcherStore, worker, "atomic.test")

	_, err := pool.Exec(ctx, `
		CREATE FUNCTION reject_successful_job_update() RETURNS trigger AS $$
		BEGIN
			IF NEW.status = 'succeeded' THEN
				RAISE EXCEPTION 'forced job completion failure';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql;
	`)
	if err != nil {
		t.Fatalf("install completion failure function: %v", err)
	}
	_, err = pool.Exec(ctx, `
		CREATE TRIGGER reject_successful_job_update
		BEFORE UPDATE ON jobs
		FOR EACH ROW EXECUTE FUNCTION reject_successful_job_update();
	`)
	if err != nil {
		t.Fatalf("install completion failure trigger: %v", err)
	}

	err = dispatcherStore.ReportSuccess(ctx, worker, job.ID, attempt.AttemptNumber, mustResult(t, `{"ok":true}`))
	if err == nil {
		t.Fatal("successful report unexpectedly passed the forced job update failure")
	}

	storedJob, err := jobStore.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job after rolled-back report: %v", err)
	}
	attempts, err := jobStore.ListJobAttempts(ctx, job.ID)
	if err != nil {
		t.Fatalf("list attempts after rolled-back report: %v", err)
	}
	if storedJob.Status != domain.JobStatusRunning || storedJob.Result != nil || storedJob.FinishedAt != nil {
		t.Fatalf("job changed after rolled-back report: %#v", storedJob)
	}
	if len(attempts) != 1 || attempts[0].Status != domain.AttemptStatusRunning || attempts[0].FinishedAt != nil {
		t.Fatalf("attempt changed after rolled-back report: %#v", attempts)
	}
}

func newDispatcherTestPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()

	pool, err := postgres.NewPool(ctx, startMigratedPostgres(t))
	if err != nil {
		t.Fatalf("create PostgreSQL pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func registerTestWorker(
	t *testing.T,
	ctx context.Context,
	store *postgres.DispatcherStore,
	concurrency int32,
) domain.WorkerID {
	t.Helper()

	workerID := domain.NewWorkerID()
	err := store.RegisterWorker(ctx, postgres.WorkerRegistration{
		ID:          workerID,
		Hostname:    "test-worker-" + workerID.String(),
		Version:     "test",
		Concurrency: concurrency,
		StartedAt:   time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("register test worker: %v", err)
	}
	return workerID
}

func createTestJob(
	t *testing.T,
	ctx context.Context,
	store *postgres.JobStore,
	jobTypeValue string,
	payloadValue string,
) domain.Job {
	t.Helper()

	jobType := mustJobType(t, jobTypeValue)
	payload, err := domain.ParsePayload(json.RawMessage(payloadValue))
	if err != nil {
		t.Fatalf("parse test payload: %v", err)
	}
	submission, err := domain.NewJobSubmission(jobType, payload, 3, 30*time.Second)
	if err != nil {
		t.Fatalf("create test submission: %v", err)
	}
	job, err := store.CreateJob(ctx, submission)
	if err != nil {
		t.Fatalf("create test job: %v", err)
	}
	return job
}

func acquireOneTestJob(
	t *testing.T,
	ctx context.Context,
	store *postgres.DispatcherStore,
	workerID domain.WorkerID,
	jobTypeValue string,
) postgres.AcquiredJob {
	t.Helper()

	jobs, err := store.AcquireJobs(ctx, workerID, 1, []domain.JobType{mustJobType(t, jobTypeValue)})
	if err != nil {
		t.Fatalf("acquire test job: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("acquired test jobs = %d, want 1", len(jobs))
	}
	return jobs[0]
}

func mustResult(t *testing.T, value string) domain.Result {
	t.Helper()

	result, err := domain.ParseResult(json.RawMessage(value))
	if err != nil {
		t.Fatalf("parse test result: %v", err)
	}
	return result
}

func mustJobType(t *testing.T, value string) domain.JobType {
	t.Helper()

	jobType, err := domain.ParseJobType(value)
	if err != nil {
		t.Fatalf("parse job type %q: %v", value, err)
	}
	return jobType
}

func setJobTimes(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	jobID domain.JobID,
	availableAt time.Time,
	createdAt time.Time,
) {
	t.Helper()

	_, err := pool.Exec(ctx, `
		UPDATE jobs
		SET available_at = $2, created_at = $3
		WHERE id = $1
	`, jobID.UUID(), availableAt, createdAt)
	if err != nil {
		t.Fatalf("set job times: %v", err)
	}
}
