package main

import (
	"encoding/json"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Config structure.
type Config struct {
	TargetURL   string `json:"target_url"`
	Concurrency int    `json:"concurrency"`
}

// Global variables.
var (
	config        Config
	configLock    sync.RWMutex
	reqCounter    uint64
	lastModTime   time.Time
	workerWg      sync.WaitGroup
	workersActive bool
	jobQueue      chan struct{} // Controls worker jobs
)

// LoadConfig reads `config.json` only if modified.
func LoadConfig() bool {
	fileInfo, err := os.Stat("config.json")
	if err != nil {
		log.Println("Failed to open config file:", err)
		return false
	}

	// Skip reloading if file hasn't changed
	if fileInfo.ModTime().Equal(lastModTime) {
		return false
	}

	file, err := os.Open("config.json")
	if err != nil {
		log.Println("Failed to read config file:", err)
		return false
	}
	defer file.Close()

	var newConfig Config
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&newConfig); err != nil {
		log.Println("Failed to parse config file:", err)
		return false
	}

	// Update config if it changed
	configLock.Lock()
	config = newConfig
	configLock.Unlock()

	lastModTime = fileInfo.ModTime()
	log.Println("Config updated:", config)
	return true
}

// Worker function that sends HTTP requests.
func worker(client *http.Client, wg *sync.WaitGroup) {
	defer wg.Done()

	for range jobQueue {
		configLock.RLock()
		url := config.TargetURL
		configLock.RUnlock()

		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			log.Println("Request creation failed:", err)
			continue
		}

		// Retry logic
		maxRetries := 3
		for i := 0; i < maxRetries; i++ {
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
				atomic.AddUint64(&reqCounter, 1)
				break // Exit retry loop if request succeeds
			}

			log.Printf("Request failed (attempt %d): %v\n", i+1, err)
			time.Sleep(time.Duration(rand.Intn(100)) * time.Millisecond) // Exponential backoff
		}

		// Introduce a slight random delay to avoid bulk failures
		time.Sleep(time.Duration(rand.Intn(10)) * time.Millisecond)
	}
}

// MonitorConfig checks for config updates every 5 seconds
func MonitorConfig() {
	ticker := time.NewTicker(5 * time.Second)
	for range ticker.C {
		if LoadConfig() {
			RestartWorkers()
		}
	}
}

// RestartWorkers will restart the workers if concurrency changes.
func RestartWorkers() {
	// Stop current workers
	if workersActive {
		log.Println("Stopping workers...")
		close(jobQueue) // Close the job channel so old workers exit
		workerWg.Wait()
	}

	// Start new workers
	log.Println("Starting new workers:", config.Concurrency)
	client := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        config.Concurrency,
			MaxIdleConnsPerHost: config.Concurrency,
			DisableKeepAlives:   false, // Keep-alive should be enabled
			DialContext: (&net.Dialer{
				Timeout:   2 * time.Second, // Keep connection timeout low
				KeepAlive: 30 * time.Second,
			}).DialContext,
		},
		Timeout: 10 * time.Second, // Adjust timeout to prevent excessive failures
	}

	workerWg = sync.WaitGroup{}
	workersActive = true
	jobQueue = make(chan struct{}, config.Concurrency)

	for i := 0; i < config.Concurrency; i++ {
		workerWg.Add(1)
		go worker(client, &workerWg)
	}

	go func() {
		for {
			jobQueue <- struct{}{} // Properly enqueue jobs
		}
	}()
}

// LogStats logs requests every second and tracks 100K request time.
func LogStats() {
	ticker := time.NewTicker(1 * time.Second)
	startTime := time.Now()

	for range ticker.C {
		count := atomic.SwapUint64(&reqCounter, 0)
		elapsed := time.Since(startTime).Seconds()

		log.Printf("Requests in last second: %d\n", count)

		if count >= 100000 {
			log.Printf("Time taken for last 100k requests: %.2f seconds\n", elapsed)
			startTime = time.Now()
		}
	}
}

func main() {
	// Load initial config
	LoadConfig()

	// Start monitoring. Reload workers when config changes.
	go MonitorConfig()

	// Start logging stats
	go LogStats()

	// Start workers
	RestartWorkers()

	// Keep the main routine alive
	select {}
}
