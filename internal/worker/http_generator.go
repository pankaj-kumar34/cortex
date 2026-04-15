package worker

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pankaj-kumar34/cortex/internal/logger"
	"github.com/pankaj-kumar34/cortex/internal/models"
)

// HTTPGenerator generates HTTP/HTTPS load
type HTTPGenerator struct {
	config      *models.HTTPConfig
	client      *http.Client
	metrics     *HTTPMetrics
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	startTime   time.Time
	patternCalc *LoadPatternCalculator
}

// HTTPMetrics tracks HTTP load test metrics
type HTTPMetrics struct {
	requestsSent      int64
	requestsSucceeded int64
	requestsFailed    int64
	latencies         []time.Duration
	latenciesMu       sync.Mutex
	lastRequestsSent  int64
	lastCheckTime     time.Time
	lastThroughput    float64
	lastCheckMu       sync.Mutex
}

// NewHTTPGenerator creates a new HTTP load generator
func NewHTTPGenerator(config *models.HTTPConfig) *HTTPGenerator {
	// Create HTTP client with custom transport
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second, //nolint:mnd // Standard HTTP idle timeout
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: config.InsecureSkipVerify,
		},
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   config.Timeout,
	}

	if !config.FollowRedirects {
		client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), config.Duration)
	now := time.Now()

	// Initialize load pattern calculator
	// Get RPS from PatternConfig based on pattern type
	targetRPS := 0
	if config.PatternConfig != nil {
		// For constant pattern, use RequestsPerSecond
		// For ramp-up pattern, use TargetRPS
		switch config.LoadPattern {
		case models.PatternConstant:
			targetRPS = config.PatternConfig.RequestsPerSecond
		case models.PatternRampUp:
			targetRPS = config.PatternConfig.TargetRPS
		case models.PatternSpike, models.PatternRepeatingSpike, models.PatternStep:
			// These patterns use TargetRPS from PatternConfig
			targetRPS = config.PatternConfig.TargetRPS
		}
	}

	patternCalc := NewLoadPatternCalculator(
		config.LoadPattern,
		targetRPS,
		config.Duration,
		config.PatternConfig,
	)

	return &HTTPGenerator{
		config: config,
		client: client,
		metrics: &HTTPMetrics{
			latencies:     make([]time.Duration, 0, 10000), //nolint:mnd // Initial capacity for latency tracking
			lastCheckTime: now,
		},
		ctx:         ctx,
		cancel:      cancel,
		startTime:   now,
		patternCalc: patternCalc,
	}
}

// Start begins the HTTP load test
//
//nolint:dupl // Similar to SMTPGenerator.Start but handles HTTP protocol
func (hg *HTTPGenerator) Start() {
	// Use pattern calculator to determine total requests
	totalRequests := hg.patternCalc.GetTotalRequests()
	if totalRequests <= 0 {
		return
	}

	// Send first request immediately
	hg.wg.Add(1)
	go hg.sendRequest()

	if totalRequests == 1 {
		return
	}

	requestCount := int64(1)

	// Use a high-resolution ticker for precise rate control
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	var currentInterval time.Duration
	var nextRequestTime time.Time
	var lastElapsed time.Duration

	for {
		select {
		case <-hg.ctx.Done():
			return
		case now := <-ticker.C:
			// Recalculate interval based on current elapsed time
			elapsed := now.Sub(hg.startTime)

			// Check if we've moved to a new second (for pattern recalculation)
			if elapsed.Truncate(time.Second) != lastElapsed.Truncate(time.Second) {
				newInterval := hg.patternCalc.GetRequestInterval(elapsed)
				if newInterval != currentInterval {
					currentInterval = newInterval
					// Reset next request time when interval changes to avoid using stale timing
					nextRequestTime = now.Add(currentInterval)
				}
				lastElapsed = elapsed
			}

			// Initialize interval on first iteration
			if currentInterval == 0 {
				currentInterval = hg.patternCalc.GetRequestInterval(elapsed)
				nextRequestTime = now.Add(currentInterval)
			}

			// Send request if it's time
			if now.After(nextRequestTime) || now.Equal(nextRequestTime) {
				if requestCount >= totalRequests {
					return
				}
				hg.wg.Add(1)
				go hg.sendRequest()
				requestCount++
				nextRequestTime = nextRequestTime.Add(currentInterval)
			}
		}
	}
}

// Stop stops the HTTP load test
func (hg *HTTPGenerator) Stop() {
	hg.cancel()
	hg.wg.Wait()
}

// sendRequest sends a single HTTP request
func (hg *HTTPGenerator) sendRequest() {
	defer hg.wg.Done()

	// Check if context is already cancelled before attempting to send
	select {
	case <-hg.ctx.Done():
		// Test duration completed, don't send request
		return
	default:
	}

	// Create request with timeout context that respects the parent context
	ctx, cancel := context.WithTimeout(hg.ctx, hg.config.Timeout)
	defer cancel()

	// Create request
	var body io.Reader
	if hg.config.Body != "" {
		body = bytes.NewBufferString(hg.config.Body)
	}

	req, err := http.NewRequestWithContext(ctx, hg.config.Method, hg.config.TargetURL, body)
	if err != nil {
		// Don't count as sent if we couldn't even create the request
		atomic.AddInt64(&hg.metrics.requestsSent, 1)
		atomic.AddInt64(&hg.metrics.requestsFailed, 1)
		logger.Error().
			Err(err).
			Str("url", hg.config.TargetURL).
			Msg("Failed to create HTTP request")
		return
	}

	// Count as sent only after successful request creation
	atomic.AddInt64(&hg.metrics.requestsSent, 1)

	// Add headers
	for key, value := range hg.config.Headers {
		req.Header.Set(key, value)
	}

	// Log request details
	logger.Debug().
		Str("method", req.Method).
		Str("url", req.URL.String()).
		Msg("HTTP request")

	// Send request and measure latency
	start := time.Now()
	resp, err := hg.client.Do(req)
	latency := time.Since(start)

	// Record latency
	hg.metrics.latenciesMu.Lock()
	hg.metrics.latencies = append(hg.metrics.latencies, latency)
	hg.metrics.latenciesMu.Unlock()

	if err != nil {
		atomic.AddInt64(&hg.metrics.requestsFailed, 1)
		logger.Error().
			Err(err).
			Dur("latency_ms", latency).
			Str("url", req.URL.String()).
			Msg("HTTP request failed")
		return
	}
	defer resp.Body.Close()

	// Read and discard response body
	_, _ = io.Copy(io.Discard, resp.Body) //nolint:errcheck // Draining response body, error not critical

	// Log response details
	logger.Debug().
		Int("status", resp.StatusCode).
		Dur("latency_ms", latency).
		Str("url", req.URL.String()).
		Msg("HTTP response")

	// Check status code
	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		atomic.AddInt64(&hg.metrics.requestsSucceeded, 1)
	} else {
		atomic.AddInt64(&hg.metrics.requestsFailed, 1)
	}
}

// GetMetrics returns current metrics
//
//nolint:dupl // Similar to SMTPGenerator.GetMetrics but for HTTP metrics
func (hg *HTTPGenerator) GetMetrics() *models.Metrics {
	hg.metrics.latenciesMu.Lock()
	defer hg.metrics.latenciesMu.Unlock()

	requestsSent := atomic.LoadInt64(&hg.metrics.requestsSent)
	requestsSucceeded := atomic.LoadInt64(&hg.metrics.requestsSucceeded)
	requestsFailed := atomic.LoadInt64(&hg.metrics.requestsFailed)

	metrics := &models.Metrics{
		Timestamp:         time.Now(),
		RequestsSent:      requestsSent,
		RequestsSucceeded: requestsSucceeded,
		RequestsFailed:    requestsFailed,
	}

	// Calculate error rate
	if requestsSent > 0 {
		metrics.ErrorRate = float64(requestsFailed) / float64(requestsSent) * 100
	}

	// Calculate throughput (requests per second) based on recent activity
	hg.metrics.lastCheckMu.Lock()
	now := time.Now()
	timeSinceLastCheck := now.Sub(hg.metrics.lastCheckTime)

	// Check if test is still running
	select {
	case <-hg.ctx.Done():
		// Test has completed, set throughput to 0
		metrics.Throughput = 0
		hg.metrics.lastThroughput = 0
	default:
		// Test is still running, calculate throughput
		if timeSinceLastCheck.Seconds() > 0 {
			requestsSinceLastCheck := requestsSent - hg.metrics.lastRequestsSent
			currentThroughput := float64(requestsSinceLastCheck) / timeSinceLastCheck.Seconds()

			// Only update throughput if we're actively sending requests
			// Otherwise, preserve the last known throughput
			if requestsSinceLastCheck > 0 {
				hg.metrics.lastThroughput = currentThroughput
			}
			metrics.Throughput = hg.metrics.lastThroughput
		}
	}

	hg.metrics.lastRequestsSent = requestsSent
	hg.metrics.lastCheckTime = now
	hg.metrics.lastCheckMu.Unlock()

	// Calculate latency percentiles
	if len(hg.metrics.latencies) > 0 {
		sortedLatencies := make([]time.Duration, len(hg.metrics.latencies))
		copy(sortedLatencies, hg.metrics.latencies)
		slices.Sort(sortedLatencies)

		metrics.LatencyP50 = calculatePercentile(sortedLatencies, 0.50) //nolint:mnd // P50 percentile
		metrics.LatencyP95 = calculatePercentile(sortedLatencies, 0.95) //nolint:mnd // P95 percentile
		metrics.LatencyP99 = calculatePercentile(sortedLatencies, 0.99) //nolint:mnd // P99 percentile
	}

	return metrics
}

// calculatePercentile calculates the percentile value from sorted latencies
func calculatePercentile(sortedLatencies []time.Duration, percentile float64) time.Duration {
	if len(sortedLatencies) == 0 {
		return 0
	}

	index := int(float64(len(sortedLatencies)) * percentile)
	if index >= len(sortedLatencies) {
		index = len(sortedLatencies) - 1
	}

	return sortedLatencies[index]
}

// Made with Bob
