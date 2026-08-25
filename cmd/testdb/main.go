package main

import (
	"context"
	"fmt"
	"time"

	"github/rebik/internal/usage"
)

func main() {
	store, err := usage.NewStore("postgres://postgres:postgres@localhost:5432/router?sslmode=disable")
	if err != nil {
		fmt.Printf("Error connecting to database: %v\n", err)
		return
	}
	defer store.Close()

	err = store.Record(context.Background(), "Bearer tenant-A", "mock-llm-v1", 1200, 3400, time.Now())
	if err != nil {
		fmt.Printf("Error recording event: %v\n", err)
		return
	}

	fmt.Println("✅ Successfully recorded usage event in Postgres!")
}
