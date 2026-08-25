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
	"google.golang.org/grpc/credentials/insecure"
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
		dispatcher.NewService(postgres.NewDispatcherStore(pool, 20*time.Second)),
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
		if _, err := jobStore.CreateJob(ctx, submission); err != nil {
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

func dispatcherMigrationDirectory(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate dispatcher integration test")
	}
	return filepath.Join(filepath.Dir(filename), "..", "store", "postgres", "migrations")
}
