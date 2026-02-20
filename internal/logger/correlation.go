package logger

import "github.com/google/uuid"

// GenerateCorrelationID generates a new UUID v4 correlation ID
func GenerateCorrelationID() string {
	return uuid.New().String()
}
