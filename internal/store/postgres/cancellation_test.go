package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shaibalmuhtadee/quarry/internal/domain"
	"github.com/shaibalmuhtadee/quarry/internal/store/postgres"
)

func TestJobStoreRequestsCancellationByJobState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	jobStore := postgres.NewJobStore(pool)
	dispatcherStore := newDispatcherTestStore(t, pool, testLeaseDuration)
	worker := registerTestWorker(t, ctx, dispatcherStore, 1)

	queued := createTestJob(t, ctx, jobStore, "cancel.queued", `{}`)
	cancelledQueued, err := jobStore.RequestCancellation(ctx, queued.ID)
	if err != nil {
		t.Fatalf("cancel queued job: %v", err)
	}
	assertImmediatelyCancelled(t, cancelledQueued)
	repeatedQueued, err := jobStore.RequestCancellation(ctx, queued.ID)
	if err != nil {
		t.Fatalf("repeat queued cancellation: %v", err)
	}
	if !repeatedQueued.CancelRequestedAt.Equal(*cancelledQueued.CancelRequestedAt) ||
		!repeatedQueued.FinishedAt.Equal(*cancelledQueued.FinishedAt) ||
		!repeatedQueued.UpdatedAt.Equal(cancelledQueued.UpdatedAt) {
		t.Fatalf("repeated cancellation changed timestamps: first %#v, repeated %#v", cancelledQueued, repeatedQueued)
	}

	retryWait := createTestJob(t, ctx, jobStore, "cancel.retry", `{}`)
	if _, err := pool.Exec(ctx, `UPDATE jobs SET status = 'retry_wait' WHERE id = $1`, retryWait.ID.UUID()); err != nil {
		t.Fatalf("put job in retry wait: %v", err)
	}
	cancelledRetry, err := jobStore.RequestCancellation(ctx, retryWait.ID)
	if err != nil {
		t.Fatalf("cancel retry-wait job: %v", err)
	}
	assertImmediatelyCancelled(t, cancelledRetry)

	running := createTestJob(t, ctx, jobStore, "cancel.running", `{}`)
	acquireOneTestJob(t, ctx, dispatcherStore, worker, "cancel.running")
	var assignedBefore uuid.UUID
	var leaseBefore time.Time
	if err := pool.QueryRow(ctx, `SELECT current_worker_id, lease_expires_at FROM jobs WHERE id = $1`, running.ID.UUID()).Scan(&assignedBefore, &leaseBefore); err != nil {
		t.Fatalf("read running lease before cancellation: %v", err)
	}
	requestedRunning, err := jobStore.RequestCancellation(ctx, running.ID)
	if err != nil {
		t.Fatalf("request running cancellation: %v", err)
	}
	if requestedRunning.Status != domain.JobStatusRunning || requestedRunning.CancelRequestedAt == nil || requestedRunning.FinishedAt != nil {
		t.Fatalf("running cancellation = %#v", requestedRunning)
	}
	var assignedAfter uuid.UUID
	var leaseAfter time.Time
	if err := pool.QueryRow(ctx, `SELECT current_worker_id, lease_expires_at FROM jobs WHERE id = $1`, running.ID.UUID()).Scan(&assignedAfter, &leaseAfter); err != nil {
		t.Fatalf("read running lease after cancellation: %v", err)
	}
	if assignedAfter != assignedBefore || !leaseAfter.Equal(leaseBefore) {
		t.Fatalf("running cancellation changed lease: worker %s to %s, lease %s to %s", assignedBefore, assignedAfter, leaseBefore, leaseAfter)
	}

	for _, status := range []domain.JobStatus{domain.JobStatusSucceeded, domain.JobStatusDeadLettered} {
		job := createTestJob(t, ctx, jobStore, "cancel."+string(status), `{}`)
		if _, err := pool.Exec(ctx, `
			UPDATE jobs
			SET status = $2, finished_at = statement_timestamp(), updated_at = statement_timestamp()
			WHERE id = $1
		`, job.ID.UUID(), status); err != nil {
			t.Fatalf("set terminal status %q: %v", status, err)
		}
		before, err := jobStore.GetJob(ctx, job.ID)
		if err != nil {
			t.Fatalf("read terminal job before cancellation: %v", err)
		}
		if _, err := jobStore.RequestCancellation(ctx, job.ID); !errors.Is(err, domain.ErrJobCancellationConflict) {
			t.Fatalf("cancel %q job error = %v, want ErrJobCancellationConflict", status, err)
		}
		after, err := jobStore.GetJob(ctx, job.ID)
		if err != nil {
			t.Fatalf("read terminal job after cancellation: %v", err)
		}
		if after.Status != before.Status || after.CancelRequestedAt != nil || !after.UpdatedAt.Equal(before.UpdatedAt) {
			t.Fatalf("conflicting cancellation changed job: before %#v, after %#v", before, after)
		}
	}

	if _, err := jobStore.RequestCancellation(ctx, domain.NewJobID()); !errors.Is(err, domain.ErrJobNotFound) {
		t.Fatalf("cancel missing job error = %v, want ErrJobNotFound", err)
	}
}

func TestJobCancellationAndClaimRaceEndsInAConsistentState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	jobStore := postgres.NewJobStore(pool)
	dispatcherStore := newDispatcherTestStore(t, pool, testLeaseDuration)
	worker := registerTestWorker(t, ctx, dispatcherStore, 1)
	job := createTestJob(t, ctx, jobStore, "cancel.claim-race", `{}`)

	start := make(chan struct{})
	cancelled := make(chan domain.Job, 1)
	cancelErrors := make(chan error, 1)
	claimed := make(chan []postgres.AcquiredJob, 1)
	claimErrors := make(chan error, 1)
	go func() {
		<-start
		result, err := jobStore.RequestCancellation(ctx, job.ID)
		cancelled <- result
		cancelErrors <- err
	}()
	go func() {
		<-start
		result, err := dispatcherStore.AcquireJobs(ctx, worker, 1, []domain.JobType{mustJobType(t, "cancel.claim-race")})
		claimed <- result
		claimErrors <- err
	}()
	close(start)

	cancelledJob, cancelErr := <-cancelled, <-cancelErrors
	claimedJobs, claimErr := <-claimed, <-claimErrors
	if cancelErr != nil || claimErr != nil {
		t.Fatalf("race errors = cancellation %v, claim %v", cancelErr, claimErr)
	}
	stored, err := jobStore.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("read raced job: %v", err)
	}
	if len(claimedJobs) == 0 {
		if stored.Status != domain.JobStatusCancelled || cancelledJob.Status != domain.JobStatusCancelled || stored.AttemptCount != 0 {
			t.Fatalf("cancellation winner state = stored %#v, returned %#v", stored, cancelledJob)
		}
		return
	}
	if len(claimedJobs) != 1 || claimedJobs[0].ID != job.ID {
		t.Fatalf("claim race returned %#v", claimedJobs)
	}
	if stored.Status != domain.JobStatusRunning || cancelledJob.Status != domain.JobStatusRunning ||
		stored.CancelRequestedAt == nil || stored.AttemptCount != 1 {
		t.Fatalf("claim winner state = stored %#v, returned %#v", stored, cancelledJob)
	}
}

func TestDispatcherStoreRecoversCancellationRequestedExpiredAttempt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	jobStore := postgres.NewJobStore(pool)
	dispatcherStore := newDispatcherTestStore(t, pool, testLeaseDuration)
	worker := registerTestWorker(t, ctx, dispatcherStore, 1)
	job := createTestJob(t, ctx, jobStore, "cancel.recovery", `{}`)
	acquireOneTestJob(t, ctx, dispatcherStore, worker, "cancel.recovery")
	requested, err := jobStore.RequestCancellation(ctx, job.ID)
	if err != nil {
		t.Fatalf("request running cancellation: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE jobs SET lease_expires_at = statement_timestamp() - interval '1 second' WHERE id = $1`, job.ID.UUID()); err != nil {
		t.Fatalf("expire cancelled attempt lease: %v", err)
	}

	recovered, err := dispatcherStore.RecoverExpiredAttempts(ctx, 1, time.Hour)
	if err != nil || recovered != 1 {
		t.Fatalf("recover cancelled attempt = (%d, %v), want (1, nil)", recovered, err)
	}
	stored, err := jobStore.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("read recovered cancelled job: %v", err)
	}
	if stored.Status != domain.JobStatusCancelled || stored.CancelRequestedAt == nil || stored.FinishedAt == nil ||
		!stored.CancelRequestedAt.Equal(*requested.CancelRequestedAt) {
		t.Fatalf("recovered cancelled job = %#v", stored)
	}
	attempts, err := jobStore.ListJobAttempts(ctx, job.ID)
	if err != nil {
		t.Fatalf("list cancelled attempt: %v", err)
	}
	if len(attempts) != 1 || attempts[0].Status != domain.AttemptStatusCancelled || attempts[0].Failure == nil ||
		attempts[0].Failure.Code() != "cancellation_requested" || attempts[0].Failure.Message() != "job cancellation was requested" ||
		attempts[0].FinishedAt == nil {
		t.Fatalf("recovered cancelled attempt = %#v", attempts)
	}
	if jobs, err := dispatcherStore.AcquireJobs(ctx, worker, 1, []domain.JobType{mustJobType(t, "cancel.recovery")}); err != nil || len(jobs) != 0 {
		t.Fatalf("claim recovered cancelled job = (%#v, %v), want none", jobs, err)
	}
}

func TestDispatcherStoreConvertsReportedFailuresAfterCancellationRequest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	jobStore := postgres.NewJobStore(pool)
	dispatcherStore := newDispatcherTestStore(t, pool, testLeaseDuration)
	worker := registerTestWorker(t, ctx, dispatcherStore, 5)

	tests := []struct {
		name        string
		jobType     string
		constructor func(domain.AttemptFailure) (domain.AttemptOutcome, error)
	}{
		{name: "retryable", jobType: "cancel.report-retryable", constructor: domain.NewRetryableFailureOutcome},
		{name: "permanent", jobType: "cancel.report-permanent", constructor: domain.NewPermanentFailureOutcome},
		{name: "cancelled", jobType: "cancel.report-cancelled", constructor: domain.NewCancelledOutcome},
		{name: "timed out", jobType: "cancel.report-timeout", constructor: domain.NewTimedOutOutcome},
		{name: "panicked", jobType: "cancel.report-panic", constructor: domain.NewPanickedOutcome},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			jobType := test.jobType
			job := createTestJob(t, ctx, jobStore, jobType, `{}`)
			attempt := acquireOneTestJob(t, ctx, dispatcherStore, worker, jobType)
			if _, err := jobStore.RequestCancellation(ctx, job.ID); err != nil {
				t.Fatalf("request cancellation: %v", err)
			}
			outcome := mustFailureOutcome(t, test.constructor)
			if err := dispatcherStore.ReportAttempt(ctx, worker, job.ID, attempt.AttemptNumber, outcome); err != nil {
				t.Fatalf("report after cancellation: %v", err)
			}
			if err := dispatcherStore.ReportAttempt(ctx, worker, job.ID, attempt.AttemptNumber, outcome); err != nil {
				t.Fatalf("repeat report after lost acknowledgement: %v", err)
			}

			stored, err := jobStore.GetJob(ctx, job.ID)
			if err != nil {
				t.Fatalf("read cancelled job: %v", err)
			}
			attempts, err := jobStore.ListJobAttempts(ctx, job.ID)
			if err != nil {
				t.Fatalf("read cancelled attempt: %v", err)
			}
			if stored.Status != domain.JobStatusCancelled || stored.FinishedAt == nil || len(attempts) != 1 ||
				attempts[0].Status != domain.AttemptStatusCancelled || attempts[0].Failure == nil ||
				attempts[0].Failure.Code() != "cancellation_requested" || attempts[0].Failure.Message() != "job cancellation was requested" {
				t.Fatalf("cancelled report state = job %#v, attempts %#v", stored, attempts)
			}
			if claimed, err := dispatcherStore.AcquireJobs(ctx, worker, 1, []domain.JobType{mustJobType(t, jobType)}); err != nil || len(claimed) != 0 {
				t.Fatalf("claim after cancelled report = (%#v, %v), want none", claimed, err)
			}
		})
	}
}

func TestDispatcherStoreSuccessAndCancellationRaceAllowsSuccessToWin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	jobStore := postgres.NewJobStore(pool)
	dispatcherStore := newDispatcherTestStore(t, pool, testLeaseDuration)
	worker := registerTestWorker(t, ctx, dispatcherStore, 1)
	job := createTestJob(t, ctx, jobStore, "cancel.success-race", `{}`)
	attempt := acquireOneTestJob(t, ctx, dispatcherStore, worker, "cancel.success-race")

	start := make(chan struct{})
	reportErrors := make(chan error, 1)
	cancelErrors := make(chan error, 1)
	go func() {
		<-start
		reportErrors <- reportSuccess(ctx, dispatcherStore, worker, job.ID, attempt.AttemptNumber, mustResult(t, `{"ok":true}`))
	}()
	go func() {
		<-start
		_, err := jobStore.RequestCancellation(ctx, job.ID)
		cancelErrors <- err
	}()
	close(start)

	if err := <-reportErrors; err != nil {
		t.Fatalf("success race report: %v", err)
	}
	if err := <-cancelErrors; err != nil && !errors.Is(err, domain.ErrJobCancellationConflict) {
		t.Fatalf("success race cancellation: %v", err)
	}
	stored, err := jobStore.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("read success race job: %v", err)
	}
	if stored.Status != domain.JobStatusSucceeded || stored.Result == nil || stored.FinishedAt == nil {
		t.Fatalf("success race job = %#v", stored)
	}
}

func TestDispatcherStoreTimeoutAndCancellationRaceNeverRetries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	jobStore := postgres.NewJobStore(pool)
	dispatcherStore := newDispatcherTestStore(t, pool, testLeaseDuration)
	worker := registerTestWorker(t, ctx, dispatcherStore, 1)
	job := createTestJob(t, ctx, jobStore, "cancel.timeout-race", `{}`)
	attempt := acquireOneTestJob(t, ctx, dispatcherStore, worker, "cancel.timeout-race")

	start := make(chan struct{})
	reportErrors := make(chan error, 1)
	cancelErrors := make(chan error, 1)
	go func() {
		<-start
		reportErrors <- dispatcherStore.ReportAttempt(
			ctx, worker, job.ID, attempt.AttemptNumber, mustFailureOutcome(t, domain.NewTimedOutOutcome),
		)
	}()
	go func() {
		<-start
		_, err := jobStore.RequestCancellation(ctx, job.ID)
		cancelErrors <- err
	}()
	close(start)

	if err := <-reportErrors; err != nil {
		t.Fatalf("timeout race report: %v", err)
	}
	if err := <-cancelErrors; err != nil {
		t.Fatalf("timeout race cancellation: %v", err)
	}
	stored, err := jobStore.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("read timeout race job: %v", err)
	}
	attempts, err := jobStore.ListJobAttempts(ctx, job.ID)
	if err != nil {
		t.Fatalf("read timeout race attempt: %v", err)
	}
	if stored.Status != domain.JobStatusCancelled || stored.FinishedAt == nil || len(attempts) != 1 ||
		(attempts[0].Status != domain.AttemptStatusTimedOut && attempts[0].Status != domain.AttemptStatusCancelled) {
		t.Fatalf("timeout race state = job %#v, attempts %#v", stored, attempts)
	}
	if claimed, err := dispatcherStore.AcquireJobs(ctx, worker, 1, []domain.JobType{mustJobType(t, "cancel.timeout-race")}); err != nil || len(claimed) != 0 {
		t.Fatalf("claim after timeout race = (%#v, %v), want none", claimed, err)
	}
}

func TestDispatcherStoreSuccessAfterCancellationRequestWins(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	jobStore := postgres.NewJobStore(pool)
	dispatcherStore := newDispatcherTestStore(t, pool, testLeaseDuration)
	worker := registerTestWorker(t, ctx, dispatcherStore, 1)
	job := createTestJob(t, ctx, jobStore, "cancel.success-after-request", `{}`)
	attempt := acquireOneTestJob(t, ctx, dispatcherStore, worker, "cancel.success-after-request")
	requested, err := jobStore.RequestCancellation(ctx, job.ID)
	if err != nil {
		t.Fatalf("request cancellation: %v", err)
	}
	if err := reportSuccess(ctx, dispatcherStore, worker, job.ID, attempt.AttemptNumber, mustResult(t, `{"ok":true}`)); err != nil {
		t.Fatalf("report success after cancellation request: %v", err)
	}
	stored, err := jobStore.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("read successful job: %v", err)
	}
	if stored.Status != domain.JobStatusSucceeded || stored.Result == nil || stored.FinishedAt == nil ||
		stored.CancelRequestedAt == nil || !stored.CancelRequestedAt.Equal(*requested.CancelRequestedAt) {
		t.Fatalf("success after cancellation request = %#v", stored)
	}
}

func TestDispatcherStoreCancellationAfterTimeoutPreventsRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	jobStore := postgres.NewJobStore(pool)
	dispatcherStore := newDispatcherTestStore(t, pool, testLeaseDuration)
	worker := registerTestWorker(t, ctx, dispatcherStore, 1)
	job := createTestJob(t, ctx, jobStore, "cancel.after-timeout", `{}`)
	attempt := acquireOneTestJob(t, ctx, dispatcherStore, worker, "cancel.after-timeout")
	if err := dispatcherStore.ReportAttempt(
		ctx, worker, job.ID, attempt.AttemptNumber, mustFailureOutcome(t, domain.NewTimedOutOutcome),
	); err != nil {
		t.Fatalf("report timeout: %v", err)
	}
	if _, err := jobStore.RequestCancellation(ctx, job.ID); err != nil {
		t.Fatalf("cancel retry-wait job: %v", err)
	}
	stored, err := jobStore.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("read cancelled timeout job: %v", err)
	}
	attempts, err := jobStore.ListJobAttempts(ctx, job.ID)
	if err != nil {
		t.Fatalf("read timed-out attempt: %v", err)
	}
	if stored.Status != domain.JobStatusCancelled || len(attempts) != 1 || attempts[0].Status != domain.AttemptStatusTimedOut {
		t.Fatalf("timeout then cancellation = job %#v, attempts %#v", stored, attempts)
	}
	if claimed, err := dispatcherStore.AcquireJobs(ctx, worker, 1, []domain.JobType{mustJobType(t, "cancel.after-timeout")}); err != nil || len(claimed) != 0 {
		t.Fatalf("claim after timeout cancellation = (%#v, %v), want none", claimed, err)
	}
}

func TestDispatcherStoreCancelledOutcomeNeverRetries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	jobStore := postgres.NewJobStore(pool)
	dispatcherStore := newDispatcherTestStore(t, pool, testLeaseDuration)
	worker := registerTestWorker(t, ctx, dispatcherStore, 1)
	job := createTestJob(t, ctx, jobStore, "cancel.outcome", `{}`)
	attempt := acquireOneTestJob(t, ctx, dispatcherStore, worker, "cancel.outcome")
	if err := dispatcherStore.ReportAttempt(
		ctx, worker, job.ID, attempt.AttemptNumber, mustFailureOutcome(t, domain.NewCancelledOutcome),
	); err != nil {
		t.Fatalf("report cancelled outcome: %v", err)
	}
	stored, err := jobStore.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("read cancelled outcome job: %v", err)
	}
	if stored.Status != domain.JobStatusCancelled || stored.FinishedAt == nil {
		t.Fatalf("cancelled outcome job = %#v", stored)
	}
	if claimed, err := dispatcherStore.AcquireJobs(ctx, worker, 1, []domain.JobType{mustJobType(t, "cancel.outcome")}); err != nil || len(claimed) != 0 {
		t.Fatalf("claim after cancelled outcome = (%#v, %v), want none", claimed, err)
	}
}

func assertImmediatelyCancelled(t *testing.T, job domain.Job) {
	t.Helper()

	if job.Status != domain.JobStatusCancelled || job.CancelRequestedAt == nil || job.FinishedAt == nil {
		t.Fatalf("immediately cancelled job = %#v", job)
	}
	if !job.CancelRequestedAt.Equal(*job.FinishedAt) || !job.UpdatedAt.Equal(*job.FinishedAt) {
		t.Fatalf("cancellation timestamps differ: request %s, finish %s, update %s", job.CancelRequestedAt, job.FinishedAt, job.UpdatedAt)
	}
}
