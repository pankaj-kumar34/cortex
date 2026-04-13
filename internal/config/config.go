package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/pankaj-kumar34/cortex/internal/models"
	"gopkg.in/yaml.v3"
)

// Config represents the complete configuration for the load tester
type Config struct {
	Master       MasterConfig           `yaml:"master"`
	ActiveTestID string                 `yaml:"active_test_id"`
	Tests        map[string]*TestConfig `yaml:"tests"`
}

// MasterConfig contains master node configuration
type MasterConfig struct {
	Port          int    `yaml:"port"`
	SharedToken   string `yaml:"token"`
	DashboardPort int    `yaml:"dashboard_port"`
}

// TestConfig contains test execution configuration
type TestConfig struct {
	Protocol string          `yaml:"protocol"`
	Duration string          `yaml:"duration"`
	HTTP     *HTTPTestConfig `yaml:"http,omitempty"`
	SMTP     *SMTPTestConfig `yaml:"smtp,omitempty"`
}

// HTTPTestConfig contains HTTP/HTTPS specific configuration
type HTTPTestConfig struct {
	TargetURL          string             `yaml:"target_url"`
	Method             string             `yaml:"method"`
	Headers            map[string]string  `yaml:"headers"`
	Body               string             `yaml:"body"`
	Timeout            string             `yaml:"timeout"`
	FollowRedirects    bool               `yaml:"follow_redirects"`
	InsecureSkipVerify bool               `yaml:"insecure_skip_verify"`
	LoadPattern        string             `yaml:"load_pattern,omitempty"`
	PatternConfig      *PatternConfigYAML `yaml:"pattern_config,omitempty"`
}

// SMTPTestConfig contains SMTP specific configuration
type SMTPTestConfig struct {
	Host               string             `yaml:"host"`
	Port               int                `yaml:"port"`
	From               string             `yaml:"from"`
	To                 []string           `yaml:"to"`
	Subject            string             `yaml:"subject"`
	Body               string             `yaml:"body"`
	UseTLS             bool               `yaml:"use_tls"`
	Username           string             `yaml:"username"`
	Password           string             `yaml:"password"`
	InsecureSkipVerify bool               `yaml:"insecure_skip_verify"`
	LoadPattern        string             `yaml:"load_pattern,omitempty"`
	PatternConfig      *PatternConfigYAML `yaml:"pattern_config,omitempty"`
}

// PatternConfigYAML contains YAML configuration for load patterns
type PatternConfigYAML struct {
	// Constant pattern
	RequestsPerSecond int `yaml:"requests_per_second,omitempty"`

	// Ramp-Up pattern
	RampUpDuration   string `yaml:"ramp_up_duration,omitempty"`
	RampDownDuration string `yaml:"ramp_down_duration,omitempty"`
	StartRPS         int    `yaml:"start_rps,omitempty"`
	TargetRPS        int    `yaml:"target_rps,omitempty"`

	// Spike pattern
	SpikeDuration string `yaml:"spike_duration,omitempty"`
	SpikeRPS      int    `yaml:"spike_rps,omitempty"`
	BaseRPS       int    `yaml:"base_rps,omitempty"`

	// Step pattern
	StepDuration  string `yaml:"step_duration,omitempty"`
	StepIncrement int    `yaml:"step_increment,omitempty"`
	StepCount     int    `yaml:"step_count,omitempty"`
}

// DefaultConfigPath returns the default cortex config file path.
func DefaultConfigPath() string {
	return "~/.cortex/config.yaml"
}

// ResolveConfigPath expands supported home-directory prefixes in config paths.
func ResolveConfigPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("config path is required")
	}

	if path == "~" || strings.HasPrefix(path, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("failed to resolve home directory: %w", err)
		}

		if path == "~" {
			return homeDir, nil
		}

		return filepath.Join(homeDir, path[2:]), nil
	}

	return path, nil
}

// GenerateSharedToken creates a secure random shared token for worker authentication.
func GenerateSharedToken() (string, error) {
	tokenBytes := make([]byte, 32) //nolint:mnd // 32 bytes = 256 bits for secure token
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("failed to generate shared token: %w", err)
	}

	return hex.EncodeToString(tokenBytes), nil
}

// NewInitialConfig creates a valid initial master configuration.
func NewInitialConfig(masterPort, dashboardPort int, sharedToken string) *Config {
	return &Config{
		Master: MasterConfig{
			Port:          masterPort,
			DashboardPort: dashboardPort,
			SharedToken:   sharedToken,
		},
		Tests: make(map[string]*TestConfig),
	}
}

// EnsureConfigFile creates a new config file when it does not already exist.
func EnsureConfigFile(path string, masterPort, dashboardPort int) (*Config, bool, error) {
	resolvedPath, err := ResolveConfigPath(path)
	if err != nil {
		return nil, false, err
	}

	if _, err := os.Stat(resolvedPath); err == nil {
		cfg, loadErr := LoadConfig(resolvedPath)
		return cfg, false, loadErr
	} else if !os.IsNotExist(err) {
		return nil, false, fmt.Errorf("failed to check config file: %w", err)
	}

	sharedToken, err := GenerateSharedToken()
	if err != nil {
		return nil, false, err
	}

	cfg := NewInitialConfig(masterPort, dashboardPort, sharedToken)
	if err := SaveConfig(resolvedPath, cfg); err != nil {
		return nil, false, err
	}

	return cfg, true, nil
}

// LoadConfig loads configuration from a YAML file
func LoadConfig(configPath string) (*Config, error) {
	resolvedPath, err := ResolveConfigPath(configPath)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Validate configuration
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &config, nil
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}

	cloned := make(map[string]string, len(input))
	maps.Copy(cloned, input)
	return cloned
}

func cloneStringSlice(input []string) []string {
	if input == nil {
		return nil
	}

	cloned := make([]string, len(input))
	copy(cloned, input)
	return cloned
}

// Clone creates a deep copy of the configuration.
func (c *Config) Clone() *Config {
	if c == nil {
		return nil
	}

	cloned := *c
	cloned.Tests = make(map[string]*TestConfig, len(c.Tests))

	for testID, testCfg := range c.Tests {
		if testCfg == nil {
			cloned.Tests[testID] = nil
			continue
		}

		testClone := *testCfg

		if testCfg.HTTP != nil {
			httpConfig := *testCfg.HTTP
			httpConfig.Headers = cloneStringMap(testCfg.HTTP.Headers)
			testClone.HTTP = &httpConfig
		}

		if testCfg.SMTP != nil {
			smtpConfig := *testCfg.SMTP
			smtpConfig.To = cloneStringSlice(testCfg.SMTP.To)
			testClone.SMTP = &smtpConfig
		}

		cloned.Tests[testID] = &testClone
	}

	return &cloned
}

// SaveConfig validates and writes configuration to a YAML file.
func SaveConfig(path string, cfg *Config) error {
	if cfg == nil {
		return errors.New("config is required")
	}

	cloned := cfg.Clone()
	if err := cloned.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	data, err := yaml.Marshal(cloned)
	if err != nil {
		return fmt.Errorf("failed to marshal config file: %w", err)
	}

	dir := filepath.Dir(path)
	if dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:mnd // Standard directory permissions
			return fmt.Errorf("failed to create config directory: %w", err)
		}
	}

	tempFile, err := os.CreateTemp(dir, "cortex-config-*.yaml")
	if err != nil {
		return fmt.Errorf("failed to create temp config file: %w", err)
	}
	tempPath := tempFile.Name()

	cleanupTempFile := func() {
		tempFile.Close()
		os.Remove(tempPath)
	}

	if _, err := tempFile.Write(data); err != nil {
		cleanupTempFile()
		return fmt.Errorf("failed to write temp config file: %w", err)
	}

	if err := tempFile.Close(); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("failed to close temp config file: %w", err)
	}

	if err := os.Rename(tempPath, path); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("failed to replace config file: %w", err)
	}

	return nil
}

var testIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// IsValidTestID reports whether a test ID contains only alphanumeric characters and hyphens.
func IsValidTestID(testID string) bool {
	return testIDPattern.MatchString(testID)
}

// Validate checks if the configuration is valid
func (c *Config) Validate() error {
	// Validate master config
	if c.Master.Port <= 0 || c.Master.Port > 65535 {
		return fmt.Errorf("invalid master port: %d", c.Master.Port)
	}
	if c.Master.DashboardPort <= 0 || c.Master.DashboardPort > 65535 {
		return fmt.Errorf("invalid dashboard port: %d", c.Master.DashboardPort)
	}
	if c.Master.SharedToken == "" {
		return errors.New("token is required")
	}

	// Validate tests config
	if len(c.Tests) == 0 {
		return nil
	}
	if c.ActiveTestID == "" {
		return errors.New("active_test_id is required when tests are configured")
	}

	activeTest, ok := c.Tests[c.ActiveTestID]
	if !ok || activeTest == nil {
		return fmt.Errorf("active_test_id %q does not reference a valid test", c.ActiveTestID)
	}

	for testID, testCfg := range c.Tests {
		if testID == "" {
			return errors.New("test id cannot be empty")
		}
		if !IsValidTestID(testID) {
			return fmt.Errorf("test id %q must contain only letters, numbers, and hyphens", testID)
		}
		if testCfg == nil {
			return fmt.Errorf("test %q configuration is required", testID)
		}
		if err := validateTestConfig(testID, testCfg); err != nil {
			return err
		}
	}

	return nil
}

// validateHTTPConfig validates HTTP-specific configuration
func validateHTTPConfig(testID string, testCfg *TestConfig) error {
	if testCfg.HTTP.TargetURL == "" {
		return fmt.Errorf("target_url is required for HTTP test %q", testID)
	}
	if testCfg.HTTP.Method == "" {
		testCfg.HTTP.Method = "GET" // default
	}
	// Validate that pattern_config has requests_per_second for constant pattern
	loadPattern := testCfg.HTTP.LoadPattern
	if loadPattern == "" || loadPattern == "constant" {
		if testCfg.HTTP.PatternConfig == nil || testCfg.HTTP.PatternConfig.RequestsPerSecond <= 0 {
			//nolint:lll // Error message needs to be descriptive
			return fmt.Errorf("pattern_config.requests_per_second must be positive for constant load pattern in HTTP test %q", testID)
		}
	}
	return nil
}

// validateSMTPConfig validates SMTP-specific configuration
func validateSMTPConfig(testID string, testCfg *TestConfig) error {
	if testCfg.SMTP.Host == "" {
		return fmt.Errorf("smtp host is required for test %q", testID)
	}
	if testCfg.SMTP.Port <= 0 || testCfg.SMTP.Port > 65535 {
		return fmt.Errorf("invalid smtp port %d for test %q", testCfg.SMTP.Port, testID)
	}
	if testCfg.SMTP.From == "" {
		return fmt.Errorf("smtp from address is required for test %q", testID)
	}
	if len(testCfg.SMTP.To) == 0 {
		return fmt.Errorf("at least one smtp recipient is required for test %q", testID)
	}
	// Validate that pattern_config has requests_per_second for constant pattern
	loadPattern := testCfg.SMTP.LoadPattern
	if loadPattern == "" || loadPattern == "constant" {
		if testCfg.SMTP.PatternConfig == nil || testCfg.SMTP.PatternConfig.RequestsPerSecond <= 0 {
			//nolint:lll // Error message needs to be descriptive
			return fmt.Errorf("pattern_config.requests_per_second must be positive for constant load pattern in SMTP test %q", testID)
		}
	}
	return nil
}

func validateTestConfig(testID string, testCfg *TestConfig) error {
	if testCfg.Protocol == "" {
		return fmt.Errorf("test protocol is required for test %q", testID)
	}
	if testCfg.Duration == "" {
		return fmt.Errorf("test duration is required for test %q", testID)
	}

	switch models.TestProtocol(testCfg.Protocol) {
	case models.ProtocolHTTP, models.ProtocolHTTPS:
		if testCfg.HTTP == nil {
			return fmt.Errorf("http configuration is required for test %q", testID)
		}
		if err := validateHTTPConfig(testID, testCfg); err != nil {
			return err
		}
	case models.ProtocolSMTP:
		if testCfg.SMTP == nil {
			return fmt.Errorf("smtp configuration is required for test %q", testID)
		}
		if err := validateSMTPConfig(testID, testCfg); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported protocol %q for test %q", testCfg.Protocol, testID)
	}

	return nil
}

// GetActiveTest returns the currently selected test configuration.
func (c *Config) GetActiveTest() (*TestConfig, error) {
	if c.ActiveTestID == "" {
		return nil, errors.New("active_test_id is not configured")
	}

	testCfg, ok := c.Tests[c.ActiveTestID]
	if !ok || testCfg == nil {
		return nil, fmt.Errorf("active test %q not found", c.ActiveTestID)
	}

	return testCfg, nil
}

// GetHTTPConfig converts the active HTTPTestConfig to models.HTTPConfig.
func (c *Config) GetHTTPConfig() (*models.HTTPConfig, error) {
	testCfg, err := c.GetActiveTest()
	if err != nil {
		return nil, err
	}
	if testCfg.HTTP == nil {
		return nil, fmt.Errorf("http config not available for active test %q", c.ActiveTestID)
	}

	duration, err := time.ParseDuration(testCfg.Duration)
	if err != nil {
		return nil, fmt.Errorf("invalid duration: %w", err)
	}

	timeout := 30 * time.Second //nolint:mnd // Default HTTP timeout
	if testCfg.HTTP.Timeout != "" {
		timeout, err = time.ParseDuration(testCfg.HTTP.Timeout)
		if err != nil {
			return nil, fmt.Errorf("invalid timeout: %w", err)
		}
	}

	// Convert pattern config
	var patternConfig *models.PatternConfig
	if testCfg.HTTP.PatternConfig != nil {
		patternConfig, err = convertPatternConfig(testCfg.HTTP.PatternConfig)
		if err != nil {
			return nil, fmt.Errorf("invalid pattern config: %w", err)
		}
	}

	return &models.HTTPConfig{
		TargetURL:          testCfg.HTTP.TargetURL,
		Method:             testCfg.HTTP.Method,
		Headers:            testCfg.HTTP.Headers,
		Body:               testCfg.HTTP.Body,
		Timeout:            timeout,
		Duration:           duration,
		FollowRedirects:    testCfg.HTTP.FollowRedirects,
		InsecureSkipVerify: testCfg.HTTP.InsecureSkipVerify,
		LoadPattern:        models.LoadPattern(testCfg.HTTP.LoadPattern),
		PatternConfig:      patternConfig,
	}, nil
}

// GetSMTPConfig converts the active SMTPTestConfig to models.SMTPConfig.
func (c *Config) GetSMTPConfig() (*models.SMTPConfig, error) {
	testCfg, err := c.GetActiveTest()
	if err != nil {
		return nil, err
	}
	if testCfg.SMTP == nil {
		return nil, fmt.Errorf("smtp config not available for active test %q", c.ActiveTestID)
	}

	duration, err := time.ParseDuration(testCfg.Duration)
	if err != nil {
		return nil, fmt.Errorf("invalid duration: %w", err)
	}

	// Convert pattern config
	var patternConfig *models.PatternConfig
	if testCfg.SMTP.PatternConfig != nil {
		patternConfig, err = convertPatternConfig(testCfg.SMTP.PatternConfig)
		if err != nil {
			return nil, fmt.Errorf("invalid pattern config: %w", err)
		}
	}

	return &models.SMTPConfig{
		Host:               testCfg.SMTP.Host,
		Port:               testCfg.SMTP.Port,
		From:               testCfg.SMTP.From,
		To:                 testCfg.SMTP.To,
		Subject:            testCfg.SMTP.Subject,
		Body:               testCfg.SMTP.Body,
		Duration:           duration,
		UseTLS:             testCfg.SMTP.UseTLS,
		Username:           testCfg.SMTP.Username,
		Password:           testCfg.SMTP.Password,
		InsecureSkipVerify: testCfg.SMTP.InsecureSkipVerify,
		LoadPattern:        models.LoadPattern(testCfg.SMTP.LoadPattern),
		PatternConfig:      patternConfig,
	}, nil
}

// convertPatternConfig converts PatternConfigYAML to models.PatternConfig
func convertPatternConfig(yamlConfig *PatternConfigYAML) (*models.PatternConfig, error) {
	if yamlConfig == nil {
		return &models.PatternConfig{}, nil
	}

	config := &models.PatternConfig{
		RequestsPerSecond: yamlConfig.RequestsPerSecond,
		StartRPS:          yamlConfig.StartRPS,
		TargetRPS:         yamlConfig.TargetRPS,
		SpikeRPS:          yamlConfig.SpikeRPS,
		BaseRPS:           yamlConfig.BaseRPS,
		StepIncrement:     yamlConfig.StepIncrement,
		StepCount:         yamlConfig.StepCount,
	}

	var err error
	if yamlConfig.RampUpDuration != "" {
		config.RampUpDuration, err = time.ParseDuration(yamlConfig.RampUpDuration)
		if err != nil {
			return nil, fmt.Errorf("invalid ramp_up_duration: %w", err)
		}
	}

	if yamlConfig.RampDownDuration != "" {
		config.RampDownDuration, err = time.ParseDuration(yamlConfig.RampDownDuration)
		if err != nil {
			return nil, fmt.Errorf("invalid ramp_down_duration: %w", err)
		}
	}

	if yamlConfig.SpikeDuration != "" {
		config.SpikeDuration, err = time.ParseDuration(yamlConfig.SpikeDuration)
		if err != nil {
			return nil, fmt.Errorf("invalid spike_duration: %w", err)
		}
	}

	if yamlConfig.StepDuration != "" {
		config.StepDuration, err = time.ParseDuration(yamlConfig.StepDuration)
		if err != nil {
			return nil, fmt.Errorf("invalid step_duration: %w", err)
		}
	}

	return config, nil
}

// Made with Bob
