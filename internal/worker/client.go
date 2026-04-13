package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pankaj-kumar34/cortex/internal/logger"
	"github.com/pankaj-kumar34/cortex/internal/models"
)

// Client represents a worker client
type Client struct {
	workerID        string
	masterAddr      string
	token           string
	conn            *websocket.Conn
	connMu          sync.Mutex
	reconnectMu     sync.Mutex
	reconnecting    bool
	httpGenerator   *HTTPGenerator
	smtpGenerator   *SMTPGenerator
	metricsReporter *MetricsReporter
	ctx             context.Context
	cancel          context.CancelFunc
}

// NewClient creates a new worker client
func NewClient(workerID, masterAddr, token string) *Client {
	ctx, cancel := context.WithCancel(context.Background())

	return &Client{
		workerID:   workerID,
		masterAddr: masterAddr,
		token:      token,
		ctx:        ctx,
		cancel:     cancel,
	}
}

// Connect establishes connection to the master
func (c *Client) Connect() error {
	// Connect to master via WebSocket
	url := fmt.Sprintf("ws://%s/worker", c.masterAddr)
	conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to master: %w", err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}

	// Send registration message
	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}
	reg := models.WorkerRegistration{
		WorkerID: c.workerID,
		Token:    c.token,
		Address:  hostname,
	}

	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()

	if err := c.writeJSON(reg); err != nil {
		conn.Close()
		c.clearConnection(conn)
		return fmt.Errorf("failed to send registration: %w", err)
	}

	// Wait for registration response
	var response models.WorkerRegistrationResponse
	if err := conn.ReadJSON(&response); err != nil {
		conn.Close()
		c.clearConnection(conn)
		return fmt.Errorf("failed to read registration response: %w", err)
	}

	if !response.Success {
		conn.Close()
		c.clearConnection(conn)
		return fmt.Errorf("registration failed: %s", response.Message)
	}

	logger.Info().
		Str("worker_id", c.workerID).
		Msg("Registration successful")

	// Start heartbeat sender
	go c.sendHeartbeats()

	// Start listening for commands
	go c.listenForCommands(conn)

	return nil
}

// Disconnect closes the connection to the master
func (c *Client) Disconnect() {
	c.cancel()

	if c.httpGenerator != nil {
		c.httpGenerator.Stop()
	}
	if c.smtpGenerator != nil {
		c.smtpGenerator.Stop()
	}
	if c.metricsReporter != nil {
		c.metricsReporter.Stop()
	}

	c.connMu.Lock()
	conn := c.conn
	c.conn = nil
	c.connMu.Unlock()

	if conn != nil {
		conn.Close()
	}
}

// sendHeartbeats periodically sends heartbeat messages to master
func (c *Client) sendHeartbeats() {
	ticker := time.NewTicker(5 * time.Second) //nolint:mnd // Heartbeat interval
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			status := models.StatusIdle
			if c.httpGenerator != nil || c.smtpGenerator != nil {
				status = models.StatusActive
			}

			heartbeat := map[string]any{
				"type":      "heartbeat",
				"worker_id": c.workerID,
				"timestamp": time.Now(),
				"status":    status,
			}

			if err := c.writeJSON(heartbeat); err != nil {
				logger.Error().Err(err).Msg("Failed to send heartbeat")
				c.handleConnectionLoss(nil)
				return
			}
		}
	}
}

// listenForCommands listens for commands from the master
func (c *Client) listenForCommands(conn *websocket.Conn) {
	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			_, message, err := conn.ReadMessage()
			if err != nil {
				// Don't log error if we're shutting down
				select {
				case <-c.ctx.Done():
					return
				default:
					logger.Error().Err(err).Msg("WebSocket connection lost - error reading message")
					c.handleConnectionLoss(conn)
					return
				}
			}

			var cmd models.TestCommand
			if err := json.Unmarshal(message, &cmd); err != nil {
				logger.Error().Err(err).Msg("Failed to parse command")
				continue
			}

			c.handleCommand(&cmd)
		}
	}
}

// handleCommand processes a command from the master
func (c *Client) handleCommand(cmd *models.TestCommand) {
	switch cmd.Action {
	case "start":
		c.startTest(cmd)
	case "stop":
		c.stopTest()
	case "status":
		c.sendStatus()
	default:
		logger.Warn().Str("action", cmd.Action).Msg("Unknown command")
	}
}

// startTest starts a load test based on the command
func (c *Client) startTest(cmd *models.TestCommand) {
	logger.Info().
		Str("protocol", string(cmd.Protocol)).
		Msg("Starting load test")

	// Stop any existing test
	c.stopTest()

	switch cmd.Protocol {
	case models.ProtocolHTTP, models.ProtocolHTTPS:
		c.startHTTPTest(cmd)
	case models.ProtocolSMTP:
		c.startSMTPTest(cmd)
	default:
		logger.Warn().Str("protocol", string(cmd.Protocol)).Msg("Unsupported protocol")
	}
}

// startHTTPTest starts an HTTP/HTTPS load test
//
//nolint:dupl // Similar to startSMTPTest but handles different protocol
func (c *Client) startHTTPTest(cmd *models.TestCommand) {
	// Parse HTTP config
	configBytes, err := json.Marshal(cmd.Config)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to marshal HTTP config")
		return
	}
	var httpConfig models.HTTPConfig
	if err := json.Unmarshal(configBytes, &httpConfig); err != nil {
		logger.Error().Err(err).Msg("Failed to parse HTTP config")
		return
	}

	// Create and start HTTP generator
	c.httpGenerator = NewHTTPGenerator(&httpConfig)

	go c.httpGenerator.Start()

	// Start metrics reporter
	c.metricsReporter = NewMetricsReporter(c.workerID, c.writeJSON, func() *models.Metrics {
		if c.httpGenerator == nil {
			return &models.Metrics{
				WorkerID:  c.workerID,
				Timestamp: time.Now(),
			}
		}
		return c.httpGenerator.GetMetrics()
	})
	go c.metricsReporter.Start()

	// Monitor for test completion
	go c.monitorTestCompletion(c.httpGenerator.ctx)

	logger.Info().
		Str("protocol", "HTTP").
		Msg("Load test started")
}

// startSMTPTest starts an SMTP load test
//
//nolint:dupl // Similar to startHTTPTest but handles different protocol
func (c *Client) startSMTPTest(cmd *models.TestCommand) {
	// Parse SMTP config
	configBytes, err := json.Marshal(cmd.Config)
	if err != nil {
		logger.Error().Err(err).Msg("Failed to marshal SMTP config")
		return
	}
	var smtpConfig models.SMTPConfig
	if err := json.Unmarshal(configBytes, &smtpConfig); err != nil {
		logger.Error().Err(err).Msg("Failed to parse SMTP config")
		return
	}

	// Create and start SMTP generator
	c.smtpGenerator = NewSMTPGenerator(&smtpConfig)

	go c.smtpGenerator.Start()

	// Start metrics reporter
	c.metricsReporter = NewMetricsReporter(c.workerID, c.writeJSON, func() *models.Metrics {
		if c.smtpGenerator == nil {
			return &models.Metrics{
				WorkerID:  c.workerID,
				Timestamp: time.Now(),
			}
		}
		return c.smtpGenerator.GetMetrics()
	})
	go c.metricsReporter.Start()

	// Monitor for test completion
	go c.monitorTestCompletion(c.smtpGenerator.ctx)

	logger.Info().
		Str("protocol", "SMTP").
		Msg("Load test started")
}

// stopTest stops the current load test
func (c *Client) stopTest() {
	if c.metricsReporter != nil {
		c.metricsReporter.Stop()
		c.metricsReporter = nil
	}

	httpGenerator := c.httpGenerator
	c.httpGenerator = nil
	if httpGenerator != nil {
		httpGenerator.Stop()
		c.sendMetricsSnapshot(httpGenerator.GetMetrics())
		logger.Info().
			Str("protocol", "HTTP").
			Msg("Load test stopped")
	}

	smtpGenerator := c.smtpGenerator
	c.smtpGenerator = nil
	if smtpGenerator != nil {
		smtpGenerator.Stop()
		c.sendMetricsSnapshot(smtpGenerator.GetMetrics())
		logger.Info().
			Str("protocol", "SMTP").
			Msg("Load test stopped")
	}
}

// sendStatus sends current status to master
func (c *Client) sendStatus() {
	status := models.StatusIdle
	if c.httpGenerator != nil || c.smtpGenerator != nil {
		status = models.StatusActive
	}

	statusMsg := map[string]any{
		"type":      "status",
		"worker_id": c.workerID,
		"status":    status,
		"timestamp": time.Now(),
	}

	if err := c.writeJSON(statusMsg); err != nil {
		logger.Error().Err(err).Msg("Failed to send status")
	}
}

// monitorTestCompletion monitors when a test completes and cleans up
func (c *Client) monitorTestCompletion(testCtx context.Context) {
	<-testCtx.Done()

	// Test duration completed, stop the test
	logger.Info().Msg("Test duration completed")
	c.stopTest()
	c.sendStatus()
}

func (c *Client) sendMetricsSnapshot(metrics *models.Metrics) {
	if metrics == nil {
		return
	}

	metrics.WorkerID = c.workerID

	metricsMsg := map[string]any{
		"type":               "metrics",
		"worker_id":          metrics.WorkerID,
		"timestamp":          metrics.Timestamp,
		"requests_sent":      metrics.RequestsSent,
		"requests_succeeded": metrics.RequestsSucceeded,
		"requests_failed":    metrics.RequestsFailed,
		"latency_p50":        metrics.LatencyP50,
		"latency_p95":        metrics.LatencyP95,
		"latency_p99":        metrics.LatencyP99,
		"error_rate":         metrics.ErrorRate,
		"throughput":         metrics.Throughput,
	}

	if err := c.writeJSON(metricsMsg); err != nil {
		logger.Error().Err(err).Msg("Failed to send final metrics snapshot")
	}
}

func (c *Client) writeJSON(v any) error {
	c.connMu.Lock()
	defer c.connMu.Unlock()

	if c.conn == nil {
		return errors.New("websocket connection is nil")
	}

	return c.conn.WriteJSON(v)
}

func (c *Client) clearConnection(conn *websocket.Conn) {
	c.connMu.Lock()
	defer c.connMu.Unlock()

	if c.conn == conn {
		c.conn = nil
	}
}

func (c *Client) handleConnectionLoss(conn *websocket.Conn) {
	c.clearConnection(conn)

	select {
	case <-c.ctx.Done():
		return
	default:
	}

	if conn != nil {
		conn.Close()
	}

	c.reconnectMu.Lock()
	if c.reconnecting {
		c.reconnectMu.Unlock()
		return
	}
	c.reconnecting = true
	c.reconnectMu.Unlock()

	defer func() {
		c.reconnectMu.Lock()
		c.reconnecting = false
		c.reconnectMu.Unlock()
	}()

	logger.Warn().Msg("Connection to master lost - attempting reconnection")
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(5 * time.Second): //nolint:mnd // Reconnection retry interval
			if err := c.Connect(); err != nil {
				logger.Debug().Err(err).Msg("Reconnection attempt failed")
				continue
			}
			logger.Info().Msg("Reconnected to master successfully")
			return
		}
	}
}

// MetricsReporter periodically reports metrics to the master
type MetricsReporter struct {
	workerID   string
	writeJSON  func(any) error
	getMetrics func() *models.Metrics
	ctx        context.Context
	cancel     context.CancelFunc
}

// NewMetricsReporter creates a new metrics reporter
//
//nolint:lll // Function signature requires descriptive parameter names
func NewMetricsReporter(workerID string, writeJSON func(any) error, getMetrics func() *models.Metrics) *MetricsReporter {
	ctx, cancel := context.WithCancel(context.Background())
	return &MetricsReporter{
		workerID:   workerID,
		writeJSON:  writeJSON,
		getMetrics: getMetrics,
		ctx:        ctx,
		cancel:     cancel,
	}
}

// Start begins reporting metrics
func (mr *MetricsReporter) Start() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-mr.ctx.Done():
			return
		case <-ticker.C:
			metrics := mr.getMetrics()
			metrics.WorkerID = mr.workerID

			metricsMsg := map[string]any{
				"type":               "metrics",
				"worker_id":          metrics.WorkerID,
				"timestamp":          metrics.Timestamp,
				"requests_sent":      metrics.RequestsSent,
				"requests_succeeded": metrics.RequestsSucceeded,
				"requests_failed":    metrics.RequestsFailed,
				"latency_p50":        metrics.LatencyP50,
				"latency_p95":        metrics.LatencyP95,
				"latency_p99":        metrics.LatencyP99,
				"error_rate":         metrics.ErrorRate,
				"throughput":         metrics.Throughput,
			}

			if err := mr.writeJSON(metricsMsg); err != nil {
				logger.Error().Err(err).Msg("Failed to send metrics")
				return
			}
		}
	}
}

// Stop stops the metrics reporter
func (mr *MetricsReporter) Stop() {
	mr.cancel()
}

// Made with Bob
