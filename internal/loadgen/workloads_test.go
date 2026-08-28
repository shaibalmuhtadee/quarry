package loadgen

import (
	"bytes"
	"testing"
	"time"
)

func TestWorkloadPayloadsAreExactAndDeterministic(t *testing.T) {
	for _, test := range []struct {
		name     string
		workload Workload
		jobType  string
		payload  string
	}{
		{name: "queue overhead", workload: WorkloadQueueOverhead, jobType: "demo.echo", payload: `{"seed":41,"sequence":7}`},
		{name: "simulated IO", workload: WorkloadSimulatedIO, jobType: "demo.sleep", payload: `{"duration_ms":25,"seed":41,"sequence":7}`},
		{name: "recovery", workload: WorkloadRecovery, jobType: "demo.sleep", payload: `{"duration_ms":6000,"seed":41,"sequence":7}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			factory, err := NewWorkloadFactory(test.workload, 41, 3, time.Second)
			if err != nil {
				t.Fatalf("create workload: %v", err)
			}
			first := factory(7)
			second := factory(7)
			if first.JobType != test.jobType || string(first.Payload) != test.payload {
				t.Fatalf("submission = %#v, want type %q and payload %s", first, test.jobType, test.payload)
			}
			if !bytes.Equal(first.Payload, second.Payload) {
				t.Fatalf("same seed and sequence produced %q then %q", first.Payload, second.Payload)
			}
			if bytes.Equal(first.Payload, factory(8).Payload) {
				t.Fatal("different sequences produced the same payload")
			}
			otherSeed, err := NewWorkloadFactory(test.workload, 42, 3, time.Second)
			if err != nil {
				t.Fatalf("create second workload: %v", err)
			}
			if bytes.Equal(first.Payload, otherSeed(7).Payload) {
				t.Fatal("different seeds produced the same payload")
			}
		})
	}
}

func TestSimulatedIODurationIsExactly25Milliseconds(t *testing.T) {
	if SimulatedIODuration != 25*time.Millisecond {
		t.Fatalf("simulated I/O duration = %s, want 25ms", SimulatedIODuration)
	}
}

func TestRecoveryDurationIsExactlySixSeconds(t *testing.T) {
	if RecoveryDuration != 6*time.Second {
		t.Fatalf("recovery duration = %s, want 6s", RecoveryDuration)
	}
}

func TestWorkloadFactoryRejectsInvalidConfiguration(t *testing.T) {
	for _, test := range []struct {
		name        string
		workload    Workload
		maxAttempts int32
		timeout     time.Duration
	}{
		{name: "workload", workload: "unknown", maxAttempts: 1, timeout: time.Second},
		{name: "attempts", workload: WorkloadQueueOverhead, maxAttempts: 0, timeout: time.Second},
		{name: "timeout", workload: WorkloadQueueOverhead, maxAttempts: 1, timeout: time.Microsecond},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewWorkloadFactory(test.workload, 1, test.maxAttempts, test.timeout); err == nil {
				t.Fatal("NewWorkloadFactory accepted invalid configuration")
			}
		})
	}
}
