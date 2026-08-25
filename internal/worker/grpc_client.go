package worker

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
	dispatcherv1 "github.com/shaibalmuhtadee/quarry/internal/rpc/generated/dispatcher/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type GRPCClient struct {
	client  dispatcherv1.DispatcherServiceClient
	timeout time.Duration
}

func NewGRPCClient(client dispatcherv1.DispatcherServiceClient, timeout time.Duration) (*GRPCClient, error) {
	if client == nil || timeout <= 0 {
		return nil, fmt.Errorf("%w: gRPC client and positive timeout are required", ErrInvalidConfiguration)
	}
	return &GRPCClient{client: client, timeout: timeout}, nil
}

func (client *GRPCClient) Register(ctx context.Context, registration Registration) error {
	callCtx, cancel := context.WithTimeout(ctx, client.timeout)
	defer cancel()
	_, err := client.client.RegisterWorker(callCtx, &dispatcherv1.RegisterWorkerRequest{
		WorkerId:    registration.WorkerID.String(),
		Hostname:    registration.Hostname,
		Version:     registration.Version,
		Concurrency: registration.Concurrency,
		StartedAt:   timestamppb.New(registration.StartedAt),
	})
	return err
}

func (client *GRPCClient) Acquire(
	ctx context.Context,
	workerID domain.WorkerID,
	capacity uint32,
	supportedTypes []domain.JobType,
) ([]Job, error) {
	rawTypes := make([]string, len(supportedTypes))
	for i, jobType := range supportedTypes {
		rawTypes[i] = jobType.String()
	}

	callCtx, cancel := context.WithTimeout(ctx, client.timeout)
	defer cancel()
	response, err := client.client.AcquireJobs(callCtx, &dispatcherv1.AcquireJobsRequest{
		WorkerId:          workerID.String(),
		AvailableCapacity: capacity,
		SupportedJobTypes: rawTypes,
	})
	if err != nil {
		return nil, err
	}

	jobs := make([]Job, len(response.GetJobs()))
	for i, acquired := range response.GetJobs() {
		job, err := parseAcquiredJob(acquired)
		if err != nil {
			return nil, fmt.Errorf("parse acquired job %d: %w", i, err)
		}
		jobs[i] = job
	}
	return jobs, nil
}

func (client *GRPCClient) Heartbeat(
	ctx context.Context,
	workerID domain.WorkerID,
	attempts []HeartbeatAttempt,
) ([]HeartbeatResult, error) {
	requestAttempts := make([]*dispatcherv1.HeartbeatAttempt, len(attempts))
	for i, attempt := range attempts {
		requestAttempts[i] = &dispatcherv1.HeartbeatAttempt{
			JobId:     attempt.JobID.String(),
			AttemptNo: uint32(attempt.AttemptNumber.Int32()),
		}
	}

	callCtx, cancel := context.WithTimeout(ctx, client.timeout)
	defer cancel()
	response, err := client.client.Heartbeat(callCtx, &dispatcherv1.HeartbeatRequest{
		WorkerId:       workerID.String(),
		ActiveAttempts: requestAttempts,
	})
	if err != nil {
		return nil, err
	}
	if len(response.GetAttempts()) != len(attempts) {
		return nil, fmt.Errorf("heartbeat returned %d results for %d attempts", len(response.GetAttempts()), len(attempts))
	}

	results := make([]HeartbeatResult, len(response.GetAttempts()))
	for i, value := range response.GetAttempts() {
		result, err := parseHeartbeatResult(value)
		if err != nil {
			return nil, fmt.Errorf("parse heartbeat result %d: %w", i, err)
		}
		if result.Attempt != attempts[i] {
			return nil, fmt.Errorf("heartbeat result %d identity does not match its request", i)
		}
		results[i] = result
	}
	return results, nil
}

func (client *GRPCClient) ReportAttempt(
	ctx context.Context,
	workerID domain.WorkerID,
	jobID domain.JobID,
	attemptNumber domain.AttemptNumber,
	outcome domain.AttemptOutcome,
) error {
	request := &dispatcherv1.ReportAttemptRequest{
		WorkerId:  workerID.String(),
		JobId:     jobID.String(),
		AttemptNo: uint32(attemptNumber.Int32()),
	}
	switch outcome.Kind() {
	case domain.AttemptOutcomeKindSucceeded:
		result, ok := outcome.Result()
		if !ok {
			return domain.ErrInvalidAttemptOutcome
		}
		request.Outcome = &dispatcherv1.ReportAttemptRequest_Succeeded{
			Succeeded: &dispatcherv1.AttemptSucceeded{ResultJson: result.JSON()},
		}
	case domain.AttemptOutcomeKindRetryableFailure:
		failure, ok := outcome.Failure()
		if !ok {
			return domain.ErrInvalidAttemptOutcome
		}
		request.Outcome = &dispatcherv1.ReportAttemptRequest_RetryableFailure{
			RetryableFailure: mapAttemptFailure(failure),
		}
	case domain.AttemptOutcomeKindPermanentFailure:
		failure, ok := outcome.Failure()
		if !ok {
			return domain.ErrInvalidAttemptOutcome
		}
		request.Outcome = &dispatcherv1.ReportAttemptRequest_PermanentFailure{
			PermanentFailure: mapAttemptFailure(failure),
		}
	default:
		return fmt.Errorf("%w: worker cannot report outcome %q in this milestone slice", domain.ErrInvalidAttemptOutcome, outcome.Kind())
	}

	callCtx, cancel := context.WithTimeout(ctx, client.timeout)
	defer cancel()
	_, err := client.client.ReportAttempt(callCtx, request)
	return err
}

func mapAttemptFailure(failure domain.AttemptFailure) *dispatcherv1.AttemptFailure {
	return &dispatcherv1.AttemptFailure{
		ErrorCode:    failure.Code(),
		ErrorMessage: failure.Message(),
	}
}

func parseAcquiredJob(acquired *dispatcherv1.AcquiredJob) (Job, error) {
	if acquired == nil {
		return Job{}, errors.New("job is required")
	}
	jobID, err := domain.ParseJobID(acquired.GetJobId())
	if err != nil {
		return Job{}, err
	}
	if acquired.GetAttemptNo() > math.MaxInt32 {
		return Job{}, domain.ErrInvalidAttemptNumber
	}
	attemptNumber, err := domain.NewAttemptNumber(int32(acquired.GetAttemptNo()))
	if err != nil {
		return Job{}, err
	}
	jobType, err := domain.ParseJobType(acquired.GetJobType())
	if err != nil {
		return Job{}, err
	}
	payload, err := domain.ParsePayload(acquired.GetPayloadJson())
	if err != nil {
		return Job{}, err
	}
	if acquired.GetTimeoutMs() <= 0 || acquired.GetTimeoutMs() > math.MaxInt64/int64(time.Millisecond) {
		return Job{}, domain.ErrInvalidTimeout
	}
	return Job{
		ID:            jobID,
		AttemptNumber: attemptNumber,
		Type:          jobType,
		Payload:       payload,
		Timeout:       time.Duration(acquired.GetTimeoutMs()) * time.Millisecond,
	}, nil
}

func parseHeartbeatResult(value *dispatcherv1.HeartbeatAttemptResult) (HeartbeatResult, error) {
	if value == nil {
		return HeartbeatResult{}, errors.New("heartbeat result is required")
	}
	jobID, err := domain.ParseJobID(value.GetJobId())
	if err != nil {
		return HeartbeatResult{}, err
	}
	if value.GetAttemptNo() > math.MaxInt32 {
		return HeartbeatResult{}, domain.ErrInvalidAttemptNumber
	}
	attemptNumber, err := domain.NewAttemptNumber(int32(value.GetAttemptNo()))
	if err != nil {
		return HeartbeatResult{}, err
	}

	var valid bool
	switch value.GetState() {
	case dispatcherv1.HeartbeatAttemptState_HEARTBEAT_ATTEMPT_STATE_VALID:
		valid = true
	case dispatcherv1.HeartbeatAttemptState_HEARTBEAT_ATTEMPT_STATE_STALE:
		valid = false
	default:
		return HeartbeatResult{}, errors.New("heartbeat result state must be valid or stale")
	}

	return HeartbeatResult{
		Attempt: HeartbeatAttempt{
			JobID:         jobID,
			AttemptNumber: attemptNumber,
		},
		Valid: valid,
	}, nil
}
