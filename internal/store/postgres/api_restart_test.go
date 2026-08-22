package postgres_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/api"
	"github.com/shaibalmuhtadee/quarry/internal/store/postgres"
)

func TestAPIJobSurvivesServerAndPoolRestart(t *testing.T) {
	connectionString := startMigratedPostgres(t)

	type submissionResponse struct {
		ID           string    `json:"id"`
		Status       string    `json:"status"`
		Deduplicated bool      `json:"deduplicated"`
		CreatedAt    time.Time `json:"created_at"`
	}
	var submitted submissionResponse
	var firstBaseURL string
	useFreshAPIInstance(t, connectionString, func(client *http.Client, baseURL string) {
		firstBaseURL = baseURL
		request, err := http.NewRequest(
			http.MethodPost,
			baseURL+"/v1/jobs",
			bytes.NewBufferString(`{"type":"restart.test","payload":{"message":"durable"},"max_attempts":5,"timeout_ms":30000}`),
		)
		if err != nil {
			t.Fatalf("create submission request: %v", err)
		}
		request.Header.Set("Content-Type", "application/json")

		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("submit job through API instance A: %v", err)
		}
		decodeAPIResponse(t, response, http.StatusCreated, &submitted)
		if got, want := response.Header.Get("Location"), "/v1/jobs/"+submitted.ID; got != want {
			t.Fatalf("submission Location = %q, want %q", got, want)
		}
	})

	closedClient := &http.Client{Timeout: 200 * time.Millisecond}
	response, err := closedClient.Get(firstBaseURL + "/healthz")
	if err == nil {
		response.Body.Close()
		t.Fatal("API instance A still accepts requests after its lifecycle ended")
	}
	if submitted.ID == "" {
		t.Fatal("submission response has no job ID")
	}
	if submitted.Status != "queued" {
		t.Fatalf("submission status = %q, want queued", submitted.Status)
	}
	if submitted.Deduplicated {
		t.Fatal("submission response marked a new job as deduplicated")
	}
	if submitted.CreatedAt.IsZero() {
		t.Fatal("submission created timestamp is zero")
	}

	type jobResponse struct {
		ID           string    `json:"id"`
		Type         string    `json:"type"`
		Status       string    `json:"status"`
		AttemptCount int32     `json:"attempt_count"`
		MaxAttempts  int32     `json:"max_attempts"`
		TimeoutMS    int64     `json:"timeout_ms"`
		CreatedAt    time.Time `json:"created_at"`
		UpdatedAt    time.Time `json:"updated_at"`
	}
	var retrieved jobResponse
	var retrievedFields map[string]json.RawMessage
	useFreshAPIInstance(t, connectionString, func(client *http.Client, baseURL string) {
		response, err := client.Get(baseURL + "/v1/jobs/" + submitted.ID)
		if err != nil {
			t.Fatalf("retrieve job through API instance B: %v", err)
		}
		body := decodeAPIResponse(t, response, http.StatusOK, &retrieved)
		if err := json.Unmarshal(body, &retrievedFields); err != nil {
			t.Fatalf("decode retrieved job fields: %v", err)
		}
	})

	if retrieved.ID != submitted.ID {
		t.Fatalf("retrieved job ID = %q, want %q", retrieved.ID, submitted.ID)
	}
	if retrieved.Type != "restart.test" {
		t.Fatalf("retrieved job type = %q, want restart.test", retrieved.Type)
	}
	if retrieved.Status != "queued" {
		t.Fatalf("retrieved job status = %q, want queued", retrieved.Status)
	}
	if retrieved.AttemptCount != 0 {
		t.Fatalf("retrieved attempt count = %d, want 0", retrieved.AttemptCount)
	}
	if retrieved.MaxAttempts != 5 {
		t.Fatalf("retrieved maximum attempts = %d, want 5", retrieved.MaxAttempts)
	}
	if retrieved.TimeoutMS != 30000 {
		t.Fatalf("retrieved timeout = %dms, want 30000ms", retrieved.TimeoutMS)
	}
	if !retrieved.CreatedAt.Equal(submitted.CreatedAt) {
		t.Fatalf("retrieved created timestamp = %s, want %s", retrieved.CreatedAt, submitted.CreatedAt)
	}
	if !retrieved.UpdatedAt.Equal(submitted.CreatedAt) {
		t.Fatalf("retrieved updated timestamp = %s, want %s", retrieved.UpdatedAt, submitted.CreatedAt)
	}
	if _, exists := retrievedFields["payload"]; exists {
		t.Fatal("retrieved job exposed its stored payload")
	}
}

func useFreshAPIInstance(
	t *testing.T,
	connectionString string,
	use func(*http.Client, string),
) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := postgres.NewPool(ctx, connectionString)
	if err != nil {
		t.Fatalf("create PostgreSQL pool for API instance: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping PostgreSQL from API instance: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := httptest.NewServer(api.NewHandler(postgres.NewJobStore(pool), pool, logger))
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second

	use(client, server.URL)
}

func decodeAPIResponse(t *testing.T, response *http.Response, wantStatus int, destination any) []byte {
	t.Helper()
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read API response: %v", err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("API response status = %d, want %d: %s", response.StatusCode, wantStatus, body)
	}
	if got := response.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("API Content-Type = %q, want application/json", got)
	}
	if err := json.Unmarshal(body, destination); err != nil {
		t.Fatalf("decode API response: %v", err)
	}

	return body
}
