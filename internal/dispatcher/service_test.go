package dispatcher_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/dispatcher"
	"github.com/shaibalmuhtadee/quarry/internal/domain"
	dispatcherv1 "github.com/shaibalmuhtadee/quarry/internal/rpc/generated/dispatcher/v1"
	"github.com/shaibalmuhtadee/quarry/internal/store/postgres"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakeStore struct {
	registerWorker func(context.Context, postgres.WorkerRegistration) error
	acquireJobs    func(context.Context, domain.WorkerID, int32, []domain.JobType) ([]postgres.AcquiredJob, error)
	reportSuccess  func(context.Context, domain.WorkerID, domain.JobID, domain.AttemptNumber, domain.Result) error
}

func (store *fakeStore) RegisterWorker(ctx context.Context, registration postgres.WorkerRegistration) error {
	if store.registerWorker == nil {
		return nil
	}
	return store.registerWorker(ctx, registration)
}

func (store *fakeStore) AcquireJobs(
	ctx context.Context,
	workerID domain.WorkerID,
	capacity int32,
	types []domain.JobType,
) ([]postgres.AcquiredJob, error) {
	if store.acquireJobs == nil {
		return []postgres.AcquiredJob{}, nil
	}
	return store.acquireJobs(ctx, workerID, capacity, types)
}

func (store *fakeStore) ReportSuccess(
	ctx context.Context,
	workerID domain.WorkerID,
	jobID domain.JobID,
	attemptNumber domain.AttemptNumber,
	result domain.Result,
) error {
	if store.reportSuccess == nil {
		return nil
	}
	return store.reportSuccess(ctx, workerID, jobID, attemptNumber, result)
}

func TestRegisterWorkerParsesRequest(t *testing.T) {
	workerID := domain.NewWorkerID()
	startedAt := time.Date(2026, time.August, 24, 12, 0, 0, 123456000, time.UTC)
	var captured postgres.WorkerRegistration
	service := dispatcher.NewService(&fakeStore{
		registerWorker: func(_ context.Context, registration postgres.WorkerRegistration) error {
			captured = registration
			return nil
		},
	})

	response, err := service.RegisterWorker(context.Background(), &dispatcherv1.RegisterWorkerRequest{
		WorkerId:    workerID.String(),
		Hostname:    "worker-a",
		Version:     "v0.1.0",
		Concurrency: 4,
		StartedAt:   timestamppb.New(startedAt),
	})
	if err != nil {
		t.Fatalf("RegisterWorker: %v", err)
	}
	if response == nil {
		t.Fatal("RegisterWorker returned a nil response")
	}
	if captured.ID != workerID || captured.Hostname != "worker-a" || captured.Version != "v0.1.0" ||
		captured.Concurrency != 4 || !captured.StartedAt.Equal(startedAt) {
		t.Fatalf("captured registration = %#v", captured)
	}
}

func TestRegisterWorkerRejectsInvalidRequests(t *testing.T) {
	valid := func() *dispatcherv1.RegisterWorkerRequest {
		return &dispatcherv1.RegisterWorkerRequest{
			WorkerId:    domain.NewWorkerID().String(),
			Hostname:    "worker-a",
			Version:     "test",
			Concurrency: 1,
			StartedAt:   timestamppb.Now(),
		}
	}
	tests := []struct {
		name    string
		request func() *dispatcherv1.RegisterWorkerRequest
	}{
		{name: "nil request", request: func() *dispatcherv1.RegisterWorkerRequest { return nil }},
		{name: "invalid worker ID", request: func() *dispatcherv1.RegisterWorkerRequest { value := valid(); value.WorkerId = "bad"; return value }},
		{name: "zero worker ID", request: func() *dispatcherv1.RegisterWorkerRequest {
			value := valid()
			value.WorkerId = "00000000-0000-0000-0000-000000000000"
			return value
		}},
		{name: "blank hostname", request: func() *dispatcherv1.RegisterWorkerRequest { value := valid(); value.Hostname = "  "; return value }},
		{name: "blank version", request: func() *dispatcherv1.RegisterWorkerRequest { value := valid(); value.Version = ""; return value }},
		{name: "zero concurrency", request: func() *dispatcherv1.RegisterWorkerRequest { value := valid(); value.Concurrency = 0; return value }},
		{name: "concurrency overflow", request: func() *dispatcherv1.RegisterWorkerRequest {
			value := valid()
			value.Concurrency = math.MaxInt32 + 1
			return value
		}},
		{name: "missing start time", request: func() *dispatcherv1.RegisterWorkerRequest { value := valid(); value.StartedAt = nil; return value }},
		{name: "invalid start time", request: func() *dispatcherv1.RegisterWorkerRequest {
			value := valid()
			value.StartedAt = &timestamppb.Timestamp{Seconds: 253402300800}
			return value
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			storeCalls := 0
			service := dispatcher.NewService(&fakeStore{
				registerWorker: func(context.Context, postgres.WorkerRegistration) error {
					storeCalls++
					return nil
				},
			})
			_, err := service.RegisterWorker(context.Background(), test.request())
			assertStatusCode(t, err, codes.InvalidArgument)
			if storeCalls != 0 {
				t.Fatalf("store calls = %d, want 0", storeCalls)
			}
		})
	}
}

func TestAcquireJobsParsesRequestAndMapsJobs(t *testing.T) {
	workerID := domain.NewWorkerID()
	jobID := domain.NewJobID()
	attemptNumber, err := domain.NewAttemptNumber(2)
	if err != nil {
		t.Fatalf("create attempt number: %v", err)
	}
	jobType := mustJobType(t, "demo.echo")
	payload := mustPayload(t, `{"message":"hello"}`)
	var capturedWorkerID domain.WorkerID
	var capturedCapacity int32
	var capturedTypes []domain.JobType
	service := dispatcher.NewService(&fakeStore{
		acquireJobs: func(
			_ context.Context,
			gotWorkerID domain.WorkerID,
			capacity int32,
			types []domain.JobType,
		) ([]postgres.AcquiredJob, error) {
			capturedWorkerID = gotWorkerID
			capturedCapacity = capacity
			capturedTypes = append([]domain.JobType(nil), types...)
			return []postgres.AcquiredJob{
				{
					ID:            jobID,
					AttemptNumber: attemptNumber,
					Type:          jobType,
					Payload:       payload,
					Timeout:       30 * time.Second,
				},
			}, nil
		},
	})

	response, err := service.AcquireJobs(context.Background(), &dispatcherv1.AcquireJobsRequest{
		WorkerId:          workerID.String(),
		AvailableCapacity: 3,
		SupportedJobTypes: []string{"demo.echo", "demo.payload_size"},
	})
	if err != nil {
		t.Fatalf("AcquireJobs: %v", err)
	}
	if capturedWorkerID != workerID || capturedCapacity != 3 {
		t.Fatalf("captured acquisition = worker %s, capacity %d", capturedWorkerID, capturedCapacity)
	}
	if got := []string{capturedTypes[0].String(), capturedTypes[1].String()}; !reflect.DeepEqual(got, []string{"demo.echo", "demo.payload_size"}) {
		t.Fatalf("captured job types = %v", got)
	}
	if len(response.GetJobs()) != 1 {
		t.Fatalf("response jobs = %d, want 1", len(response.GetJobs()))
	}
	job := response.GetJobs()[0]
	if job.GetJobId() != jobID.String() || job.GetAttemptNo() != 2 || job.GetJobType() != "demo.echo" ||
		string(job.GetPayloadJson()) != `{"message":"hello"}` || job.GetTimeoutMs() != 30000 {
		t.Fatalf("acquired response job = %#v", job)
	}
}

func TestAcquireJobsRejectsInvalidRequests(t *testing.T) {
	workerID := domain.NewWorkerID().String()
	tests := []struct {
		name    string
		request *dispatcherv1.AcquireJobsRequest
	}{
		{name: "nil request"},
		{name: "invalid worker ID", request: &dispatcherv1.AcquireJobsRequest{WorkerId: "bad"}},
		{name: "capacity overflow", request: &dispatcherv1.AcquireJobsRequest{WorkerId: workerID, AvailableCapacity: math.MaxInt32 + 1}},
		{name: "invalid supported type", request: &dispatcherv1.AcquireJobsRequest{WorkerId: workerID, SupportedJobTypes: []string{"Bad Type"}}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := dispatcher.NewService(&fakeStore{}).AcquireJobs(context.Background(), test.request)
			assertStatusCode(t, err, codes.InvalidArgument)
		})
	}
}

func TestReportAttemptParsesSuccessfulOutcome(t *testing.T) {
	workerID := domain.NewWorkerID()
	jobID := domain.NewJobID()
	var capturedResult domain.Result
	service := dispatcher.NewService(&fakeStore{
		reportSuccess: func(
			_ context.Context,
			gotWorkerID domain.WorkerID,
			gotJobID domain.JobID,
			attemptNumber domain.AttemptNumber,
			result domain.Result,
		) error {
			if gotWorkerID != workerID || gotJobID != jobID || attemptNumber.Int32() != 1 {
				t.Fatalf("reported identity = worker %s, job %s, attempt %d", gotWorkerID, gotJobID, attemptNumber.Int32())
			}
			capturedResult = result
			return nil
		},
	})

	response, err := service.ReportAttempt(context.Background(), &dispatcherv1.ReportAttemptRequest{
		WorkerId:  workerID.String(),
		JobId:     jobID.String(),
		AttemptNo: 1,
		Outcome: &dispatcherv1.ReportAttemptRequest_Succeeded{
			Succeeded: &dispatcherv1.AttemptSucceeded{ResultJson: []byte(`{"ok":true}`)},
		},
	})
	if err != nil {
		t.Fatalf("ReportAttempt: %v", err)
	}
	if response == nil || string(capturedResult.JSON()) != `{"ok":true}` {
		t.Fatalf("captured result = %s", capturedResult.JSON())
	}
}

func TestReportAttemptRejectsInvalidRequests(t *testing.T) {
	valid := func() *dispatcherv1.ReportAttemptRequest {
		return &dispatcherv1.ReportAttemptRequest{
			WorkerId:  domain.NewWorkerID().String(),
			JobId:     domain.NewJobID().String(),
			AttemptNo: 1,
			Outcome: &dispatcherv1.ReportAttemptRequest_Succeeded{
				Succeeded: &dispatcherv1.AttemptSucceeded{ResultJson: []byte(`null`)},
			},
		}
	}
	tests := []struct {
		name    string
		request func() *dispatcherv1.ReportAttemptRequest
	}{
		{name: "nil request", request: func() *dispatcherv1.ReportAttemptRequest { return nil }},
		{name: "invalid worker ID", request: func() *dispatcherv1.ReportAttemptRequest { value := valid(); value.WorkerId = "bad"; return value }},
		{name: "invalid job ID", request: func() *dispatcherv1.ReportAttemptRequest { value := valid(); value.JobId = "bad"; return value }},
		{name: "zero attempt", request: func() *dispatcherv1.ReportAttemptRequest { value := valid(); value.AttemptNo = 0; return value }},
		{name: "attempt overflow", request: func() *dispatcherv1.ReportAttemptRequest {
			value := valid()
			value.AttemptNo = math.MaxInt32 + 1
			return value
		}},
		{name: "missing outcome", request: func() *dispatcherv1.ReportAttemptRequest { value := valid(); value.Outcome = nil; return value }},
		{name: "missing result", request: func() *dispatcherv1.ReportAttemptRequest {
			value := valid()
			value.GetSucceeded().ResultJson = nil
			return value
		}},
		{name: "malformed result", request: func() *dispatcherv1.ReportAttemptRequest {
			value := valid()
			value.GetSucceeded().ResultJson = []byte(`{"ok":`)
			return value
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := dispatcher.NewService(&fakeStore{}).ReportAttempt(context.Background(), test.request())
			assertStatusCode(t, err, codes.InvalidArgument)
		})
	}
}

func TestServiceMapsStoreErrorsToStableStatusCodes(t *testing.T) {
	workerID := domain.NewWorkerID()
	jobID := domain.NewJobID()
	startedAt := timestamppb.Now()
	tests := []struct {
		name     string
		service  *dispatcher.Service
		call     func(*dispatcher.Service) error
		wantCode codes.Code
	}{
		{
			name: "registration conflict",
			service: dispatcher.NewService(&fakeStore{
				registerWorker: func(context.Context, postgres.WorkerRegistration) error {
					return postgres.ErrWorkerRegistrationConflict
				},
			}),
			call: func(service *dispatcher.Service) error {
				_, err := service.RegisterWorker(context.Background(), &dispatcherv1.RegisterWorkerRequest{
					WorkerId: workerID.String(), Hostname: "worker", Version: "test", Concurrency: 1, StartedAt: startedAt,
				})
				return err
			},
			wantCode: codes.AlreadyExists,
		},
		{
			name: "worker not registered",
			service: dispatcher.NewService(&fakeStore{
				acquireJobs: func(context.Context, domain.WorkerID, int32, []domain.JobType) ([]postgres.AcquiredJob, error) {
					return nil, postgres.ErrWorkerNotRegistered
				},
			}),
			call: func(service *dispatcher.Service) error {
				_, err := service.AcquireJobs(context.Background(), &dispatcherv1.AcquireJobsRequest{WorkerId: workerID.String()})
				return err
			},
			wantCode: codes.FailedPrecondition,
		},
		{
			name: "attempt conflict",
			service: dispatcher.NewService(&fakeStore{
				reportSuccess: func(context.Context, domain.WorkerID, domain.JobID, domain.AttemptNumber, domain.Result) error {
					return postgres.ErrAttemptReportConflict
				},
			}),
			call: func(service *dispatcher.Service) error {
				_, err := service.ReportAttempt(context.Background(), validReportRequest(workerID, jobID))
				return err
			},
			wantCode: codes.FailedPrecondition,
		},
		{
			name: "deadline",
			service: dispatcher.NewService(&fakeStore{
				acquireJobs: func(context.Context, domain.WorkerID, int32, []domain.JobType) ([]postgres.AcquiredJob, error) {
					return nil, context.DeadlineExceeded
				},
			}),
			call: func(service *dispatcher.Service) error {
				_, err := service.AcquireJobs(context.Background(), &dispatcherv1.AcquireJobsRequest{WorkerId: workerID.String()})
				return err
			},
			wantCode: codes.DeadlineExceeded,
		},
		{
			name: "internal",
			service: dispatcher.NewService(&fakeStore{
				registerWorker: func(context.Context, postgres.WorkerRegistration) error {
					return errors.New("database password secret")
				},
			}),
			call: func(service *dispatcher.Service) error {
				_, err := service.RegisterWorker(context.Background(), &dispatcherv1.RegisterWorkerRequest{
					WorkerId: workerID.String(), Hostname: "worker", Version: "test", Concurrency: 1, StartedAt: startedAt,
				})
				return err
			},
			wantCode: codes.Internal,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.call(test.service)
			assertStatusCode(t, err, test.wantCode)
			if test.wantCode == codes.Internal && status.Convert(err).Message() != "internal dispatcher error" {
				t.Fatalf("internal status message = %q", status.Convert(err).Message())
			}
		})
	}
}

func validReportRequest(workerID domain.WorkerID, jobID domain.JobID) *dispatcherv1.ReportAttemptRequest {
	return &dispatcherv1.ReportAttemptRequest{
		WorkerId:  workerID.String(),
		JobId:     jobID.String(),
		AttemptNo: 1,
		Outcome: &dispatcherv1.ReportAttemptRequest_Succeeded{
			Succeeded: &dispatcherv1.AttemptSucceeded{ResultJson: []byte(`null`)},
		},
	}
}

func mustJobType(t *testing.T, value string) domain.JobType {
	t.Helper()
	jobType, err := domain.ParseJobType(value)
	if err != nil {
		t.Fatalf("parse job type: %v", err)
	}
	return jobType
}

func mustPayload(t *testing.T, value string) domain.Payload {
	t.Helper()
	payload, err := domain.ParsePayload(json.RawMessage(value))
	if err != nil {
		t.Fatalf("parse payload: %v", err)
	}
	return payload
}

func assertStatusCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if got := status.Code(err); got != want {
		t.Fatalf("status code = %s, want %s: %v", got, want, err)
	}
}
