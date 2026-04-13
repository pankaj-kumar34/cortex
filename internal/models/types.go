package models

import "time"

// TestProtocol represents the type of load test
type TestProtocol string

const (
	ProtocolHTTP  TestProtocol = "http"
	ProtocolHTTPS TestProtocol = "https"
	ProtocolSMTP  TestProtocol = "smtp"
)

// LoadPattern represents the traffic pattern for load generation
type LoadPattern string

const (
	PatternConstant       LoadPattern = "constant"        // Fixed RPS throughout
	PatternRampUp         LoadPattern = "ramp-up"         // Gradually increase to target RPS
	PatternSpike          LoadPattern = "spike"           // Single burst of traffic at start
	PatternRepeatingSpike LoadPattern = "repeating-spike" // Multiple bursts throughout test
	PatternStep           LoadPattern = "step"            // Incremental increases in steps
)

// WorkerStatus represents the current state of a worker
type WorkerStatus string

const (
	StatusActive       WorkerStatus = "active"
	StatusIdle         WorkerStatus = "idle"
	StatusDisconnected WorkerStatus = "disconnected"
)

// WorkerInfo contains information about a connected worker
type WorkerInfo struct {
	ID            string       `json:"id"`
	Address       string       `json:"address"`
	ConnectedAt   time.Time    `json:"connected_at"`
	LastHeartbeat time.Time    `json:"last_heartbeat"`
	Status        WorkerStatus `json:"status"`
}

// Metrics represents performance metrics from a worker
type Metrics struct {
	WorkerID          string        `json:"worker_id"`
	Timestamp         time.Time     `json:"timestamp"`
	RequestsSent      int64         `json:"requests_sent"`
	RequestsSucceeded int64         `json:"requests_succeeded"`
	RequestsFailed    int64         `json:"requests_failed"`
	LatencyP50        time.Duration `json:"latency_p50"`
	LatencyP95        time.Duration `json:"latency_p95"`
	LatencyP99        time.Duration `json:"latency_p99"`
	ErrorRate         float64       `json:"error_rate"`
	Throughput        float64       `json:"throughput"` // requests per second
}

// AggregatedMetrics represents combined metrics from all workers
type AggregatedMetrics struct {
	Timestamp         time.Time     `json:"timestamp"`
	TotalWorkers      int           `json:"total_workers"`
	ActiveWorkers     int           `json:"active_workers"`
	TestRunning       bool          `json:"test_running"`
	TestStartedAt     time.Time     `json:"test_started_at"`
	RequestsSent      int64         `json:"requests_sent"`
	RequestsSucceeded int64         `json:"requests_succeeded"`
	RequestsFailed    int64         `json:"requests_failed"`
	LatencyP50        time.Duration `json:"latency_p50"`
	LatencyP95        time.Duration `json:"latency_p95"`
	LatencyP99        time.Duration `json:"latency_p99"`
	ErrorRate         float64       `json:"error_rate"`
	Throughput        float64       `json:"throughput"` // total requests per second
}

// TestCommand represents a command sent from master to worker
type TestCommand struct {
	Action   string       `json:"action"` // start, stop, status
	Protocol TestProtocol `json:"protocol"`
	Config   any          `json:"config"` // HTTPConfig or SMTPConfig
}

// HTTPConfig contains configuration for HTTP/HTTPS load tests
type HTTPConfig struct {
	TargetURL          string            `json:"target_url"`
	Method             string            `json:"method"`
	Headers            map[string]string `json:"headers"`
	Body               string            `json:"body"`
	Timeout            time.Duration     `json:"timeout"`
	Duration           time.Duration     `json:"duration"`
	FollowRedirects    bool              `json:"follow_redirects"`
	InsecureSkipVerify bool              `json:"insecure_skip_verify"`
	LoadPattern        LoadPattern       `json:"load_pattern,omitempty"`
	PatternConfig      *PatternConfig    `json:"pattern_config,omitempty"`
}

// SMTPConfig contains configuration for SMTP load tests
type SMTPConfig struct {
	Host               string         `json:"host"`
	Port               int            `json:"port"`
	From               string         `json:"from"`
	To                 []string       `json:"to"`
	Subject            string         `json:"subject"`
	Body               string         `json:"body"`
	Duration           time.Duration  `json:"duration"`
	UseTLS             bool           `json:"use_tls"`
	Username           string         `json:"username"`
	Password           string         `json:"password"`
	InsecureSkipVerify bool           `json:"insecure_skip_verify"`
	LoadPattern        LoadPattern    `json:"load_pattern,omitempty"`
	PatternConfig      *PatternConfig `json:"pattern_config,omitempty"`
}

// PatternConfig contains configuration for different load patterns
type PatternConfig struct {
	// Constant pattern
	RequestsPerSecond int `json:"requests_per_second,omitempty"` // RPS for constant pattern

	// Ramp-Up pattern
	RampUpDuration   time.Duration `json:"ramp_up_duration,omitempty"`   // Time to reach target RPS
	RampDownDuration time.Duration `json:"ramp_down_duration,omitempty"` // Time to ramp down from target
	StartRPS         int           `json:"start_rps,omitempty"`          // Starting RPS (default: 0)
	TargetRPS        int           `json:"target_rps,omitempty"`         // Target RPS to ramp up to

	// Spike pattern
	SpikeDuration time.Duration `json:"spike_duration,omitempty"` // Duration of spike
	SpikeRPS      int           `json:"spike_rps,omitempty"`      // RPS during spike
	BaseRPS       int           `json:"base_rps,omitempty"`       // Base RPS before/after spike

	// Step pattern
	StepDuration  time.Duration `json:"step_duration,omitempty"`  // Duration of each step
	StepIncrement int           `json:"step_increment,omitempty"` // RPS increase per step
	StepCount     int           `json:"step_count,omitempty"`     // Number of steps
}

// DashboardPatternConfig contains pattern configuration with string durations for dashboard API
type DashboardPatternConfig struct {
	// Constant pattern
	RequestsPerSecond int `json:"requests_per_second,omitempty"`

	// Ramp-Up pattern
	RampUpDuration   string `json:"ramp_up_duration,omitempty"`
	RampDownDuration string `json:"ramp_down_duration,omitempty"`
	StartRPS         int    `json:"start_rps,omitempty"`
	TargetRPS        int    `json:"target_rps,omitempty"`

	// Spike pattern
	SpikeDuration string `json:"spike_duration,omitempty"`
	SpikeRPS      int    `json:"spike_rps,omitempty"`
	BaseRPS       int    `json:"base_rps,omitempty"`

	// Step pattern
	StepDuration  string `json:"step_duration,omitempty"`
	StepIncrement int    `json:"step_increment,omitempty"`
	StepCount     int    `json:"step_count,omitempty"`
}

// WorkerRegistration represents a worker registration request
type WorkerRegistration struct {
	WorkerID string `json:"worker_id"`
	Token    string `json:"token"`
	Address  string `json:"address"`
}

// WorkerRegistrationResponse represents the response to a registration request
type WorkerRegistrationResponse struct {
	Success  bool   `json:"success"`
	Message  string `json:"message"`
	WorkerID string `json:"worker_id"`
}

// HeartbeatMessage represents a heartbeat from worker to master
type HeartbeatMessage struct {
	WorkerID  string       `json:"worker_id"`
	Timestamp time.Time    `json:"timestamp"`
	Status    WorkerStatus `json:"status"`
}

// DashboardConfigPayload represents editable dashboard configuration.
type DashboardConfigPayload struct {
	Master       DashboardMasterConfig          `json:"master"`
	ActiveTestID string                         `json:"active_test_id"`
	Tests        map[string]DashboardTestConfig `json:"tests"`
}

// DashboardMasterConfig contains editable master settings.
type DashboardMasterConfig struct {
	Port          int `json:"port"`
	DashboardPort int `json:"dashboard_port"`
}

// DashboardTestConfig contains editable test settings.
type DashboardTestConfig struct {
	Protocol string                      `json:"protocol"`
	Duration string                      `json:"duration"`
	HTTP     *DashboardHTTPConfigPayload `json:"http,omitempty"`
	SMTP     *DashboardSMTPConfigPayload `json:"smtp,omitempty"`
}

// DashboardHTTPConfigPayload contains editable HTTP/HTTPS settings.
type DashboardHTTPConfigPayload struct {
	TargetURL          string                  `json:"target_url"`
	Method             string                  `json:"method"`
	Headers            map[string]string       `json:"headers"`
	Body               string                  `json:"body"`
	Timeout            string                  `json:"timeout"`
	FollowRedirects    bool                    `json:"follow_redirects"`
	InsecureSkipVerify bool                    `json:"insecure_skip_verify"`
	LoadPattern        LoadPattern             `json:"load_pattern"`
	PatternConfig      *DashboardPatternConfig `json:"pattern_config,omitempty"`
}

// DashboardSMTPConfigPayload contains editable SMTP settings.
type DashboardSMTPConfigPayload struct {
	Host               string                  `json:"host"`
	Port               int                     `json:"port"`
	From               string                  `json:"from"`
	To                 []string                `json:"to"`
	Subject            string                  `json:"subject"`
	Body               string                  `json:"body"`
	UseTLS             bool                    `json:"use_tls"`
	Username           string                  `json:"username"`
	Password           string                  `json:"password"`
	InsecureSkipVerify bool                    `json:"insecure_skip_verify"`
	LoadPattern        LoadPattern             `json:"load_pattern"`
	PatternConfig      *DashboardPatternConfig `json:"pattern_config,omitempty"`
}

// DashboardActiveTestPayload represents an active test selection request.
type DashboardActiveTestPayload struct {
	ActiveTestID string `json:"active_test_id"`
}

// DashboardTestMutationPayload represents create/update requests for a single test.
type DashboardTestMutationPayload struct {
	ID   string              `json:"id"`
	Test DashboardTestConfig `json:"test"`
}

// Made with Bob
