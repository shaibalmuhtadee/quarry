package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/shaibalmuhtadee/quarry/internal/store/postgres"
)

func TestNewPoolRejectsInvalidConnectionString(t *testing.T) {
	pool, err := postgres.NewPool(context.Background(), "://invalid")
	if err == nil {
		pool.Close()
		t.Fatal("NewPool accepted an invalid connection string")
	}
	if !strings.Contains(err.Error(), "parse PostgreSQL connection string") {
		t.Fatalf("NewPool error = %q, want parse context", err)
	}
}
