//go:build integration

package testenv

import (
	"os"
	"strings"
	"testing"
)

// TestImagesMatchCompose fails when the integration tests and the deployed
// stack would run different database versions.
func TestImagesMatchCompose(t *testing.T) {
	compose, err := os.ReadFile(RepoFile("deployments/docker-compose.yml"))
	if err != nil {
		t.Fatalf("read compose file: %v", err)
	}
	for _, image := range []string{PostgresImage, ClickHouseImage, CollectorImage, PrometheusImage} {
		if !strings.Contains(string(compose), "image: "+image+"\n") {
			t.Errorf("deployments/docker-compose.yml does not use %s; update testenv or the stack so they match", image)
		}
	}
}
