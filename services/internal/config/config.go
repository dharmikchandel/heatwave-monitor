// Package config reads service settings from environment variables.
package config

import (
	"os"
	"strconv"
	"time"
)

// String returns the variable's value, or fallback when unset or empty.
func String(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Int returns the variable parsed as an int, or fallback when unset or invalid.
func Int(key string, fallback int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return v
}

// Duration returns the variable parsed with time.ParseDuration (e.g. "15m"),
// or fallback when unset or invalid.
func Duration(key string, fallback time.Duration) time.Duration {
	v, err := time.ParseDuration(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return v
}
