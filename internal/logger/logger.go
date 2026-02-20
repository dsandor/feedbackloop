package logger

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// LogLevel represents the severity of a log message
type LogLevel string

const (
	LevelInfo  LogLevel = "info"
	LevelError LogLevel = "error"
)

// Direction indicates message flow direction
type Direction string

const (
	DirectionInbound  Direction = "inbound"
	DirectionOutbound Direction = "outbound"
)

// LogEntry represents a single structured log entry
type LogEntry struct {
	Timestamp     string      `json:"timestamp"`
	Level         LogLevel    `json:"level"`
	EventType     string      `json:"event_type"`
	CorrelationID string      `json:"correlation_id"`
	Direction     Direction   `json:"direction,omitempty"`
	Message       interface{} `json:"message"`
}

// Logger handles structured JSON logging to stdout
type Logger struct {
	encoder *json.Encoder
	mu      sync.Mutex
}

// New creates a new Logger instance
func New() *Logger {
	encoder := json.NewEncoder(os.Stdout)
	return &Logger{
		encoder: encoder,
	}
}

// Log writes a log entry as a JSON line to stdout
func (l *Logger) Log(level LogLevel, eventType string, correlationID string, direction Direction, message interface{}) {
	entry := LogEntry{
		Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
		Level:         level,
		EventType:     eventType,
		CorrelationID: correlationID,
		Direction:     direction,
		Message:       message,
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if err := l.encoder.Encode(entry); err != nil {
		// Fallback to stderr if JSON encoding fails
		fmt.Fprintf(os.Stderr, "LOGGER ERROR: failed to encode log entry: %v\n", err)
	}
}

// Info logs an informational message
func (l *Logger) Info(eventType string, correlationID string, direction Direction, message interface{}) {
	l.Log(LevelInfo, eventType, correlationID, direction, message)
}

// Error logs an error message
func (l *Logger) Error(eventType string, correlationID string, direction Direction, message interface{}) {
	l.Log(LevelError, eventType, correlationID, direction, message)
}

// LogInbound logs an inbound message
func (l *Logger) LogInbound(eventType string, correlationID string, message interface{}) {
	l.Info(eventType, correlationID, DirectionInbound, message)
}

// LogOutbound logs an outbound message
func (l *Logger) LogOutbound(eventType string, correlationID string, message interface{}) {
	l.Info(eventType, correlationID, DirectionOutbound, message)
}

// LogErrorInbound logs an inbound error
func (l *Logger) LogErrorInbound(eventType string, correlationID string, message interface{}) {
	l.Error(eventType, correlationID, DirectionInbound, message)
}

// LogErrorOutbound logs an outbound error
func (l *Logger) LogErrorOutbound(eventType string, correlationID string, message interface{}) {
	l.Error(eventType, correlationID, DirectionOutbound, message)
}

// LogEvent logs an event without direction (for lifecycle events)
func (l *Logger) LogEvent(eventType string, correlationID string, message interface{}) {
	l.Info(eventType, correlationID, "", message)
}

// LogErrorEvent logs an error event without direction
func (l *Logger) LogErrorEvent(eventType string, correlationID string, message interface{}) {
	l.Error(eventType, correlationID, "", message)
}
