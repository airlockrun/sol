package session

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// MaxRetryAttempts is the maximum number of retry attempts.
	MaxRetryAttempts = 5

	// BaseRetryDelay is the base delay for exponential backoff.
	BaseRetryDelay = 2 * time.Second

	// MaxRetryDelay is the maximum delay between retries.
	MaxRetryDelay = 30 * time.Second
)

// RetryableError checks if an error is retryable and returns a reason.
// Returns empty string if not retryable.
func RetryableError(err error) string {
	if err == nil {
		return ""
	}

	errStr := err.Error()
	lowerErr := strings.ToLower(errStr)

	// Never retry replay exhaustion errors (telescope replay mode)
	if strings.Contains(lowerErr, "no more recorded responses") {
		return ""
	}

	// Check for common retryable error patterns
	retryablePatterns := []string{
		"overloaded",
		"rate_limit",
		"rate limit",
		"too_many_requests",
		"too many requests",
		"server_error",
		"internal_server_error",
		"service_unavailable",
		"503",
		"529",
		"timeout",
		"connection reset",
		"connection refused",
	}

	for _, pattern := range retryablePatterns {
		if strings.Contains(lowerErr, pattern) {
			if strings.Contains(lowerErr, "overloaded") {
				return "Provider is overloaded"
			}
			return errStr
		}
	}

	return ""
}

// RetryDelay calculates the delay before the next retry attempt.
// Respects Retry-After headers if provided.
func RetryDelay(attempt int, resp *http.Response) time.Duration {
	// Check for Retry-After header
	if resp != nil {
		if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
			// Try parsing as seconds
			if seconds, err := strconv.Atoi(retryAfter); err == nil {
				return time.Duration(seconds) * time.Second
			}
			// Try parsing as HTTP date
			if t, err := http.ParseTime(retryAfter); err == nil {
				return time.Until(t)
			}
		}
	}

	// Exponential backoff: 2s * 2^(attempt-1)
	delay := BaseRetryDelay * time.Duration(math.Pow(2, float64(attempt-1)))
	if delay > MaxRetryDelay {
		delay = MaxRetryDelay
	}
	return delay
}

// RetryStatus tracks retry state for a session.
type RetryStatus struct {
	Type    string    // "idle", "retry", "busy"
	Attempt int       // Current attempt number
	Message string    // Error message
	Next    time.Time // Time of next retry
}

// NewRetryStatus creates a new idle retry status.
func NewRetryStatus() RetryStatus {
	return RetryStatus{Type: "idle"}
}

// SetRetrying updates status to retrying state.
func (rs *RetryStatus) SetRetrying(attempt int, message string, next time.Time) {
	rs.Type = "retry"
	rs.Attempt = attempt
	rs.Message = message
	rs.Next = next
}

// SetBusy updates status to busy state.
func (rs *RetryStatus) SetBusy() {
	rs.Type = "busy"
}

// SetIdle updates status to idle state.
func (rs *RetryStatus) SetIdle() {
	rs.Type = "idle"
	rs.Attempt = 0
	rs.Message = ""
	rs.Next = time.Time{}
}

// IsRetrying returns true if currently in retry state.
func (rs *RetryStatus) IsRetrying() bool {
	return rs.Type == "retry"
}

// IsBusy returns true if currently busy.
func (rs *RetryStatus) IsBusy() bool {
	return rs.Type == "busy"
}

// BusyError is returned when the session is already processing.
var ErrBusy = errors.New("session is busy")
