package worker

import (
	"context"
	"crypto/tls"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pankaj-kumar34/cortex/internal/logger"
	"github.com/pankaj-kumar34/cortex/internal/models"
	gomail "github.com/wneessen/go-mail"
)

// SMTPGenerator generates SMTP load
type SMTPGenerator struct {
	config           *models.SMTPConfig
	metrics          *SMTPMetrics
	ctx              context.Context
	cancel           context.CancelFunc
	sendCtx          context.Context
	sendCancel       context.CancelFunc
	hardStopCtx      context.Context
	hardStopCancel   context.CancelFunc
	durationDoneOnce sync.Once
	wg               sync.WaitGroup
	startTime        time.Time
	patternCalc      *LoadPatternCalculator
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
	hardStopCtx, hardStopCancel := context.WithCancel(context.Background())
	sendCtx, sendCancel := context.WithCancel(context.Background())
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

	sg := &SMTPGenerator{
		config: config,
		metrics: &SMTPMetrics{
			latencies:     make([]time.Duration, 0, 10000), //nolint:mnd // Initial capacity for latency tracking
			lastCheckTime: now,
		},
		ctx:            ctx,
		cancel:         cancel,
		sendCtx:        sendCtx,
		sendCancel:     sendCancel,
		hardStopCtx:    hardStopCtx,
		hardStopCancel: hardStopCancel,
		startTime:      now,
		patternCalc:    patternCalc,
	}
	go sg.watchDurationCompletion()
	go sg.watchHardStop()
	return sg
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
	sg.scheduleHardStop()
	sg.wg.Wait()
	sg.hardStopCancel()
	sg.sendCancel()
}

// sendEmail sends a single email
func (sg *SMTPGenerator) sendEmail() {
	defer sg.wg.Done()

	// Check if context is already cancelled before attempting to send
	select {
	case <-sg.ctx.Done():
		// Test duration completed, don't send email
		return
	default:
	}

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

// sendSMTPEmail sends an email via SMTP using go-mail package
func (sg *SMTPGenerator) sendSMTPEmail() error {
	// Create a context with timeout that can be hard-cancelled shortly after test duration completion
	ctx, cancel := context.WithTimeout(sg.sendCtx, sg.config.Timeout)
	defer cancel()

	// Create new message
	msg := gomail.NewMsg()

	// Set sender
	if err := msg.From(sg.config.From); err != nil {
		return fmt.Errorf("failed to set sender: %w", err)
	}

	// Set recipients
	if err := msg.To(sg.config.To...); err != nil {
		return fmt.Errorf("failed to set recipients: %w", err)
	}

	// Set subject and body
	msg.Subject(sg.config.Subject)
	msg.SetBodyString(gomail.TypeTextPlain, sg.config.Body)

	// Attach files if specified
	for _, attachmentPath := range sg.config.Attachments {
		if attachmentPath != "" {
			msg.AttachFile(attachmentPath)
		}
	}

	// Create client options
	clientOptions := []gomail.Option{
		gomail.WithPort(sg.config.Port),
		gomail.WithTimeout(sg.config.Timeout),
	}

	// Configure TLS based on port and settings
	// Port 465 uses implicit TLS (SSL), other ports use STARTTLS
	if sg.config.Port == 465 {
		// Port 465: Use implicit TLS/SSL
		clientOptions = append(clientOptions, gomail.WithSSL())
		if sg.config.InsecureSkipVerify {
			clientOptions = append(clientOptions, gomail.WithTLSConfig(&tls.Config{
				InsecureSkipVerify: true,
			}))
		}
	} else {
		// Ports 587, 25, etc: Use STARTTLS (do NOT use WithSSL or WithSSLPort)
		if sg.config.UseTLS || sg.config.Port == 587 {
			// Use TLSOpportunistic for STARTTLS
			clientOptions = append(clientOptions, gomail.WithTLSPolicy(gomail.TLSOpportunistic))
			if sg.config.InsecureSkipVerify {
				clientOptions = append(clientOptions, gomail.WithTLSConfig(&tls.Config{
					InsecureSkipVerify: true,
				}))
			}
		} else {
			// Explicitly disable TLS
			clientOptions = append(clientOptions, gomail.WithTLSPolicy(gomail.NoTLS))
		}
	}

	// Configure authentication
	if sg.config.Username != "" && sg.config.Password != "" {
		clientOptions = append(clientOptions,
			gomail.WithSMTPAuth(gomail.SMTPAuthPlain),
			gomail.WithUsername(sg.config.Username),
			gomail.WithPassword(sg.config.Password),
		)
	}

	// Create client
	client, err := gomail.NewClient(sg.config.Host, clientOptions...)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}

	// Send email with context
	if err := client.DialAndSendWithContext(ctx, msg); err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	return nil
}

func (sg *SMTPGenerator) watchDurationCompletion() {
	<-sg.ctx.Done()
	sg.scheduleHardStop()
}

func (sg *SMTPGenerator) scheduleHardStop() {
	sg.durationDoneOnce.Do(func() {
		go func() {
			timer := time.NewTimer(3 * time.Second)
			defer timer.Stop()

			select {
			case <-timer.C:
				sg.hardStopCancel()
			case <-sg.hardStopCtx.Done():
			}
		}()
	})
}

func (sg *SMTPGenerator) watchHardStop() {
	<-sg.hardStopCtx.Done()
	sg.sendCancel()
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
