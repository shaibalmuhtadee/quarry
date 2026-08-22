package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

type requestLogDetailsKey struct{}

type requestLogDetails struct {
	jobID string
}

type responseStateWriter struct {
	http.ResponseWriter
	status int
}

func (writer *responseStateWriter) WriteHeader(status int) {
	if writer.status != 0 {
		return
	}
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *responseStateWriter) Write(body []byte) (int, error) {
	if writer.status == 0 {
		writer.WriteHeader(http.StatusOK)
	}
	return writer.ResponseWriter.Write(body)
}

func (writer *responseStateWriter) Unwrap() http.ResponseWriter {
	return writer.ResponseWriter
}

func logRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		startedAt := time.Now()
		details := &requestLogDetails{}
		request = request.WithContext(context.WithValue(request.Context(), requestLogDetailsKey{}, details))
		stateWriter := &responseStateWriter{ResponseWriter: writer}

		next.ServeHTTP(stateWriter, request)

		status := stateWriter.status
		if status == 0 {
			status = http.StatusOK
		}
		attributes := []slog.Attr{
			slog.String("method", request.Method),
			slog.String("path", request.URL.Path),
			slog.Int("status", status),
			slog.String("outcome", requestOutcome(status)),
			slog.Duration("duration", time.Since(startedAt)),
		}
		if details.jobID != "" {
			attributes = append(attributes, slog.String("job_id", details.jobID))
		}

		logger.LogAttrs(request.Context(), requestLogLevel(status), "http request", attributes...)
	})
}

func setRequestJobID(request *http.Request, jobID string) {
	details, ok := request.Context().Value(requestLogDetailsKey{}).(*requestLogDetails)
	if ok {
		details.jobID = jobID
	}
}

func requestOutcome(status int) string {
	switch {
	case status >= http.StatusInternalServerError:
		return "server_error"
	case status >= http.StatusBadRequest:
		return "client_error"
	default:
		return "success"
	}
}

func requestLogLevel(status int) slog.Level {
	switch {
	case status >= http.StatusInternalServerError:
		return slog.LevelError
	case status >= http.StatusBadRequest:
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}
