package embedding

import "net/http"

// fallbackItems calls embedFn for each item in wave where out[start+i] is nil.
// Items that succeed update out[start+i]; items that fail leave their slot nil.
// Callers inspect nil slots as per-item embedding failures.
func fallbackItems(embedFn func(string) ([]float64, error), wave []string, start int, out [][]float64) {
	for i, text := range wave {
		if out[start+i] != nil {
			continue
		}
		vec, err := embedFn(text)
		if err == nil {
			out[start+i] = vec
		}
	}
}

// is4xxFallback reports whether the HTTP status code warrants a per-item
// fallback: any client error (4xx) except 429, which retryOn429 handles.
func is4xxFallback(statusCode int) bool {
	return statusCode >= http.StatusBadRequest &&
		statusCode < http.StatusInternalServerError &&
		statusCode != http.StatusTooManyRequests
}
