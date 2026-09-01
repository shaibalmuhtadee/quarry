package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type apiResource struct {
	Name         string   `json:"name"`
	SingularName string   `json:"singularName"`
	Namespaced   bool     `json:"namespaced"`
	Kind         string   `json:"kind"`
	Verbs        []string `json:"verbs"`
}

func main() {
	addressFile := flag.String("address-file", "", "path that receives the discovery server URL")
	flag.Parse()
	if *addressFile == "" {
		fmt.Fprintln(os.Stderr, "-address-file is required")
		os.Exit(2)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen: %v\n", err)
		os.Exit(1)
	}

	server := &http.Server{
		Handler:           discoveryHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := os.WriteFile(*addressFile, []byte("http://"+listener.Addr().String()), 0o600); err != nil {
		_ = listener.Close()
		fmt.Fprintf(os.Stderr, "write address file: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "serve discovery: %v\n", err)
		os.Exit(1)
	}
}

func discoveryHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api", writeJSON(map[string]any{
		"apiVersion": "v1",
		"kind":       "APIVersions",
		"versions":   []string{"v1"},
	}))
	mux.HandleFunc("GET /apis", writeJSON(map[string]any{
		"apiVersion": "v1",
		"kind":       "APIGroupList",
		"groups": []any{
			apiGroup("apps", "v1"),
			apiGroup("batch", "v1"),
		},
	}))
	mux.HandleFunc("GET /api/v1", writeJSON(resourceList("v1",
		resource("namespaces", "Namespace", false),
		resource("secrets", "Secret", true),
		resource("services", "Service", true),
	)))
	mux.HandleFunc("GET /apis/apps/v1", writeJSON(resourceList("apps/v1",
		resource("deployments", "Deployment", true),
		resource("statefulsets", "StatefulSet", true),
	)))
	mux.HandleFunc("GET /apis/batch/v1", writeJSON(resourceList("batch/v1",
		resource("jobs", "Job", true),
	)))
	return mux
}

func apiGroup(name, version string) map[string]any {
	groupVersion := name + "/" + version
	return map[string]any{
		"name": name,
		"versions": []any{
			map[string]string{"groupVersion": groupVersion, "version": version},
		},
		"preferredVersion": map[string]string{
			"groupVersion": groupVersion,
			"version":      version,
		},
	}
}

func resourceList(groupVersion string, resources ...apiResource) map[string]any {
	return map[string]any{
		"apiVersion":   "v1",
		"groupVersion": groupVersion,
		"kind":         "APIResourceList",
		"resources":    resources,
	}
}

func resource(name, kind string, namespaced bool) apiResource {
	return apiResource{
		Name:         name,
		SingularName: "",
		Namespaced:   namespaced,
		Kind:         kind,
		Verbs:        []string{"create", "get", "list", "patch", "update"},
	}
}

func writeJSON(value any) http.HandlerFunc {
	return func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(response).Encode(value); err != nil {
			http.Error(response, err.Error(), http.StatusInternalServerError)
		}
	}
}
