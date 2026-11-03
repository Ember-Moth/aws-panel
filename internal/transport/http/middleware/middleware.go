package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"time"

	"github.com/Ember-Moth/aws-panel/pkg/httpx"
	"github.com/cloudwego/hertz/pkg/app"
	"go.opentelemetry.io/otel"
)

func Request(logger *slog.Logger) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		var id [16]byte
		_, _ = rand.Read(id[:])
		requestID := hex.EncodeToString(id[:])
		c.Set(httpx.RequestIDKey, requestID)
		c.Header("X-Request-ID", requestID)
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "same-origin")
		c.Header("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'")
		started := time.Now()
		ctx, span := otel.Tracer("aws-panel/http").Start(ctx, string(c.Method())+" "+c.FullPath())
		defer span.End()
		defer func() {
			if recover() != nil {
				logger.Error("HTTP handler panic", "request_id", requestID)
				httpx.Failure(c, 500, "INTERNAL_ERROR", "服务内部错误")
			}
			logger.Info("http request",
				"request_id", requestID, "trace_id", span.SpanContext().TraceID().String(),
				"method", string(c.Method()), "path", string(c.Path()),
				"status", c.Response.StatusCode(), "elapsed_ms", time.Since(started).Milliseconds())
		}()
		c.Next(ctx)
	}
}
