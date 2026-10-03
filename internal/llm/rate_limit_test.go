package llm

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestRateLimitsPreserveRetryWindowWithoutAutomaticRequests(t *testing.T) {
	for _, tc := range []struct {
		header, body string
		wait         time.Duration
	}{
		{"0.02", `{"error":{"message":"rate limit reached"}}`, 20 * time.Millisecond},
		{"", `{"error":{"message":"Please try again in 11.625s."}}`, 11625 * time.Millisecond},
		{"", "unavailable", time.Minute},
		{"-2", "unavailable", time.Minute},
	} {
		calls := 0
		p := Ollama{Model: "test", Compatible: true, Client: handlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Header().Set("Retry-After", tc.header)
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(tc.body))
		}))}
		err := p.TestGeneration(context.Background())
		delay, ok := RetryDelay(err)
		if !ok || delay != tc.wait || calls != 1 {
			t.Fatalf("delay=%v error=%v calls=%d", delay, err, calls)
		}
	}
}
