package llm

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ResponseError means generation completed without a usable proposal. A new
// request can correct it, but must be authorized separately by the user.
type ResponseError struct{ Problem string }

func (e *ResponseError) Error() string { return e.Problem }
func IsResponseError(err error) bool {
	var response *ResponseError
	return errors.As(err, &response)
}

// RateLimitError is recoverable only after a new investigation approval.
// Transport code never retries a generation request on its own.
type RateLimitError struct {
	Problem    string
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string { return e.Problem }

func RetryDelay(err error) (time.Duration, bool) {
	var limited *RateLimitError
	if errors.As(err, &limited) {
		return limited.RetryAfter, true
	}
	return 0, false
}

var retrySeconds = regexp.MustCompile(`(?i)try again in ([0-9]+(?:\.[0-9]+)?)s`)

func retryDelay(header, message string) time.Duration {
	if seconds, err := strconv.ParseFloat(strings.TrimSpace(header), 64); err == nil && seconds >= 0 && seconds <= 3600 {
		return time.Duration(seconds * float64(time.Second))
	}
	if date, err := http.ParseTime(header); err == nil {
		return max(0, min(time.Hour, time.Until(date)))
	}
	if match := retrySeconds.FindStringSubmatch(message); len(match) == 2 {
		seconds, _ := strconv.ParseFloat(match[1], 64)
		return time.Duration(min(seconds, 3600) * float64(time.Second))
	}
	return time.Minute
}
