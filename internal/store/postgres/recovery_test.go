package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
	"github.com/shaibalmuhtadee/quarry/internal/store/postgres"
)

func TestDispatcherStoreRecoversExpiredAttemptAndFencesStaleSuccess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	store := newDispatcherTestStore(t, pool, testLeaseDuration)
	jobStore := postgres.NewJobStore(pool)
	firstWorker := registerTestWorker(t, ctx, store, 1)
	secondWorker := registerTestWorker(t, ctx, store, 1)
	job := createTestJob(t, ctx, jobStore, "recovery.retry", `{}`)
	firstAttempt := acquireOneTestJob(t, ctx, store, firstWorker, "recovery.retry")

	if _, err := pool.Exec(ctx, `UPDATE jobs SET lease_expires_at = statement_timestamp() - interval '1 second' WHERE id = $1`, job.ID.UUID()); err != nil {
		t.Fatalf("expire attempt: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workers SET last_seen_at = statement_timestamp() - interval '1 minute' WHERE id = $1`, firstWorker.UUID()); err != nil {
		t.Fatalf("expire worker heartbeat: %v", err)
	}

	recovered, err := store.RecoverExpiredAttempts(ctx, 10, 20*time.Second)
	if err != nil {
		t.Fatalf("recover expired attempt: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("recovered jobs = %d, want 1", recovered)
	}
	if repeated, err := store.RecoverExpiredAttempts(ctx, 10, 20*time.Second); err != nil || repeated != 0 {
		t.Fatalf("repeated recovery = (%d, %v), want (0, nil)", repeated, err)
	}

	stored, err := jobStore.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get recovered job: %v", err)
	}
	if stored.Status != domain.JobStatusRetryWait || stored.AttemptCount != 1 || stored.FinishedAt != nil {
		t.Fatalf("recovered job = %#v, want retry_wait attempt 1 without finish time", stored)
	}
	var assigned, leased, immediatelyAvailable bool
	if err := pool.QueryRow(ctx, `
		SELECT current_worker_id IS NOT NULL, lease_expires_at IS NOT NULL, available_at <= statement_timestamp()
		FROM jobs WHERE id = $1
	`, job.ID.UUID()).Scan(&assigned, &leased, &immediatelyAvailable); err != nil {
		t.Fatalf("read recovered lease state: %v", err)
	}
	if assigned || leased || !immediatelyAvailable {
		t.Fatalf("recovered lease state = assigned %t, leased %t, immediately available %t", assigned, leased, immediatelyAvailable)
	}
	attempts, err := jobStore.ListJobAttempts(ctx, job.ID)
	if err != nil {
		t.Fatalf("list recovered attempts: %v", err)
	}
	if len(attempts) != 1 || attempts[0].Status != domain.AttemptStatusAbandoned || attempts[0].FinishedAt == nil {
		t.Fatalf("recovered attempts = %#v, want one finished abandoned attempt", attempts)
	}
	var workerState string
	if err := pool.QueryRow(ctx, `SELECT state FROM workers WHERE id = $1`, firstWorker.UUID()).Scan(&workerState); err != nil {
		t.Fatalf("read lost worker: %v", err)
	}
	if workerState != "lost" {
		t.Fatalf("worker state = %q, want lost", workerState)
	}

	secondAttempt := acquireOneTestJob(t, ctx, store, secondWorker, "recovery.retry")
	if secondAttempt.ID != job.ID || secondAttempt.AttemptNumber.Int32() != 2 {
		t.Fatalf("replacement attempt = (%s, %d), want (%s, 2)", secondAttempt.ID, secondAttempt.AttemptNumber.Int32(), job.ID)
	}
	if err := reportSuccess(ctx, store, firstWorker, job.ID, firstAttempt.AttemptNumber, mustResult(t, `{"stale":true}`)); !errors.Is(err, postgres.ErrAttemptReportConflict) {
		t.Fatalf("stale attempt success error = %v, want ErrAttemptReportConflict", err)
	}
	stored, err = jobStore.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job after stale success: %v", err)
	}
	if stored.Status != domain.JobStatusRunning || stored.AttemptCount != 2 {
		t.Fatalf("job after stale success = (%q, %d), want (running, 2)", stored.Status, stored.AttemptCount)
	}

	if _, err := store.Heartbeat(ctx, firstWorker, nil); err != nil {
		t.Fatalf("restore lost worker with heartbeat: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT state FROM workers WHERE id = $1`, firstWorker.UUID()).Scan(&workerState); err != nil {
		t.Fatalf("read restored worker: %v", err)
	}
	if workerState != "active" {
		t.Fatalf("worker state after heartbeat = %q, want active", workerState)
	}
}

func TestDispatcherStoreSchedulesExpiredLeaseWithExactBackoff(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	retryPolicy, err := domain.NewRetryPolicy(time.Second, time.Minute, func(upperExclusive int64) int64 {
		if upperExclusive != 1001 {
			t.Fatalf("retry jitter upper bound = %d, want 1001", upperExclusive)
		}
		return 625
	})
	if err != nil {
		t.Fatalf("create retry policy: %v", err)
	}
	store := postgres.NewDispatcherStore(pool, testLeaseDuration, retryPolicy)
	jobStore := postgres.NewJobStore(pool)
	worker := registerTestWorker(t, ctx, store, 1)
	job := createTestJob(t, ctx, jobStore, "recovery.backoff", `{}`)
	acquireOneTestJob(t, ctx, store, worker, "recovery.backoff")
	if _, err := pool.Exec(ctx, `UPDATE jobs SET lease_expires_at = statement_timestamp() - interval '1 second' WHERE id = $1`, job.ID.UUID()); err != nil {
		t.Fatalf("expire attempt: %v", err)
	}

	if recovered, err := store.RecoverExpiredAttempts(ctx, 1, time.Hour); err != nil || recovered != 1 {
		t.Fatalf("recover expired attempt = (%d, %v), want (1, nil)", recovered, err)
	}
	var availableAt, updatedAt time.Time
	if err := pool.QueryRow(ctx, `SELECT available_at, updated_at FROM jobs WHERE id = $1`, job.ID.UUID()).Scan(&availableAt, &updatedAt); err != nil {
		t.Fatalf("read recovery schedule: %v", err)
	}
	if got := availableAt.Sub(updatedAt); got != 625*time.Millisecond {
		t.Fatalf("recovery delay = %s, want 625ms", got)
	}
	attempts, err := jobStore.ListJobAttempts(ctx, job.ID)
	if err != nil {
		t.Fatalf("list abandoned attempt: %v", err)
	}
	if len(attempts) != 1 || attempts[0].Failure == nil || attempts[0].Failure.Code() != "lease_expired" {
		t.Fatalf("abandoned attempt = %#v", attempts)
	}
}

func TestDispatcherStoreDeadLettersExpiredFinalAttempt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	store := newDispatcherTestStore(t, pool, testLeaseDuration)
	jobStore := postgres.NewJobStore(pool)
	worker := registerTestWorker(t, ctx, store, 1)
	job := createTestJob(t, ctx, jobStore, "recovery.exhausted", `{}`)
	if _, err := pool.Exec(ctx, `UPDATE jobs SET max_attempts = 1 WHERE id = $1`, job.ID.UUID()); err != nil {
		t.Fatalf("set final attempt: %v", err)
	}
	acquireOneTestJob(t, ctx, store, worker, "recovery.exhausted")
	if _, err := pool.Exec(ctx, `UPDATE jobs SET lease_expires_at = statement_timestamp() - interval '1 second' WHERE id = $1`, job.ID.UUID()); err != nil {
		t.Fatalf("expire final attempt: %v", err)
	}

	recovered, err := store.RecoverExpiredAttempts(ctx, 1, time.Hour)
	if err != nil || recovered != 1 {
		t.Fatalf("recover final attempt = (%d, %v), want (1, nil)", recovered, err)
	}
	stored, err := jobStore.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get dead-lettered job: %v", err)
	}
	if stored.Status != domain.JobStatusDeadLettered || stored.FinishedAt == nil || stored.AttemptCount != 1 {
		t.Fatalf("dead-lettered job = %#v", stored)
	}
	attempts, err := jobStore.ListJobAttempts(ctx, job.ID)
	if err != nil {
		t.Fatalf("list dead-lettered attempts: %v", err)
	}
	if len(attempts) != 1 || attempts[0].Status != domain.AttemptStatusAbandoned || attempts[0].FinishedAt == nil {
		t.Fatalf("dead-lettered attempts = %#v", attempts)
	}
}

func TestDispatcherStoreConcurrentReapersTransitionEachExpiredAttemptOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	store := newDispatcherTestStore(t, pool, testLeaseDuration)
	jobStore := postgres.NewJobStore(pool)
	worker := registerTestWorker(t, ctx, store, 12)
	for i := 0; i < 12; i++ {
		createTestJob(t, ctx, jobStore, "recovery.concurrent", fmt.Sprintf(`{"job":%d}`, i))
	}
	jobs, err := store.AcquireJobs(ctx, worker, 12, []domain.JobType{mustJobType(t, "recovery.concurrent")})
	if err != nil || len(jobs) != 12 {
		t.Fatalf("acquire concurrent recovery jobs = (%d, %v), want (12, nil)", len(jobs), err)
	}
	if _, err := pool.Exec(ctx, `UPDATE jobs SET lease_expires_at = statement_timestamp() - interval '1 second' WHERE status = 'running'`); err != nil {
		t.Fatalf("expire concurrent recovery jobs: %v", err)
	}

	start := make(chan struct{})
	results := make(chan int64, 3)
	errs := make(chan error, 3)
	var group sync.WaitGroup
	for i := 0; i < 3; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			recovered, err := store.RecoverExpiredAttempts(ctx, 5, time.Hour)
			results <- recovered
			errs <- err
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errs)
	var total int64
	for recovered := range results {
		if recovered > 5 {
			t.Errorf("one reaper recovered %d jobs, want at most batch size 5", recovered)
		}
		total += recovered
	}
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent recovery: %v", err)
		}
	}
	if total != 12 {
		t.Fatalf("concurrently recovered jobs = %d, want 12", total)
	}
	var retrying, abandoned, duplicateAttempts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE status = 'retry_wait' AND current_worker_id IS NULL AND lease_expires_at IS NULL`).Scan(&retrying); err != nil {
		t.Fatalf("count recovered jobs: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM job_attempts WHERE status = 'abandoned' AND finished_at IS NOT NULL`).Scan(&abandoned); err != nil {
		t.Fatalf("count abandoned attempts: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM (SELECT job_id FROM job_attempts GROUP BY job_id HAVING count(*) <> 1) duplicates`).Scan(&duplicateAttempts); err != nil {
		t.Fatalf("count duplicate attempts: %v", err)
	}
	if retrying != 12 || abandoned != 12 || duplicateAttempts != 0 {
		t.Fatalf("recovered state = jobs %d, attempts %d, duplicate groups %d; want 12, 12, 0", retrying, abandoned, duplicateAttempts)
	}
}

func TestDispatcherStoreRecoverySkipsLockedExpiredJob(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	store := newDispatcherTestStore(t, pool, testLeaseDuration)
	jobStore := postgres.NewJobStore(pool)
	worker := registerTestWorker(t, ctx, store, 2)
	first := createTestJob(t, ctx, jobStore, "recovery.locked", `{"order":1}`)
	second := createTestJob(t, ctx, jobStore, "recovery.locked", `{"order":2}`)
	if jobs, err := store.AcquireJobs(ctx, worker, 2, []domain.JobType{mustJobType(t, "recovery.locked")}); err != nil || len(jobs) != 2 {
		t.Fatalf("acquire locked recovery jobs = (%d, %v), want (2, nil)", len(jobs), err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE jobs
		SET lease_expires_at = CASE
			WHEN id = $1 THEN statement_timestamp() - interval '2 seconds'
			ELSE statement_timestamp() - interval '1 second'
		END
		WHERE id IN ($1, $2)
	`, first.ID.UUID(), second.ID.UUID()); err != nil {
		t.Fatalf("expire locked recovery jobs: %v", err)
	}
	lock, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin job lock: %v", err)
	}
	defer func() { _ = lock.Rollback(ctx) }()
	if _, err := lock.Exec(ctx, `SELECT id FROM jobs WHERE id = $1 FOR UPDATE`, first.ID.UUID()); err != nil {
		t.Fatalf("lock first expired job: %v", err)
	}

	recovered, err := store.RecoverExpiredAttempts(ctx, 2, time.Hour)
	if err != nil || recovered != 1 {
		t.Fatalf("recover around locked row = (%d, %v), want (1, nil)", recovered, err)
	}
	firstStored, err := jobStore.GetJob(ctx, first.ID)
	if err != nil {
		t.Fatalf("get locked job: %v", err)
	}
	secondStored, err := jobStore.GetJob(ctx, second.ID)
	if err != nil {
		t.Fatalf("get unlocked job: %v", err)
	}
	if firstStored.Status != domain.JobStatusRunning || secondStored.Status != domain.JobStatusRetryWait {
		t.Fatalf("states around lock = (%q, %q), want (running, retry_wait)", firstStored.Status, secondStored.Status)
	}
	if err := lock.Commit(ctx); err != nil {
		t.Fatalf("release job lock: %v", err)
	}
	if recovered, err := store.RecoverExpiredAttempts(ctx, 2, time.Hour); err != nil || recovered != 1 {
		t.Fatalf("recover released row = (%d, %v), want (1, nil)", recovered, err)
	}
}

func TestDispatcherStoreRenewalAndRecoveryRaceHasOneConsistentWinner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	const shortLease = 250 * time.Millisecond
	store := newDispatcherTestStore(t, pool, shortLease)
	jobStore := postgres.NewJobStore(pool)
	worker := registerTestWorker(t, ctx, store, 1)
	job := createTestJob(t, ctx, jobStore, "recovery.race", `{}`)
	attempt := acquireOneTestJob(t, ctx, store, worker, "recovery.race")

	lock, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin race lock: %v", err)
	}
	defer func() { _ = lock.Rollback(ctx) }()
	if _, err := lock.Exec(ctx, `SELECT id FROM jobs WHERE id = $1 FOR UPDATE`, job.ID.UUID()); err != nil {
		t.Fatalf("lock racing job: %v", err)
	}
	heartbeatDone := make(chan []postgres.HeartbeatResult, 1)
	heartbeatErrors := make(chan error, 1)
	go func() {
		results, err := store.Heartbeat(ctx, worker, []postgres.HeartbeatAttempt{{JobID: job.ID, AttemptNumber: attempt.AttemptNumber}})
		heartbeatDone <- results
		heartbeatErrors <- err
	}()
	time.Sleep(300 * time.Millisecond)
	recoveryDone := make(chan int64, 1)
	recoveryErrors := make(chan error, 1)
	go func() {
		recovered, err := store.RecoverExpiredAttempts(ctx, 1, time.Hour)
		recoveryDone <- recovered
		recoveryErrors <- err
	}()
	time.Sleep(25 * time.Millisecond)
	if err := lock.Commit(ctx); err != nil {
		t.Fatalf("release race lock: %v", err)
	}
	results, heartbeatErr := <-heartbeatDone, <-heartbeatErrors
	recovered, recoveryErr := <-recoveryDone, <-recoveryErrors
	if heartbeatErr != nil || recoveryErr != nil {
		t.Fatalf("race errors = heartbeat %v, recovery %v", heartbeatErr, recoveryErr)
	}
	if len(results) != 1 {
		t.Fatalf("heartbeat race results = %#v", results)
	}
	stored, err := jobStore.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get raced job: %v", err)
	}
	if results[0].Valid {
		if recovered != 0 || stored.Status != domain.JobStatusRunning {
			t.Fatalf("renewal winner state = recovered %d, status %q; want 0, running", recovered, stored.Status)
		}
	} else if recovered != 1 || stored.Status != domain.JobStatusRetryWait {
		t.Fatalf("recovery winner state = recovered %d, status %q; want 1, retry_wait", recovered, stored.Status)
	}
}

func TestDispatcherStoreFailureReportAndRecoveryRaceHasOneConsistentWinner(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	const shortLease = 250 * time.Millisecond
	store := newDispatcherTestStore(t, pool, shortLease)
	jobStore := postgres.NewJobStore(pool)
	worker := registerTestWorker(t, ctx, store, 1)
	job := createTestJob(t, ctx, jobStore, "recovery.failure-race", `{}`)
	attempt := acquireOneTestJob(t, ctx, store, worker, "recovery.failure-race")
	outcome := mustFailureOutcome(t, domain.NewRetryableFailureOutcome)

	lock, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin race lock: %v", err)
	}
	defer func() { _ = lock.Rollback(ctx) }()
	if _, err := lock.Exec(ctx, `SELECT id FROM jobs WHERE id = $1 FOR UPDATE`, job.ID.UUID()); err != nil {
		t.Fatalf("lock racing job: %v", err)
	}
	reportDone := make(chan error, 1)
	go func() {
		reportDone <- store.ReportAttempt(ctx, worker, job.ID, attempt.AttemptNumber, outcome)
	}()
	time.Sleep(300 * time.Millisecond)
	recoveryDone := make(chan int64, 1)
	recoveryErrors := make(chan error, 1)
	go func() {
		recovered, err := store.RecoverExpiredAttempts(ctx, 1, time.Hour)
		recoveryDone <- recovered
		recoveryErrors <- err
	}()
	time.Sleep(25 * time.Millisecond)
	if err := lock.Commit(ctx); err != nil {
		t.Fatalf("release race lock: %v", err)
	}
	reportErr := <-reportDone
	recovered, recoveryErr := <-recoveryDone, <-recoveryErrors
	if recoveryErr != nil {
		t.Fatalf("recovery race error: %v", recoveryErr)
	}
	if errors.Is(reportErr, postgres.ErrAttemptReportConflict) && recovered == 0 {
		recovered, recoveryErr = store.RecoverExpiredAttempts(ctx, 1, time.Hour)
		if recoveryErr != nil {
			t.Fatalf("retry recovery after locked-row skip: %v", recoveryErr)
		}
	}

	stored, err := jobStore.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get raced job: %v", err)
	}
	attempts, err := jobStore.ListJobAttempts(ctx, job.ID)
	if err != nil {
		t.Fatalf("list raced attempts: %v", err)
	}
	if stored.Status != domain.JobStatusRetryWait || len(attempts) != 1 {
		t.Fatalf("raced state = report %v, recovered %d, job %#v, attempts %#v", reportErr, recovered, stored, attempts)
	}
	if reportErr == nil {
		if recovered != 0 || attempts[0].Status != domain.AttemptStatusRetryableFailed {
			t.Fatalf("report winner = recovered %d, attempt %q; want 0, retryable_failed", recovered, attempts[0].Status)
		}
		return
	}
	if !errors.Is(reportErr, postgres.ErrAttemptReportConflict) || recovered != 1 || attempts[0].Status != domain.AttemptStatusAbandoned {
		t.Fatalf("recovery winner = report %v, recovered %d, attempt %q", reportErr, recovered, attempts[0].Status)
	}
}

func TestDispatcherStoreRejectsSuccessAfterLeaseExpiryBeforeRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	store := newDispatcherTestStore(t, pool, testLeaseDuration)
	jobStore := postgres.NewJobStore(pool)
	worker := registerTestWorker(t, ctx, store, 1)
	job := createTestJob(t, ctx, jobStore, "recovery.expired-success", `{}`)
	attempt := acquireOneTestJob(t, ctx, store, worker, "recovery.expired-success")
	if _, err := pool.Exec(ctx, `UPDATE jobs SET lease_expires_at = statement_timestamp() - interval '1 second' WHERE id = $1`, job.ID.UUID()); err != nil {
		t.Fatalf("expire success lease: %v", err)
	}

	err := reportSuccess(ctx, store, worker, job.ID, attempt.AttemptNumber, mustResult(t, `{"ok":true}`))
	if !errors.Is(err, postgres.ErrAttemptReportConflict) {
		t.Fatalf("expired success error = %v, want ErrAttemptReportConflict", err)
	}
	stored, err := jobStore.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job after expired success: %v", err)
	}
	attempts, err := jobStore.ListJobAttempts(ctx, job.ID)
	if err != nil {
		t.Fatalf("list attempts after expired success: %v", err)
	}
	if stored.Status != domain.JobStatusRunning || stored.Result != nil || stored.FinishedAt != nil {
		t.Fatalf("job changed after expired success: %#v", stored)
	}
	if len(attempts) != 1 || attempts[0].Status != domain.AttemptStatusRunning || attempts[0].FinishedAt != nil {
		t.Fatalf("attempt changed after expired success: %#v", attempts)
	}
}
