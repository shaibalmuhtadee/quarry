package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"github.com/shaibalmuhtadee/quarry/internal/loadgen"
)

const (
	manifestFileName        = "manifest.json"
	jobSamplesFileName      = "jobs.jsonl.gz"
	resourceSamplesFileName = "resources.jsonl"
	runSummaryFileName      = "summary.json"
	campaignSummaryFileName = "campaign-summary.json"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("benchmarkctl requires summarize-run, summarize-campaign, verify-runs, or verify")
	}
	switch args[0] {
	case "summarize-run":
		root, runID, err := parseRunCommand(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		summary, path, err := regenerateRun(root, runID)
		if err != nil {
			return err
		}
		if err := writeJSON(path, summary); err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "summarized run %s\n", runID)
		return err
	case "summarize-campaign":
		root, err := parseRootCommand(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		manifest, summaries, err := regenerateValidRuns(root, false)
		if err != nil {
			return err
		}
		summary, err := loadgen.AggregateCampaign(manifest, summaries)
		if err != nil {
			return err
		}
		if err := writeJSON(filepath.Join(root, campaignSummaryFileName), summary); err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "summarized campaign %s\n", manifest.CampaignID)
		return err
	case "verify-runs":
		root, err := parseRootCommand(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		manifest, summaries, err := regenerateValidRuns(root, true)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(stdout, "verified %d valid runs in campaign %s\n", len(summaries), manifest.CampaignID)
		return err
	case "verify":
		root, err := parseRootCommand(args[0], args[1:], stderr)
		if err != nil {
			return err
		}
		manifest, summaries, err := regenerateValidRuns(root, true)
		if err != nil {
			return err
		}
		regenerated, err := loadgen.AggregateCampaign(manifest, summaries)
		if err != nil {
			return err
		}
		var persisted loadgen.CampaignSummary
		if err := readStrictJSON(filepath.Join(root, campaignSummaryFileName), &persisted); err != nil {
			return err
		}
		if !reflect.DeepEqual(persisted, regenerated) {
			return errors.New("persisted campaign summary does not match regenerated raw-data summary")
		}
		_, err = fmt.Fprintf(stdout, "verified campaign %s\n", manifest.CampaignID)
		return err
	default:
		return fmt.Errorf("unknown benchmarkctl command %q", args[0])
	}
}

func parseRunCommand(name string, args []string, stderr io.Writer) (string, string, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	var root, runID string
	flags.StringVar(&root, "campaign-root", "", "campaign directory")
	flags.StringVar(&runID, "run-id", "", "run identifier")
	if err := flags.Parse(args); err != nil {
		return "", "", err
	}
	if flags.NArg() != 0 || root == "" || runID == "" {
		return "", "", errors.New("-campaign-root and -run-id are required without positional arguments")
	}
	return root, runID, nil
}

func parseRootCommand(name string, args []string, stderr io.Writer) (string, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	var root string
	flags.StringVar(&root, "campaign-root", "", "campaign directory")
	if err := flags.Parse(args); err != nil {
		return "", err
	}
	if flags.NArg() != 0 || root == "" {
		return "", errors.New("-campaign-root is required without positional arguments")
	}
	return root, nil
}

func regenerateValidRuns(root string, comparePersisted bool) (loadgen.CampaignManifest, []loadgen.RunSummary, error) {
	manifest, err := readManifest(root)
	if err != nil {
		return loadgen.CampaignManifest{}, nil, err
	}
	var summaries []loadgen.RunSummary
	for _, run := range manifest.Runs {
		runDirectory := filepath.Join(root, filepath.FromSlash(run.Directory))
		if run.Status == loadgen.RunStatusInvalid {
			if info, err := os.Stat(runDirectory); err != nil || !info.IsDir() {
				return loadgen.CampaignManifest{}, nil, fmt.Errorf("invalid run %q directory is not preserved", run.RunID)
			}
			continue
		}
		regenerated, _, err := regenerateRunWithManifest(root, run)
		if err != nil {
			return loadgen.CampaignManifest{}, nil, err
		}
		if comparePersisted {
			var persisted loadgen.RunSummary
			if err := readStrictJSON(filepath.Join(runDirectory, runSummaryFileName), &persisted); err != nil {
				return loadgen.CampaignManifest{}, nil, err
			}
			if !reflect.DeepEqual(persisted, regenerated) {
				return loadgen.CampaignManifest{}, nil, fmt.Errorf("persisted summary for run %q does not match raw data", run.RunID)
			}
		}
		summaries = append(summaries, regenerated)
	}
	return manifest, summaries, nil
}

func regenerateRun(root, runID string) (loadgen.RunSummary, string, error) {
	manifest, err := readManifest(root)
	if err != nil {
		return loadgen.RunSummary{}, "", err
	}
	run, exists := manifest.Run(runID)
	if !exists {
		return loadgen.RunSummary{}, "", fmt.Errorf("campaign has no run %q", runID)
	}
	return regenerateRunWithManifest(root, run)
}

func regenerateRunWithManifest(
	root string,
	run loadgen.RunManifest,
) (loadgen.RunSummary, string, error) {
	runDirectory := filepath.Join(root, filepath.FromSlash(run.Directory))
	jobs, err := readJobSamples(filepath.Join(runDirectory, jobSamplesFileName))
	if err != nil {
		return loadgen.RunSummary{}, "", fmt.Errorf("run %q: %w", run.RunID, err)
	}
	resources, err := readResourceSamples(filepath.Join(runDirectory, resourceSamplesFileName))
	if err != nil {
		return loadgen.RunSummary{}, "", fmt.Errorf("run %q: %w", run.RunID, err)
	}
	summary, err := loadgen.SummarizeRun(run, jobs, resources)
	if err != nil {
		return loadgen.RunSummary{}, "", fmt.Errorf("run %q: %w", run.RunID, err)
	}
	return summary, filepath.Join(runDirectory, runSummaryFileName), nil
}

func readManifest(root string) (loadgen.CampaignManifest, error) {
	input, err := os.Open(filepath.Join(root, manifestFileName))
	if err != nil {
		return loadgen.CampaignManifest{}, fmt.Errorf("open campaign manifest: %w", err)
	}
	defer input.Close()
	return loadgen.ReadCampaignManifest(input)
}

func readJobSamples(path string) ([]loadgen.Sample, error) {
	input, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open job samples: %w", err)
	}
	defer input.Close()
	samples, err := loadgen.ReadGzipJSONLines(input)
	if err != nil {
		return nil, fmt.Errorf("read job samples: %w", err)
	}
	return samples, nil
}

func readResourceSamples(path string) ([]loadgen.ResourceSample, error) {
	input, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open resource samples: %w", err)
	}
	defer input.Close()
	samples, err := loadgen.ReadResourceJSONLines(input)
	if err != nil {
		return nil, fmt.Errorf("read resource samples: %w", err)
	}
	return samples, nil
}

func readStrictJSON(path string, target any) error {
	input, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", filepath.Base(path), err)
	}
	defer input.Close()
	decoder := json.NewDecoder(input)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", filepath.Base(path), err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode %s: multiple JSON values", filepath.Base(path))
	}
	return nil
}

func writeJSON(path string, value any) (writeErr error) {
	output, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", filepath.Base(path), err)
	}
	defer func() { writeErr = errors.Join(writeErr, output.Close()) }()
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("encode %s: %w", filepath.Base(path), err)
	}
	return nil
}
