package worker

import (
	"testing"
	"time"

	"github.com/pankaj-kumar34/cortex/internal/models"
)

// TestConstantPattern tests the constant load pattern
func TestConstantPattern(t *testing.T) {
	targetRPS := 100
	duration := 5 * time.Minute

	calc := NewLoadPatternCalculator(models.PatternConstant, targetRPS, duration, nil)

	tests := []struct {
		name    string
		elapsed time.Duration
		want    int
	}{
		{"start", 0, 100},
		{"30 seconds", 30 * time.Second, 100},
		{"2 minutes", 2 * time.Minute, 100},
		{"end", 5 * time.Minute, 100},
		{"beyond duration", 10 * time.Minute, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calc.GetRPSAtTime(tt.elapsed)
			if got != tt.want {
				t.Errorf("GetRPSAtTime(%v) = %v, want %v", tt.elapsed, got, tt.want)
			}
		})
	}
}

// TestRampUpPattern tests the ramp-up load pattern
func TestRampUpPattern(t *testing.T) {
	targetRPS := 100
	duration := 10 * time.Minute

	config := &models.PatternConfig{
		StartRPS:         0,
		RampUpDuration:   2 * time.Minute,
		RampDownDuration: 2 * time.Minute,
	}

	calc := NewLoadPatternCalculator(models.PatternRampUp, targetRPS, duration, config)

	tests := []struct {
		name    string
		elapsed time.Duration
		want    int
	}{
		{"start of ramp-up", 0, 0},
		{"25% ramp-up", 30 * time.Second, 25},
		{"50% ramp-up", 1 * time.Minute, 50},
		{"75% ramp-up", 90 * time.Second, 75},
		{"end of ramp-up", 2 * time.Minute, 100},
		{"sustain phase start", 2*time.Minute + 1*time.Second, 100},
		{"sustain phase middle", 5 * time.Minute, 100},
		{"sustain phase end", 8 * time.Minute, 100},
		{"start of ramp-down", 8*time.Minute + 1*time.Second, 99},
		{"50% ramp-down", 9 * time.Minute, 50},
		{"end of ramp-down", 10 * time.Minute, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calc.GetRPSAtTime(tt.elapsed)
			// Allow ±1 tolerance for rounding
			if abs(got-tt.want) > 1 {
				t.Errorf("GetRPSAtTime(%v) = %v, want %v (±1)", tt.elapsed, got, tt.want)
			}
		})
	}
}

// TestRampUpPatternWithDefaults tests ramp-up with default durations
func TestRampUpPatternWithDefaults(t *testing.T) {
	targetRPS := 100
	duration := 8 * time.Minute

	// No ramp durations specified - should default to 25% each
	config := &models.PatternConfig{
		StartRPS: 10,
	}

	calc := NewLoadPatternCalculator(models.PatternRampUp, targetRPS, duration, config)

	tests := []struct {
		name    string
		elapsed time.Duration
		want    int
	}{
		{"start", 0, 10},
		{"end of default ramp-up (2min)", 2 * time.Minute, 100},
		{"sustain phase", 4 * time.Minute, 100},
		{"start of default ramp-down (6min)", 6 * time.Minute, 100},
		{"end", 8 * time.Minute, 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calc.GetRPSAtTime(tt.elapsed)
			if abs(got-tt.want) > 1 {
				t.Errorf("GetRPSAtTime(%v) = %v, want %v (±1)", tt.elapsed, got, tt.want)
			}
		})
	}
}

// TestSpikePattern tests the spike/burst load pattern
func TestSpikePattern(t *testing.T) {
	targetRPS := 100
	duration := 5 * time.Minute

	config := &models.PatternConfig{
		BaseRPS:       20,
		SpikeRPS:      200,
		SpikeDuration: 30 * time.Second,
	}

	calc := NewLoadPatternCalculator(models.PatternSpike, targetRPS, duration, config)

	// Spike occurs at the beginning: starts at 0s
	// Spike ends at: 0s + 30s = 30s

	tests := []struct {
		name    string
		elapsed time.Duration
		want    int
	}{
		{"start of spike", 0, 200},
		{"during spike", 15 * time.Second, 200},
		{"end of spike", 29 * time.Second, 200},
		{"after spike", 30 * time.Second, 20},
		{"after spike", 1 * time.Minute, 20},
		{"after spike", 2 * time.Minute, 20},
		{"end", 5 * time.Minute, 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calc.GetRPSAtTime(tt.elapsed)
			if got != tt.want {
				t.Errorf("GetRPSAtTime(%v) = %v, want %v", tt.elapsed, got, tt.want)
			}
		})
	}
}

// TestSpikePatternWithDefaults tests spike pattern with default values
func TestSpikePatternWithDefaults(t *testing.T) {
	targetRPS := 100
	duration := 5 * time.Minute

	// No config - should use defaults
	calc := NewLoadPatternCalculator(models.PatternSpike, targetRPS, duration, &models.PatternConfig{})

	// Default: baseRPS = 10 (10% of target), spikeRPS = 100, spikeDuration = 10s
	// Spike starts at: 0s, ends at 10s

	tests := []struct {
		name    string
		elapsed time.Duration
		want    int
	}{
		{"start of spike", 0, 100},
		{"during spike", 5 * time.Second, 100},
		{"after spike", 15 * time.Second, 10},
		{"after spike", 4 * time.Minute, 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calc.GetRPSAtTime(tt.elapsed)
			if got != tt.want {
				t.Errorf("GetRPSAtTime(%v) = %v, want %v", tt.elapsed, got, tt.want)
			}
		})
	}
}

// TestStepPattern tests the step load pattern
func TestStepPattern(t *testing.T) {
	targetRPS := 100
	duration := 10 * time.Minute

	config := &models.PatternConfig{
		StartRPS:      10,
		StepIncrement: 20,
		StepDuration:  2 * time.Minute,
		StepCount:     5,
	}

	calc := NewLoadPatternCalculator(models.PatternStep, targetRPS, duration, config)

	tests := []struct {
		name    string
		elapsed time.Duration
		want    int
	}{
		{"step 0 start", 0, 10},
		{"step 0 end", 1*time.Minute + 59*time.Second, 10},
		{"step 1 start", 2 * time.Minute, 30},
		{"step 1 middle", 3 * time.Minute, 30},
		{"step 2 start", 4 * time.Minute, 50},
		{"step 3 start", 6 * time.Minute, 70},
		{"step 4 start", 8 * time.Minute, 90},
		{"step 4 end", 9*time.Minute + 59*time.Second, 90},
		{"beyond last step", 12 * time.Minute, 90},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calc.GetRPSAtTime(tt.elapsed)
			if got != tt.want {
				t.Errorf("GetRPSAtTime(%v) = %v, want %v", tt.elapsed, got, tt.want)
			}
		})
	}
}

// TestStepPatternWithDefaults tests step pattern with default values
func TestStepPatternWithDefaults(t *testing.T) {
	targetRPS := 100
	duration := 10 * time.Minute

	// No config - should use defaults
	calc := NewLoadPatternCalculator(models.PatternStep, targetRPS, duration, &models.PatternConfig{})

	// Defaults: stepCount=5, stepDuration=2min, stepIncrement=20, startRPS=20

	tests := []struct {
		name    string
		elapsed time.Duration
		want    int
	}{
		{"step 0", 0, 20},
		{"step 1", 2 * time.Minute, 40},
		{"step 2", 4 * time.Minute, 60},
		{"step 3", 6 * time.Minute, 80},
		{"step 4", 8 * time.Minute, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calc.GetRPSAtTime(tt.elapsed)
			if got != tt.want {
				t.Errorf("GetRPSAtTime(%v) = %v, want %v", tt.elapsed, got, tt.want)
			}
		})
	}
}

// TestDefaultPattern tests that empty pattern defaults to constant
func TestDefaultPattern(t *testing.T) {
	targetRPS := 50
	duration := 2 * time.Minute

	calc := NewLoadPatternCalculator("", targetRPS, duration, nil)

	got := calc.GetRPSAtTime(1 * time.Minute)
	if got != targetRPS {
		t.Errorf("Empty pattern should default to constant: got %v, want %v", got, targetRPS)
	}
}

// TestGetRequestInterval tests interval calculation
func TestGetRequestInterval(t *testing.T) {
	tests := []struct {
		name      string
		rps       int
		wantNanos int64
	}{
		{"1 RPS", 1, 1_000_000_000},
		{"10 RPS", 10, 100_000_000},
		{"100 RPS", 100, 10_000_000},
		{"1000 RPS", 1000, 1_000_000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calc := NewLoadPatternCalculator(models.PatternConstant, tt.rps, time.Minute, nil)
			got := calc.GetRequestInterval(0)
			if got.Nanoseconds() != tt.wantNanos {
				t.Errorf("GetRequestInterval() = %v ns, want %v ns", got.Nanoseconds(), tt.wantNanos)
			}
		})
	}
}

// TestGetRequestIntervalZeroRPS tests fallback for zero RPS
func TestGetRequestIntervalZeroRPS(t *testing.T) {
	config := &models.PatternConfig{
		StartRPS:       0,
		RampUpDuration: 1 * time.Minute,
	}
	calc := NewLoadPatternCalculator(models.PatternRampUp, 100, 2*time.Minute, config)

	// At time 0, RPS should be 0, so interval should fallback to 1 second
	got := calc.GetRequestInterval(0)
	want := time.Second
	if got != want {
		t.Errorf("GetRequestInterval(0) with 0 RPS = %v, want %v", got, want)
	}
}

// TestGetTotalRequestsConstant tests total request estimation for constant pattern
func TestGetTotalRequestsConstant(t *testing.T) {
	targetRPS := 100
	duration := 5 * time.Minute

	calc := NewLoadPatternCalculator(models.PatternConstant, targetRPS, duration, nil)

	want := int64(100 * 5 * 60) // 100 RPS * 5 minutes * 60 seconds
	got := calc.GetTotalRequests()

	if got != want {
		t.Errorf("GetTotalRequests() = %v, want %v", got, want)
	}
}

// TestGetTotalRequestsRampUp tests total request estimation for ramp-up pattern
func TestGetTotalRequestsRampUp(t *testing.T) {
	targetRPS := 100
	duration := 10 * time.Minute

	config := &models.PatternConfig{
		StartRPS:         0,
		RampUpDuration:   2 * time.Minute,
		RampDownDuration: 2 * time.Minute,
	}

	calc := NewLoadPatternCalculator(models.PatternRampUp, targetRPS, duration, config)

	// Ramp-up (2min): avg 50 RPS = 50 * 120 = 6000
	// Sustain (6min): 100 RPS = 100 * 360 = 36000
	// Ramp-down (2min): avg 50 RPS = 50 * 120 = 6000
	// Total: 48000
	want := int64(48000)
	got := calc.GetTotalRequests()

	if got != want {
		t.Errorf("GetTotalRequests() = %v, want %v", got, want)
	}
}

// TestGetTotalRequestsSpike tests total request estimation for spike pattern
func TestGetTotalRequestsSpike(t *testing.T) {
	targetRPS := 100
	duration := 5 * time.Minute

	config := &models.PatternConfig{
		BaseRPS:       20,
		SpikeRPS:      200,
		SpikeDuration: 30 * time.Second,
	}

	calc := NewLoadPatternCalculator(models.PatternSpike, targetRPS, duration, config)

	// Base (4.5min): 20 RPS * 270s = 5400
	// Spike (30s): 200 RPS * 30s = 6000
	// Total: 11400
	want := int64(11400)
	got := calc.GetTotalRequests()

	if got != want {
		t.Errorf("GetTotalRequests() = %v, want %v", got, want)
	}
}

// TestGetTotalRequestsStep tests total request estimation for step pattern
func TestGetTotalRequestsStep(t *testing.T) {
	targetRPS := 100
	duration := 10 * time.Minute

	config := &models.PatternConfig{
		StartRPS:      10,
		StepIncrement: 20,
		StepDuration:  2 * time.Minute,
		StepCount:     5,
	}

	calc := NewLoadPatternCalculator(models.PatternStep, targetRPS, duration, config)

	// Step 0 (2min): 10 RPS * 120s = 1200
	// Step 1 (2min): 30 RPS * 120s = 3600
	// Step 2 (2min): 50 RPS * 120s = 6000
	// Step 3 (2min): 70 RPS * 120s = 8400
	// Step 4 (2min): 90 RPS * 120s = 10800
	// Total: 30000
	want := int64(30000)
	got := calc.GetTotalRequests()

	if got != want {
		t.Errorf("GetTotalRequests() = %v, want %v", got, want)
	}
}

// TestEdgeCaseVeryShortDuration tests patterns with very short durations
func TestEdgeCaseVeryShortDuration(t *testing.T) {
	targetRPS := 100
	duration := 1 * time.Second

	config := &models.PatternConfig{
		StartRPS:         0,
		RampUpDuration:   500 * time.Millisecond,
		RampDownDuration: 500 * time.Millisecond,
	}

	calc := NewLoadPatternCalculator(models.PatternRampUp, targetRPS, duration, config)

	tests := []struct {
		name    string
		elapsed time.Duration
		minRPS  int
		maxRPS  int
	}{
		{"start", 0, 0, 10},
		{"mid ramp-up", 250 * time.Millisecond, 40, 60},
		{"end ramp-up", 500 * time.Millisecond, 90, 100},
		{"start ramp-down", 501 * time.Millisecond, 90, 100},
		{"end", 1 * time.Second, 0, 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := calc.GetRPSAtTime(tt.elapsed)
			if got < tt.minRPS || got > tt.maxRPS {
				t.Errorf("GetRPSAtTime(%v) = %v, want between %v and %v", tt.elapsed, got, tt.minRPS, tt.maxRPS)
			}
		})
	}
}

// TestEdgeCaseHighRPS tests patterns with very high RPS
func TestEdgeCaseHighRPS(t *testing.T) {
	targetRPS := 100000 // 100k RPS
	duration := 1 * time.Minute

	calc := NewLoadPatternCalculator(models.PatternConstant, targetRPS, duration, nil)

	got := calc.GetRPSAtTime(30 * time.Second)
	if got != targetRPS {
		t.Errorf("High RPS test: got %v, want %v", got, targetRPS)
	}

	interval := calc.GetRequestInterval(0)
	expectedInterval := time.Second / time.Duration(targetRPS)
	if interval != expectedInterval {
		t.Errorf("High RPS interval: got %v, want %v", interval, expectedInterval)
	}
}

// TestNilPatternConfig tests that nil config doesn't cause panics
func TestNilPatternConfig(t *testing.T) {
	patterns := []models.LoadPattern{
		models.PatternConstant,
		models.PatternRampUp,
		models.PatternSpike,
		models.PatternStep,
	}

	for _, pattern := range patterns {
		t.Run(string(pattern), func(t *testing.T) {
			calc := NewLoadPatternCalculator(pattern, 100, 5*time.Minute, nil)
			// Should not panic
			_ = calc.GetRPSAtTime(1 * time.Minute)
			_ = calc.GetRequestInterval(1 * time.Minute)
			_ = calc.GetTotalRequests()
		})
	}
}

// TestUnknownPattern tests that unknown patterns default to constant
func TestUnknownPattern(t *testing.T) {
	targetRPS := 75
	duration := 3 * time.Minute

	calc := NewLoadPatternCalculator("unknown-pattern", targetRPS, duration, nil)

	got := calc.GetRPSAtTime(1 * time.Minute)
	if got != targetRPS {
		t.Errorf("Unknown pattern should default to constant: got %v, want %v", got, targetRPS)
	}
}

// Helper function for absolute value
func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// BenchmarkConstantPattern benchmarks constant pattern calculation
func BenchmarkConstantPattern(b *testing.B) {
	calc := NewLoadPatternCalculator(models.PatternConstant, 100, 5*time.Minute, nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = calc.GetRPSAtTime(time.Duration(i) * time.Millisecond)
	}
}

// BenchmarkRampUpPattern benchmarks ramp-up pattern calculation
func BenchmarkRampUpPattern(b *testing.B) {
	config := &models.PatternConfig{
		StartRPS:         0,
		RampUpDuration:   2 * time.Minute,
		RampDownDuration: 2 * time.Minute,
	}
	calc := NewLoadPatternCalculator(models.PatternRampUp, 100, 10*time.Minute, config)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = calc.GetRPSAtTime(time.Duration(i) * time.Millisecond)
	}
}

// BenchmarkSpikePattern benchmarks spike pattern calculation
func BenchmarkSpikePattern(b *testing.B) {
	config := &models.PatternConfig{
		BaseRPS:       20,
		SpikeRPS:      200,
		SpikeDuration: 30 * time.Second,
	}
	calc := NewLoadPatternCalculator(models.PatternSpike, 100, 5*time.Minute, config)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = calc.GetRPSAtTime(time.Duration(i) * time.Millisecond)
	}
}

// BenchmarkStepPattern benchmarks step pattern calculation
func BenchmarkStepPattern(b *testing.B) {
	config := &models.PatternConfig{
		StartRPS:      10,
		StepIncrement: 20,
		StepDuration:  2 * time.Minute,
		StepCount:     5,
	}
	calc := NewLoadPatternCalculator(models.PatternStep, 100, 10*time.Minute, config)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = calc.GetRPSAtTime(time.Duration(i) * time.Millisecond)
	}
}

// BenchmarkGetRequestInterval benchmarks interval calculation
func BenchmarkGetRequestInterval(b *testing.B) {
	calc := NewLoadPatternCalculator(models.PatternConstant, 1000, 5*time.Minute, nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = calc.GetRequestInterval(time.Duration(i) * time.Millisecond)
	}
}

// BenchmarkGetTotalRequests benchmarks total request estimation
func BenchmarkGetTotalRequests(b *testing.B) {
	config := &models.PatternConfig{
		StartRPS:         0,
		RampUpDuration:   2 * time.Minute,
		RampDownDuration: 2 * time.Minute,
	}
	calc := NewLoadPatternCalculator(models.PatternRampUp, 100, 10*time.Minute, config)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = calc.GetTotalRequests()
	}
}

// Made with Bob
