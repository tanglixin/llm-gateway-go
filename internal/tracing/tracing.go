// Package tracing 接 OpenTelemetry SDK，span 直接写进 PostgreSQL。
//
// 没有引入 Jaeger/Tempo：一个 exporter 就够，换 OTLP 只改 InitProivder 里的一行。
package tracing

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/llm-gateway/llm-gateway/internal/store"
)

const serviceName = "llm-gateway"

// exporter 是自建的 span 导出器：批量写 spans 表。
type exporter struct {
	db *store.Store
}

func (e *exporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	rows := make([]store.SpanRow, 0, len(spans))
	for _, s := range spans {
		// OTel 里 span 没有父时 trace_id == span_id 的父；统一转成空串
		parent := ""
		if p := s.Parent(); p.IsValid() {
			parent = p.SpanID().String()
		}

		attrs := make(map[string]any, len(s.Attributes()))
		for _, a := range s.Attributes() {
			attrs[string(a.Key)] = a.Value.AsInterface()
		}

		status := "ok"
		if s.Status().Code == codes.Error {
			status = "error"
		}

		rows = append(rows, store.SpanRow{
			TraceID:      s.SpanContext().TraceID().String(),
			SpanID:       s.SpanContext().SpanID().String(),
			ParentSpanID: parent,
			Name:         s.Name(),
			Service:      serviceName,
			Kind:         s.SpanKind().String(),
			StartAt:      s.StartTime(),
			EndAt:        s.EndTime(),
			DurationMs:   int(s.EndTime().Sub(s.StartTime()).Milliseconds()),
			Status:       status,
			Attributes:   attrs,
		})
	}
	return e.db.InsertSpans(ctx, rows)
}

func (e *exporter) Shutdown(ctx context.Context) error { return nil }

// Init 配置全局 TracerProvider。
func Init(ctx context.Context, db *store.Store) (*sdktrace.TracerProvider, error) {
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithResource(resource.NewSchemaless(
			attribute.String("service.name", serviceName),
		)),
		// 批处理：最多攒 200ms，避免每来一个 span 就写一次库
		sdktrace.WithBatcher(&exporter{db: db}, sdktrace.WithBatchTimeout(200*time.Millisecond)),
	)
	otel.SetTracerProvider(tp)
	return tp, nil
}

// Tracer 返回本服务的 tracer。
func Tracer() trace.Tracer { return otel.Tracer(serviceName) }

// Span 是一个便捷的「开 span + 记错误」封装。
func Span(ctx context.Context, name string, fn func(context.Context) error) error {
	ctx, span := Tracer().Start(ctx, name)
	defer span.End()
	if err := fn(ctx); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	return nil
}

// Attr 是 attribute.String 的简写。
func Attr(k, v string) attribute.KeyValue { return attribute.String(k, v) }

// Int 是 attribute.Int 的简写。
func Int(k string, v int) attribute.KeyValue { return attribute.Int(k, v) }
