package core

import (
	"context"
	"net/http"
	"time"
)

type proxyKey struct{}

// WithProxy returns a context carrying proxy configuration from credentials.
// Call this in the pipeline before dispatching to a connector:
//
//	ctx = core.WithProxy(ctx, attempt.Creds)
func WithProxy(ctx context.Context, creds Credentials) context.Context {
	if creds.ProxyURL == "" && creds.RelayURL == "" {
		return ctx
	}
	return context.WithValue(ctx, proxyKey{}, creds)
}

// ProxyFromContext extracts proxy credentials from context, or returns false.
func ProxyFromContext(ctx context.Context) (Credentials, bool) {
	creds, ok := ctx.Value(proxyKey{}).(Credentials)
	return creds, ok
}

type responseHeaderTimeoutKey struct{}

// WithResponseHeaderTimeout bounds how long a connector may wait for upstream
// response headers on the request issued under ctx. It is the time-to-first-
// byte budget for streaming calls; unary calls are bounded by the context
// deadline instead because their headers only arrive once generation is done.
func WithResponseHeaderTimeout(ctx context.Context, d time.Duration) context.Context {
	if d <= 0 {
		return ctx
	}
	return context.WithValue(ctx, responseHeaderTimeoutKey{}, d)
}

// ResponseHeaderTimeoutFromContext returns the header timeout carried by ctx,
// or 0 when none was set.
func ResponseHeaderTimeoutFromContext(ctx context.Context) time.Duration {
	d, _ := ctx.Value(responseHeaderTimeoutKey{}).(time.Duration)
	return d
}

type responseHeaderObserverKey struct{}

// WithResponseHeaderObserver returns a context whose upstream calls report
// their response headers to fn as soon as they arrive, whatever the status.
func WithResponseHeaderObserver(ctx context.Context, fn func(http.Header)) context.Context {
	if fn == nil {
		return ctx
	}
	return context.WithValue(ctx, responseHeaderObserverKey{}, fn)
}

// ObserveResponseHeader hands h to the observer carried by ctx, if any.
func ObserveResponseHeader(ctx context.Context, h http.Header) {
	if fn, ok := ctx.Value(responseHeaderObserverKey{}).(func(http.Header)); ok {
		fn(h)
	}
}
