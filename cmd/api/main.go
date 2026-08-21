package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/shaibalmuhtadee/quarry/internal/store/postgres"
	postgresdb "github.com/shaibalmuhtadee/quarry/internal/store/postgres/generated"
)

const (
	defaultDatabaseURL = "postgres://quarry:quarry@localhost:5432/quarry?sslmode=disable"
	connectionTimeout  = 5 * time.Second
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), connectionTimeout)
	defer cancel()

	databaseURL := os.Getenv("QUARRY_DATABASE_URL")
	if databaseURL == "" {
		databaseURL = defaultDatabaseURL
	}

	if err := checkDatabase(ctx, databaseURL); err != nil {
		log.Fatal(err)
	}
	fmt.Println("PostgreSQL connection successful")
}

func checkDatabase(ctx context.Context, databaseURL string) error {
	pool, err := postgres.NewPool(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	status, err := postgresdb.New(pool).HealthCheck(ctx)
	if err != nil {
		return fmt.Errorf("run PostgreSQL health query: %w", err)
	}
	if status != 1 {
		return fmt.Errorf("PostgreSQL health query returned %d", status)
	}

	return nil
}
