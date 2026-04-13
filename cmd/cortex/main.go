package main

import (
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/pankaj-kumar34/cortex/internal/config"
	"github.com/pankaj-kumar34/cortex/internal/logger"
	"github.com/pankaj-kumar34/cortex/internal/master"
)

func main() {
	// Parse command-line flags
	configFile := flag.String("config", config.DefaultConfigPath(), "Path to configuration file")
	//nolint:mnd // Default port
	masterPort := flag.Int("master-port", 8080, "Optional override for master port when bootstrapping")
	//nolint:mnd // Default port
	dashboardPort := flag.Int("dashboard-port", 3001, "Optional override for dashboard port")
	logLevel := flag.String("log-level", "info", "Log level (trace, debug, info, warn, error, fatal, panic)")
	flag.Parse()

	// Initialize logger with specified log level
	logger.Init(*logLevel)

	resolvedConfigPath, err := config.ResolveConfigPath(*configFile)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to resolve configuration path")
	}

	cfg, created, err := config.EnsureConfigFile(resolvedConfigPath, *masterPort, *dashboardPort)
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to initialize configuration")
	}

	if !created {
		cfg, err = config.LoadConfig(resolvedConfigPath)
		if err != nil {
			logger.Fatal().Err(err).Msg("Failed to load configuration")
		}
	}

	logger.Info().Msg("Starting Cortex Load Tester - Master Node")

	if created {
		logger.Info().
			Str("path", resolvedConfigPath).
			Int("master_port", cfg.Master.Port).
			Int("dashboard_port", cfg.Master.DashboardPort).
			Msg("Created new configuration file")
		logger.Info().
			Str("token", cfg.Master.SharedToken).
			Msg("Worker authentication token (save this for worker nodes)")
	} else {
		logger.Info().
			Str("path", resolvedConfigPath).
			Msg("Configuration loaded successfully")
	}

	if activeTest, err := cfg.GetActiveTest(); err == nil {
		logger.Info().
			Str("test_id", cfg.ActiveTestID).
			Str("protocol", activeTest.Protocol).
			Str("duration", activeTest.Duration).
			Msg("Active test configured")
	} else {
		logger.Info().Msg("No active test configured - use dashboard to create one")
	}

	// Create and start master server
	server := master.NewServer(cfg, resolvedConfigPath)

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		logger.Info().Msg("Shutdown signal received")
		if err := server.StopTest(); err != nil && err.Error() != "no test running" {
			logger.Error().Err(err).Msg("Error stopping test")
		}
		logger.Info().Msg("Master server stopped")
		os.Exit(0)
	}()

	// Start server in a goroutine
	go func() {
		if err := server.Start(); err != nil {
			logger.Fatal().Err(err).Msg("Failed to start server")
		}
	}()

	logger.Info().
		Int("master_port", cfg.Master.Port).
		Int("dashboard_port", cfg.Master.DashboardPort).
		Msgf("Server ready - Dashboard: http://localhost:%d", cfg.Master.DashboardPort)
	logger.Info().
		Str("token", cfg.Master.SharedToken).
		Msg("Worker authentication token")

	// Keep the main goroutine alive
	select {}
}

// Made with Bob
