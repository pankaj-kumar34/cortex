package master

import (
	"sync"
	"time"

	"github.com/pankaj-kumar34/cortex/internal/models"
)

// WorkerRegistry manages connected workers
type WorkerRegistry struct {
	workers map[string]*models.WorkerInfo
	mu      sync.RWMutex
}

// NewWorkerRegistry creates a new worker registry
func NewWorkerRegistry() *WorkerRegistry {
	return &WorkerRegistry{
		workers: make(map[string]*models.WorkerInfo),
	}
}

// Register adds a new worker to the registry
func (wr *WorkerRegistry) Register(workerID, address string) {
	wr.mu.Lock()
	defer wr.mu.Unlock()

	wr.workers[workerID] = &models.WorkerInfo{
		ID:            workerID,
		Address:       address,
		ConnectedAt:   time.Now(),
		LastHeartbeat: time.Now(),
		Status:        models.StatusIdle,
	}
}

// Unregister removes a worker from the registry
func (wr *WorkerRegistry) Unregister(workerID string) {
	wr.mu.Lock()
	defer wr.mu.Unlock()

	delete(wr.workers, workerID)
}

// UpdateHeartbeat updates the last heartbeat time for a worker
func (wr *WorkerRegistry) UpdateHeartbeat(workerID string) {
	wr.mu.Lock()
	defer wr.mu.Unlock()

	if worker, exists := wr.workers[workerID]; exists {
		worker.LastHeartbeat = time.Now()
	}
}

// UpdateStatus updates the status of a worker
func (wr *WorkerRegistry) UpdateStatus(workerID string, status models.WorkerStatus) {
	wr.mu.Lock()
	defer wr.mu.Unlock()

	if worker, exists := wr.workers[workerID]; exists {
		worker.Status = status
	}
}

// GetAllWorkers returns a list of all registered workers
func (wr *WorkerRegistry) GetAllWorkers() []*models.WorkerInfo {
	wr.mu.RLock()
	defer wr.mu.RUnlock()

	workers := make([]*models.WorkerInfo, 0, len(wr.workers))
	for _, worker := range wr.workers {
		workers = append(workers, worker)
	}
	return workers
}

// GetActiveWorkers returns a list of active workers
func (wr *WorkerRegistry) GetActiveWorkers() []*models.WorkerInfo {
	wr.mu.RLock()
	defer wr.mu.RUnlock()

	workers := make([]*models.WorkerInfo, 0)
	for _, worker := range wr.workers {
		if worker.Status == models.StatusActive {
			workers = append(workers, worker)
		}
	}
	return workers
}

// Count returns the total number of registered workers
func (wr *WorkerRegistry) Count() int {
	wr.mu.RLock()
	defer wr.mu.RUnlock()

	return len(wr.workers)
}

// ActiveCount returns the number of active workers
func (wr *WorkerRegistry) ActiveCount() int {
	wr.mu.RLock()
	defer wr.mu.RUnlock()

	count := 0
	for _, worker := range wr.workers {
		if worker.Status == models.StatusActive {
			count++
		}
	}
	return count
}

// CheckStaleWorkers identifies workers that haven't sent heartbeats recently
func (wr *WorkerRegistry) CheckStaleWorkers(timeout time.Duration) []string {
	wr.mu.Lock()
	defer wr.mu.Unlock()

	staleWorkers := make([]string, 0)
	now := time.Now()

	for workerID, worker := range wr.workers {
		if now.Sub(worker.LastHeartbeat) > timeout {
			worker.Status = models.StatusDisconnected
			staleWorkers = append(staleWorkers, workerID)
		}
	}

	return staleWorkers
}

// Made with Bob
