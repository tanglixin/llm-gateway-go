package tracing

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/gin-gonic/gin"
)

// Middleware 给每个 HTTP 请求开一个 server span。
func Middleware() gin.HandlerFunc {
	tracer := Tracer()
	return func(c *gin.Context) {
		path := c.FullPath()
		if path == "" {
			path = c.Request.URL.Path
		}

		// 静态资源与探针不产生 trace：否则列表里全是 favicon 和 js 文件
		if isNoisy(path) {
			c.Next()
			return
		}

		ctx, span := tracer.Start(c.Request.Context(), path,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("http.method", c.Request.Method),
				attribute.String("http.path", path),
			),
		)
		defer span.End()

		c.Request = c.Request.WithContext(ctx)
		c.Next()

		status := c.Writer.Status()
		span.SetAttributes(attribute.Int("http.status_code", status))
		if status >= 500 {
			span.SetStatus(codes.Error, "5xx")
		}
	}
}

func isNoisy(path string) bool {
	switch {
	case path == "/healthz", path == "/readyz", path == "/favicon.ico":
		return true
	case len(path) >= 8 && path[:8] == "/assets/":
		return true
	}
	return false
}
