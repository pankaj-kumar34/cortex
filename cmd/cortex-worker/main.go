package main

import (
	"flag"
	"os"
	"os/signal"
	"syscall"

	"github.com/pankaj-kumar34/cortex/internal/logger"
	"github.com/pankaj-kumar34/cortex/internal/worker"
)

func main() {
	// Parse command-line flags (can override environment variables)
	masterAddr := flag.String("master", os.Getenv("MASTER_ADDR"), "Master server address (e.g., localhost:8080)")
	token := flag.String("token", os.Getenv("TOKEN"), "Shared authentication token")
	workerID := flag.String("id", os.Getenv("WORKER_ID"), "Worker ID (auto-generated if not provided)")
	logLevel := flag.String("log-level", "info", "Log level (trace, debug, info, warn, error, fatal, panic)")
	flag.Parse()

	// Initialize logger with specified log level
	logger.Init(*logLevel)

	// Validate required parameters
	if *masterAddr == "" {
		logger.Fatal().Msg("Master address is required. Set MASTER_ADDR environment variable or use -master flag")
	}
	if *token == "" {
		logger.Fatal().Msg("Shared token is required. Set TOKEN environment variable or use -token flag")
	}
	if *workerID == "" {
		logger.Fatal().Msg("Worker ID is required. Set WORKER_ID environment variable or use -id flag")
	}

	logger.Info().Msg("Starting Cortex Load Tester - Worker Node")
	logger.Info().
		Str("worker_id", *workerID).
		Str("master", *masterAddr).
		Msg("Connecting to master")

	// Create worker client
	client := worker.NewClient(*workerID, *masterAddr, *token)

	// Connect to master
	if err := client.Connect(); err != nil {
		logger.Fatal().Err(err).Msg("Failed to connect to master")
	}

	logger.Info().Msg("Connected to master - ready to receive test commands")

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	<-sigChan
	logger.Info().Msg("Shutdown signal received")
	client.Disconnect()
	logger.Info().Msg("Worker stopped gracefully")
}

// Made with Bob
