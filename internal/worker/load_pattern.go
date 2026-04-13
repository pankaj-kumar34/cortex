package worker

import (
	"math"
	"time"

	"github.com/pankaj-kumar34/cortex/internal/models"
)

// LoadPatternCalculator calculates the target RPS at any given time based on the load pattern
type LoadPatternCalculator struct {
	pattern       models.LoadPattern
	targetRPS     int
	duration      time.Duration
	patternConfig *models.PatternConfig
	startTime     time.Time
}

// NewLoadPatternCalculator creates a new load pattern calculator
//
//nolint:lll // Function signature requires descriptive parameter names
func NewLoadPatternCalculator(pattern models.LoadPattern, targetRPS int, duration time.Duration, config *models.PatternConfig) *LoadPatternCalculator {
	// Default to constant pattern if not specified
	if pattern == "" {
		pattern = models.PatternConstant
	}

	return &LoadPatternCalculator{
		pattern:       pattern,
		targetRPS:     targetRPS,
		duration:      duration,
		patternConfig: config,
		startTime:     time.Now(),
	}
}

// GetRPSAtTime returns the target RPS at the given elapsed time
func (lpc *LoadPatternCalculator) GetRPSAtTime(elapsed time.Duration) int {
	switch lpc.pattern {
	case models.PatternConstant:
		return lpc.constantPattern()
	case models.PatternRampUp:
		return lpc.rampUpPattern(elapsed)
	case models.PatternSpike:
		return lpc.repeatingSpikePattern(elapsed) // Dashboard expects recurring spikes
	case models.PatternRepeatingSpike:
		return lpc.repeatingSpikePattern(elapsed)
	case models.PatternStep:
		return lpc.stepPattern(elapsed)
	default:
		return lpc.constantPattern()
	}
}

// constantPattern returns constant RPS
func (lpc *LoadPatternCalculator) constantPattern() int {
	if lpc.patternConfig != nil {
		return lpc.patternConfig.RequestsPerSecond
	}
	return lpc.targetRPS
}

// rampUpPattern gradually increases from startRPS to targetRPS, sustains, then ramps down
func (lpc *LoadPatternCalculator) rampUpPattern(elapsed time.Duration) int {
	if lpc.patternConfig == nil {
		return lpc.targetRPS
	}

	startRPS := lpc.patternConfig.StartRPS
	targetRPS := lpc.patternConfig.TargetRPS
	if targetRPS == 0 {
		targetRPS = lpc.targetRPS // Fallback to old field if not set
	}
	rampUpDuration := lpc.patternConfig.RampUpDuration
	rampDownDuration := lpc.patternConfig.RampDownDuration

	// Default ramp durations if not specified
	if rampUpDuration == 0 {
		rampUpDuration = lpc.duration / 4 //nolint:mnd // 25% of total duration for ramp-up
	}
	if rampDownDuration == 0 {
		rampDownDuration = lpc.duration / 4 //nolint:mnd // 25% of total duration for ramp-down
	}

	// Ramp-up phase
	if elapsed < rampUpDuration {
		progress := float64(elapsed) / float64(rampUpDuration)
		rpsRange := targetRPS - startRPS
		return startRPS + int(float64(rpsRange)*progress)
	}

	// Sustain phase
	rampDownStart := lpc.duration - rampDownDuration
	if elapsed < rampDownStart {
		return targetRPS
	}

	// Ramp-down phase
	if elapsed < lpc.duration {
		elapsedInRampDown := elapsed - rampDownStart
		progress := float64(elapsedInRampDown) / float64(rampDownDuration)
		rpsRange := targetRPS - startRPS
		return targetRPS - int(float64(rpsRange)*progress)
	}

	return startRPS
}

// repeatingSpikePattern creates multiple bursts of traffic throughout the test duration
func (lpc *LoadPatternCalculator) repeatingSpikePattern(elapsed time.Duration) int {
	if lpc.patternConfig == nil {
		return lpc.targetRPS
	}

	baseRPS := lpc.patternConfig.BaseRPS
	spikeRPS := lpc.patternConfig.SpikeRPS
	spikeDuration := lpc.patternConfig.SpikeDuration

	// Default values
	if baseRPS == 0 {
		baseRPS = lpc.targetRPS / 10 // 10% of target as base
	}
	if spikeRPS == 0 {
		spikeRPS = lpc.targetRPS
	}
	if spikeDuration == 0 {
		spikeDuration = 2 * time.Second
	}

	// Calculate the cycle duration (spike + base period)
	// Default: spike every 10 seconds
	cycleDuration := spikeDuration + (10 * time.Second)

	// Find position within current cycle
	positionInCycle := elapsed % cycleDuration

	// Check if we're in the spike window of this cycle
	if positionInCycle < spikeDuration {
		return spikeRPS
	}

	return baseRPS
}

// stepPattern increases load in discrete steps
func (lpc *LoadPatternCalculator) stepPattern(elapsed time.Duration) int {
	if lpc.patternConfig == nil {
		return lpc.targetRPS
	}

	stepDuration := lpc.patternConfig.StepDuration
	stepIncrement := lpc.patternConfig.StepIncrement
	stepCount := lpc.patternConfig.StepCount
	startRPS := lpc.patternConfig.StartRPS

	// Default values
	if stepCount == 0 {
		stepCount = 5
	}
	if stepDuration == 0 {
		stepDuration = lpc.duration / time.Duration(stepCount)
	}
	if stepIncrement == 0 {
		stepIncrement = lpc.targetRPS / stepCount
	}
	if startRPS == 0 {
		startRPS = stepIncrement
	}

	// Calculate current step
	currentStep := int(elapsed / stepDuration)
	if currentStep >= stepCount {
		currentStep = stepCount - 1
	}

	return startRPS + (currentStep * stepIncrement)
}

// GetRequestInterval calculates the interval between requests for the current RPS
func (lpc *LoadPatternCalculator) GetRequestInterval(elapsed time.Duration) time.Duration {
	rps := lpc.GetRPSAtTime(elapsed)
	if rps <= 0 {
		return time.Second // Fallback to 1 request per second
	}
	return time.Second / time.Duration(rps)
}

// GetTotalRequests estimates the total number of requests for the entire test
func (lpc *LoadPatternCalculator) GetTotalRequests() int64 {
	switch lpc.pattern {
	case models.PatternConstant:
		return int64(lpc.duration.Seconds() * float64(lpc.targetRPS))
	case models.PatternRampUp:
		return lpc.estimateRampUpRequests()
	case models.PatternSpike:
		return lpc.estimateRepeatingSpikeRequests() // Dashboard expects recurring spikes
	case models.PatternRepeatingSpike:
		return lpc.estimateRepeatingSpikeRequests()
	case models.PatternStep:
		return lpc.estimateStepRequests()
	default:
		return int64(lpc.duration.Seconds() * float64(lpc.targetRPS))
	}
}

func (lpc *LoadPatternCalculator) estimateRampUpRequests() int64 {
	if lpc.patternConfig == nil {
		return int64(lpc.duration.Seconds() * float64(lpc.targetRPS))
	}

	startRPS := lpc.patternConfig.StartRPS
	rampUpDuration := lpc.patternConfig.RampUpDuration
	rampDownDuration := lpc.patternConfig.RampDownDuration

	if rampUpDuration == 0 {
		rampUpDuration = lpc.duration / 4 //nolint:mnd // 25% of total duration for ramp-up
	}
	if rampDownDuration == 0 {
		rampDownDuration = lpc.duration / 4 //nolint:mnd // 25% of total duration for ramp-down
	}

	// Ramp-up: average of start and target
	rampUpRequests := rampUpDuration.Seconds() * float64(startRPS+lpc.targetRPS) / 2

	// Sustain: full target RPS
	sustainDuration := lpc.duration - rampUpDuration - rampDownDuration
	sustainRequests := sustainDuration.Seconds() * float64(lpc.targetRPS)

	// Ramp-down: average of target and start
	rampDownRequests := rampDownDuration.Seconds() * float64(lpc.targetRPS+startRPS) / 2

	return int64(rampUpRequests + sustainRequests + rampDownRequests)
}

func (lpc *LoadPatternCalculator) estimateRepeatingSpikeRequests() int64 {
	if lpc.patternConfig == nil {
		return int64(lpc.duration.Seconds() * float64(lpc.targetRPS))
	}

	baseRPS := lpc.patternConfig.BaseRPS
	spikeRPS := lpc.patternConfig.SpikeRPS
	spikeDuration := lpc.patternConfig.SpikeDuration

	if baseRPS == 0 {
		baseRPS = lpc.targetRPS / 10
	}
	if spikeRPS == 0 {
		spikeRPS = lpc.targetRPS
	}
	if spikeDuration == 0 {
		spikeDuration = 2 * time.Second
	}

	// Calculate cycle duration
	cycleDuration := spikeDuration + (10 * time.Second)

	// Calculate number of complete cycles
	completeCycles := int(lpc.duration / cycleDuration)
	remainingTime := lpc.duration % cycleDuration

	// Requests from complete cycles
	requestsPerCycle := (spikeDuration.Seconds() * float64(spikeRPS)) +
		((cycleDuration - spikeDuration).Seconds() * float64(baseRPS))
	totalFromCycles := float64(completeCycles) * requestsPerCycle

	// Requests from remaining partial cycle
	var remainingRequests float64
	if remainingTime < spikeDuration {
		remainingRequests = remainingTime.Seconds() * float64(spikeRPS)
	} else {
		remainingRequests = (spikeDuration.Seconds() * float64(spikeRPS)) +
			((remainingTime - spikeDuration).Seconds() * float64(baseRPS))
	}

	return int64(totalFromCycles + remainingRequests)
}

func (lpc *LoadPatternCalculator) estimateStepRequests() int64 {
	if lpc.patternConfig == nil {
		return int64(lpc.duration.Seconds() * float64(lpc.targetRPS))
	}

	stepDuration := lpc.patternConfig.StepDuration
	stepIncrement := lpc.patternConfig.StepIncrement
	stepCount := lpc.patternConfig.StepCount
	startRPS := lpc.patternConfig.StartRPS

	if stepCount == 0 {
		stepCount = 5
	}
	if stepDuration == 0 {
		stepDuration = lpc.duration / time.Duration(stepCount)
	}
	if stepIncrement == 0 {
		stepIncrement = lpc.targetRPS / stepCount
	}
	if startRPS == 0 {
		startRPS = stepIncrement
	}

	totalRequests := 0.0
	for i := 0; i < stepCount; i++ {
		stepRPS := startRPS + (i * stepIncrement)
		totalRequests += stepDuration.Seconds() * float64(stepRPS)
	}

	return int64(math.Ceil(totalRequests))
}

// Made with Bob
