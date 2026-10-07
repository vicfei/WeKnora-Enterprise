package langfuse

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Tencent/WeKnora/internal/logger"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// propagator is the W3C TraceContext propagator used to extract/inject
// traceparent across process boundaries (HTTP requests from sop3, asynq
// payloads). It is a package-level value rather than the global OTel
// propagator so tests remain isolated and Init never mutates global state.
var propagator = propagation.TraceContext{}

// Manager is the public façade of the langfuse package. A singleton is
// installed via Init(); callers should treat a nil *Manager as "disabled"
// and still invoke methods — every public method tolerates a nil receiver.
//
// Internally the manager owns an OpenTelemetry TracerProvider backed by an
// OTLP/HTTP exporter pointing at the Langfuse v3+ / LiteFuse OTel endpoint.
// The handles (*Trace / *Span / *Generation) wrap OTel spans; spans are
// buffered by the BatchSpanProcessor and exported complete on End, so there
// is no per-flush-batch duplication of root spans (the bug the legacy
// hand-rolled translator had on long traces spanning multiple flushes).
type Manager struct {
	cfg Config

	tp     *sdktrace.TracerProvider
	tracer trace.Tracer

	closed atomic.Bool
}

var (
	globalMu sync.RWMutex
	global   *Manager
)

// Init builds a Manager from cfg and installs it as the package-wide
// singleton. When cfg.Enabled is false this returns a disabled manager that
// behaves as a no-op for every public method.
func Init(cfg Config) (*Manager, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	m := &Manager{cfg: cfg}
	if cfg.Enabled {
		resAttrs := []attribute.KeyValue{
			attribute.String("service.name", "weknora"),
			attribute.String(attrLangfusePubKey, cfg.PublicKey),
		}
		if cfg.Environment != "" {
			resAttrs = append(resAttrs, attribute.String(attrEnvironment, cfg.Environment))
		}
		if cfg.Release != "" {
			resAttrs = append(resAttrs, attribute.String(attrRelease, cfg.Release))
		}
		res, err := resource.New(context.Background(), resource.WithAttributes(resAttrs...))
		if err != nil {
			return nil, err
		}
		var sp sdktrace.SpanProcessor
		if cfg.testExporter != nil {
			// Test mode: synchronous export on span End (deterministic).
			sp = sdktrace.NewSimpleSpanProcessor(cfg.testExporter)
		} else {
			exp, err := newExporter(context.Background(), cfg)
			if err != nil {
				return nil, err
			}
			sp = sdktrace.NewBatchSpanProcessor(exp,
				sdktrace.WithBatchTimeout(cfg.FlushInterval),
				sdktrace.WithMaxExportBatchSize(cfg.FlushAt),
				sdktrace.WithMaxQueueSize(cfg.QueueSize),
			)
		}
		m.tp = sdktrace.NewTracerProvider(
			sdktrace.WithResource(res),
			sdktrace.WithSpanProcessor(sp),
			sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRate))),
		)
		m.tracer = m.tp.Tracer(langfuseScopeName,
			trace.WithInstrumentationVersion(langfuseScopeVersion),
			trace.WithInstrumentationAttributes(attribute.String("public_key", cfg.PublicKey)),
		)
		// Extraction/injection in this package use the package-level
		// `propagator` directly, so we deliberately do NOT call
		// otel.SetTextMapPropagator here — mutating global OTel state could
		// interfere with any other OTel instrumentation in the process.
	}

	globalMu.Lock()
	global = m
	globalMu.Unlock()

	if cfg.Enabled {
		logger.Infof(context.Background(),
			"[Langfuse] enabled host=%s flush_at=%d flush_interval=%s sample_rate=%.2f (OTLP/OTel SDK)",
			cfg.Host, cfg.FlushAt, cfg.FlushInterval, cfg.SampleRate,
		)
	}
	return m, nil
}

// GetManager returns the installed singleton, or nil if Init has not been
// called. Callers must tolerate a nil return.
func GetManager() *Manager {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return global
}

// Enabled reports whether the manager would actually emit spans.
func (m *Manager) Enabled() bool {
	return m != nil && m.cfg.Enabled && !m.closed.Load() && m.tp != nil
}

// Tracer exposes the OTel tracer so middleware can create spans directly
// when needed (e.g. extracting a remote traceparent). Returns a no-op tracer
// when disabled.
func (m *Manager) Tracer() trace.Tracer {
	if !m.Enabled() {
		return noop.NewTracerProvider().Tracer(langfuseScopeName)
	}
	return m.tracer
}

// Shutdown flushes pending spans and releases the exporter. Safe to call
// multiple times.
func (m *Manager) Shutdown(ctx context.Context) error {
	if m == nil || !m.cfg.Enabled || m.tp == nil {
		return nil
	}
	if !m.closed.CompareAndSwap(false, true) {
		return nil
	}
	return m.tp.Shutdown(ctx)
}

// Score is one user-feedback score posted to the Langfuse ingestion API.
// TraceID correlates the score to the chat turn's trace (extracted from the
// W3C traceparent stored on the message).
type Score struct {
	TraceID string
	Name    string
	Value   float64
	Comment string
}

// CreateScore posts one score via the public ingestion API. It is a
// fire-and-report operation: a disabled manager or a transport error
// returns the error and the caller logs it — the local feedback table stays
// authoritative (台账 T13 双写约定).
func (m *Manager) CreateScore(ctx context.Context, sc Score) error {
	if !m.Enabled() || sc.TraceID == "" {
		return nil
	}
	body := map[string]any{
		"traceId":  sc.TraceID,
		"name":     sc.Name,
		"value":    sc.Value,
		"comment":  sc.Comment,
		// Langfuse 的 dataType 判别器只认 traces|observations|sessions；
		// 写成 "SCORING" 会 400 "No matching discriminator"。
		"dataType": "traces",
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(m.cfg.Host, "/")+"/api/public/scores", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(m.cfg.PublicKey, m.cfg.SecretKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("langfuse score POST %s: %s", resp.Status, string(b))
	}
	return nil
}

// TraceIDFromTraceparent extracts the 32-hex trace id from a W3C
// traceparent ("00-<trace>-<span>-01").
func TraceIDFromTraceparent(tp string) string {
	parts := strings.Split(strings.TrimSpace(tp), "-")
	if len(parts) >= 3 && len(parts[1]) == 32 {
		return parts[1]
	}
	return ""
}
