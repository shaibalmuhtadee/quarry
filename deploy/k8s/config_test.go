package k8s_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestKustomizeStagesAndKindOverlay(t *testing.T) {
	t.Parallel()

	requireInOrder(t, readFile(t, "base", "kustomization.yaml"),
		"- postgres", "- migration", "- applications")
	requireInOrder(t, readFile(t, "overlays", "kind", "kustomization.yaml"),
		"- postgres", "- migration", "- applications")

	postgresOverlay := readFile(t, "overlays", "kind", "postgres", "kustomization.yaml")
	requireContains(t, postgresOverlay,
		"- ../../../base/postgres",
		"disableNameSuffixHash: true",
		"name: quarry-postgres",
		"password=quarry-kind-local",
		"database-url=postgres://quarry:quarry-kind-local@postgres:5432/quarry?sslmode=disable",
	)

	migrationOverlay := readFile(t, "overlays", "kind", "migration", "kustomization.yaml")
	requireContains(t, migrationOverlay,
		"- ../../../base/migration",
		"name: quarry-migration",
		"newTag: kind",
	)

	applicationsOverlay := readFile(t, "overlays", "kind", "applications", "kustomization.yaml")
	for _, image := range []string{"quarry-api", "quarry-dispatcher", "quarry-worker"} {
		requireContains(t, applicationsOverlay, "name: "+image, "newName: "+image)
	}
	if count := strings.Count(applicationsOverlay, "newTag: kind"); count != 3 {
		t.Fatalf("kind applications image tag count = %d, want 3", count)
	}

	base := readTree(t, "base")
	if strings.Contains(base, "quarry-kind-local") {
		t.Fatal("kind-only credential leaked into the Kubernetes base")
	}
}

func TestPostgresAndMigrationResources(t *testing.T) {
	t.Parallel()

	postgresService := readFile(t, "base", "postgres", "service.yaml")
	requireContains(t, postgresService,
		"kind: Service",
		"name: postgres",
		"clusterIP: None",
		"port: 5432",
	)

	postgres := readFile(t, "base", "postgres", "statefulset.yaml")
	requireContains(t, postgres,
		"kind: StatefulSet",
		"serviceName: postgres",
		"replicas: 1",
		"image: postgres:18.6",
		"name: quarry-postgres",
		"mountPath: /var/lib/postgresql",
		"volumeClaimTemplates:",
		"storage: 1Gi",
	)
	requireContainerResources(t, postgres)

	migration := readFile(t, "base", "migration", "job.yaml")
	requireContains(t, migration,
		"apiVersion: batch/v1",
		"kind: Job",
		"name: quarry-migration",
		"backoffLimit: 3",
		"restartPolicy: Never",
		"image: quarry-migration:dev",
		"name: GOOSE_DBSTRING",
		"key: database-url",
	)
	requireContainerResources(t, migration)
}

func TestApplicationServicesProbesReplicasAndResources(t *testing.T) {
	t.Parallel()

	api := readFile(t, "base", "applications", "api.yaml")
	requireContains(t, api,
		"kind: Service",
		"name: quarry-api",
		"kind: Deployment",
		"replicas: 1",
		"image: quarry-api:dev",
		"path: /healthz",
		"path: /readyz",
	)
	requireContainerResources(t, api)

	dispatcher := readFile(t, "base", "applications", "dispatcher.yaml")
	requireContains(t, dispatcher,
		"kind: Service",
		"name: quarry-dispatcher",
		"kind: Deployment",
		"replicas: 2",
		"image: quarry-dispatcher:dev",
		"service: quarry.dispatcher.liveness",
		"service: quarry.dispatcher.readiness",
	)
	if count := strings.Count(dispatcher, "grpc:"); count != 2 {
		t.Fatalf("dispatcher gRPC probe count = %d, want 2", count)
	}
	requireContainerResources(t, dispatcher)

	worker := readFile(t, "base", "applications", "worker.yaml")
	requireContains(t, worker,
		"kind: Deployment",
		"name: quarry-worker",
		"replicas: 3",
		"terminationGracePeriodSeconds: 20",
		"image: quarry-worker:dev",
		"name: QUARRY_WORKER_SHUTDOWN_TIMEOUT",
		"value: 10s",
	)
	if strings.Contains(worker, "kind: Service") {
		t.Fatal("worker manifest must not define a Service")
	}
	requireContainerResources(t, worker)
}

func TestKubernetesTreeContainsNoDeferredResourceTypes(t *testing.T) {
	t.Parallel()

	configuration := strings.ToLower(readTree(t, "."))
	for _, forbidden := range []string{
		"kind: horizontalpodautoscaler",
		"kind: verticalpodautoscaler",
		"kind: customresourcedefinition",
		"kind: operator",
		"helm",
		"terraform",
		"type: loadbalancer",
	} {
		if strings.Contains(configuration, forbidden) {
			t.Errorf("Kubernetes configuration contains deferred resource marker %q", forbidden)
		}
	}

	for _, line := range strings.Split(configuration, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "apiversion:") {
			continue
		}
		switch line {
		case "apiversion: v1",
			"apiversion: apps/v1",
			"apiversion: batch/v1",
			"apiversion: kustomize.config.k8s.io/v1beta1":
		default:
			t.Errorf("Kubernetes configuration uses unexpected API %q", line)
		}
	}
}

func requireContainerResources(t *testing.T, manifest string) {
	t.Helper()
	requireContains(t, manifest,
		"resources:",
		"requests:",
		"limits:",
		"cpu:",
		"memory:",
	)
}

func requireContains(t *testing.T, contents string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if !strings.Contains(contents, fragment) {
			t.Errorf("configuration is missing %q", fragment)
		}
	}
}

func requireInOrder(t *testing.T, contents string, fragments ...string) {
	t.Helper()
	position := 0
	for _, fragment := range fragments {
		index := strings.Index(contents[position:], fragment)
		if index < 0 {
			t.Fatalf("configuration is missing ordered fragment %q", fragment)
		}
		position += index + len(fragment)
	}
}

func readTree(t *testing.T, root string) string {
	t.Helper()

	paths := make([]string, 0)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && (strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")) {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(paths)

	var contents strings.Builder
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		contents.Write(data)
		contents.WriteByte('\n')
	}
	return contents.String()
}

func readFile(t *testing.T, elements ...string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(elements...))
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Join(elements...), err)
	}
	return string(data)
}
