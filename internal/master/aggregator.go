package master

import (
	"slices"
	"sync"
	"time"

	"github.com/pankaj-kumar34/cortex/internal/models"
)

// MetricsAggregator collects and aggregates metrics from all workers
type MetricsAggregator struct {
	workerMetrics    map[string]*models.Metrics
	mu               sync.RWMutex
	registry         *WorkerRegistry
	lastThroughput   float64
	lastThroughputMu sync.RWMutex
}

// NewMetricsAggregator creates a new metrics aggregator
func NewMetricsAggregator(registry *WorkerRegistry) *MetricsAggregator {
	return &MetricsAggregator{
		workerMetrics: make(map[string]*models.Metrics),
		registry:      registry,
	}
}

// UpdateMetrics updates metrics for a specific worker
func (ma *MetricsAggregator) UpdateMetrics(metrics *models.Metrics) {
	ma.mu.Lock()
	defer ma.mu.Unlock()

	ma.workerMetrics[metrics.WorkerID] = metrics
}

// GetWorkerMetrics retrieves metrics for a specific worker
func (ma *MetricsAggregator) GetWorkerMetrics(workerID string) (*models.Metrics, bool) {
	ma.mu.RLock()
	defer ma.mu.RUnlock()

	metrics, exists := ma.workerMetrics[workerID]
	return metrics, exists
}

// GetAggregatedMetrics calculates aggregated metrics from all workers
func (ma *MetricsAggregator) GetAggregatedMetrics() *models.AggregatedMetrics {
	ma.mu.RLock()
	defer ma.mu.RUnlock()

	aggregated := &models.AggregatedMetrics{
		Timestamp:     time.Now(),
		TotalWorkers:  ma.registry.Count(),
		ActiveWorkers: ma.registry.ActiveCount(),
	}

	if len(ma.workerMetrics) == 0 {
		return aggregated
	}

	// Collect all latencies for percentile calculation
	var latenciesP50 []time.Duration
	var latenciesP95 []time.Duration
	var latenciesP99 []time.Duration

	// Aggregate metrics from all workers
	for _, metrics := range ma.workerMetrics {
		aggregated.RequestsSent += metrics.RequestsSent
		aggregated.RequestsSucceeded += metrics.RequestsSucceeded
		aggregated.RequestsFailed += metrics.RequestsFailed

		// Sum throughput from all workers (will be 0 if test is stopped)
		aggregated.Throughput += metrics.Throughput

		latenciesP50 = append(latenciesP50, metrics.LatencyP50)
		latenciesP95 = append(latenciesP95, metrics.LatencyP95)
		latenciesP99 = append(latenciesP99, metrics.LatencyP99)
	}

	// Calculate error rate
	if aggregated.RequestsSent > 0 {
		aggregated.ErrorRate = float64(aggregated.RequestsFailed) / float64(aggregated.RequestsSent) * 100
	}

	// Calculate aggregated latency percentiles (median of worker percentiles)
	aggregated.LatencyP50 = calculateMedianDuration(latenciesP50)
	aggregated.LatencyP95 = calculateMedianDuration(latenciesP95)
	aggregated.LatencyP99 = calculateMedianDuration(latenciesP99)

	return aggregated
}

// GetAllWorkerMetrics returns metrics for all workers
func (ma *MetricsAggregator) GetAllWorkerMetrics() map[string]*models.Metrics {
	ma.mu.RLock()
	defer ma.mu.RUnlock()

	// Create a copy to avoid race conditions
	metrics := make(map[string]*models.Metrics, len(ma.workerMetrics))
	for k, v := range ma.workerMetrics {
		metricsCopy := *v
		metrics[k] = &metricsCopy
	}

	return metrics
}

// Reset clears all collected metrics
func (ma *MetricsAggregator) Reset() {
	ma.mu.Lock()
	defer ma.mu.Unlock()

	ma.workerMetrics = make(map[string]*models.Metrics)

	ma.lastThroughputMu.Lock()
	ma.lastThroughput = 0
	ma.lastThroughputMu.Unlock()
}

// RemoveWorkerMetrics removes metrics for a specific worker
func (ma *MetricsAggregator) RemoveWorkerMetrics(workerID string) {
	ma.mu.Lock()
	defer ma.mu.Unlock()

	delete(ma.workerMetrics, workerID)
}

// calculateMedianDuration calculates the median of a slice of durations
func calculateMedianDuration(durations []time.Duration) time.Duration {
	if len(durations) == 0 {
		return 0
	}

	// Sort durations
	sorted := make([]time.Duration, len(durations))
	copy(sorted, durations)
	slices.Sort(sorted)

	// Calculate median
	mid := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[mid-1] + sorted[mid]) / 2
	}
	return sorted[mid]
}

// Made with Bob
