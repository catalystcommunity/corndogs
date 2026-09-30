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
var PrometheusEnabled = GetEnvAsBoolOrDefault("PROMETHEUS_ENABLED", "false")
var PrometheusNamespace = GetEnvOrDefault("PROMETHEUS_NAMESPACE", "corndogs")
var PrometheusQueueSizeEnabled = GetEnvAsBoolOrDefault("PROMETHEUS_QUEUE_SIZE_ENABLED", "true")
var PrometheusQueueSizeInterval = GetEnvAsDurationOrDefault("PROMETHEUS_QUEUE_SIZE_INTERVAL", "15s")
var PrometheusMetricQueryTimeout = GetEnvAsDurationOrDefault("PROMETHEUS_METRIC_QUERY_TIMEOUT", "5s")
var DefaultQueue = GetEnvOrDefault("DEFAULT_QUEUE", "default")
var DefaultStartingState = GetEnvOrDefault("DEFAULT_STARTING_STATE", "submitted")
var DefaultTimeout = int64(GetEnvAsIntOrDefault("DEFAULT_TIMEOUT", "0"))
var DefaultWorkingSuffix = "-working"
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

// Version is the server release. Release builds set it with
// -ldflags "-X github.com/CatalystCommunity/corndogs/corndogs/server/config.Version=...".
var Version = "dev"

// Admission policies of the resilience contract. "compatibility" accepts the
// legacy operations that a feature replaces; "required" rejects them before
// they change data. The two features have separate settings, so an operator
// can require submission keys after the producers upgrade and task guards
// after the workers upgrade. The default is "compatibility" for both, so an
// upgraded server keeps serving released clients.
const (
	PolicyCompatibility = "compatibility"
	PolicyRequired      = "required"
)

var SubmissionKeyPolicy = GetEnvOrDefault("CORNDOGS_SUBMISSION_KEY_POLICY", PolicyCompatibility)
var TaskGuardPolicy = GetEnvOrDefault("CORNDOGS_TASK_GUARD_POLICY", PolicyCompatibility)

// ReceiptRetention is how long the server keeps submission and operation
// receipts. A retry after this period is not deduplicated.
var ReceiptRetention = GetEnvAsDurationOrDefault("CORNDOGS_RECEIPT_RETENTION", "1h")

// MinReceiptRetention keeps the retention longer than any client retry budget.
const MinReceiptRetention = time.Minute

// ValidateResilience checks the policy settings. The server does not start with
// an invalid value, because a typo must not weaken enforcement silently.
func ValidateResilience() error {
	for name, v := range map[string]string{
		"CORNDOGS_SUBMISSION_KEY_POLICY": SubmissionKeyPolicy,
		"CORNDOGS_TASK_GUARD_POLICY":     TaskGuardPolicy,
	} {
		if v != PolicyCompatibility && v != PolicyRequired {
			return fmt.Errorf("%s must be %q or %q, got %q", name, PolicyCompatibility, PolicyRequired, v)
		}
	}
	if ReceiptRetention < MinReceiptRetention {
		return fmt.Errorf("CORNDOGS_RECEIPT_RETENTION must be at least %s, got %s", MinReceiptRetention, ReceiptRetention)
	}
	return nil
}

// KeysRequired reports whether legacy SubmitTask is rejected.
func KeysRequired() bool { return SubmissionKeyPolicy == PolicyRequired }

// GuardsRequired reports whether legacy mutations and unguarded submissions are
// rejected.
func GuardsRequired() bool { return TaskGuardPolicy == PolicyRequired }
