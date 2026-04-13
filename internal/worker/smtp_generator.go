package worker

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/smtp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pankaj-kumar34/cortex/internal/logger"
	"github.com/pankaj-kumar34/cortex/internal/models"
)

// SMTPGenerator generates SMTP load
type SMTPGenerator struct {
	config      *models.SMTPConfig
	metrics     *SMTPMetrics
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	startTime   time.Time
	patternCalc *LoadPatternCalculator
}

// SMTPMetrics tracks SMTP load test metrics
type SMTPMetrics struct {
	emailsSent      int64
	emailsSucceeded int64
	emailsFailed    int64
	latencies       []time.Duration
	latenciesMu     sync.Mutex
	lastEmailsSent  int64
	lastCheckTime   time.Time
	lastThroughput  float64
	lastCheckMu     sync.Mutex
}

// NewSMTPGenerator creates a new SMTP load generator
func NewSMTPGenerator(config *models.SMTPConfig) *SMTPGenerator {
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

	return &SMTPGenerator{
		config: config,
		metrics: &SMTPMetrics{
			latencies:     make([]time.Duration, 0, 10000), //nolint:mnd // Initial capacity for latency tracking
			lastCheckTime: now,
		},
		ctx:         ctx,
		cancel:      cancel,
		startTime:   now,
		patternCalc: patternCalc,
	}
}

// Start begins the SMTP load test
//
//nolint:dupl // Similar to HTTPGenerator.Start but handles SMTP protocol
func (sg *SMTPGenerator) Start() {
	// Use pattern calculator to determine total emails
	totalEmails := sg.patternCalc.GetTotalRequests()
	if totalEmails <= 0 {
		return
	}

	// Send first email immediately
	sg.wg.Add(1)
	go sg.sendEmail()

	if totalEmails == 1 {
		return
	}

	emailCount := int64(1)

	// Use a high-resolution ticker for precise rate control
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	var currentInterval time.Duration
	var nextEmailTime time.Time
	var lastElapsed time.Duration

	for {
		select {
		case <-sg.ctx.Done():
			return
		case now := <-ticker.C:
			// Recalculate interval based on current elapsed time
			elapsed := now.Sub(sg.startTime)

			// Check if we've moved to a new second (for pattern recalculation)
			if elapsed.Truncate(time.Second) != lastElapsed.Truncate(time.Second) {
				newInterval := sg.patternCalc.GetRequestInterval(elapsed)
				if newInterval != currentInterval {
					currentInterval = newInterval
					// Reset next email time when interval changes to avoid using stale timing
					nextEmailTime = now.Add(currentInterval)
				}
				lastElapsed = elapsed
			}

			// Initialize interval on first iteration
			if currentInterval == 0 {
				currentInterval = sg.patternCalc.GetRequestInterval(elapsed)
				nextEmailTime = now.Add(currentInterval)
			}

			// Send email if it's time
			if now.After(nextEmailTime) || now.Equal(nextEmailTime) {
				if emailCount >= totalEmails {
					return
				}
				sg.wg.Add(1)
				go sg.sendEmail()
				emailCount++
				nextEmailTime = nextEmailTime.Add(currentInterval)
			}
		}
	}
}

// Stop stops the SMTP load test
func (sg *SMTPGenerator) Stop() {
	sg.cancel()
	sg.wg.Wait()
}

// sendEmail sends a single email
func (sg *SMTPGenerator) sendEmail() {
	defer sg.wg.Done()

	// Count as sent before attempting
	atomic.AddInt64(&sg.metrics.emailsSent, 1)

	// Log email attempt
	logger.Debug().
		Strs("recipients", sg.config.To).
		Str("host", sg.config.Host).
		Int("port", sg.config.Port).
		Msg("Sending SMTP email")

	start := time.Now()
	err := sg.sendSMTPEmail()
	latency := time.Since(start)

	// Record latency
	sg.metrics.latenciesMu.Lock()
	sg.metrics.latencies = append(sg.metrics.latencies, latency)
	sg.metrics.latenciesMu.Unlock()

	if err != nil {
		atomic.AddInt64(&sg.metrics.emailsFailed, 1)
		logger.Error().
			Err(err).
			Dur("latency_ms", latency).
			Str("host", sg.config.Host).
			Msg("SMTP email failed")
		return
	}

	atomic.AddInt64(&sg.metrics.emailsSucceeded, 1)
	logger.Debug().
		Dur("latency_ms", latency).
		Int("recipients", len(sg.config.To)).
		Msg("SMTP email sent")
}

// sendSMTPEmail sends an email via SMTP
func (sg *SMTPGenerator) sendSMTPEmail() error {
	// Build email message
	message := sg.buildEmailMessage()

	// Connect to SMTP server
	addr := fmt.Sprintf("%s:%d", sg.config.Host, sg.config.Port)

	var auth smtp.Auth
	if sg.config.Username != "" && sg.config.Password != "" {
		auth = smtp.PlainAuth("", sg.config.Username, sg.config.Password, sg.config.Host)
	}

	// Send email with or without TLS
	if sg.config.UseTLS {
		return sg.sendWithTLS(addr, auth, message)
	}

	return smtp.SendMail(addr, auth, sg.config.From, sg.config.To, []byte(message))
}

// sendWithTLS sends email using TLS
func (sg *SMTPGenerator) sendWithTLS(addr string, auth smtp.Auth, message string) error {
	// Create TLS config
	tlsConfig := &tls.Config{
		ServerName:         sg.config.Host,
		InsecureSkipVerify: sg.config.InsecureSkipVerify,
	}

	// Connect to server with context
	dialer := &tls.Dialer{
		Config: tlsConfig,
	}
	conn, err := dialer.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer conn.Close()

	// Create SMTP client
	client, err := smtp.NewClient(conn, sg.config.Host)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	defer func() {
		_ = client.Quit() //nolint:errcheck // Defer cleanup, error already logged
	}()

	// Authenticate if credentials provided
	if auth != nil {
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("authentication failed: %w", err)
		}
	}

	// Set sender
	if err := client.Mail(sg.config.From); err != nil {
		return fmt.Errorf("failed to set sender: %w", err)
	}

	// Set recipients
	for _, recipient := range sg.config.To {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("failed to set recipient: %w", err)
		}
	}

	// Send message
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("failed to get data writer: %w", err)
	}

	_, err = w.Write([]byte(message))
	if err != nil {
		return fmt.Errorf("failed to write message: %w", err)
	}

	err = w.Close()
	if err != nil {
		return fmt.Errorf("failed to close writer: %w", err)
	}

	return nil
}

// buildEmailMessage constructs the email message
func (sg *SMTPGenerator) buildEmailMessage() string {
	message := fmt.Sprintf("From: %s\r\n", sg.config.From)
	message += fmt.Sprintf("To: %s\r\n", sg.config.To[0])

	if len(sg.config.To) > 1 {
		var messageSb277 strings.Builder
		for _, recipient := range sg.config.To[1:] {
			fmt.Fprintf(&messageSb277, "Cc: %s\r\n", recipient)
		}
		message += messageSb277.String()
	}

	message += fmt.Sprintf("Subject: %s\r\n", sg.config.Subject)
	message += "MIME-Version: 1.0\r\n"
	message += "Content-Type: text/plain; charset=utf-8\r\n"
	message += "\r\n"
	message += sg.config.Body

	return message
}

// GetMetrics returns current metrics
//
//nolint:dupl // Similar to HTTPGenerator.GetMetrics but for SMTP metrics
func (sg *SMTPGenerator) GetMetrics() *models.Metrics {
	sg.metrics.latenciesMu.Lock()
	defer sg.metrics.latenciesMu.Unlock()

	emailsSent := atomic.LoadInt64(&sg.metrics.emailsSent)
	emailsSucceeded := atomic.LoadInt64(&sg.metrics.emailsSucceeded)
	emailsFailed := atomic.LoadInt64(&sg.metrics.emailsFailed)

	metrics := &models.Metrics{
		Timestamp:         time.Now(),
		RequestsSent:      emailsSent,
		RequestsSucceeded: emailsSucceeded,
		RequestsFailed:    emailsFailed,
	}

	// Calculate error rate
	if emailsSent > 0 {
		metrics.ErrorRate = float64(emailsFailed) / float64(emailsSent) * 100
	}

	// Calculate throughput (emails per second) based on recent activity
	sg.metrics.lastCheckMu.Lock()
	now := time.Now()
	timeSinceLastCheck := now.Sub(sg.metrics.lastCheckTime)

	// Check if test is still running
	select {
	case <-sg.ctx.Done():
		// Test has completed, set throughput to 0
		metrics.Throughput = 0
		sg.metrics.lastThroughput = 0
	default:
		// Test is still running, calculate throughput
		if timeSinceLastCheck.Seconds() > 0 {
			emailsSinceLastCheck := emailsSent - sg.metrics.lastEmailsSent
			currentThroughput := float64(emailsSinceLastCheck) / timeSinceLastCheck.Seconds()

			// Only update throughput if we're actively sending emails
			// Otherwise, preserve the last known throughput
			if emailsSinceLastCheck > 0 {
				sg.metrics.lastThroughput = currentThroughput
			}
			metrics.Throughput = sg.metrics.lastThroughput
		}
	}

	sg.metrics.lastEmailsSent = emailsSent
	sg.metrics.lastCheckTime = now
	sg.metrics.lastCheckMu.Unlock()

	// Calculate latency percentiles
	if len(sg.metrics.latencies) > 0 {
		sortedLatencies := make([]time.Duration, len(sg.metrics.latencies))
		copy(sortedLatencies, sg.metrics.latencies)
		slices.Sort(sortedLatencies)

		metrics.LatencyP50 = calculatePercentile(sortedLatencies, 0.50) //nolint:mnd // P50 percentile
		metrics.LatencyP95 = calculatePercentile(sortedLatencies, 0.95) //nolint:mnd // P95 percentile
		metrics.LatencyP99 = calculatePercentile(sortedLatencies, 0.99) //nolint:mnd // P99 percentile
	}

	return metrics
}

// Made with Bob
