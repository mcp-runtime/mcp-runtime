package registryauth

import "testing"

func TestSharedPullRepositoriesCoverGatewayAndDoctorSmoke(t *testing.T) {
	scope := PullScope{Namespace: "mcp-servers", Repositories: SharedPullRepositories()}
	for _, repo := range []string{"mcp-gateway", "mcp-runtime-doctor-smoke"} {
		if !scope.Allows(repo) {
			t.Fatalf("shared pull scope must allow %s", repo)
		}
	}
	if scope.Allows("acme/app") {
		t.Fatal("shared pull scope must not allow tenant repositories")
	}
}

func TestSharedPullRepositoriesReturnsFreshSlice(t *testing.T) {
	first := SharedPullRepositories()
	first[0] = "tampered"
	if SharedPullRepositories()[0] == "tampered" {
		t.Fatal("callers must not be able to mutate the shared list")
	}
}
