package log

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/Laisky/errors/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/resource"
)

const (
	otlpQueueSize = 512
	otlpBatchSize = 128
	otlpTimeout   = 5 * time.Second
)

// otlpConfig contains explicit deployment inputs and private test-only bounds.
type otlpConfig struct {
	endpoint, tokenFile, caFile, host, environment string
	allowLoopbackHTTP                              bool
	queueSize, batchSize                           int
	interval, timeout                              time.Duration
}

// otlpRuntime owns one private SDK and serializes shutdown with callback admission.
type otlpRuntime struct {
	config                              otlpConfig
	provider                            *sdklog.LoggerProvider
	logger                              otellog.Logger
	emitted, exported, failed, rejected atomic.Uint64
	admission                           sync.RWMutex
	stopped                             bool
	shutdownOnce                        sync.Once
	shutdownErr                         error
}

var (
	activeOTLP      atomic.Pointer[otlpRuntime]
	configureOTLPMu sync.Mutex
	configuredOTLP  *otlpRuntime
)

// OTLPStats describes application-side export outcomes, never durable collector delivery.
// Unresolved includes queued, in-flight and overflow-lost records; it is not a drop counter.
type OTLPStats struct {
	Emitted, ExportedRecords, FailedExportRecords, Unresolved, Rejected uint64
}

// SnapshotOTLP returns process-local counters without request data or credentials.
func SnapshotOTLP() OTLPStats {
	configureOTLPMu.Lock()
	runtime := configuredOTLP
	configureOTLPMu.Unlock()
	if runtime == nil {
		return OTLPStats{}
	}
	return runtime.stats()
}

// stats snapshots outcomes without treating unresolved records as exact queue depth.
func (r *otlpRuntime) stats() OTLPStats {
	emitted, exported, failed := r.emitted.Load(), r.exported.Load(), r.failed.Load()
	resolved := exported + failed
	var unresolved uint64
	if emitted > resolved {
		unresolved = emitted - resolved
	}
	return OTLPStats{Emitted: emitted, ExportedRecords: exported, FailedExportRecords: failed, Unresolved: unresolved, Rejected: r.rejected.Load()}
}

// ConfigureOTLP enables the optional private log branch once. Empty endpoint leaves stdout unchanged.
// The returned idempotent shutdown must run after application producers stop, using a fresh deadline.
// This configures a private SDK provider and never replaces the process-wide OTel providers.
func ConfigureOTLP(ctx context.Context) (func(context.Context) error, error) {
	configureOTLPMu.Lock()
	defer configureOTLPMu.Unlock()
	if configuredOTLP != nil {
		return configuredOTLP.shutdown, nil
	}
	endpoint := strings.TrimSpace(os.Getenv("APP_LOG_OTLP_ENDPOINT"))
	if endpoint == "" {
		return func(context.Context) error { return nil }, nil
	}
	config := otlpConfig{
		endpoint: endpoint, tokenFile: os.Getenv("APP_LOG_OTLP_TOKEN_FILE"), caFile: os.Getenv("APP_LOG_OTLP_CA_FILE"),
		host: os.Getenv("APP_LOG_OTLP_HOST"), environment: os.Getenv("APP_LOG_OTLP_ENV"),
	}
	runtime, err := newOTLPRuntime(ctx, config)
	if err != nil {
		return nil, errors.Wrap(err, "configure private OTLP logs")
	}
	configuredOTLP = runtime
	activeOTLP.Store(runtime)
	return runtime.shutdown, nil
}

// newOTLPRuntime validates explicit configuration before constructing the private SDK.
func newOTLPRuntime(ctx context.Context, config otlpConfig) (*otlpRuntime, error) {
	endpoint, err := url.Parse(config.endpoint)
	if err != nil || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("invalid application OTLP endpoint")
	}
	if endpoint.Path == "" || endpoint.Path == "/" {
		endpoint.Path = "/v1/logs"
	}
	if endpoint.Path != "/v1/logs" {
		return nil, errors.New("application OTLP endpoint must use /v1/logs")
	}
	loopback := net.ParseIP(endpoint.Hostname())
	insecure := endpoint.Scheme == "http" && config.allowLoopbackHTTP && loopback != nil && loopback.IsLoopback()
	if endpoint.Scheme != "https" && !insecure {
		return nil, errors.New("application OTLP requires HTTPS")
	}
	if !validResourceLabel(config.host) || !validResourceLabel(config.environment) {
		return nil, errors.New("invalid application OTLP resource identity")
	}
	if err := validateOTLPResourceEnvironment(); err != nil {
		return nil, err
	}
	token, err := readBoundedOTLPFile(config.tokenFile, 4096)
	if err != nil {
		return nil, errors.New("cannot read application OTLP token file")
	}
	tokenText := strings.TrimSpace(string(token))
	if len(tokenText) < 16 || strings.IndexFunc(tokenText, func(r rune) bool { return r < 33 || r > 126 }) >= 0 {
		return nil, errors.New("invalid application OTLP token file")
	}
	var tlsConfig *tls.Config
	if !insecure {
		ca, readErr := readBoundedOTLPFile(config.caFile, 1<<20)
		if readErr != nil {
			return nil, errors.New("cannot read application OTLP CA file")
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(ca) {
			return nil, errors.New("invalid application OTLP CA file")
		}
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	}
	if config.queueSize == 0 {
		config.queueSize = otlpQueueSize
	}
	if config.batchSize == 0 {
		config.batchSize = otlpBatchSize
	}
	if config.interval == 0 {
		config.interval = time.Second
	}
	if config.timeout == 0 {
		config.timeout = otlpTimeout
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = tlsConfig
	client := &http.Client{
		Transport: transport, Timeout: config.timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("application OTLP redirects disabled") },
	}
	exporter, err := otlploghttp.New(ctx,
		otlploghttp.WithEndpointURL(endpoint.String()),
		otlploghttp.WithHeaders(map[string]string{"Authorization": "Bearer " + tokenText}),
		otlploghttp.WithTLSClientConfig(tlsConfig),
		otlploghttp.WithHTTPClient(client),
		otlploghttp.WithCompression(otlploghttp.NoCompression),
		otlploghttp.WithTimeout(config.timeout),
		otlploghttp.WithRetry(otlploghttp.RetryConfig{Enabled: true, InitialInterval: 100 * time.Millisecond, MaxInterval: time.Second, MaxElapsedTime: config.timeout}),
	)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, errors.New("cannot initialize application OTLP exporter")
	}
	runtime := &otlpRuntime{config: config}
	counted := &privateExporter{delegate: exporter, runtime: runtime, transport: transport}
	processor := sdklog.NewBatchProcessor(counted,
		sdklog.WithMaxQueueSize(config.queueSize), sdklog.WithExportMaxBatchSize(config.batchSize),
		sdklog.WithExportInterval(config.interval), sdklog.WithExportTimeout(config.timeout))
	runtime.provider = sdklog.NewLoggerProvider(
		sdklog.WithProcessor(processor),
		sdklog.WithResource(resource.NewSchemaless(attribute.String("service.name", "graphql"),
			attribute.String("service.namespace", "laisky"), attribute.String("host.name", config.host),
			attribute.String("deployment.environment.name", config.environment))),
		sdklog.WithAttributeCountLimit(-1), sdklog.WithAttributeValueLengthLimit(-1))
	runtime.logger = runtime.provider.Logger("github.com/Laisky/laisky-blog-graphql/library/log")
	return runtime, nil
}

// validResourceLabel requires an explicitly chosen value representable by OTLP strings.
func validResourceLabel(value string) bool { return value != "" && utf8.ValidString(value) }

// readBoundedOTLPFile reads a regular bounded file without exposing its path or bytes in errors.
func readBoundedOTLPFile(path string, limit int64) (data []byte, readErr error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open OTLP configuration file")
	}
	defer func() {
		if err := file.Close(); err != nil && readErr == nil {
			data = nil
			readErr = errors.New("cannot close OTLP configuration file")
		}
	}()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > limit {
		return nil, errors.New("invalid OTLP configuration file")
	}
	data, err = io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, errors.New("cannot read OTLP configuration file")
	}
	return data, nil
}

// shutdown rejects new callbacks, drains admitted callbacks and bounds SDK shutdown to ten seconds.
func (r *otlpRuntime) shutdown(ctx context.Context) error {
	r.shutdownOnce.Do(func() {
		r.admission.Lock()
		r.stopped = true
		activeOTLP.CompareAndSwap(r, nil)
		r.admission.Unlock()
		drainCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := r.provider.Shutdown(drainCtx); err != nil {
			r.shutdownErr = errors.New("application OTLP shutdown failed")
		}
	})
	return r.shutdownErr
}

// privateExporter counts SDK outcomes and replaces returned transport errors with fixed diagnostics.
type privateExporter struct {
	delegate  sdklog.Exporter
	runtime   *otlpRuntime
	transport *http.Transport
}

// Export counts one final SDK outcome after its internal retry policy completes.
func (e *privateExporter) Export(ctx context.Context, records []sdklog.Record) error {
	if err := e.delegate.Export(ctx, records); err != nil {
		e.runtime.failed.Add(uint64(len(records)))
		return errors.New("application OTLP export failed")
	}
	e.runtime.exported.Add(uint64(len(records)))
	return nil
}

// ForceFlush forwards the exporter flush while sanitizing returned errors.
func (e *privateExporter) ForceFlush(ctx context.Context) error {
	if err := e.delegate.ForceFlush(ctx); err != nil {
		return errors.New("application OTLP flush failed")
	}
	return nil
}

// Shutdown closes private idle connections after shutting down the exporter.
func (e *privateExporter) Shutdown(ctx context.Context) error {
	defer e.transport.CloseIdleConnections()
	if err := e.delegate.Shutdown(ctx); err != nil {
		return errors.New("application OTLP exporter shutdown failed")
	}
	return nil
}

// validateOTLPResourceEnvironment rejects malformed resource syntax before SDK diagnostics.
// Application-selected custom attributes remain intact; explicit application identity wins on merge.
func validateOTLPResourceEnvironment() error {
	raw := strings.TrimSpace(os.Getenv("OTEL_RESOURCE_ATTRIBUTES"))
	if raw == "" {
		return nil
	}
	for _, pair := range strings.Split(raw, ",") {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || strings.TrimSpace(key) == "" || !utf8.ValidString(key) || !utf8.ValidString(value) {
			return errors.New("invalid application OTLP resource environment")
		}
		if _, err := url.PathUnescape(strings.TrimSpace(value)); err != nil {
			return errors.New("invalid application OTLP resource environment")
		}
	}
	return nil
}
