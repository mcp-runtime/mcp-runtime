package integration

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq"

	"mcp-platform-api/internal/platformstore"
)

// This opt-in test owns an isolated schema in a disposable local Postgres.
// Never use a production database to exercise schema migrations.
func TestTeamSlugCanBeReusedAfterProvisioningRollback(t *testing.T) {
	dsn := os.Getenv("MCP_PLATFORM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set MCP_PLATFORM_TEST_POSTGRES_DSN for disposable local Postgres")
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") {
		t.Fatal("integration database must use a localhost PostgreSQL URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	schema := "team_slug_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("clean test schema: %v", err)
		}
	}()
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	store, err := platformstore.Open(ctx, u.String(), []byte("integration-test-key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// Recreate the legacy inline UNIQUE constraint, then re-open to exercise
	// the real upgrade migration rather than only the fresh-install schema.
	if _, err := db.ExecContext(ctx, "ALTER TABLE "+schema+".teams ADD CONSTRAINT teams_slug_key UNIQUE (slug)"); err != nil {
		t.Fatal(err)
	}
	store, err = platformstore.Open(ctx, u.String(), []byte("integration-test-key"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, err := store.CreateTeam(ctx, "examples", "Examples", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateTeam(ctx, "examples", "Duplicate", ""); err == nil {
		t.Fatal("an active team slug must remain unique")
	}
	if err := store.DeleteTeamBySlug(ctx, "examples"); err != nil {
		t.Fatal(err)
	}
	second, err := store.CreateTeam(ctx, "examples", "Retry", "")
	if err != nil {
		t.Fatalf("team retry after rollback failed: %v", err)
	}
	if first.ID == second.ID || first.Namespace != second.Namespace {
		t.Fatalf("retry should create a new team in the same derived namespace: first=%#v second=%#v", first, second)
	}
	active, ok, err := store.GetTeamBySlug(ctx, "examples")
	if err != nil || !ok || active.ID != second.ID {
		t.Fatalf("active team = %#v, found=%t, error=%v", active, ok, err)
	}
}
