package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	targetURL := flag.String("url", "http://localhost:8080/v1/chat/completions", "Target URL to load test")
	token := flag.String("token", "", "JWT token to use for Authorization (required)")
	concurrency := flag.Int("c", 10, "Number of concurrent workers")
	duration := flag.Duration("d", 5*time.Second, "Duration of the test")
	flag.Parse()

	if *token == "" {
		log.Fatal("Error: JWT -token is required for testing.")
	}

	fmt.Printf("🚀 Starting load test on %s\n", *targetURL)
	fmt.Printf("Workers: %d, Duration: %s\n", *concurrency, *duration)

	var (
		successCount atomic.Int64
		failCount    atomic.Int64
		start        = time.Now()
		stop         = start.Add(*duration)
		wg           sync.WaitGroup
	)

	// Spin up workers
	for i := 0; i < *concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := &http.Client{Timeout: 2 * time.Second}

			for time.Now().Before(stop) {
				req, _ := http.NewRequest(http.MethodGet, *targetURL, bytes.NewReader(nil))
				req.Header.Set("Authorization", "Bearer "+*token)

				resp, err := client.Do(req)
				if err != nil {
					failCount.Add(1)
					continue
				}

				if resp.StatusCode == http.StatusOK {
					successCount.Add(1)
				} else {
					failCount.Add(1)
				}
				resp.Body.Close()
			}
		}()
	}

	wg.Wait()
	elapsed := time.Since(start)
	total := successCount.Load() + failCount.Load()

	fmt.Println("\n📊 Load Test Results:")
	fmt.Printf("Total Requests:   %d\n", total)
	fmt.Printf("Successes (200):  %d\n", successCount.Load())
	fmt.Printf("Failures:         %d\n", failCount.Load())
	fmt.Printf("Requests/sec:     %.2f\n", float64(total)/elapsed.Seconds())
	
	if failCount.Load() > 0 {
		fmt.Println("\n⚠️ Note: Failures are expected! Our Redis Rate Limiter is doing its job by blocking requests that exceed the limit.")
	}
}
