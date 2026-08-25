package dispatcher_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/shaibalmuhtadee/quarry/internal/dispatcher"
	"github.com/shaibalmuhtadee/quarry/internal/domain"
	dispatcherv1 "github.com/shaibalmuhtadee/quarry/internal/rpc/generated/dispatcher/v1"
	"github.com/shaibalmuhtadee/quarry/internal/store/postgres"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const dispatcherTestPostgresImage = "postgres:18.6"

func TestConcurrentAcquireJobsThroughGRPCAndPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := startDispatcherTestPostgres(t, ctx)
	jobStore := postgres.NewJobStore(pool)

	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	dispatcherv1.RegisterDispatcherServiceServer(
		server,
		dispatcher.NewService(newIntegrationDispatcherStore(t, pool, 20*time.Second)),
	)
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
		<-serveDone
	})

	connection, err := grpc.NewClient(
		"passthrough:///dispatcher-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
	)
	if err != nil {
		t.Fatalf("create gRPC client connection: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	client := dispatcherv1.NewDispatcherServiceClient(connection)

	jobType := mustJobType(t, "grpc.test")
	const jobCount = 100
	for i := 0; i < jobCount; i++ {
		payload, err := domain.ParsePayload(json.RawMessage(fmt.Sprintf(`{"job":%d}`, i)))
		if err != nil {
			t.Fatalf("parse job %d payload: %v", i, err)
		}
		submission, err := domain.NewJobSubmission(jobType, payload, 3, 30*time.Second)
		if err != nil {
			t.Fatalf("create job %d submission: %v", i, err)
		}
		if _, err := jobStore.SubmitJob(ctx, submission); err != nil {
			t.Fatalf("create job %d: %v", i, err)
		}
	}

	const workerCount = 8
	workers := make([]domain.WorkerID, workerCount)
	for i := range workers {
		workers[i] = domain.NewWorkerID()
		_, err := client.RegisterWorker(ctx, &dispatcherv1.RegisterWorkerRequest{
			WorkerId:    workers[i].String(),
			Hostname:    fmt.Sprintf("worker-%d", i),
			Version:     "test",
			Concurrency: 20,
			StartedAt:   timestamppb.Now(),
		})
		if err != nil {
			t.Fatalf("register worker %d: %v", i, err)
		}
	}

	type workerJobs struct {
		workerID domain.WorkerID
		jobs     []*dispatcherv1.AcquiredJob
	}
	start := make(chan struct{})
	results := make(chan workerJobs, workerCount)
	errorsByWorker := make(chan error, workerCount)
	var group sync.WaitGroup
	for _, workerID := range workers {
		group.Add(1)
		go func(workerID domain.WorkerID) {
			defer group.Done()
			<-start
			response, err := client.AcquireJobs(ctx, &dispatcherv1.AcquireJobsRequest{
				WorkerId:          workerID.String(),
				AvailableCapacity: 20,
				SupportedJobTypes: []string{jobType.String()},
			})
			if err != nil {
				errorsByWorker <- err
				return
			}
			results <- workerJobs{workerID: workerID, jobs: response.GetJobs()}
		}(workerID)
	}
	close(start)
	group.Wait()
	close(results)
	close(errorsByWorker)

	for err := range errorsByWorker {
		t.Errorf("concurrent AcquireJobs RPC: %v", err)
	}
	claimed := make(map[string]domain.WorkerID, jobCount)
	var firstWorker domain.WorkerID
	var firstJob *dispatcherv1.AcquiredJob
	for result := range results {
		for _, job := range result.jobs {
			if _, exists := claimed[job.GetJobId()]; exists {
				t.Fatalf("job %s was returned by more than one AcquireJobs RPC", job.GetJobId())
			}
			claimed[job.GetJobId()] = result.workerID
			if firstJob == nil {
				firstWorker = result.workerID
				firstJob = job
			}
		}
	}
	if len(claimed) != jobCount {
		t.Fatalf("unique jobs claimed through gRPC = %d, want %d", len(claimed), jobCount)
	}

	var runningJobs, attempts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE status = 'running' AND attempt_count = 1`).Scan(&runningJobs); err != nil {
		t.Fatalf("count running jobs: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM job_attempts WHERE status = 'running'`).Scan(&attempts); err != nil {
		t.Fatalf("count running attempts: %v", err)
	}
	if runningJobs != jobCount || attempts != jobCount {
		t.Fatalf("stored claims = %d jobs and %d attempts, want %d each", runningJobs, attempts, jobCount)
	}
	var leaseBefore time.Time
	if err := pool.QueryRow(ctx, `SELECT lease_expires_at FROM jobs WHERE id = $1`, firstJob.GetJobId()).Scan(&leaseBefore); err != nil {
		t.Fatalf("read lease before heartbeat: %v", err)
	}
	heartbeatResponse, err := client.Heartbeat(ctx, &dispatcherv1.HeartbeatRequest{
		WorkerId: firstWorker.String(),
		ActiveAttempts: []*dispatcherv1.HeartbeatAttempt{{
			JobId:     firstJob.GetJobId(),
			AttemptNo: firstJob.GetAttemptNo(),
		}},
	})
	if err != nil {
		t.Fatalf("heartbeat through gRPC: %v", err)
	}
	if len(heartbeatResponse.GetAttempts()) != 1 ||
		heartbeatResponse.GetAttempts()[0].GetState() != dispatcherv1.HeartbeatAttemptState_HEARTBEAT_ATTEMPT_STATE_VALID {
		t.Fatalf("heartbeat response = %#v", heartbeatResponse)
	}
	var leaseAfter time.Time
	if err := pool.QueryRow(ctx, `SELECT lease_expires_at FROM jobs WHERE id = $1`, firstJob.GetJobId()).Scan(&leaseAfter); err != nil {
		t.Fatalf("read lease after heartbeat: %v", err)
	}
	if !leaseAfter.After(leaseBefore) {
		t.Fatalf("renewed lease = %s, want after %s", leaseAfter, leaseBefore)
	}

	_, err = client.ReportAttempt(ctx, &dispatcherv1.ReportAttemptRequest{
		WorkerId:  firstWorker.String(),
		JobId:     firstJob.GetJobId(),
		AttemptNo: firstJob.GetAttemptNo(),
		Outcome: &dispatcherv1.ReportAttemptRequest_Succeeded{
			Succeeded: &dispatcherv1.AttemptSucceeded{ResultJson: []byte(`{"ok":true}`)},
		},
	})
	if err != nil {
		t.Fatalf("report attempt through gRPC: %v", err)
	}
	completedJobID, err := domain.ParseJobID(firstJob.GetJobId())
	if err != nil {
		t.Fatalf("parse completed job ID: %v", err)
	}
	completed, err := jobStore.GetJob(ctx, completedJobID)
	if err != nil {
		t.Fatalf("get completed job: %v", err)
	}
	if completed.Status != domain.JobStatusSucceeded || completed.Result == nil {
		t.Fatalf("completed job state = status %q, result %v", completed.Status, completed.Result)
	}
	var completedResult struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(completed.Result.JSON(), &completedResult); err != nil {
		t.Fatalf("decode completed result: %v", err)
	}
	if !completedResult.OK {
		t.Fatalf("completed result = %s, want ok true", completed.Result.JSON())
	}
}

func TestStaleAttemptReportAfterRecoveryThroughGRPCAndPostgres(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := startDispatcherTestPostgres(t, ctx)
	store := newIntegrationDispatcherStore(t, pool, 20*time.Second)
	jobStore := postgres.NewJobStore(pool)

	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	dispatcherv1.RegisterDispatcherServiceServer(server, dispatcher.NewService(store))
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
		<-serveDone
	})
	connection, err := grpc.NewClient(
		"passthrough:///dispatcher-recovery-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
	)
	if err != nil {
		t.Fatalf("create gRPC client connection: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	client := dispatcherv1.NewDispatcherServiceClient(connection)

	jobType := mustJobType(t, "grpc.recovery")
	payload, err := domain.ParsePayload([]byte(`{"test":"stale-report"}`))
	if err != nil {
		t.Fatalf("parse recovery payload: %v", err)
	}
	submission, err := domain.NewJobSubmission(jobType, payload, 3, 30*time.Second)
	if err != nil {
		t.Fatalf("create recovery submission: %v", err)
	}
	created, err := jobStore.SubmitJob(ctx, submission)
	if err != nil {
		t.Fatalf("create recovery job: %v", err)
	}
	job := created.Job

	workers := []domain.WorkerID{domain.NewWorkerID(), domain.NewWorkerID()}
	for index, workerID := range workers {
		if _, err := client.RegisterWorker(ctx, &dispatcherv1.RegisterWorkerRequest{
			WorkerId:    workerID.String(),
			Hostname:    fmt.Sprintf("recovery-worker-%d", index+1),
			Version:     "test",
			Concurrency: 1,
			StartedAt:   timestamppb.Now(),
		}); err != nil {
			t.Fatalf("register recovery worker %d: %v", index+1, err)
		}
	}

	firstResponse, err := client.AcquireJobs(ctx, &dispatcherv1.AcquireJobsRequest{
		WorkerId:          workers[0].String(),
		AvailableCapacity: 1,
		SupportedJobTypes: []string{jobType.String()},
	})
	if err != nil || len(firstResponse.GetJobs()) != 1 {
		t.Fatalf("acquire attempt 1 = (%d jobs, %v), want (1, nil)", len(firstResponse.GetJobs()), err)
	}
	firstAttempt := firstResponse.GetJobs()[0]
	if _, err := pool.Exec(ctx, `UPDATE jobs SET lease_expires_at = statement_timestamp() - interval '1 second' WHERE id = $1`, job.ID.UUID()); err != nil {
		t.Fatalf("expire attempt 1: %v", err)
	}
	if recovered, err := store.RecoverExpiredAttempts(ctx, 1, time.Hour); err != nil || recovered != 1 {
		t.Fatalf("recover attempt 1 = (%d, %v), want (1, nil)", recovered, err)
	}

	secondResponse, err := client.AcquireJobs(ctx, &dispatcherv1.AcquireJobsRequest{
		WorkerId:          workers[1].String(),
		AvailableCapacity: 1,
		SupportedJobTypes: []string{jobType.String()},
	})
	if err != nil || len(secondResponse.GetJobs()) != 1 {
		t.Fatalf("acquire attempt 2 = (%d jobs, %v), want (1, nil)", len(secondResponse.GetJobs()), err)
	}
	secondAttempt := secondResponse.GetJobs()[0]
	if secondAttempt.GetJobId() != job.ID.String() || secondAttempt.GetAttemptNo() != 2 {
		t.Fatalf("attempt 2 = (%s, %d), want (%s, 2)", secondAttempt.GetJobId(), secondAttempt.GetAttemptNo(), job.ID)
	}

	_, err = client.ReportAttempt(ctx, &dispatcherv1.ReportAttemptRequest{
		WorkerId:  workers[0].String(),
		JobId:     firstAttempt.GetJobId(),
		AttemptNo: firstAttempt.GetAttemptNo(),
		Outcome: &dispatcherv1.ReportAttemptRequest_Succeeded{
			Succeeded: &dispatcherv1.AttemptSucceeded{ResultJson: []byte(`{"stale":true}`)},
		},
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("stale attempt report status = %s, want FailedPrecondition", status.Code(err))
	}
	current, err := jobStore.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job after stale report: %v", err)
	}
	if current.Status != domain.JobStatusRunning || current.AttemptCount != 2 || current.Result != nil {
		t.Fatalf("job after stale report = %#v, want running attempt 2 without result", current)
	}

	_, err = client.ReportAttempt(ctx, &dispatcherv1.ReportAttemptRequest{
		WorkerId:  workers[1].String(),
		JobId:     secondAttempt.GetJobId(),
		AttemptNo: secondAttempt.GetAttemptNo(),
		Outcome: &dispatcherv1.ReportAttemptRequest_Succeeded{
			Succeeded: &dispatcherv1.AttemptSucceeded{ResultJson: []byte(`{"attempt":2}`)},
		},
	})
	if err != nil {
		t.Fatalf("report attempt 2: %v", err)
	}
	attempts, err := jobStore.ListJobAttempts(ctx, job.ID)
	if err != nil {
		t.Fatalf("list recovered attempts: %v", err)
	}
	if len(attempts) != 2 || attempts[0].Status != domain.AttemptStatusAbandoned ||
		attempts[1].Status != domain.AttemptStatusSucceeded || attempts[0].WorkerID != workers[0] ||
		attempts[1].WorkerID != workers[1] {
		t.Fatalf("recovered attempts = %#v", attempts)
	}
}

func startDispatcherTestPostgres(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()

	container, err := postgrescontainer.Run(
		ctx,
		dispatcherTestPostgresImage,
		postgrescontainer.WithDatabase("quarry"),
		postgrescontainer.WithUsername("quarry"),
		postgrescontainer.WithPassword("quarry"),
		postgrescontainer.BasicWaitStrategies(),
	)
	if err != nil {
		t.Fatalf("start PostgreSQL container: %v", err)
	}
	testcontainers.CleanupContainer(t, container)

	connectionString, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("get PostgreSQL connection string: %v", err)
	}
	database, err := sql.Open("pgx", connectionString)
	if err != nil {
		t.Fatalf("open migration connection: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set Goose dialect: %v", err)
	}
	if err := goose.UpContext(ctx, database, dispatcherMigrationDirectory(t)); err != nil {
		t.Fatalf("apply migrations: %v", err)
	}

	pool, err := postgres.NewPool(ctx, connectionString)
	if err != nil {
		t.Fatalf("create PostgreSQL pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func newIntegrationDispatcherStore(
	t *testing.T,
	pool *pgxpool.Pool,
	leaseDuration time.Duration,
) *postgres.DispatcherStore {
	t.Helper()
	retryPolicy, err := domain.NewRetryPolicy(time.Second, time.Minute, func(int64) int64 { return 0 })
	if err != nil {
		t.Fatalf("create integration retry policy: %v", err)
	}
	return postgres.NewDispatcherStore(pool, leaseDuration, retryPolicy)
}

func dispatcherMigrationDirectory(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate dispatcher integration test")
	}
	return filepath.Join(filepath.Dir(filename), "..", "store", "postgres", "migrations")
}
