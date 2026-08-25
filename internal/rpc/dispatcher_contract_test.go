package rpc_test

import (
	"testing"

	dispatcherv1 "github.com/shaibalmuhtadee/quarry/internal/rpc/generated/dispatcher/v1"
	"google.golang.org/protobuf/proto"
)

func TestHeartbeatRequestPreservesAttemptIdentity(t *testing.T) {
	t.Parallel()

	request := &dispatcherv1.HeartbeatRequest{
		WorkerId: "00000000-0000-0000-0000-000000000001",
		ActiveAttempts: []*dispatcherv1.HeartbeatAttempt{
			{
				JobId:     "00000000-0000-0000-0000-000000000002",
				AttemptNo: 3,
			},
		},
	}

	encoded, err := proto.Marshal(request)
	if err != nil {
		t.Fatalf("marshal heartbeat request: %v", err)
	}
	var decoded dispatcherv1.HeartbeatRequest
	if err := proto.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal heartbeat request: %v", err)
	}

	if decoded.GetWorkerId() != request.GetWorkerId() {
		t.Fatalf("worker ID = %q, want %q", decoded.GetWorkerId(), request.GetWorkerId())
	}
	if len(decoded.GetActiveAttempts()) != 1 {
		t.Fatalf("active attempts = %d, want 1", len(decoded.GetActiveAttempts()))
	}
	attempt := decoded.GetActiveAttempts()[0]
	if attempt.GetJobId() != request.GetActiveAttempts()[0].GetJobId() || attempt.GetAttemptNo() != 3 {
		t.Fatalf("active attempt = %#v", attempt)
	}
}

func TestHeartbeatResponsePreservesExplicitLeaseStates(t *testing.T) {
	t.Parallel()

	response := &dispatcherv1.HeartbeatResponse{
		Attempts: []*dispatcherv1.HeartbeatAttemptResult{
			{
				JobId:           "00000000-0000-0000-0000-000000000001",
				AttemptNo:       1,
				State:           dispatcherv1.HeartbeatAttemptState_HEARTBEAT_ATTEMPT_STATE_VALID,
				CancelRequested: true,
			},
			{
				JobId:     "00000000-0000-0000-0000-000000000002",
				AttemptNo: 2,
				State:     dispatcherv1.HeartbeatAttemptState_HEARTBEAT_ATTEMPT_STATE_STALE,
			},
		},
	}

	encoded, err := proto.Marshal(response)
	if err != nil {
		t.Fatalf("marshal heartbeat response: %v", err)
	}
	var decoded dispatcherv1.HeartbeatResponse
	if err := proto.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal heartbeat response: %v", err)
	}

	if len(decoded.GetAttempts()) != 2 {
		t.Fatalf("attempt results = %d, want 2", len(decoded.GetAttempts()))
	}
	if decoded.GetAttempts()[0].GetState() != dispatcherv1.HeartbeatAttemptState_HEARTBEAT_ATTEMPT_STATE_VALID {
		t.Fatalf("first lease state = %s, want valid", decoded.GetAttempts()[0].GetState())
	}
	if !decoded.GetAttempts()[0].GetCancelRequested() {
		t.Fatal("first attempt cancellation request = false, want true")
	}
	if decoded.GetAttempts()[1].GetState() != dispatcherv1.HeartbeatAttemptState_HEARTBEAT_ATTEMPT_STATE_STALE {
		t.Fatalf("second lease state = %s, want stale", decoded.GetAttempts()[1].GetState())
	}
	if decoded.GetAttempts()[1].GetCancelRequested() {
		t.Fatal("second attempt cancellation request = true, want false")
	}
}

func TestHeartbeatAttemptStateZeroValueIsUnspecified(t *testing.T) {
	t.Parallel()

	var state dispatcherv1.HeartbeatAttemptState
	if state != dispatcherv1.HeartbeatAttemptState_HEARTBEAT_ATTEMPT_STATE_UNSPECIFIED {
		t.Fatalf("zero lease state = %s, want unspecified", state)
	}
}

func TestReportAttemptPreservesEveryOutcome(t *testing.T) {
	t.Parallel()

	failure := func() *dispatcherv1.AttemptFailure {
		return &dispatcherv1.AttemptFailure{
			ErrorCode:    "handler_error",
			ErrorMessage: "handler failed",
		}
	}
	tests := []struct {
		name    string
		request *dispatcherv1.ReportAttemptRequest
		assert  func(*testing.T, *dispatcherv1.ReportAttemptRequest)
	}{
		{
			name: "succeeded",
			request: &dispatcherv1.ReportAttemptRequest{
				Outcome: &dispatcherv1.ReportAttemptRequest_Succeeded{
					Succeeded: &dispatcherv1.AttemptSucceeded{ResultJson: []byte(`{"ok":true}`)},
				},
			},
			assert: func(t *testing.T, request *dispatcherv1.ReportAttemptRequest) {
				t.Helper()
				if string(request.GetSucceeded().GetResultJson()) != `{"ok":true}` {
					t.Fatalf("success result = %s", request.GetSucceeded().GetResultJson())
				}
			},
		},
		{
			name: "retryable failure",
			request: &dispatcherv1.ReportAttemptRequest{
				Outcome: &dispatcherv1.ReportAttemptRequest_RetryableFailure{RetryableFailure: failure()},
			},
			assert: func(t *testing.T, request *dispatcherv1.ReportAttemptRequest) {
				t.Helper()
				assertAttemptFailure(t, request.GetRetryableFailure())
			},
		},
		{
			name: "permanent failure",
			request: &dispatcherv1.ReportAttemptRequest{
				Outcome: &dispatcherv1.ReportAttemptRequest_PermanentFailure{PermanentFailure: failure()},
			},
			assert: func(t *testing.T, request *dispatcherv1.ReportAttemptRequest) {
				t.Helper()
				assertAttemptFailure(t, request.GetPermanentFailure())
			},
		},
		{
			name: "cancelled",
			request: &dispatcherv1.ReportAttemptRequest{
				Outcome: &dispatcherv1.ReportAttemptRequest_Cancelled{Cancelled: failure()},
			},
			assert: func(t *testing.T, request *dispatcherv1.ReportAttemptRequest) {
				t.Helper()
				assertAttemptFailure(t, request.GetCancelled())
			},
		},
		{
			name: "timed out",
			request: &dispatcherv1.ReportAttemptRequest{
				Outcome: &dispatcherv1.ReportAttemptRequest_TimedOut{TimedOut: failure()},
			},
			assert: func(t *testing.T, request *dispatcherv1.ReportAttemptRequest) {
				t.Helper()
				assertAttemptFailure(t, request.GetTimedOut())
			},
		},
		{
			name: "panicked",
			request: &dispatcherv1.ReportAttemptRequest{
				Outcome: &dispatcherv1.ReportAttemptRequest_Panicked{Panicked: failure()},
			},
			assert: func(t *testing.T, request *dispatcherv1.ReportAttemptRequest) {
				t.Helper()
				assertAttemptFailure(t, request.GetPanicked())
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			request := test.request
			request.WorkerId = "00000000-0000-0000-0000-000000000001"
			request.JobId = "00000000-0000-0000-0000-000000000002"
			request.AttemptNo = 3
			encoded, err := proto.Marshal(request)
			if err != nil {
				t.Fatalf("marshal report request: %v", err)
			}
			var decoded dispatcherv1.ReportAttemptRequest
			if err := proto.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("unmarshal report request: %v", err)
			}
			if decoded.GetWorkerId() != request.GetWorkerId() ||
				decoded.GetJobId() != request.GetJobId() ||
				decoded.GetAttemptNo() != request.GetAttemptNo() {
				t.Fatalf("report identity = %#v, want %#v", &decoded, request)
			}
			test.assert(t, &decoded)
		})
	}
}

func TestReportAttemptZeroValueHasNoOutcome(t *testing.T) {
	t.Parallel()

	var request dispatcherv1.ReportAttemptRequest
	if request.GetOutcome() != nil {
		t.Fatalf("zero report outcome = %#v, want nil", request.GetOutcome())
	}
}

func assertAttemptFailure(t *testing.T, failure *dispatcherv1.AttemptFailure) {
	t.Helper()

	if failure == nil {
		t.Fatal("attempt failure is nil")
	}
	if failure.GetErrorCode() != "handler_error" {
		t.Fatalf("error code = %q, want handler_error", failure.GetErrorCode())
	}
	if failure.GetErrorMessage() != "handler failed" {
		t.Fatalf("error message = %q, want handler failed", failure.GetErrorMessage())
	}
}
