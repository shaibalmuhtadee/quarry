package worker

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
	dispatcherv1 "github.com/shaibalmuhtadee/quarry/internal/rpc/generated/dispatcher/v1"
	"google.golang.org/grpc"
)

type recordingRPCClient struct {
	registration *dispatcherv1.RegisterWorkerRequest
	acquisition  *dispatcherv1.AcquireJobsRequest
	heartbeat    *dispatcherv1.HeartbeatRequest
	report       *dispatcherv1.ReportAttemptRequest
	jobs         []*dispatcherv1.AcquiredJob
	heartbeats   []*dispatcherv1.HeartbeatAttemptResult
}

func (client *recordingRPCClient) RegisterWorker(
	_ context.Context,
	request *dispatcherv1.RegisterWorkerRequest,
	_ ...grpc.CallOption,
) (*dispatcherv1.RegisterWorkerResponse, error) {
	client.registration = request
	return &dispatcherv1.RegisterWorkerResponse{}, nil
}

func (client *recordingRPCClient) AcquireJobs(
	_ context.Context,
	request *dispatcherv1.AcquireJobsRequest,
	_ ...grpc.CallOption,
) (*dispatcherv1.AcquireJobsResponse, error) {
	client.acquisition = request
	return &dispatcherv1.AcquireJobsResponse{Jobs: client.jobs}, nil
}

func (client *recordingRPCClient) Heartbeat(
	_ context.Context,
	request *dispatcherv1.HeartbeatRequest,
	_ ...grpc.CallOption,
) (*dispatcherv1.HeartbeatResponse, error) {
	client.heartbeat = request
	return &dispatcherv1.HeartbeatResponse{Attempts: client.heartbeats}, nil
}

func (client *recordingRPCClient) ReportAttempt(
	_ context.Context,
	request *dispatcherv1.ReportAttemptRequest,
	_ ...grpc.CallOption,
) (*dispatcherv1.ReportAttemptResponse, error) {
	client.report = request
	return &dispatcherv1.ReportAttemptResponse{}, nil
}

func TestGRPCClientPreservesRegistrationAcquisitionAndReportIdentity(t *testing.T) {
	t.Parallel()

	workerID := domain.NewWorkerID()
	jobID := domain.NewJobID()
	attempt, err := domain.NewAttemptNumber(2)
	if err != nil {
		t.Fatal(err)
	}
	jobType, err := domain.ParseJobType("demo.echo")
	if err != nil {
		t.Fatal(err)
	}
	result, err := domain.ParseResult([]byte(`{"ok":true}`))
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now().UTC().Truncate(time.Nanosecond)
	rpc := &recordingRPCClient{
		jobs: []*dispatcherv1.AcquiredJob{{
			JobId:       jobID.String(),
			AttemptNo:   uint32(attempt.Int32()),
			JobType:     jobType.String(),
			PayloadJson: []byte(`{"message":"hello"}`),
			TimeoutMs:   2500,
		}},
		heartbeats: []*dispatcherv1.HeartbeatAttemptResult{{
			JobId:     jobID.String(),
			AttemptNo: uint32(attempt.Int32()),
			State:     dispatcherv1.HeartbeatAttemptState_HEARTBEAT_ATTEMPT_STATE_VALID,
		}},
	}
	client, err := NewGRPCClient(rpc, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	registration := Registration{
		WorkerID:    workerID,
		Hostname:    "worker-host",
		Version:     "test-version",
		Concurrency: 4,
		StartedAt:   startedAt,
	}
	if err := client.Register(context.Background(), registration); err != nil {
		t.Fatal(err)
	}
	jobs, err := client.Acquire(context.Background(), workerID, 3, []domain.JobType{jobType})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != jobID || jobs[0].AttemptNumber != attempt ||
		jobs[0].Type != jobType || string(jobs[0].Payload.JSON()) != `{"message":"hello"}` ||
		jobs[0].Timeout != 2500*time.Millisecond {
		t.Fatalf("acquired jobs = %#v", jobs)
	}
	heartbeats, err := client.Heartbeat(context.Background(), workerID, []HeartbeatAttempt{{
		JobID:         jobID,
		AttemptNumber: attempt,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(heartbeats) != 1 || heartbeats[0].Attempt.JobID != jobID ||
		heartbeats[0].Attempt.AttemptNumber != attempt || !heartbeats[0].Valid {
		t.Fatalf("heartbeat results = %#v", heartbeats)
	}
	if err := client.ReportSuccess(context.Background(), workerID, jobID, attempt, result); err != nil {
		t.Fatal(err)
	}

	if rpc.registration.GetWorkerId() != workerID.String() ||
		rpc.registration.GetHostname() != registration.Hostname ||
		rpc.registration.GetVersion() != registration.Version ||
		rpc.registration.GetConcurrency() != registration.Concurrency ||
		!rpc.registration.GetStartedAt().AsTime().Equal(startedAt) {
		t.Fatalf("registration request = %#v", rpc.registration)
	}
	if rpc.acquisition.GetWorkerId() != workerID.String() ||
		rpc.acquisition.GetAvailableCapacity() != 3 ||
		!slices.Equal(rpc.acquisition.GetSupportedJobTypes(), []string{jobType.String()}) {
		t.Fatalf("acquisition request = %#v", rpc.acquisition)
	}
	if rpc.heartbeat.GetWorkerId() != workerID.String() || len(rpc.heartbeat.GetActiveAttempts()) != 1 ||
		rpc.heartbeat.GetActiveAttempts()[0].GetJobId() != jobID.String() ||
		rpc.heartbeat.GetActiveAttempts()[0].GetAttemptNo() != uint32(attempt.Int32()) {
		t.Fatalf("heartbeat request = %#v", rpc.heartbeat)
	}
	if rpc.report.GetWorkerId() != workerID.String() || rpc.report.GetJobId() != jobID.String() ||
		rpc.report.GetAttemptNo() != uint32(attempt.Int32()) ||
		string(rpc.report.GetSucceeded().GetResultJson()) != `{"ok":true}` {
		t.Fatalf("report request = %#v", rpc.report)
	}
}

func TestParseHeartbeatResultRejectsInvalidResponses(t *testing.T) {
	jobID := domain.NewJobID().String()
	tests := []struct {
		name  string
		value *dispatcherv1.HeartbeatAttemptResult
	}{
		{name: "nil"},
		{name: "invalid job ID", value: &dispatcherv1.HeartbeatAttemptResult{JobId: "bad", AttemptNo: 1, State: dispatcherv1.HeartbeatAttemptState_HEARTBEAT_ATTEMPT_STATE_VALID}},
		{name: "zero attempt", value: &dispatcherv1.HeartbeatAttemptResult{JobId: jobID, State: dispatcherv1.HeartbeatAttemptState_HEARTBEAT_ATTEMPT_STATE_VALID}},
		{name: "attempt overflow", value: &dispatcherv1.HeartbeatAttemptResult{JobId: jobID, AttemptNo: uint32(^uint32(0)), State: dispatcherv1.HeartbeatAttemptState_HEARTBEAT_ATTEMPT_STATE_VALID}},
		{name: "unspecified state", value: &dispatcherv1.HeartbeatAttemptResult{JobId: jobID, AttemptNo: 1}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseHeartbeatResult(test.value); err == nil {
				t.Fatal("parseHeartbeatResult accepted an invalid response")
			}
		})
	}
}
