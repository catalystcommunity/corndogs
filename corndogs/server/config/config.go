package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

const (
	DefaultMaxPayloadBytes = int64(16 * 1024 * 1024)
	HardMaxPayloadBytes    = int64(1<<30 - 1)
	RPCEnvelopeAllowance   = int64(1 * 1024 * 1024)
)

var LogLevel = GetEnvOrDefault("LOGLEVEL", "error")
var FlushBytes = int64(GetEnvAsIntOrDefault("FLUSH_BYTES", "1000"))
var PrometheusEnabled = GetEnvAsBoolOrDefault("PROMETHEUS_ENABLED", "false")
var PrometheusNamespace = GetEnvOrDefault("PROMETHEUS_NAMESPACE", "corndogs")
var PrometheusQueueSizeEnabled = GetEnvAsBoolOrDefault("PROMETHEUS_QUEUE_SIZE_ENABLED", "true")
var PrometheusQueueSizeInterval = GetEnvAsDurationOrDefault("PROMETHEUS_QUEUE_SIZE_INTERVAL", "15s")
var PrometheusMetricQueryTimeout = GetEnvAsDurationOrDefault("PROMETHEUS_METRIC_QUERY_TIMEOUT", "5s")
var DefaultQueue = GetEnvOrDefault("DEFAULT_QUEUE", "default")
var DefaultStartingState = GetEnvOrDefault("DEFAULT_STARTING_STATE", "submitted")
var DefaultTimeout = int64(GetEnvAsIntOrDefault("DEFAULT_TIMEOUT", "0"))
var DefaultWorkingSuffix = "-working"
var RequestIdHeader = GetEnvOrDefault("REQUEST_ID_HEADER", "X-Request-ID")
var MaxPayloadBytes = loadMaxPayloadBytes()
var MaxRPCFrameBytes = int(MaxPayloadBytes + RPCEnvelopeAllowance)

func loadMaxPayloadBytes() int64 {
	raw := GetEnvOrDefault("CORNDOGS_MAX_PAYLOAD_BYTES", strconv.FormatInt(DefaultMaxPayloadBytes, 10))
	value, err := parseMaxPayloadBytes(raw)
	if err != nil {
		panic(err)
	}
	return value
}

func parseMaxPayloadBytes(raw string) (int64, error) {
	value, err := strconv.ParseInt(raw, 0, 64)
	if err != nil {
		return 0, fmt.Errorf("CORNDOGS_MAX_PAYLOAD_BYTES: %w", err)
	}
	if value < 1 || value > HardMaxPayloadBytes {
		return 0, fmt.Errorf(
			"CORNDOGS_MAX_PAYLOAD_BYTES must be in 1..%d, got %d",
			HardMaxPayloadBytes,
			value,
		)
	}
	return value, nil
}

func GetEnvOrDefault(env, defaultValue string) string {
	value := os.Getenv(env)
	if value == "" {
		value = defaultValue
	}
	return value
}

func GetEnvAsIntOrDefault(env, defaultValue string) int {
	value := os.Getenv(env)
	if value == "" {
		value = defaultValue
	}

	intValue, err := strconv.ParseInt(value, 0, 64)
	if err != nil {
		panic(err)
	}
	return int(intValue)
}

func GetEnvAsBoolOrDefault(env, defaultValue string) bool {
	value := os.Getenv(env)
	if value == "" {
		value = defaultValue
	}

	boolValue, err := strconv.ParseBool(value)
	if err != nil {
		panic(err)
	}
	return boolValue
}

func GetEnvAsDurationOrDefault(env, defaultValue string) time.Duration {
	value := os.Getenv(env)
	if value == "" {
		value = defaultValue
	}

	durationValue, err := time.ParseDuration(value)
	if err != nil {
		panic(err)
	}
	return durationValue
}
