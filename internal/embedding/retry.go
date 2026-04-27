package embedding

import (
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

// retryBaseDelay is the starting backoff delay. A var (not const) so tests can
// zero it out and avoid real sleeps.
var retryBaseDelay = time.Second

const (
	retryCap        = 30 * time.Second
	retryMultiplier = 2
)

// retrySleep is time.Sleep, replaced in tests to avoid real delays.
var retrySleep = time.Sleep

// retryOn429 executes makeReq and retries up to maxRetries times when the
// server responds with HTTP 429. Backoff is exponential with full jitter
// (random draw in [0, delay)), capped at retryCap. A Retry-After response
// header (seconds) overrides the computed delay. One slog.Warn per retry.
// maxRetries == 0 disables retry entirely; the first response is returned.
// Returns the final *http.Response, its already-read body, and any error.
func retryOn429(maxRetries int, makeReq func() (*http.Response, error)) (*http.Response, []byte, error) {
	delay := retryBaseDelay
	for attempt := 0; ; attempt++ {
		resp, err := makeReq()
		if err != nil {
			return nil, nil, err
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, nil, err
		}
		if resp.StatusCode != http.StatusTooManyRequests || maxRetries == 0 || attempt >= maxRetries {
			return resp, body, nil
		}

		wait := jitteredDelay(delay, resp.Header.Get("Retry-After"))
		slog.Warn("embedding rate-limited (429), retrying",
			"attempt", attempt+1,
			"of", maxRetries,
			"wait", wait.Round(time.Millisecond))
		retrySleep(wait)
		delay = min(delay*retryMultiplier, retryCap)
	}
}

// jitteredDelay computes the wait before the next retry attempt. If the
// server supplied a Retry-After header (integer seconds), that value wins.
// Otherwise a uniform random draw in [0, delay) is returned (full jitter).
func jitteredDelay(delay time.Duration, retryAfter string) time.Duration {
	if retryAfter != "" {
		if secs, err := strconv.Atoi(retryAfter); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	if delay <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(delay)))
}
