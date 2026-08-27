package observability_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const prometheusDataSourceUID = "quarry-prometheus"

type dashboard struct {
	Title  string  `json:"title"`
	UID    string  `json:"uid"`
	Panels []panel `json:"panels"`
}

type panel struct {
	Title      string `json:"title"`
	DataSource struct {
		UID string `json:"uid"`
	} `json:"datasource"`
	Targets []struct {
		Expression string `json:"expr"`
	} `json:"targets"`
}

func TestDashboardCoversBoundedQuarryMetrics(t *testing.T) {
	content := readFile(t, "grafana", "dashboards", "quarry.json")
	var got dashboard
	if err := json.Unmarshal(content, &got); err != nil {
		t.Fatalf("decode dashboard: %v", err)
	}
	if got.Title != "Quarry" || got.UID != "quarry-overview" {
		t.Fatalf("dashboard identity = %q/%q, want Quarry/quarry-overview", got.Title, got.UID)
	}

	requiredMetrics := map[string]string{
		"Queue depth":                   "quarry_queue_depth",
		"Oldest eligible job age":       "quarry_oldest_queued_job_age_seconds",
		"Active jobs":                   "quarry_active_jobs",
		"Active workers":                "quarry_active_workers",
		"Submissions per second":        "quarry_jobs_submitted_total",
		"Attempt outcomes per second":   "quarry_job_attempts_total",
		"Scheduling delay p95":          "quarry_job_scheduling_delay_seconds_bucket",
		"Execution duration p95":        "quarry_job_execution_duration_seconds_bucket",
		"Retries scheduled per second":  "quarry_retries_scheduled_total",
		"Lease expirations per second":  "quarry_lease_expirations_total",
		"Stale reports per second":      "quarry_stale_reports_total",
		"Claim size p95":                "quarry_dispatch_claim_size_bucket",
		"Worker poll errors per second": "quarry_worker_poll_errors_total",
	}
	seen := make(map[string]bool, len(got.Panels))
	for _, panel := range got.Panels {
		if panel.DataSource.UID != prometheusDataSourceUID {
			t.Errorf("panel %q datasource UID = %q, want %q", panel.Title, panel.DataSource.UID, prometheusDataSourceUID)
		}
		if len(panel.Targets) == 0 {
			t.Errorf("panel %q has no query", panel.Title)
			continue
		}
		expression := panel.Targets[0].Expression
		metric, required := requiredMetrics[panel.Title]
		if !required {
			t.Errorf("unexpected panel %q", panel.Title)
			continue
		}
		seen[panel.Title] = true
		if !strings.Contains(expression, metric) {
			t.Errorf("panel %q query %q does not use %s", panel.Title, expression, metric)
		}
		for _, forbiddenLabel := range []string{"job_id", "worker_id", "idempotency_key", "error_message"} {
			if strings.Contains(expression, forbiddenLabel) {
				t.Errorf("panel %q query contains unbounded label %q", panel.Title, forbiddenLabel)
			}
		}
	}
	for title := range requiredMetrics {
		if !seen[title] {
			t.Errorf("dashboard is missing panel %q", title)
		}
	}

	for _, title := range []string{"Queue depth", "Oldest eligible job age", "Active jobs", "Active workers"} {
		for _, panel := range got.Panels {
			if panel.Title == title && !strings.Contains(panel.Targets[0].Expression, "max") {
				t.Errorf("shared PostgreSQL gauge panel %q does not use max aggregation", title)
			}
		}
	}
}

func TestCommittedConfigurationConnectsLocalStack(t *testing.T) {
	prometheus := string(readFile(t, "prometheus.yml"))
	for _, target := range []string{
		"host.docker.internal:8080",
		"host.docker.internal:9464",
		"host.docker.internal:9465",
	} {
		if !strings.Contains(prometheus, target) {
			t.Errorf("Prometheus configuration is missing target %q", target)
		}
	}

	collector := string(readFile(t, "otel-collector.yaml"))
	for _, required := range []string{"0.0.0.0:4318", "endpoint: jaeger:4317", "insecure: true", "health_check"} {
		if !strings.Contains(collector, required) {
			t.Errorf("Collector configuration is missing %q", required)
		}
	}

	datasource := string(readFile(t, "grafana", "provisioning", "datasources", "prometheus.yaml"))
	for _, required := range []string{"uid: " + prometheusDataSourceUID, "url: http://prometheus:9090", "isDefault: true"} {
		if !strings.Contains(datasource, required) {
			t.Errorf("Grafana datasource configuration is missing %q", required)
		}
	}

	repositoryRoot := filepath.Clean(filepath.Join("..", ".."))
	compose, err := os.ReadFile(filepath.Join(repositoryRoot, "compose.yaml"))
	if err != nil {
		t.Fatalf("read compose.yaml: %v", err)
	}
	for _, image := range []string{
		"prom/prometheus:v3.12.0",
		"grafana/grafana:13.1.0",
		"opentelemetry-collector-contrib:0.153.0",
		"jaeger:2.20.0",
	} {
		if !strings.Contains(string(compose), image) {
			t.Errorf("Compose is missing pinned image %q", image)
		}
	}
}

func readFile(t *testing.T, elements ...string) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(elements...))
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Join(elements...), err)
	}
	return content
}
