package api

import (
	"context"
	"net/http"
	"time"
)

const readinessTimeout = 2 * time.Second

type ReadinessChecker interface {
	Ping(context.Context) error
}

type operationalResponse struct {
	Status string `json:"status"`
}

func (handler *handler) health(writer http.ResponseWriter, _ *http.Request) {
	writeJSON(writer, http.StatusOK, operationalResponse{Status: "ok"})
}

func (handler *handler) ready(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), readinessTimeout)
	defer cancel()

	if err := handler.readiness.Ping(ctx); err != nil {
		writeJSON(writer, http.StatusServiceUnavailable, operationalResponse{Status: "not_ready"})
		return
	}

	writeJSON(writer, http.StatusOK, operationalResponse{Status: "ready"})
}
