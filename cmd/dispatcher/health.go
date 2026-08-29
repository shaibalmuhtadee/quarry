package main

import (
	"context"

	"google.golang.org/grpc/codes"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

const (
	dispatcherLivenessService  = "quarry.dispatcher.liveness"
	dispatcherReadinessService = "quarry.dispatcher.readiness"
)

type postgresPinger interface {
	Ping(context.Context) error
}

type dispatcherHealthServer struct {
	healthv1.UnimplementedHealthServer
	postgres postgresPinger
}

func newDispatcherHealthServer(postgres postgresPinger) *dispatcherHealthServer {
	return &dispatcherHealthServer{postgres: postgres}
}

func (server *dispatcherHealthServer) Check(
	ctx context.Context,
	request *healthv1.HealthCheckRequest,
) (*healthv1.HealthCheckResponse, error) {
	switch request.GetService() {
	case dispatcherLivenessService:
		return healthResponse(healthv1.HealthCheckResponse_SERVING), nil
	case dispatcherReadinessService:
		if err := server.postgres.Ping(ctx); err != nil {
			return healthResponse(healthv1.HealthCheckResponse_NOT_SERVING), nil
		}
		return healthResponse(healthv1.HealthCheckResponse_SERVING), nil
	default:
		return nil, status.Error(codes.NotFound, "unknown health service")
	}
}

func healthResponse(servingStatus healthv1.HealthCheckResponse_ServingStatus) *healthv1.HealthCheckResponse {
	return &healthv1.HealthCheckResponse{Status: servingStatus}
}
