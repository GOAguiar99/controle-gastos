// Package metrics holds the app's Prometheus metrics and serves them on a
// private listener (METRICS_ADDR, loopback only), scraped by the local
// Grafana Alloy agent. Labels are always bounded: route patterns, status
// codes, tool and event names. Never a URL, so the MCP link token in
// /mcp/{token} can't end up in a label.
package metrics

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Registry is the app's own registry (not the global default), so tests and
// libraries can't add series by accident.
var Registry = prometheus.NewRegistry()

var latencyBuckets = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}

var (
	httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "cg_http_request_duration_seconds",
		Help:    "Duração das requisições HTTP por padrão de rota e status.",
		Buckets: latencyBuckets,
	}, []string{"route", "code"})
	httpInFlight = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "cg_http_requests_in_flight",
		Help: "Requisições HTTP em andamento.",
	})
	toolDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "cg_mcp_tool_duration_seconds",
		Help:    "Duração das chamadas de ferramentas MCP.",
		Buckets: latencyBuckets,
	}, []string{"tool"})
	toolCalls = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "cg_mcp_tool_calls_total",
		Help: "Chamadas de ferramentas MCP por resultado (ok, invalid, error).",
	}, []string{"tool", "outcome"})
	auditEvents = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "cg_audit_events_total",
		Help: "Eventos de auditoria (login_failure, rate_limited, authz_denied...).",
	}, []string{"event"})
)

func init() {
	Registry.MustRegister(httpDuration, httpInFlight, toolDuration, toolCalls, auditEvents,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
}

// ObserveTool records one MCP tool call. outcome is "ok", "invalid" (a
// validation error the model can fix) or "error".
func ObserveTool(tool, outcome string, d time.Duration) {
	toolDuration.WithLabelValues(tool).Observe(d.Seconds())
	toolCalls.WithLabelValues(tool, outcome).Inc()
}

// CountAudit counts an audit event by name (a fixed set chosen in code).
func CountAudit(event string) { auditEvents.WithLabelValues(event).Inc() }

// Route labels for requests that never reached a route handler.
const (
	RouteUnmatched = "unmatched" // no route (404/405 from the mux)
	RouteRejected  = "rejected"  // stopped by middleware before the mux (rate limit, CSRF)
)

type routeKey struct{}

// route is filled in by Route (innermost) and read by Instrument (outermost);
// it travels in the context because middleware copies the request.
type route struct {
	pattern string
	reached bool
}

// Instrument times every request and labels it with the route pattern that
// Route recorded. Wrap the whole handler chain with it.
func Instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		httpInFlight.Inc()
		defer httpInFlight.Dec()
		rt := &route{}
		sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), routeKey{}, rt)))
		label := rt.pattern
		switch {
		case !rt.reached:
			label = RouteRejected
		case label == "":
			label = RouteUnmatched
		}
		httpDuration.WithLabelValues(label, strconv.Itoa(sw.code)).Observe(time.Since(start).Seconds())
	})
}

// Route records the pattern the mux matched (http.Request.Pattern, e.g.
// "GET /lancamentos/{id}/editar"). Wrap the ServeMux itself with it: the mux
// sets Pattern on the very request it receives.
func Route(mux http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rt, _ := r.Context().Value(routeKey{}).(*route)
		mux.ServeHTTP(w, r)
		if rt != nil {
			rt.reached, rt.pattern = true, r.Pattern
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	code    int
	written bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.written {
		w.code, w.written = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.written = true
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		w.written = true
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Handler serves the registry in the Prometheus text format.
func Handler() http.Handler {
	return promhttp.HandlerFor(Registry, promhttp.HandlerOpts{})
}

// CheckAddr refuses a metrics address that would listen beyond loopback: the
// endpoint has no auth and reveals routes, volumes and runtime details.
func CheckAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); host == "localhost" || (ip != nil && ip.IsLoopback()) {
		return nil
	}
	return errors.New("METRICS_ADDR precisa ser loopback (ex.: 127.0.0.1:9091): o endpoint de métricas não tem autenticação")
}
