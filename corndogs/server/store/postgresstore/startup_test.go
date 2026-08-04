package postgresstore

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRetryUntilTimeoutSucceedsAfterFailures(t *testing.T) {
	attempts := 0
	err := retryUntilTimeout(100*time.Millisecond, time.Millisecond, func() error {
		attempts++
		if attempts < 3 {
			return errors.New("not ready")
		}
		return nil
	})

	if err != nil {
		t.Fatalf("retryUntilTimeout() error = %v", err)
	}
	if attempts != 3 {
		t.Fatalf("retryUntilTimeout() attempts = %d, want 3", attempts)
	}
}

func TestRetryUntilTimeoutReturnsLastError(t *testing.T) {
	want := errors.New("not ready")
	started := time.Now()
	err := retryUntilTimeout(10*time.Millisecond, 2*time.Millisecond, func() error {
		return want
	})

	if !errors.Is(err, want) {
		t.Fatalf("retryUntilTimeout() error = %v, want %v", err, want)
	}
	if elapsed := time.Since(started); elapsed < 10*time.Millisecond {
		t.Fatalf("retryUntilTimeout() returned after %s, want at least 10ms", elapsed)
	}
}

func TestRetryUntilTimeoutWithZeroTimeoutAttemptsOnce(t *testing.T) {
	attempts := 0
	err := retryUntilTimeout(0, time.Second, func() error {
		attempts++
		return errors.New("not ready")
	})

	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("retryUntilTimeout() error = %v, want not ready", err)
	}
	if attempts != 1 {
		t.Fatalf("retryUntilTimeout() attempts = %d, want 1", attempts)
	}
}
