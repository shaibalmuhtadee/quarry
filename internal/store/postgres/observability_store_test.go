package postgres_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shaibalmuhtadee/quarry/internal/store/postgres"
)

func TestQueueSnapshotStoreReadsAuthoritativeQueueHealth(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	pool := newDispatcherTestPool(t, ctx)
	store := postgres.NewQueueSnapshotStore(pool)

	snapshot, err := store.QueueSnapshot(ctx)
	if err != nil {
		t.Fatalf("read empty queue snapshot: %v", err)
	}
	assertQueueSnapshotCounts(t, snapshot, 0, 0, 0, 0)
	if snapshot.OldestEligibleAge != 0 {
		t.Fatalf("empty oldest eligible age = %s, want 0", snapshot.OldestEligibleAge)
	}

	jobStore := postgres.NewJobStore(pool)
	oldQueued := createTestJob(t, ctx, jobStore, "demo.echo", `{"queued":"old"}`)
	recentQueued := createTestJob(t, ctx, jobStore, "demo.echo", `{"queued":"recent"}`)
	retry := createTestJob(t, ctx, jobStore, "demo.echo", `{"retry":true}`)
	now := time.Now().UTC()
	setJobTimes(t, ctx, pool, oldQueued.ID, now.Add(-10*time.Second), now.Add(-time.Minute))
	setJobTimes(t, ctx, pool, recentQueued.ID, now.Add(-2*time.Second), now.Add(-time.Minute))
	if _, err := pool.Exec(ctx, `
		UPDATE jobs
		SET status = 'retry_wait', available_at = $2
		WHERE id = $1
	`, retry.ID.UUID(), now.Add(time.Hour)); err != nil {
		t.Fatalf("schedule future retry: %v", err)
	}

	snapshot, err = store.QueueSnapshot(ctx)
	if err != nil {
		t.Fatalf("read queue snapshot with future retry: %v", err)
	}
	assertQueueSnapshotCounts(t, snapshot, 2, 1, 0, 0)
	assertDurationBetween(t, snapshot.OldestEligibleAge, 9*time.Second, 20*time.Second)

	if _, err := pool.Exec(ctx, `UPDATE jobs SET available_at = $2 WHERE id = $1`, retry.ID.UUID(), now.Add(-30*time.Second)); err != nil {
		t.Fatalf("make retry eligible: %v", err)
	}
	snapshot, err = store.QueueSnapshot(ctx)
	if err != nil {
		t.Fatalf("read queue snapshot with eligible retry: %v", err)
	}
	assertQueueSnapshotCounts(t, snapshot, 2, 1, 0, 0)
	assertDurationBetween(t, snapshot.OldestEligibleAge, 29*time.Second, 40*time.Second)

	if _, err := pool.Exec(ctx, `UPDATE jobs SET available_at = $2 WHERE id = $1`, retry.ID.UUID(), now.Add(time.Hour)); err != nil {
		t.Fatalf("defer retry before claim: %v", err)
	}
	dispatcherStore := newDispatcherTestStore(t, pool, testLeaseDuration)
	firstWorker := registerTestWorker(t, ctx, dispatcherStore, 1)
	registerTestWorker(t, ctx, dispatcherStore, 1)
	acquireOneTestJob(t, ctx, dispatcherStore, firstWorker, "demo.echo")
	snapshot, err = store.QueueSnapshot(ctx)
	if err != nil {
		t.Fatalf("read active queue snapshot: %v", err)
	}
	assertQueueSnapshotCounts(t, snapshot, 1, 1, 1, 2)

	if _, err := pool.Exec(ctx, `UPDATE workers SET state = 'lost' WHERE id = $1`, firstWorker.UUID()); err != nil {
		t.Fatalf("mark worker lost: %v", err)
	}
	snapshot, err = store.QueueSnapshot(ctx)
	if err != nil {
		t.Fatalf("read queue snapshot after worker loss: %v", err)
	}
	assertQueueSnapshotCounts(t, snapshot, 1, 1, 1, 1)

	assertConcurrentQueueSnapshotsNonnegative(t, ctx, store, pool, retry.ID.UUID())
}

func assertConcurrentQueueSnapshotsNonnegative(
	t *testing.T,
	ctx context.Context,
	store *postgres.QueueSnapshotStore,
	pool *pgxpool.Pool,
	retryID uuid.UUID,
) {
	t.Helper()

	errors := make(chan error, 9)
	var wait sync.WaitGroup
	wait.Add(9)
	go func() {
		defer wait.Done()
		for index := 0; index < 40; index++ {
			offset := "-1 second"
			if index%2 == 0 {
				offset = "1 hour"
			}
			if _, err := pool.Exec(ctx, `
				UPDATE jobs
				SET available_at = statement_timestamp() + $2::interval
				WHERE id = $1
			`, retryID, offset); err != nil {
				errors <- err
				return
			}
		}
	}()
	for range 8 {
		go func() {
			defer wait.Done()
			for range 25 {
				snapshot, err := store.QueueSnapshot(ctx)
				if err != nil {
					errors <- err
					return
				}
				if snapshot.Queued < 0 || snapshot.RetryWait < 0 || snapshot.OldestEligibleAge < 0 ||
					snapshot.ActiveJobs < 0 || snapshot.ActiveWorkers < 0 {
					errors <- fmt.Errorf("negative queue snapshot: %#v", snapshot)
					return
				}
			}
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
}

func assertQueueSnapshotCounts(
	t *testing.T,
	snapshot postgres.QueueSnapshot,
	queued int64,
	retryWait int64,
	activeJobs int64,
	activeWorkers int64,
) {
	t.Helper()
	if snapshot.Queued != queued || snapshot.RetryWait != retryWait ||
		snapshot.ActiveJobs != activeJobs || snapshot.ActiveWorkers != activeWorkers {
		t.Fatalf(
			"queue snapshot counts = (%d, %d, %d, %d), want (%d, %d, %d, %d)",
			snapshot.Queued,
			snapshot.RetryWait,
			snapshot.ActiveJobs,
			snapshot.ActiveWorkers,
			queued,
			retryWait,
			activeJobs,
			activeWorkers,
		)
	}
}

func assertDurationBetween(t *testing.T, got, minimum, maximum time.Duration) {
	t.Helper()
	if got < minimum || got > maximum {
		t.Fatalf("duration = %s, want between %s and %s", got, minimum, maximum)
	}
}
