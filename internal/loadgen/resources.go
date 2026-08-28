package loadgen

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"time"
)

const (
	ResourceSampleSchemaVersion = 1
	maximumResourceLineBytes    = 1 << 20
)

type ProcessResourceSample struct {
	Name                string  `json:"name"`
	ProcessID           int     `json:"process_id"`
	CPUSeconds          float64 `json:"cpu_seconds"`
	ResidentMemoryBytes uint64  `json:"resident_memory_bytes"`
}

type PostgreSQLResourceSample struct {
	CPUPercent  float64 `json:"cpu_percent"`
	MemoryBytes uint64  `json:"memory_bytes"`
}

type ResourceSample struct {
	SchemaVersion       int                      `json:"schema_version"`
	RunID               string                   `json:"run_id"`
	ObservedAt          time.Time                `json:"observed_at"`
	Processes           []ProcessResourceSample  `json:"processes"`
	PostgreSQL          PostgreSQLResourceSample `json:"postgresql"`
	DatabaseConnections int                      `json:"database_connections"`
}

type ResourceSummary struct {
	SampleCount                   int       `json:"sample_count"`
	StartedAt                     time.Time `json:"started_at"`
	EndedAt                       time.Time `json:"ended_at"`
	QuarryCPUCoreAverage          float64   `json:"quarry_cpu_core_average"`
	QuarryResidentMemoryPeakBytes uint64    `json:"quarry_resident_memory_peak_bytes"`
	PostgreSQLCPUPercentAverage   float64   `json:"postgresql_cpu_percent_average"`
	PostgreSQLMemoryPeakBytes     uint64    `json:"postgresql_memory_peak_bytes"`
	DatabaseConnectionsPeak       int       `json:"database_connections_peak"`
}

func WriteResourceJSONLines(writer io.Writer, samples []ResourceSample) error {
	encoder := json.NewEncoder(writer)
	for index, sample := range samples {
		if err := sample.validate(); err != nil {
			return fmt.Errorf("resource sample %d: %w", index, err)
		}
		if err := encoder.Encode(sample); err != nil {
			return fmt.Errorf("write resource sample %d: %w", index, err)
		}
	}
	return nil
}

func ReadResourceJSONLines(reader io.Reader) ([]ResourceSample, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maximumResourceLineBytes)
	var samples []ResourceSample
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := scanner.Bytes()
		if len(line) == 0 {
			return nil, fmt.Errorf("decode resource line %d: empty line", lineNumber)
		}
		var sample ResourceSample
		if err := decodeStrictJSON(bytes.NewReader(line), &sample); err != nil {
			return nil, fmt.Errorf("decode resource line %d: %w", lineNumber, err)
		}
		if err := sample.validate(); err != nil {
			return nil, fmt.Errorf("decode resource line %d: %w", lineNumber, err)
		}
		samples = append(samples, sample)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read resource samples: %w", err)
	}
	return samples, nil
}

func SummarizeResources(
	samples []ResourceSample,
	runID string,
	workerProcesses int,
	measurementStartedAt time.Time,
	measurementEndedAt time.Time,
) (ResourceSummary, error) {
	if runID == "" || workerProcesses <= 0 || !measurementEndedAt.After(measurementStartedAt) {
		return ResourceSummary{}, errors.New("resource summary metadata is invalid")
	}
	ordered := append([]ResourceSample(nil), samples...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ObservedAt.Before(ordered[j].ObservedAt) })
	measured := ordered[:0]
	for _, sample := range ordered {
		if err := sample.validate(); err != nil {
			return ResourceSummary{}, err
		}
		if sample.RunID != runID {
			return ResourceSummary{}, errors.New("resource samples contain a mixed run ID")
		}
		if !sample.ObservedAt.Before(measurementStartedAt) && !sample.ObservedAt.After(measurementEndedAt) {
			measured = append(measured, sample)
		}
	}
	if len(measured) < 2 {
		return ResourceSummary{}, errors.New("resource summary requires at least two measurement-window samples")
	}

	expectedProcesses := workerProcesses + 2
	processIDs := make(map[string]int, expectedProcesses)
	firstCPU := make(map[string]float64, expectedProcesses)
	previousCPU := make(map[string]float64, expectedProcesses)
	lastCPU := make(map[string]float64, expectedProcesses)
	var residentPeak, postgresMemoryPeak uint64
	var postgresCPUTotal float64
	databaseConnectionsPeak := 0
	for index, sample := range measured {
		if len(sample.Processes) > expectedProcesses {
			return ResourceSummary{}, fmt.Errorf("resource sample %d has %d processes, want at most %d", index, len(sample.Processes), expectedProcesses)
		}
		processes, err := indexProcesses(sample.Processes, len(sample.Processes))
		if err != nil {
			return ResourceSummary{}, fmt.Errorf("resource sample %d: %w", index, err)
		}
		var residentTotal uint64
		for name, process := range processes {
			processID, seen := processIDs[name]
			if seen && processID != process.ProcessID {
				return ResourceSummary{}, fmt.Errorf("resource sample %d changes process ID for %q", index, name)
			}
			if !seen {
				processIDs[name] = process.ProcessID
				firstCPU[name] = process.CPUSeconds
			}
			if previous, exists := previousCPU[name]; exists && process.CPUSeconds < previous {
				return ResourceSummary{}, fmt.Errorf("resource sample %d has a decreasing CPU counter for %q", index, name)
			}
			previousCPU[name] = process.CPUSeconds
			lastCPU[name] = process.CPUSeconds
			residentTotal += process.ResidentMemoryBytes
		}
		if residentTotal > residentPeak {
			residentPeak = residentTotal
		}
		postgresCPUTotal += sample.PostgreSQL.CPUPercent
		if sample.PostgreSQL.MemoryBytes > postgresMemoryPeak {
			postgresMemoryPeak = sample.PostgreSQL.MemoryBytes
		}
		if sample.DatabaseConnections > databaseConnectionsPeak {
			databaseConnectionsPeak = sample.DatabaseConnections
		}
	}
	if len(processIDs) != expectedProcesses {
		return ResourceSummary{}, fmt.Errorf("resource samples contain %d distinct processes, want %d", len(processIDs), expectedProcesses)
	}
	var cpuDelta float64
	for name, first := range firstCPU {
		cpuDelta += lastCPU[name] - first
	}
	elapsed := measured[len(measured)-1].ObservedAt.Sub(measured[0].ObservedAt).Seconds()
	if elapsed <= 0 {
		return ResourceSummary{}, errors.New("resource samples require increasing observation times")
	}
	return ResourceSummary{
		SampleCount:                   len(measured),
		StartedAt:                     measured[0].ObservedAt,
		EndedAt:                       measured[len(measured)-1].ObservedAt,
		QuarryCPUCoreAverage:          cpuDelta / elapsed,
		QuarryResidentMemoryPeakBytes: residentPeak,
		PostgreSQLCPUPercentAverage:   postgresCPUTotal / float64(len(measured)),
		PostgreSQLMemoryPeakBytes:     postgresMemoryPeak,
		DatabaseConnectionsPeak:       databaseConnectionsPeak,
	}, nil
}

func (sample ResourceSample) validate() error {
	if sample.SchemaVersion != ResourceSampleSchemaVersion || sample.RunID == "" || sample.ObservedAt.IsZero() {
		return errors.New("resource sample schema, run ID, or observation time is invalid")
	}
	if len(sample.Processes) == 0 {
		return errors.New("resource sample requires process metrics")
	}
	if _, err := indexProcesses(sample.Processes, len(sample.Processes)); err != nil {
		return err
	}
	if math.IsNaN(sample.PostgreSQL.CPUPercent) || math.IsInf(sample.PostgreSQL.CPUPercent, 0) ||
		sample.PostgreSQL.CPUPercent < 0 || sample.PostgreSQL.MemoryBytes == 0 {
		return errors.New("resource sample PostgreSQL metrics are invalid")
	}
	if sample.DatabaseConnections < 0 {
		return errors.New("resource sample database connections must not be negative")
	}
	return nil
}

func indexProcesses(samples []ProcessResourceSample, expected int) (map[string]ProcessResourceSample, error) {
	if len(samples) != expected {
		return nil, fmt.Errorf("resource sample has %d processes, want %d", len(samples), expected)
	}
	indexed := make(map[string]ProcessResourceSample, len(samples))
	for _, process := range samples {
		if process.Name == "" || process.ProcessID <= 0 || process.ResidentMemoryBytes == 0 ||
			math.IsNaN(process.CPUSeconds) || math.IsInf(process.CPUSeconds, 0) || process.CPUSeconds < 0 {
			return nil, errors.New("resource sample contains invalid process metrics")
		}
		if _, exists := indexed[process.Name]; exists {
			return nil, fmt.Errorf("resource sample contains duplicate process name %q", process.Name)
		}
		indexed[process.Name] = process
	}
	return indexed, nil
}
