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
				JobId:     "00000000-0000-0000-0000-000000000001",
				AttemptNo: 1,
				State:     dispatcherv1.HeartbeatAttemptState_HEARTBEAT_ATTEMPT_STATE_VALID,
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
	if decoded.GetAttempts()[1].GetState() != dispatcherv1.HeartbeatAttemptState_HEARTBEAT_ATTEMPT_STATE_STALE {
		t.Fatalf("second lease state = %s, want stale", decoded.GetAttempts()[1].GetState())
	}
}

func TestHeartbeatAttemptStateZeroValueIsUnspecified(t *testing.T) {
	t.Parallel()

	var state dispatcherv1.HeartbeatAttemptState
	if state != dispatcherv1.HeartbeatAttemptState_HEARTBEAT_ATTEMPT_STATE_UNSPECIFIED {
		t.Fatalf("zero lease state = %s, want unspecified", state)
	}
}
