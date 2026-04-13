package logger

import (
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Init initializes the global logger with console output and specified log level
func Init(level string) {
	// Parse log level
	logLevel, err := zerolog.ParseLevel(level)
	if err != nil {
		logLevel = zerolog.InfoLevel // default to info if invalid
	}

	// Set global log level
	zerolog.SetGlobalLevel(logLevel)

	// Configure zerolog for human-friendly console output
	output := zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: time.RFC3339,
	}
	log.Logger = zerolog.New(output).With().Timestamp().Logger()
}

// GetLogger returns the global logger instance
func GetLogger() *zerolog.Logger {
	return &log.Logger
}

// Info logs an info message
//
//nolint:zerologlint // This is a wrapper function that returns an event for chaining
func Info() *zerolog.Event {
	return log.Info()
}

// Error logs an error message
//
//nolint:zerologlint // This is a wrapper function that returns an event for chaining
func Error() *zerolog.Event {
	return log.Error()
}

// Fatal logs a fatal message and exits
//
//nolint:zerologlint // This is a wrapper function that returns an event for chaining
func Fatal() *zerolog.Event {
	return log.Fatal()
}

// Warn logs a warning message
//
//nolint:zerologlint // This is a wrapper function that returns an event for chaining
func Warn() *zerolog.Event {
	return log.Warn()
}

// Debug logs a debug message
//
//nolint:zerologlint // This is a wrapper function that returns an event for chaining
func Debug() *zerolog.Event {
	return log.Debug()
}

// Printf provides compatibility with standard log.Printf
func Printf(format string, v ...any) {
	log.Info().Msgf(format, v...)
}

// Println provides compatibility with standard log.Println
func Println(v ...any) {
	log.Info().Msg(sprint(v...))
}

func sprint(v ...any) string {
	s := ""
	var sSb72 strings.Builder
	for i, val := range v {
		if i > 0 {
			sSb72.WriteString(" ")
		}
		sSb72.WriteString(toString(val))
	}
	s += sSb72.String()
	return s
}

func toString(v any) string {
	switch val := v.(type) {
	case string:
		return val
	default:
		return ""
	}
}

// Made with Bob
