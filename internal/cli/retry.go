package cli

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	viapost "github.com/ViaPost-io/viapost-go"
)

const (
	retryInitialDelay = 200 * time.Millisecond
	retryMaximumDelay = 5 * time.Second
)

type retryBackend struct {
	next     Backend
	sleep    func(context.Context, time.Duration) error
	attempts int
}

func (b *retryBackend) Send(ctx context.Context, request viapost.SendRequest, idempotencyKey string) (*viapost.SendResult, error) {
	return b.next.Send(ctx, request, idempotencyKey)
}

func (b *retryBackend) ListMessages(ctx context.Context, options viapost.MessageListOptions) ([]viapost.Message, error) {
	return retrySafe(ctx, b, func() ([]viapost.Message, error) { return b.next.ListMessages(ctx, options) })
}

func (b *retryBackend) GetMessage(ctx context.Context, id string) (*viapost.Message, error) {
	return retrySafe(ctx, b, func() (*viapost.Message, error) { return b.next.GetMessage(ctx, id) })
}

func (b *retryBackend) GetUsage(ctx context.Context) (*viapost.MonthlyUsage, error) {
	return retrySafe(ctx, b, func() (*viapost.MonthlyUsage, error) { return b.next.GetUsage(ctx) })
}

func retrySafe[T any](ctx context.Context, backend *retryBackend, request func() (T, error)) (T, error) {
	var zero T
	for attempt := 0; attempt < backend.attempts; attempt++ {
		result, err := request()
		if err == nil {
			return result, nil
		}
		if attempt == backend.attempts-1 || !isRetryable(err) {
			return zero, err
		}
		delay := retryDelay(err, attempt)
		if err := backend.sleep(ctx, delay); err != nil {
			return zero, err
		}
	}
	return zero, errors.New("retry attempts exhausted")
}

func isRetryable(err error) bool {
	var apiError *viapost.APIError
	if !errors.As(err, &apiError) {
		return false
	}
	return apiError.StatusCode == http.StatusRequestTimeout || apiError.StatusCode == http.StatusTooManyRequests || apiError.StatusCode >= 500
}

func retryDelay(err error, attempt int) time.Duration {
	var apiError *viapost.APIError
	if errors.As(err, &apiError) {
		if delay, ok := parseRetryAfter(apiError.Header.Get("Retry-After"), time.Now()); ok {
			return min(delay, retryMaximumDelay)
		}
	}
	delay := retryInitialDelay << attempt
	return min(delay, retryMaximumDelay)
}

func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second, true
	}
	when, err := http.ParseTime(value)
	if err != nil || !when.After(now) {
		return 0, false
	}
	return when.Sub(now), true
}
