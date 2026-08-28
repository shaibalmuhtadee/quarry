package loadgen

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"time"
)

const (
	CampaignSchemaVersion   = 1
	RunSummarySchemaVersion = 1
)

type RunStatus string

const (
	RunStatusValid   RunStatus = "valid"
	RunStatusInvalid RunStatus = "invalid"
)

type GitMetadata struct {
	Commit        string `json:"commit"`
	WorktreeState string `json:"worktree_state"`
}

type MachineMetadata struct {
	OS               string `json:"os"`
	Architecture     string `json:"architecture"`
	CPUModel         string `json:"cpu_model"`
	LogicalCPUCount  int    `json:"logical_cpu_count"`
	TotalMemoryBytes uint64 `json:"total_memory_bytes"`
}

type SoftwareMetadata struct {
	GoVersion     string `json:"go_version"`
	DockerVersion string `json:"docker_version"`
	PostgresImage string `json:"postgres_image"`
}

type QuarryConfig struct {
	LeaseDuration           time.Duration `json:"lease_duration"`
	ReaperInterval          time.Duration `json:"reaper_interval"`
	ReaperBatchSize         int           `json:"reaper_batch_size"`
	WorkerHeartbeatInterval time.Duration `json:"worker_heartbeat_interval"`
}

type BenchmarkRunConfig struct {
	Workload            Workload      `json:"workload"`
	WorkerProcesses     int           `json:"worker_processes"`
	WorkerConcurrency   int           `json:"worker_concurrency"`
	MaxOutstanding      int           `json:"max_outstanding"`
	HTTPConcurrency     int           `json:"http_concurrency"`
	WarmupDuration      time.Duration `json:"warmup_duration"`
	MeasurementDuration time.Duration `json:"measurement_duration"`
	DrainTimeout        time.Duration `json:"drain_timeout"`
	PollInterval        time.Duration `json:"poll_interval"`
	Seed                int64         `json:"seed"`
	MaxAttempts         int32         `json:"max_attempts"`
	JobTimeout          time.Duration `json:"job_timeout"`
}

type RunManifest struct {
	RunID         string             `json:"run_id"`
	Directory     string             `json:"directory"`
	Repetition    int                `json:"repetition"`
	Status        RunStatus          `json:"status"`
	FailureReason string             `json:"failure_reason,omitempty"`
	Config        BenchmarkRunConfig `json:"config"`
}

type CampaignManifest struct {
	SchemaVersion int              `json:"schema_version"`
	CampaignID    string           `json:"campaign_id"`
	Publishable   bool             `json:"publishable"`
	CreatedAt     time.Time        `json:"created_at"`
	Git           GitMetadata      `json:"git"`
	Machine       MachineMetadata  `json:"machine"`
	Software      SoftwareMetadata `json:"software"`
	Quarry        QuarryConfig     `json:"quarry"`
	Runs          []RunManifest    `json:"runs"`
}

var gitCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func ReadCampaignManifest(reader io.Reader) (CampaignManifest, error) {
	var manifest CampaignManifest
	if err := decodeStrictJSON(reader, &manifest); err != nil {
		return CampaignManifest{}, fmt.Errorf("decode campaign manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return CampaignManifest{}, err
	}
	return manifest, nil
}

func (manifest CampaignManifest) Validate() error {
	if manifest.SchemaVersion != CampaignSchemaVersion {
		return fmt.Errorf("campaign schema version = %d, want %d", manifest.SchemaVersion, CampaignSchemaVersion)
	}
	if manifest.CampaignID == "" || manifest.CreatedAt.IsZero() {
		return errors.New("campaign ID and creation time are required")
	}
	if !gitCommitPattern.MatchString(manifest.Git.Commit) ||
		(manifest.Git.WorktreeState != "clean" && manifest.Git.WorktreeState != "dirty") {
		return errors.New("campaign Git metadata is invalid")
	}
	if manifest.Publishable && manifest.Git.WorktreeState != "clean" {
		return errors.New("publishable campaign requires a clean worktree")
	}
	if manifest.Machine.OS == "" || manifest.Machine.Architecture == "" || manifest.Machine.CPUModel == "" ||
		manifest.Machine.LogicalCPUCount <= 0 || manifest.Machine.TotalMemoryBytes == 0 {
		return errors.New("campaign machine metadata is incomplete")
	}
	if manifest.Software.GoVersion == "" || manifest.Software.DockerVersion == "" || manifest.Software.PostgresImage == "" {
		return errors.New("campaign software metadata is incomplete")
	}
	if manifest.Quarry.LeaseDuration <= 0 || manifest.Quarry.ReaperInterval <= 0 ||
		manifest.Quarry.ReaperBatchSize <= 0 || manifest.Quarry.WorkerHeartbeatInterval <= 0 {
		return errors.New("campaign Quarry configuration is invalid")
	}
	if len(manifest.Runs) == 0 {
		return errors.New("campaign requires at least one run")
	}

	runIDs := make(map[string]struct{}, len(manifest.Runs))
	directories := make(map[string]struct{}, len(manifest.Runs))
	configRepetitions := make(map[string]struct{}, len(manifest.Runs))
	maxOutstanding := manifest.Runs[0].Config.MaxOutstanding
	for index, run := range manifest.Runs {
		if err := run.validate(); err != nil {
			return fmt.Errorf("campaign run %d: %w", index, err)
		}
		if _, exists := runIDs[run.RunID]; exists {
			return fmt.Errorf("campaign contains duplicate run ID %q", run.RunID)
		}
		runIDs[run.RunID] = struct{}{}
		if _, exists := directories[run.Directory]; exists {
			return fmt.Errorf("campaign contains duplicate run directory %q", run.Directory)
		}
		directories[run.Directory] = struct{}{}
		key := fmt.Sprintf("%s/%d", run.Config.key(), run.Repetition)
		if _, exists := configRepetitions[key]; exists {
			return fmt.Errorf("campaign contains duplicate configuration repetition for run %q", run.RunID)
		}
		configRepetitions[key] = struct{}{}
		if run.Config.MaxOutstanding != maxOutstanding {
			return errors.New("campaign configurations must use one stable max-outstanding value")
		}
	}
	return nil
}

func (manifest CampaignManifest) Run(runID string) (RunManifest, bool) {
	for _, run := range manifest.Runs {
		if run.RunID == runID {
			return run, true
		}
	}
	return RunManifest{}, false
}

func (run RunManifest) validate() error {
	if run.RunID == "" || run.Repetition <= 0 {
		return errors.New("run ID and positive repetition are required")
	}
	if run.Directory == "" || path.IsAbs(run.Directory) || strings.ContainsAny(run.Directory, `\:`) ||
		path.Clean(run.Directory) != run.Directory ||
		run.Directory == "." || run.Directory == ".." || len(run.Directory) >= 3 && run.Directory[:3] == "../" {
		return errors.New("run directory must be a clean relative path")
	}
	switch run.Status {
	case RunStatusValid:
		if run.FailureReason != "" {
			return errors.New("valid run cannot have a failure reason")
		}
	case RunStatusInvalid:
		if run.FailureReason == "" {
			return errors.New("invalid run requires a failure reason")
		}
	default:
		return fmt.Errorf("invalid run status %q", run.Status)
	}
	return run.Config.validate()
}

func (config BenchmarkRunConfig) validate() error {
	if _, err := ParseWorkload(string(config.Workload)); err != nil {
		return err
	}
	if config.WorkerProcesses != 1 && config.WorkerProcesses != 2 &&
		config.WorkerProcesses != 4 && config.WorkerProcesses != 8 {
		return errors.New("worker processes must be 1, 2, 4, or 8")
	}
	if config.WorkerConcurrency != 8 {
		return errors.New("worker concurrency must be 8")
	}
	if config.MaxOutstanding <= 0 || config.HTTPConcurrency <= 0 || config.MaxAttempts <= 0 {
		return errors.New("run concurrency and attempt limits must be positive")
	}
	if config.WarmupDuration < 0 || config.MeasurementDuration <= 0 || config.DrainTimeout <= 0 ||
		config.PollInterval <= 0 || config.JobTimeout <= 0 || config.JobTimeout%time.Millisecond != 0 {
		return errors.New("run durations are invalid")
	}
	return nil
}

func (config BenchmarkRunConfig) key() string {
	return fmt.Sprintf("%s/%d/%d/%d/%d/%d/%d/%d/%d/%d/%d/%d",
		config.Workload, config.WorkerProcesses, config.WorkerConcurrency, config.MaxOutstanding,
		config.HTTPConcurrency, config.WarmupDuration, config.MeasurementDuration, config.DrainTimeout,
		config.PollInterval, config.Seed, config.MaxAttempts, config.JobTimeout)
}

func decodeStrictJSON(reader io.Reader, target any) error {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("multiple JSON values")
	}
	return nil
}
