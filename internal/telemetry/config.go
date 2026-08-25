package telemetry

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path"
	"strings"
)

const (
	otlpEndpointEnv       = "OTEL_EXPORTER_OTLP_ENDPOINT"
	otlpTracesEndpointEnv = "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"
	serviceNameEnv        = "OTEL_SERVICE_NAME"
	defaultTraceEndpoint  = "http://localhost:4318/v1/traces"
)

type Config struct {
	ServiceName    string
	MetricsAddress string
	TraceEndpoint  string
}

func LoadConfig(defaultServiceName, metricsAddressEnv, defaultMetricsAddress string) (Config, error) {
	serviceName := envOrDefault(serviceNameEnv, defaultServiceName)
	if strings.TrimSpace(serviceName) == "" {
		return Config{}, errors.New("telemetry service name must not be empty")
	}

	metricsAddress := ""
	if defaultMetricsAddress != "" {
		metricsAddress = envOrDefault(metricsAddressEnv, defaultMetricsAddress)
		if _, err := net.ResolveTCPAddr("tcp", metricsAddress); err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", metricsAddressEnv, err)
		}
	}

	traceEndpoint := defaultTraceEndpoint
	if value := os.Getenv(otlpEndpointEnv); value != "" {
		parsed, err := parseOTLPEndpoint(value)
		if err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", otlpEndpointEnv, err)
		}
		parsed.Path = path.Join(parsed.Path, "v1/traces")
		traceEndpoint = parsed.String()
	}
	if value := os.Getenv(otlpTracesEndpointEnv); value != "" {
		if _, err := parseOTLPEndpoint(value); err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", otlpTracesEndpointEnv, err)
		}
		traceEndpoint = value
	}

	return Config{
		ServiceName:    serviceName,
		MetricsAddress: metricsAddress,
		TraceEndpoint:  traceEndpoint,
	}, nil
}

func envOrDefault(name, defaultValue string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return defaultValue
}

func parseOTLPEndpoint(value string) (*url.URL, error) {
	parsed, err := url.ParseRequestURI(value)
	if err != nil {
		return nil, err
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errors.New("endpoint must be an absolute HTTP or HTTPS URL")
	}
	return parsed, nil
}
