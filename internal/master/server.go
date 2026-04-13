package master

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pankaj-kumar34/cortex/internal/auth"
	"github.com/pankaj-kumar34/cortex/internal/config"
	"github.com/pankaj-kumar34/cortex/internal/logger"
	"github.com/pankaj-kumar34/cortex/internal/models"
)

//go:embed index.html
var webApp embed.FS

var upgrader = websocket.Upgrader{
	CheckOrigin: func(_ *http.Request) bool {
		return true // Allow all origins for now
	},
}

// Server represents the master server
type Server struct {
	config           *config.Config
	configFilePath   string
	configMu         sync.RWMutex
	tokenValidator   *auth.TokenValidator
	registry         *WorkerRegistry
	aggregator       *MetricsAggregator
	workerConns      map[string]*websocket.Conn
	workerConnsMu    sync.RWMutex
	testRunning      bool
	testRunningMu    sync.RWMutex
	testStartedAt    time.Time
	currentTestCmd   *models.TestCommand
	currentTestMu    sync.RWMutex
	idleStartTime    time.Time
	idleStartMu      sync.RWMutex
	dashboardClients map[*websocket.Conn]bool
	dashboardMu      sync.RWMutex
}

// NewServer creates a new master server
func NewServer(cfg *config.Config, configFilePath string) *Server {
	registry := NewWorkerRegistry()
	return &Server{
		config:           cfg,
		configFilePath:   configFilePath,
		tokenValidator:   auth.NewTokenValidator(cfg.Master.SharedToken),
		registry:         registry,
		aggregator:       NewMetricsAggregator(registry),
		workerConns:      make(map[string]*websocket.Conn),
		dashboardClients: make(map[*websocket.Conn]bool),
	}
}

// Start starts the master server
func (s *Server) Start() error {
	// Start worker server
	http.HandleFunc("/worker", s.handleWorkerConnection)

	// Start dashboard server
	http.HandleFunc("/dashboard", s.handleDashboardConnection)
	http.HandleFunc("/api/metrics", s.handleMetricsAPI)
	http.HandleFunc("/api/workers", s.handleWorkersAPI)
	http.HandleFunc("/api/config", s.handleConfigAPI)
	http.HandleFunc("/api/config/active-test", s.handleActiveTestAPI)
	http.HandleFunc("/api/config/tests", s.handleTestsAPI)
	http.HandleFunc("/api/config/tests/", s.handleTestByIDAPI)
	http.HandleFunc("/api/test/start", s.handleStartTestAPI)
	http.HandleFunc("/api/test/stop", s.handleStopTestAPI)
	http.HandleFunc("/", s.handleDashboardPage)

	// Start heartbeat checker
	go s.heartbeatChecker()

	// Start metrics broadcaster
	go s.metricsBroadcaster()

	// Start both servers with timeouts
	const (
		readTimeout  = 15 * time.Second
		writeTimeout = 15 * time.Second
		idleTimeout  = 60 * time.Second
	)

	go func() {
		addr := fmt.Sprintf(":%d", s.config.Master.DashboardPort)
		dashboardServer := &http.Server{
			Addr:         addr,
			ReadTimeout:  readTimeout,
			WriteTimeout: writeTimeout,
			IdleTimeout:  idleTimeout,
		}
		if err := dashboardServer.ListenAndServe(); err != nil {
			logger.Fatal().Err(err).Msg("Dashboard server failed")
		}
	}()

	addr := fmt.Sprintf(":%d", s.config.Master.Port)
	masterServer := &http.Server{
		Addr:         addr,
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
		IdleTimeout:  idleTimeout,
	}
	return masterServer.ListenAndServe()
}

// handleWorkerConnection handles WebSocket connections from workers
func (s *Server) handleWorkerConnection(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to upgrade connection")
		return
	}

	// Read registration message
	var reg models.WorkerRegistration
	if err := conn.ReadJSON(&reg); err != nil {
		logger.Error().Err(err).Msg("Failed to read registration")
		conn.Close()
		return
	}

	// Validate token
	if err := s.tokenValidator.Validate(reg.Token); err != nil {
		response := models.WorkerRegistrationResponse{
			Success: false,
			Message: "Invalid authentication token",
		}
		if err := conn.WriteJSON(response); err != nil {
			logger.Error().Err(err).Msg("Failed to send error response")
		}
		conn.Close()
		return
	}

	// Register worker
	s.registry.Register(reg.WorkerID, reg.Address)
	s.workerConnsMu.Lock()
	s.workerConns[reg.WorkerID] = conn
	s.workerConnsMu.Unlock()

	// Send success response
	response := models.WorkerRegistrationResponse{
		Success:  true,
		Message:  "Registration successful",
		WorkerID: reg.WorkerID,
	}
	if err := conn.WriteJSON(response); err != nil {
		logger.Error().Err(err).Msg("Failed to send registration response")
		conn.Close()
		return
	}

	logger.Info().
		Str("worker_id", reg.WorkerID).
		Str("address", reg.Address).
		Msg("Worker registered successfully")

	// If a test is currently running, send the test command to the new worker
	s.testRunningMu.RLock()
	s.currentTestMu.RLock()
	if s.testRunning && s.currentTestCmd != nil {
		logger.Info().
			Str("worker_id", reg.WorkerID).
			Msg("Sending active test to newly joined worker")

		// Calculate remaining duration for late-joining worker
		elapsed := time.Since(s.testStartedAt)
		adjustedCmd := *s.currentTestCmd // Create a copy

		// Adjust duration based on protocol
		switch adjustedCmd.Protocol {
		case models.ProtocolHTTP, models.ProtocolHTTPS:
			if httpConfig, ok := adjustedCmd.Config.(*models.HTTPConfig); ok {
				originalDuration := httpConfig.Duration
				remainingDuration := originalDuration - elapsed

				// Only send command if there's time remaining
				if remainingDuration > 0 {
					// Create adjusted config with remaining duration
					adjustedHTTPConfig := *httpConfig
					adjustedHTTPConfig.Duration = remainingDuration
					adjustedCmd.Config = &adjustedHTTPConfig

					logger.Info().
						Str("worker_id", reg.WorkerID).
						Dur("original_duration", originalDuration).
						Dur("elapsed", elapsed).
						Dur("remaining_duration", remainingDuration).
						Msg("Adjusted test duration for late-joining worker")

					if err := conn.WriteJSON(adjustedCmd); err != nil {
						logger.Error().
							Err(err).
							Str("worker_id", reg.WorkerID).
							Msg("Failed to send test command")
					} else {
						s.registry.UpdateStatus(reg.WorkerID, models.StatusActive)
					}
				} else {
					logger.Warn().
						Str("worker_id", reg.WorkerID).
						Dur("elapsed", elapsed).
						Msg("Test already completed, not sending command to late-joining worker")
				}
			}
		case models.ProtocolSMTP:
			if smtpConfig, ok := adjustedCmd.Config.(*models.SMTPConfig); ok {
				originalDuration := smtpConfig.Duration
				remainingDuration := originalDuration - elapsed

				// Only send command if there's time remaining
				if remainingDuration > 0 {
					// Create adjusted config with remaining duration
					adjustedSMTPConfig := *smtpConfig
					adjustedSMTPConfig.Duration = remainingDuration
					adjustedCmd.Config = &adjustedSMTPConfig

					logger.Info().
						Str("worker_id", reg.WorkerID).
						Dur("original_duration", originalDuration).
						Dur("elapsed", elapsed).
						Dur("remaining_duration", remainingDuration).
						Msg("Adjusted test duration for late-joining worker")

					if err := conn.WriteJSON(adjustedCmd); err != nil {
						logger.Error().
							Err(err).
							Str("worker_id", reg.WorkerID).
							Msg("Failed to send test command")
					} else {
						s.registry.UpdateStatus(reg.WorkerID, models.StatusActive)
					}
				} else {
					logger.Warn().
						Str("worker_id", reg.WorkerID).
						Dur("elapsed", elapsed).
						Msg("Test already completed, not sending command to late-joining worker")
				}
			}
		}
	}
	s.currentTestMu.RUnlock()
	s.testRunningMu.RUnlock()

	// Handle worker messages
	go s.handleWorkerMessages(reg.WorkerID, conn)
}

// handleWorkerMessages processes messages from a worker
func (s *Server) handleWorkerMessages(workerID string, conn *websocket.Conn) {
	defer func() {
		s.workerConnsMu.Lock()
		delete(s.workerConns, workerID)
		s.workerConnsMu.Unlock()
		s.registry.Unregister(workerID)
		s.aggregator.RemoveWorkerMetrics(workerID)
		conn.Close()
		logger.Info().
			Str("worker_id", workerID).
			Msg("Worker disconnected")
	}()

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				logger.Error().Err(err).Str("worker_id", workerID).Msg("WebSocket error from worker")
			}
			break
		}

		// Parse message type
		var msgType struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(message, &msgType); err != nil {
			logger.Error().Err(err).Msg("Failed to parse message type")
			continue
		}

		switch msgType.Type {
		case "heartbeat":
			var hb models.HeartbeatMessage
			if err := json.Unmarshal(message, &hb); err != nil {
				logger.Error().Err(err).Msg("Failed to parse heartbeat")
				continue
			}
			s.registry.UpdateHeartbeat(workerID)
			s.registry.UpdateStatus(workerID, hb.Status)

		case "metrics":
			var metrics models.Metrics
			if err := json.Unmarshal(message, &metrics); err != nil {
				logger.Error().Err(err).Msg("Failed to parse metrics")
				continue
			}
			s.aggregator.UpdateMetrics(&metrics)
		}
	}
}

func (s *Server) getConfig() *config.Config {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.config.Clone()
}

func (s *Server) setConfig(cfg *config.Config) {
	s.configMu.Lock()
	s.config = cfg.Clone()
	s.tokenValidator = auth.NewTokenValidator(s.config.Master.SharedToken)
	s.configMu.Unlock()
}

// StartTest sends test command to all workers
func (s *Server) StartTest() error {
	s.testRunningMu.Lock()
	if s.testRunning {
		s.testRunningMu.Unlock()
		return errors.New("test already running")
	}
	s.testRunning = true
	s.testStartedAt = time.Now()
	s.testRunningMu.Unlock()

	// Reset metrics
	s.aggregator.Reset()

	// Prepare test command
	var testCmd models.TestCommand
	testCmd.Action = "start"
	cfg := s.getConfig()
	activeTest, err := cfg.GetActiveTest()
	if err != nil {
		s.testRunningMu.Lock()
		s.testRunning = false
		s.testStartedAt = time.Time{}
		s.testRunningMu.Unlock()
		return fmt.Errorf("failed to get active test: %w", err)
	}
	testCmd.Protocol = models.TestProtocol(activeTest.Protocol)

	switch testCmd.Protocol {
	case models.ProtocolHTTP, models.ProtocolHTTPS:
		httpConfig, err := cfg.GetHTTPConfig()
		if err != nil {
			s.testRunningMu.Lock()
			s.testRunning = false
			s.testStartedAt = time.Time{}
			s.testRunningMu.Unlock()
			return fmt.Errorf("failed to get HTTP config: %w", err)
		}
		testCmd.Config = httpConfig
	case models.ProtocolSMTP:
		smtpConfig, err := cfg.GetSMTPConfig()
		if err != nil {
			s.testRunningMu.Lock()
			s.testRunning = false
			s.testStartedAt = time.Time{}
			s.testRunningMu.Unlock()
			return fmt.Errorf("failed to get SMTP config: %w", err)
		}
		testCmd.Config = smtpConfig
	}

	// Store current test command for late-joining workers
	s.currentTestMu.Lock()
	s.currentTestCmd = &testCmd
	s.currentTestMu.Unlock()

	// Send command to all workers
	s.workerConnsMu.RLock()
	defer s.workerConnsMu.RUnlock()

	for workerID, conn := range s.workerConns {
		if err := conn.WriteJSON(testCmd); err != nil {
			logger.Error().
				Err(err).
				Str("worker_id", workerID).
				Msg("Failed to send test command")
		} else {
			s.registry.UpdateStatus(workerID, models.StatusActive)
			logger.Debug().
				Str("worker_id", workerID).
				Msg("Test command sent")
		}
	}

	logger.Info().
		Int("worker_count", len(s.workerConns)).
		Msg("Test started on all connected workers")
	return nil
}

// StopTest sends stop command to all workers
func (s *Server) StopTest() error {
	s.testRunningMu.Lock()
	defer s.testRunningMu.Unlock()

	if !s.testRunning {
		return errors.New("no test running")
	}

	testCmd := models.TestCommand{
		Action: "stop",
	}

	s.workerConnsMu.RLock()
	defer s.workerConnsMu.RUnlock()

	for workerID, conn := range s.workerConns {
		if err := conn.WriteJSON(testCmd); err != nil {
			logger.Error().
				Err(err).
				Str("worker_id", workerID).
				Msg("Failed to send stop command")
		} else {
			s.registry.UpdateStatus(workerID, models.StatusIdle)
		}
	}

	s.testRunning = false
	s.testStartedAt = time.Time{}

	// Clear current test command
	s.currentTestMu.Lock()
	s.currentTestCmd = nil
	s.currentTestMu.Unlock()

	logger.Info().
		Int("worker_count", len(s.workerConns)).
		Msg("Test stopped on all workers")
	return nil
}

// heartbeatChecker periodically checks for stale workers
func (s *Server) heartbeatChecker() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		staleWorkers := s.registry.CheckStaleWorkers(30 * time.Second) //nolint:mnd // Stale worker timeout
		for _, workerID := range staleWorkers {
			logger.Warn().
				Str("worker_id", workerID).
				Msg("Worker heartbeat timeout - marking as disconnected")
			s.workerConnsMu.Lock()
			if conn, exists := s.workerConns[workerID]; exists {
				conn.Close()
				delete(s.workerConns, workerID)
			}
			s.workerConnsMu.Unlock()
		}
	}
}

// metricsBroadcaster periodically broadcasts metrics to dashboard clients
func (s *Server) metricsBroadcaster() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		metrics := s.aggregator.GetAggregatedMetrics()

		// Auto-detect test completion when all workers go idle for 3+ seconds
		s.testRunningMu.Lock()
		if s.testRunning && metrics.ActiveWorkers == 0 && metrics.TotalWorkers > 0 {
			s.idleStartMu.Lock()
			if s.idleStartTime.IsZero() {
				// First time seeing all workers idle
				s.idleStartTime = time.Now()
			} else if time.Since(s.idleStartTime) > 1*time.Second {
				// Workers have been idle for 1+ second, test is complete
				s.testRunning = false
				s.testStartedAt = time.Time{}
				s.idleStartTime = time.Time{} // Reset

				// Clear current test command
				s.currentTestMu.Lock()
				s.currentTestCmd = nil
				s.currentTestMu.Unlock()

				logger.Info().Msg("Test completed - all workers idle")
			}
			s.idleStartMu.Unlock()
		} else {
			// Reset idle timer if workers are active
			s.idleStartMu.Lock()
			s.idleStartTime = time.Time{}
			s.idleStartMu.Unlock()
		}
		metrics.TestRunning = s.testRunning
		metrics.TestStartedAt = s.testStartedAt
		s.testRunningMu.Unlock()

		s.broadcastToDashboard(metrics)
	}
}

// broadcastToDashboard sends data to all connected dashboard clients
func (s *Server) broadcastToDashboard(data any) {
	s.dashboardMu.Lock()
	defer s.dashboardMu.Unlock()

	for client := range s.dashboardClients {
		if err := client.WriteJSON(data); err != nil {
			logger.Error().Err(err).Msg("Failed to send to dashboard client")
			client.Close()
			delete(s.dashboardClients, client)
		}
	}
}

// handleDashboardConnection handles WebSocket connections from dashboard
func (s *Server) handleDashboardConnection(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to upgrade dashboard connection")
		return
	}

	s.dashboardMu.Lock()
	s.dashboardClients[conn] = true
	s.dashboardMu.Unlock()

	logger.Debug().Msg("Dashboard client connected")

	// Send initial metrics
	metrics := s.aggregator.GetAggregatedMetrics()
	s.testRunningMu.RLock()
	metrics.TestRunning = s.testRunning
	s.testRunningMu.RUnlock()
	if err := conn.WriteJSON(metrics); err != nil {
		logger.Error().Err(err).Msg("Failed to send metrics to dashboard")
	}

	// Keep connection alive and handle disconnection
	for {
		if _, _, err := conn.ReadMessage(); err != nil { //nolint:gocritic // Checking for connection errors
			s.dashboardMu.Lock()
			delete(s.dashboardClients, conn)
			s.dashboardMu.Unlock()
			conn.Close()
			logger.Debug().Msg("Dashboard client disconnected")
			break
		}
	}
}

func configToDashboardPayload(cfg *config.Config) *models.DashboardConfigPayload {
	payload := &models.DashboardConfigPayload{
		Master: models.DashboardMasterConfig{
			Port:          cfg.Master.Port,
			DashboardPort: cfg.Master.DashboardPort,
		},
		ActiveTestID: cfg.ActiveTestID,
		Tests:        make(map[string]models.DashboardTestConfig, len(cfg.Tests)),
	}

	for testID, testCfg := range cfg.Tests {
		if testCfg == nil {
			continue
		}

		dashboardTest := models.DashboardTestConfig{
			Protocol: testCfg.Protocol,
			Duration: testCfg.Duration,
		}

		if testCfg.HTTP != nil {
			loadPattern, patternConfig := configPatternToDashboard(testCfg.HTTP.LoadPattern, testCfg.HTTP.PatternConfig)
			dashboardTest.HTTP = &models.DashboardHTTPConfigPayload{
				TargetURL:          testCfg.HTTP.TargetURL,
				Method:             testCfg.HTTP.Method,
				Headers:            cloneHeaders(testCfg.HTTP.Headers),
				Body:               testCfg.HTTP.Body,
				Timeout:            testCfg.HTTP.Timeout,
				FollowRedirects:    testCfg.HTTP.FollowRedirects,
				InsecureSkipVerify: testCfg.HTTP.InsecureSkipVerify,
				LoadPattern:        loadPattern,
				PatternConfig:      patternConfig,
			}
		}

		if testCfg.SMTP != nil {
			loadPattern, patternConfig := configPatternToDashboard(testCfg.SMTP.LoadPattern, testCfg.SMTP.PatternConfig)
			dashboardTest.SMTP = &models.DashboardSMTPConfigPayload{
				Host:               testCfg.SMTP.Host,
				Port:               testCfg.SMTP.Port,
				From:               testCfg.SMTP.From,
				To:                 append([]string(nil), testCfg.SMTP.To...),
				Subject:            testCfg.SMTP.Subject,
				Body:               testCfg.SMTP.Body,
				UseTLS:             testCfg.SMTP.UseTLS,
				Username:           testCfg.SMTP.Username,
				Password:           testCfg.SMTP.Password,
				InsecureSkipVerify: testCfg.SMTP.InsecureSkipVerify,
				LoadPattern:        loadPattern,
				PatternConfig:      patternConfig,
			}
		}

		payload.Tests[testID] = dashboardTest
	}

	return payload
}

func cloneHeaders(headers map[string]string) map[string]string {
	if headers == nil {
		return map[string]string{}
	}

	cloned := make(map[string]string, len(headers))
	maps.Copy(cloned, headers)
	return cloned
}

//nolint:lll // Function signature requires descriptive parameter names
//nolint:lll // Function signature requires descriptive parameter names
func configPatternToDashboard(cfgPattern string, cfgPatternConfig *config.PatternConfigYAML) (models.LoadPattern, *models.DashboardPatternConfig) {
	// Convert string to LoadPattern type
	pattern := models.LoadPattern(cfgPattern)
	if pattern == "" {
		pattern = models.PatternConstant
	}

	// Convert PatternConfigYAML to DashboardPatternConfig
	if cfgPatternConfig == nil {
		return pattern, nil
	}

	dashboardConfig := &models.DashboardPatternConfig{
		RequestsPerSecond: cfgPatternConfig.RequestsPerSecond,
		StartRPS:          cfgPatternConfig.StartRPS,
		TargetRPS:         cfgPatternConfig.TargetRPS,
		BaseRPS:           cfgPatternConfig.BaseRPS,
		SpikeRPS:          cfgPatternConfig.SpikeRPS,
		StepIncrement:     cfgPatternConfig.StepIncrement,
		StepCount:         cfgPatternConfig.StepCount,
		RampUpDuration:    cfgPatternConfig.RampUpDuration,
		RampDownDuration:  cfgPatternConfig.RampDownDuration,
		SpikeDuration:     cfgPatternConfig.SpikeDuration,
		StepDuration:      cfgPatternConfig.StepDuration,
	}

	return pattern, dashboardConfig
}

//nolint:lll // Function signature requires descriptive parameter names
//nolint:lll // Function signature requires descriptive parameter names
func dashboardPatternToConfig(dashboardPattern models.LoadPattern, dashboardPatternConfig *models.DashboardPatternConfig) (string, *config.PatternConfigYAML) {
	// Convert LoadPattern to string
	pattern := string(dashboardPattern)
	if pattern == "" {
		pattern = string(models.PatternConstant)
	}

	// Convert DashboardPatternConfig to PatternConfigYAML
	// Always create a config object, even if nil, to ensure pattern_config exists
	cfgConfig := &config.PatternConfigYAML{}

	if dashboardPatternConfig != nil {
		cfgConfig.RequestsPerSecond = dashboardPatternConfig.RequestsPerSecond
		cfgConfig.StartRPS = dashboardPatternConfig.StartRPS
		cfgConfig.TargetRPS = dashboardPatternConfig.TargetRPS
		cfgConfig.BaseRPS = dashboardPatternConfig.BaseRPS
		cfgConfig.SpikeRPS = dashboardPatternConfig.SpikeRPS
		cfgConfig.StepIncrement = dashboardPatternConfig.StepIncrement
		cfgConfig.StepCount = dashboardPatternConfig.StepCount
		cfgConfig.RampUpDuration = dashboardPatternConfig.RampUpDuration
		cfgConfig.RampDownDuration = dashboardPatternConfig.RampDownDuration
		cfgConfig.SpikeDuration = dashboardPatternConfig.SpikeDuration
		cfgConfig.StepDuration = dashboardPatternConfig.StepDuration
	}

	return pattern, cfgConfig
}

func dashboardPayloadToConfig(payload *models.DashboardConfigPayload, current *config.Config) *config.Config {
	next := current.Clone()
	next.Master.Port = payload.Master.Port
	next.Master.DashboardPort = payload.Master.DashboardPort
	next.ActiveTestID = payload.ActiveTestID
	next.Tests = make(map[string]*config.TestConfig, len(payload.Tests))

	for testID, testPayload := range payload.Tests {
		testCfg := &config.TestConfig{
			Protocol: testPayload.Protocol,
			Duration: testPayload.Duration,
		}

		if testPayload.HTTP != nil {
			//nolint:lll // Long line due to function call with descriptive parameters
			loadPattern, patternConfig := dashboardPatternToConfig(testPayload.HTTP.LoadPattern, testPayload.HTTP.PatternConfig)
			testCfg.HTTP = &config.HTTPTestConfig{
				TargetURL:          testPayload.HTTP.TargetURL,
				Method:             testPayload.HTTP.Method,
				Headers:            cloneHeaders(testPayload.HTTP.Headers),
				Body:               testPayload.HTTP.Body,
				Timeout:            testPayload.HTTP.Timeout,
				FollowRedirects:    testPayload.HTTP.FollowRedirects,
				InsecureSkipVerify: testPayload.HTTP.InsecureSkipVerify,
				LoadPattern:        loadPattern,
				PatternConfig:      patternConfig,
			}
		}

		if testPayload.SMTP != nil {
			//nolint:lll // Long line due to function call with descriptive parameters
			loadPattern, patternConfig := dashboardPatternToConfig(testPayload.SMTP.LoadPattern, testPayload.SMTP.PatternConfig)
			testCfg.SMTP = &config.SMTPTestConfig{
				Host:               testPayload.SMTP.Host,
				Port:               testPayload.SMTP.Port,
				From:               testPayload.SMTP.From,
				To:                 append([]string(nil), testPayload.SMTP.To...),
				Subject:            testPayload.SMTP.Subject,
				Body:               testPayload.SMTP.Body,
				UseTLS:             testPayload.SMTP.UseTLS,
				Username:           testPayload.SMTP.Username,
				Password:           testPayload.SMTP.Password,
				InsecureSkipVerify: testPayload.SMTP.InsecureSkipVerify,
				LoadPattern:        loadPattern,
				PatternConfig:      patternConfig,
			}
		}

		next.Tests[testID] = testCfg
	}

	return next
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck // HTTP response
		"success": "false",
		"message": message,
	})
}

// handleMetricsAPI returns current aggregated metrics as JSON
func (s *Server) handleMetricsAPI(w http.ResponseWriter, _ *http.Request) {
	metrics := s.aggregator.GetAggregatedMetrics()
	s.testRunningMu.RLock()
	metrics.TestRunning = s.testRunning
	metrics.TestStartedAt = s.testStartedAt
	s.testRunningMu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(metrics) //nolint:errcheck // HTTP response
}

// handleWorkersAPI returns list of workers as JSON
func (s *Server) handleWorkersAPI(w http.ResponseWriter, _ *http.Request) {
	workers := s.registry.GetAllWorkers()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(workers) //nolint:errcheck // HTTP response
}

func (s *Server) handleConfigAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := s.getConfig()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(configToDashboardPayload(cfg)) //nolint:errcheck // HTTP response
	case http.MethodPut:
		if s.configFilePath == "" {
			writeJSONError(w, http.StatusInternalServerError, "config file path is not configured")
			return
		}

		var payload models.DashboardConfigPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		currentConfig := s.getConfig()
		nextConfig := dashboardPayloadToConfig(&payload, currentConfig)

		if err := nextConfig.Validate(); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		if err := config.SaveConfig(s.configFilePath, nextConfig); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}

		s.setConfig(nextConfig)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // HTTP response
			"success": "true",
			"message": "Configuration saved successfully",
			"config":  configToDashboardPayload(nextConfig),
		})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleStartTestAPI handles API request to start test
func (s *Server) handleStartTestAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	err := s.StartTest()
	w.Header().Set("Content-Type", "application/json")

	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck // HTTP response
			"success": "false",
			"message": err.Error(),
		})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck // HTTP response
		"success": "true",
		"message": "Test started successfully",
	})
}

// handleStopTestAPI handles API request to stop test
func (s *Server) handleStopTestAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	err := s.StopTest()
	w.Header().Set("Content-Type", "application/json")

	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck // HTTP response
			"success": "false",
			"message": err.Error(),
		})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]string{ //nolint:errcheck // HTTP response
		"success": "true",
		"message": "Test stopped successfully",
	})
}

func (s *Server) handleActiveTestAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	if s.configFilePath == "" {
		writeJSONError(w, http.StatusInternalServerError, "config file path is not configured")
		return
	}

	var payload models.DashboardActiveTestPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
		return
	}

	currentConfig := s.getConfig()
	nextConfig := currentConfig.Clone()
	nextConfig.ActiveTestID = payload.ActiveTestID

	if err := nextConfig.Validate(); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := config.SaveConfig(s.configFilePath, nextConfig); err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.setConfig(nextConfig)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // HTTP response
		"success": "true",
		"message": "Active test updated successfully",
		"config":  configToDashboardPayload(nextConfig),
	})
}

func (s *Server) handleTestsAPI(w http.ResponseWriter, r *http.Request) {
	if s.configFilePath == "" {
		writeJSONError(w, http.StatusInternalServerError, "config file path is not configured")
		return
	}

	switch r.Method {
	case http.MethodGet:
		cfg := s.getConfig()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // HTTP response
			"success":        "true",
			"active_test_id": cfg.ActiveTestID,
			"tests":          configToDashboardPayload(cfg).Tests,
		})
	case http.MethodPost:
		var payload models.DashboardTestMutationPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		if payload.ID == "" {
			writeJSONError(w, http.StatusBadRequest, "test id is required")
			return
		}

		currentConfig := s.getConfig()
		if _, exists := currentConfig.Tests[payload.ID]; exists {
			writeJSONError(w, http.StatusConflict, "test id already exists")
			return
		}

		nextConfig := currentConfig.Clone()
		nextConfig.Tests[payload.ID] = dashboardTestToConfig(&payload.Test)
		if nextConfig.ActiveTestID == "" {
			nextConfig.ActiveTestID = payload.ID
		}

		if err := nextConfig.Validate(); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		if err := config.SaveConfig(s.configFilePath, nextConfig); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}

		s.setConfig(nextConfig)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // HTTP response
			"success": "true",
			"message": "Test created successfully",
			"config":  configToDashboardPayload(nextConfig),
		})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleTestByIDAPI(w http.ResponseWriter, r *http.Request) {
	if s.configFilePath == "" {
		writeJSONError(w, http.StatusInternalServerError, "config file path is not configured")
		return
	}

	testID := r.URL.Path[len("/api/config/tests/"):]
	if testID == "" {
		writeJSONError(w, http.StatusBadRequest, "test id is required")
		return
	}

	decodedTestID, err := url.PathUnescape(testID)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid test id")
		return
	}
	testID = decodedTestID

	currentConfig := s.getConfig()
	if _, exists := currentConfig.Tests[testID]; !exists {
		writeJSONError(w, http.StatusNotFound, "test not found")
		return
	}

	nextConfig := currentConfig.Clone()

	switch r.Method {
	case http.MethodPut:
		var payload models.DashboardTestMutationPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			writeJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid request body: %v", err))
			return
		}

		nextConfig.Tests[testID] = dashboardTestToConfig(&payload.Test)

		if err := nextConfig.Validate(); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		if err := config.SaveConfig(s.configFilePath, nextConfig); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}

		s.setConfig(nextConfig)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // HTTP response
			"success": "true",
			"message": "Test updated successfully",
			"config":  configToDashboardPayload(nextConfig),
		})
	case http.MethodDelete:
		delete(nextConfig.Tests, testID)
		if nextConfig.ActiveTestID == testID {
			nextConfig.ActiveTestID = ""
			for candidateID := range nextConfig.Tests {
				nextConfig.ActiveTestID = candidateID
				break
			}
		}

		if err := nextConfig.Validate(); err != nil {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		if err := config.SaveConfig(s.configFilePath, nextConfig); err != nil {
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}

		s.setConfig(nextConfig)

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck // HTTP response
			"success": "true",
			"message": "Test deleted successfully",
			"config":  configToDashboardPayload(nextConfig),
		})
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func dashboardTestToConfig(testPayload *models.DashboardTestConfig) *config.TestConfig {
	testCfg := &config.TestConfig{
		Protocol: testPayload.Protocol,
		Duration: testPayload.Duration,
	}

	if testPayload.HTTP != nil {
		//nolint:lll // Long line due to function call with descriptive parameters
		loadPattern, patternConfig := dashboardPatternToConfig(testPayload.HTTP.LoadPattern, testPayload.HTTP.PatternConfig)
		testCfg.HTTP = &config.HTTPTestConfig{
			TargetURL:          testPayload.HTTP.TargetURL,
			Method:             testPayload.HTTP.Method,
			Headers:            cloneHeaders(testPayload.HTTP.Headers),
			Body:               testPayload.HTTP.Body,
			Timeout:            testPayload.HTTP.Timeout,
			FollowRedirects:    testPayload.HTTP.FollowRedirects,
			InsecureSkipVerify: testPayload.HTTP.InsecureSkipVerify,
			LoadPattern:        loadPattern,
			PatternConfig:      patternConfig,
		}
	}

	if testPayload.SMTP != nil {
		//nolint:lll // Long line due to function call with descriptive parameters
		loadPattern, patternConfig := dashboardPatternToConfig(testPayload.SMTP.LoadPattern, testPayload.SMTP.PatternConfig)
		testCfg.SMTP = &config.SMTPTestConfig{
			Host:               testPayload.SMTP.Host,
			Port:               testPayload.SMTP.Port,
			From:               testPayload.SMTP.From,
			To:                 append([]string(nil), testPayload.SMTP.To...),
			Subject:            testPayload.SMTP.Subject,
			Body:               testPayload.SMTP.Body,
			UseTLS:             testPayload.SMTP.UseTLS,
			Username:           testPayload.SMTP.Username,
			Password:           testPayload.SMTP.Password,
			InsecureSkipVerify: testPayload.SMTP.InsecureSkipVerify,
			LoadPattern:        loadPattern,
			PatternConfig:      patternConfig,
		}
	}

	return testCfg
}

// handleDashboardPage serves the dashboard HTML page
func (s *Server) handleDashboardPage(w http.ResponseWriter, _ *http.Request) {
	data, err := webApp.ReadFile("index.html")
	if err != nil {
		http.Error(w, "Dashboard not found", http.StatusInternalServerError)
		logger.Error().Err(err).Msg("Failed to read embedded dashboard")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if _, err := w.Write(data); err != nil {
		logger.Error().Err(err).Msg("Failed to write dashboard page")
	}
}

// Made with Bob
