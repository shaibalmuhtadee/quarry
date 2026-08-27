package loadgen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestRunnerCompletesJobsWithinBothBounds(t *testing.T) {
	client := newBoundedClient(2 * time.Millisecond)
	runner := newTestRunner(t, client, Config{
		RunID:               "bounded",
		MeasurementDuration: 60 * time.Millisecond,
		DrainTimeout:        100 * time.Millisecond,
		PollInterval:        time.Millisecond,
		MaxOutstanding:      4,
		MaxHTTPConcurrency:  2,
	})

	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("run load generator: %v", err)
	}
	if len(result.Samples) < 4 {
		t.Fatalf("sample count = %d, want at least 4", len(result.Samples))
	}
	for _, sample := range result.Samples {
		terminal, ok := sample.(TerminalJobSample)
		if !ok {
			t.Fatalf("sample type = %T, want terminal", sample)
		}
		if terminal.Status != JobStatusSucceeded || len(terminal.Attempts) != 1 {
			t.Fatalf("terminal sample = %#v", terminal)
		}
	}
	if got := client.maximumOutstanding(); got > 4 {
		t.Fatalf("maximum outstanding jobs = %d, want at most 4", got)
	}
	if got := client.maximumRequests(); got > 2 {
		t.Fatalf("maximum concurrent requests = %d, want at most 2", got)
	}
}

func TestRunnerRetriesAmbiguousSubmissionWithOneIdempotencyKey(t *testing.T) {
	client := &retrySubmissionClient{}
	runner := newTestRunner(t, client, Config{
		RunID:               "retry",
		MeasurementDuration: 10 * time.Millisecond,
		DrainTimeout:        100 * time.Millisecond,
		PollInterval:        time.Millisecond,
		MaxOutstanding:      1,
		MaxHTTPConcurrency:  1,
	})

	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("run load generator: %v", err)
	}
	if len(result.Samples) == 0 {
		t.Fatal("load generator returned no samples")
	}
	terminal, ok := result.Samples[0].(TerminalJobSample)
	if !ok {
		t.Fatalf("first sample type = %T, want terminal", result.Samples[0])
	}
	if len(terminal.Errors) != 1 || terminal.Errors[0].Operation != OperationSubmit ||
		!terminal.Errors[0].Retryable || !terminal.Errors[0].Ambiguous {
		t.Fatalf("submission errors = %#v", terminal.Errors)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.keys) < 2 || client.keys[0] != client.keys[1] {
		t.Fatalf("idempotency keys = %q", client.keys)
	}
}

func TestRunnerPreservesTerminalFailure(t *testing.T) {
	client := terminalFailureClient{now: time.Now().UTC()}
	runner := newTestRunner(t, client, Config{
		RunID:               "terminal-failure",
		MeasurementDuration: 5 * time.Millisecond,
		DrainTimeout:        50 * time.Millisecond,
		PollInterval:        time.Millisecond,
		MaxOutstanding:      1,
		MaxHTTPConcurrency:  1,
	})

	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("run load generator: %v", err)
	}
	terminal, ok := result.Samples[0].(TerminalJobSample)
	if !ok || terminal.Status != JobStatusDeadLettered || len(terminal.Attempts) != 1 {
		t.Fatalf("terminal failure sample = %#v", result.Samples[0])
	}
	if terminal.Attempts[0].ErrorCode == nil || *terminal.Attempts[0].ErrorCode != "invalid_input" {
		t.Fatalf("terminal attempt = %#v", terminal.Attempts[0])
	}
}

func TestRunnerPreservesRetryablePollingErrors(t *testing.T) {
	client := &retryPollClient{createdAt: time.Now().UTC()}
	runner := newTestRunner(t, client, Config{
		RunID:               "poll-retry",
		MeasurementDuration: 5 * time.Millisecond,
		DrainTimeout:        50 * time.Millisecond,
		PollInterval:        time.Millisecond,
		MaxOutstanding:      1,
		MaxHTTPConcurrency:  1,
	})

	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("run load generator: %v", err)
	}
	terminal, ok := result.Samples[0].(TerminalJobSample)
	if !ok || len(terminal.Errors) != 2 {
		t.Fatalf("terminal polling errors = %#v", result.Samples[0])
	}
	if terminal.Errors[0].Operation != OperationPoll || terminal.Errors[1].Operation != OperationAttempts ||
		!terminal.Errors[0].Retryable || !terminal.Errors[1].Retryable {
		t.Fatalf("terminal polling errors = %#v", terminal.Errors)
	}
}

func TestRunnerRecordsIncompleteDrain(t *testing.T) {
	client := &neverTerminalClient{createdAt: time.Now().UTC()}
	runner := newTestRunner(t, client, Config{
		RunID:               "incomplete",
		MeasurementDuration: 5 * time.Millisecond,
		DrainTimeout:        10 * time.Millisecond,
		PollInterval:        time.Millisecond,
		MaxOutstanding:      1,
		MaxHTTPConcurrency:  1,
	})

	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("run load generator: %v", err)
	}
	if len(result.Samples) != 1 {
		t.Fatalf("sample count = %d, want 1", len(result.Samples))
	}
	incomplete, ok := result.Samples[0].(IncompleteJobSample)
	if !ok {
		t.Fatalf("sample type = %T, want incomplete", result.Samples[0])
	}
	if incomplete.LastStatus != JobStatusRunning || incomplete.JobID == "" || len(incomplete.Errors) == 0 {
		t.Fatalf("incomplete sample = %#v", incomplete)
	}
}

func TestRunnerStopsSlotAfterAmbiguousMalformedSubmission(t *testing.T) {
	client := &submissionErrorClient{err: NewClientError(errors.New("malformed response"), false, true)}
	runner := newTestRunner(t, client, Config{
		RunID:               "malformed",
		MeasurementDuration: 20 * time.Millisecond,
		DrainTimeout:        20 * time.Millisecond,
		PollInterval:        time.Millisecond,
		MaxOutstanding:      1,
		MaxHTTPConcurrency:  1,
	})

	result, err := runner.Run(context.Background())
	if err != nil {
		t.Fatalf("run load generator: %v", err)
	}
	if len(result.Samples) != 1 {
		t.Fatalf("sample count = %d, want 1", len(result.Samples))
	}
	failure, ok := result.Samples[0].(SubmissionFailureSample)
	if !ok || !failure.MayHaveCommitted {
		t.Fatalf("submission sample = %#v", result.Samples[0])
	}
	if client.calls != 1 {
		t.Fatalf("submission calls = %d, want 1", client.calls)
	}
}

func TestPhaseAttribution(t *testing.T) {
	boundary := time.Date(2026, time.August, 27, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name      string
		startedAt time.Time
		want      Phase
	}{
		{name: "warmup", startedAt: boundary.Add(-time.Nanosecond), want: PhaseWarmup},
		{name: "measurement boundary", startedAt: boundary, want: PhaseMeasurement},
		{name: "measurement", startedAt: boundary.Add(time.Nanosecond), want: PhaseMeasurement},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := phaseAt(test.startedAt, boundary); got != test.want {
				t.Fatalf("phase = %q, want %q", got, test.want)
			}
		})
	}
}

func newTestRunner(t *testing.T, client Client, cfg Config) *Runner {
	t.Helper()
	runner, err := NewRunner(client, cfg, func(sequence uint64) Submission {
		return Submission{
			JobType:     "demo.echo",
			Payload:     json.RawMessage(fmt.Sprintf(`{"sequence":%d}`, sequence)),
			MaxAttempts: 3,
			Timeout:     time.Second,
		}
	})
	if err != nil {
		t.Fatalf("create runner: %v", err)
	}
	return runner
}

type boundedClient struct {
	mu             sync.Mutex
	delay          time.Duration
	nextID         int
	jobs           map[string]time.Time
	requests       int
	maxRequests    int
	outstanding    int
	maxOutstanding int
}

func newBoundedClient(delay time.Duration) *boundedClient {
	return &boundedClient{delay: delay, jobs: make(map[string]time.Time)}
}

func (client *boundedClient) SubmitJob(ctx context.Context, _ SubmissionRequest) (SubmittedJob, error) {
	if err := client.enter(ctx); err != nil {
		return SubmittedJob{}, err
	}
	defer client.leave()
	client.mu.Lock()
	client.nextID++
	id := fmt.Sprintf("job-%d", client.nextID)
	createdAt := time.Now().UTC()
	client.jobs[id] = createdAt
	client.outstanding++
	client.maxOutstanding = max(client.maxOutstanding, client.outstanding)
	client.mu.Unlock()
	return SubmittedJob{ID: id, Status: JobStatusQueued, CreatedAt: createdAt}, nil
}

func (client *boundedClient) GetJob(ctx context.Context, id string) (Job, error) {
	if err := client.enter(ctx); err != nil {
		return Job{}, err
	}
	defer client.leave()
	client.mu.Lock()
	createdAt := client.jobs[id]
	delete(client.jobs, id)
	client.outstanding--
	client.mu.Unlock()
	finishedAt := time.Now().UTC()
	return Job{ID: id, Status: JobStatusSucceeded, CreatedAt: createdAt, FinishedAt: &finishedAt}, nil
}

func (client *boundedClient) GetJobAttempts(ctx context.Context, _ string) ([]Attempt, error) {
	if err := client.enter(ctx); err != nil {
		return nil, err
	}
	defer client.leave()
	startedAt := time.Now().UTC().Add(-time.Millisecond)
	finishedAt := time.Now().UTC()
	return []Attempt{{Number: 1, WorkerID: "worker", Status: AttemptStatusSucceeded, StartedAt: startedAt, FinishedAt: &finishedAt}}, nil
}

func (client *boundedClient) enter(ctx context.Context) error {
	client.mu.Lock()
	client.requests++
	client.maxRequests = max(client.maxRequests, client.requests)
	client.mu.Unlock()
	timer := time.NewTimer(client.delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		client.leave()
		return ctx.Err()
	}
}

func (client *boundedClient) leave() {
	client.mu.Lock()
	client.requests--
	client.mu.Unlock()
}

func (client *boundedClient) maximumOutstanding() int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.maxOutstanding
}

func (client *boundedClient) maximumRequests() int {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.maxRequests
}

type retrySubmissionClient struct {
	mu       sync.Mutex
	keys     []string
	attempts int
}

func (client *retrySubmissionClient) SubmitJob(_ context.Context, request SubmissionRequest) (SubmittedJob, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.keys = append(client.keys, request.IdempotencyKey)
	client.attempts++
	if client.attempts == 1 {
		return SubmittedJob{}, NewClientError(errors.New("connection reset"), true, true)
	}
	return SubmittedJob{ID: "job", Status: JobStatusQueued, CreatedAt: time.Now().UTC()}, nil
}

func (*retrySubmissionClient) GetJob(context.Context, string) (Job, error) {
	now := time.Now().UTC()
	return Job{ID: "job", Status: JobStatusSucceeded, CreatedAt: now.Add(-time.Millisecond), FinishedAt: &now}, nil
}

func (*retrySubmissionClient) GetJobAttempts(context.Context, string) ([]Attempt, error) {
	return []Attempt{}, nil
}

type terminalFailureClient struct {
	now time.Time
}

func (client terminalFailureClient) SubmitJob(context.Context, SubmissionRequest) (SubmittedJob, error) {
	return SubmittedJob{ID: "job", Status: JobStatusQueued, CreatedAt: client.now}, nil
}

func (client terminalFailureClient) GetJob(context.Context, string) (Job, error) {
	finishedAt := client.now.Add(time.Millisecond)
	return Job{ID: "job", Status: JobStatusDeadLettered, CreatedAt: client.now, FinishedAt: &finishedAt}, nil
}

func (client terminalFailureClient) GetJobAttempts(context.Context, string) ([]Attempt, error) {
	code := "invalid_input"
	message := "handler rejected input"
	finishedAt := client.now.Add(time.Millisecond)
	return []Attempt{{
		Number: 1, WorkerID: "worker", Status: AttemptStatusPermanentFailed,
		ErrorCode: &code, ErrorMessage: &message, StartedAt: client.now, FinishedAt: &finishedAt,
	}}, nil
}

type neverTerminalClient struct {
	createdAt time.Time
}

func (client *neverTerminalClient) SubmitJob(context.Context, SubmissionRequest) (SubmittedJob, error) {
	return SubmittedJob{ID: "job", Status: JobStatusQueued, CreatedAt: client.createdAt}, nil
}

func (client *neverTerminalClient) GetJob(context.Context, string) (Job, error) {
	return Job{ID: "job", Status: JobStatusRunning, CreatedAt: client.createdAt}, nil
}

func (client *neverTerminalClient) GetJobAttempts(context.Context, string) ([]Attempt, error) {
	return nil, errors.New("unexpected attempt-history call")
}

type submissionErrorClient struct {
	err   error
	calls int
}

type retryPollClient struct {
	mu           sync.Mutex
	createdAt    time.Time
	pollCalls    int
	attemptCalls int
}

func (client *retryPollClient) SubmitJob(context.Context, SubmissionRequest) (SubmittedJob, error) {
	return SubmittedJob{ID: "job", Status: JobStatusQueued, CreatedAt: client.createdAt}, nil
}

func (client *retryPollClient) GetJob(context.Context, string) (Job, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.pollCalls++
	if client.pollCalls == 1 {
		return Job{}, NewClientError(errors.New("temporary poll error"), true, false)
	}
	finishedAt := client.createdAt.Add(time.Millisecond)
	return Job{ID: "job", Status: JobStatusSucceeded, CreatedAt: client.createdAt, FinishedAt: &finishedAt}, nil
}

func (client *retryPollClient) GetJobAttempts(context.Context, string) ([]Attempt, error) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.attemptCalls++
	if client.attemptCalls == 1 {
		return nil, NewClientError(errors.New("temporary attempt error"), true, false)
	}
	finishedAt := client.createdAt.Add(time.Millisecond)
	return []Attempt{{Number: 1, WorkerID: "worker", Status: AttemptStatusSucceeded, StartedAt: client.createdAt, FinishedAt: &finishedAt}}, nil
}

func (client *submissionErrorClient) SubmitJob(context.Context, SubmissionRequest) (SubmittedJob, error) {
	client.calls++
	return SubmittedJob{}, client.err
}

func (*submissionErrorClient) GetJob(context.Context, string) (Job, error) {
	return Job{}, errors.New("unexpected poll")
}

func (*submissionErrorClient) GetJobAttempts(context.Context, string) ([]Attempt, error) {
	return nil, errors.New("unexpected attempts")
}
