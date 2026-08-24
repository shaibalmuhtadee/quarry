package dispatcher

import (
	"context"
	"errors"
	"math"
	"strings"

	"github.com/shaibalmuhtadee/quarry/internal/domain"
	dispatcherv1 "github.com/shaibalmuhtadee/quarry/internal/rpc/generated/dispatcher/v1"
	"github.com/shaibalmuhtadee/quarry/internal/store/postgres"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type store interface {
	RegisterWorker(context.Context, postgres.WorkerRegistration) error
	AcquireJobs(context.Context, domain.WorkerID, int32, []domain.JobType) ([]postgres.AcquiredJob, error)
	ReportSuccess(context.Context, domain.WorkerID, domain.JobID, domain.AttemptNumber, domain.Result) error
}

type Service struct {
	dispatcherv1.UnimplementedDispatcherServiceServer
	store store
}

func NewService(store store) *Service {
	return &Service{store: store}
}

func (service *Service) RegisterWorker(
	ctx context.Context,
	request *dispatcherv1.RegisterWorkerRequest,
) (*dispatcherv1.RegisterWorkerResponse, error) {
	if request == nil {
		return nil, invalidArgument("request is required")
	}
	workerID, err := domain.ParseWorkerID(request.GetWorkerId())
	if err != nil {
		return nil, invalidArgument("worker_id must be a non-zero UUID")
	}
	if strings.TrimSpace(request.GetHostname()) == "" {
		return nil, invalidArgument("hostname is required")
	}
	if strings.TrimSpace(request.GetVersion()) == "" {
		return nil, invalidArgument("version is required")
	}
	if request.GetConcurrency() == 0 || request.GetConcurrency() > math.MaxInt32 {
		return nil, invalidArgument("concurrency must be between 1 and 2147483647")
	}
	if request.GetStartedAt() == nil {
		return nil, invalidArgument("started_at is required")
	}
	if err := request.GetStartedAt().CheckValid(); err != nil {
		return nil, invalidArgument("started_at must be a valid protobuf timestamp")
	}

	err = service.store.RegisterWorker(ctx, postgres.WorkerRegistration{
		ID:          workerID,
		Hostname:    request.GetHostname(),
		Version:     request.GetVersion(),
		Concurrency: int32(request.GetConcurrency()),
		StartedAt:   request.GetStartedAt().AsTime(),
	})
	if errors.Is(err, postgres.ErrWorkerRegistrationConflict) {
		return nil, status.Error(codes.AlreadyExists, "worker_id is already registered with different process metadata")
	}
	if err != nil {
		return nil, internalError(err)
	}

	return &dispatcherv1.RegisterWorkerResponse{}, nil
}

func (service *Service) AcquireJobs(
	ctx context.Context,
	request *dispatcherv1.AcquireJobsRequest,
) (*dispatcherv1.AcquireJobsResponse, error) {
	if request == nil {
		return nil, invalidArgument("request is required")
	}
	workerID, err := domain.ParseWorkerID(request.GetWorkerId())
	if err != nil {
		return nil, invalidArgument("worker_id must be a non-zero UUID")
	}
	if request.GetAvailableCapacity() > math.MaxInt32 {
		return nil, invalidArgument("available_capacity must not exceed 2147483647")
	}

	supportedJobTypes := make([]domain.JobType, len(request.GetSupportedJobTypes()))
	for i, value := range request.GetSupportedJobTypes() {
		jobType, err := domain.ParseJobType(value)
		if err != nil {
			return nil, invalidArgument("supported_job_types must contain valid job types")
		}
		supportedJobTypes[i] = jobType
	}

	jobs, err := service.store.AcquireJobs(
		ctx,
		workerID,
		int32(request.GetAvailableCapacity()),
		supportedJobTypes,
	)
	if errors.Is(err, postgres.ErrWorkerNotRegistered) {
		return nil, status.Error(codes.FailedPrecondition, "worker must register before acquiring jobs")
	}
	if err != nil {
		return nil, internalError(err)
	}

	response := &dispatcherv1.AcquireJobsResponse{
		Jobs: make([]*dispatcherv1.AcquiredJob, 0, len(jobs)),
	}
	for _, job := range jobs {
		response.Jobs = append(response.Jobs, &dispatcherv1.AcquiredJob{
			JobId:       job.ID.String(),
			AttemptNo:   uint32(job.AttemptNumber.Int32()),
			JobType:     job.Type.String(),
			PayloadJson: job.Payload.JSON(),
			TimeoutMs:   job.Timeout.Milliseconds(),
		})
	}

	return response, nil
}

func (service *Service) ReportAttempt(
	ctx context.Context,
	request *dispatcherv1.ReportAttemptRequest,
) (*dispatcherv1.ReportAttemptResponse, error) {
	if request == nil {
		return nil, invalidArgument("request is required")
	}
	workerID, err := domain.ParseWorkerID(request.GetWorkerId())
	if err != nil {
		return nil, invalidArgument("worker_id must be a non-zero UUID")
	}
	jobID, err := domain.ParseJobID(request.GetJobId())
	if err != nil {
		return nil, invalidArgument("job_id must be a UUID")
	}
	if request.GetAttemptNo() == 0 || request.GetAttemptNo() > math.MaxInt32 {
		return nil, invalidArgument("attempt_no must be between 1 and 2147483647")
	}
	attemptNumber, err := domain.NewAttemptNumber(int32(request.GetAttemptNo()))
	if err != nil {
		return nil, invalidArgument("attempt_no must be positive")
	}
	succeeded := request.GetSucceeded()
	if succeeded == nil {
		return nil, invalidArgument("a succeeded outcome is required")
	}
	result, err := domain.ParseResult(succeeded.GetResultJson())
	if err != nil {
		return nil, invalidArgument("succeeded.result_json must contain one JSON value")
	}

	err = service.store.ReportSuccess(ctx, workerID, jobID, attemptNumber, result)
	if errors.Is(err, postgres.ErrAttemptReportConflict) {
		return nil, status.Error(codes.FailedPrecondition, "attempt report does not match the current stored attempt")
	}
	if err != nil {
		return nil, internalError(err)
	}

	return &dispatcherv1.ReportAttemptResponse{}, nil
}

func invalidArgument(message string) error {
	return status.Error(codes.InvalidArgument, message)
}

func internalError(err error) error {
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, "request canceled")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "request deadline exceeded")
	}
	return status.Error(codes.Internal, "internal dispatcher error")
}
