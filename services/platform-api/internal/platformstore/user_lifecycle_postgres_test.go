package platformstore

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"mcp-runtime/pkg/platformauth"
	"mcp-runtime/pkg/policy"
	"mcp-runtime/pkg/sessionstate"
)

// Uses an explicitly configured disposable database, never ambient POSTGRES_DSN.
func lifecyclePostgresStore(t *testing.T) *Store {
	t.Helper()
	s, _ := lifecyclePostgresStoreWithDSN(t)
	return s
}

func lifecyclePostgresStoreWithDSN(t *testing.T) (*Store, string) {
	t.Helper()
	dsn := os.Getenv("PLATFORM_SESSION_TEST_DSN")
	if dsn == "" {
		t.Skip("set PLATFORM_SESSION_TEST_DSN to disposable Postgres")
	}
	admin, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "user_lifecycle_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.ExecContext(t.Context(), `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dsn, "://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		q := u.Query()
		q.Set("search_path", schema+",public")
		u.RawQuery = q.Encode()
		dsn = u.String()
	} else {
		dsn += " search_path=" + schema + ",public"
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		if _, err := admin.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	s := &Store{db: db, jwtSecret: []byte("test-lifecycle-secret")}
	if err = s.migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = s.initializeSessions(t.Context()); err != nil {
		t.Fatal(err)
	}
	return s, dsn
}

func TestPostgresAccountDeactivationRevokesCredentialsSessionsAndIsIdempotent(t *testing.T) {
	s := lifecyclePostgresStore(t)
	ctx := t.Context()
	admin, err := s.CreatePasswordUser(ctx, "admin@example.test", "password-for-test", "admin")
	if err != nil {
		t.Fatal(err)
	}
	team, err := s.CreateTeam(ctx, "cleanup", "Cleanup", admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	user, _, err := s.CreateTeamUser(ctx, team.Slug, "temporary@example.test", "password-for-test", "member")
	if err != nil {
		t.Fatal(err)
	}
	bearer, err := s.CreateAccessToken(user, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	auth := platformauth.Authenticator{Secret: s.jwtSecret, Audience: platformauth.AudiencePlatform, Principals: s}
	request := httptest.NewRequest(http.MethodGet, "/private", nil)
	request.Header.Set("Authorization", "Bearer "+bearer)
	if _, ok, err := auth.AuthenticateRequest(request); err != nil || !ok {
		t.Fatalf("active bearer rejected: %v", err)
	}
	key, rawKey, err := s.CreateUserAPIKey(ctx, user.ID, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	registryKey, _, err := s.CreateRegistryCredential(ctx, user.ID, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	item := sessionFixture()
	item.Binding.AgentID = ""
	item.Binding.TeamID = ""
	item.Binding.HumanID = policy.HumanID(user.ID)
	if _, err = s.PutSession(ctx, item); err != nil {
		t.Fatal(err)
	}
	server := sessionstate.Server{Namespace: item.ServerNamespace, Name: item.ServerName, UID: item.ServerUID}
	snapshot, err := s.SessionSnapshot(ctx, server)
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.DeactivateUser(ctx, user.ID, admin.ID, AuditEvent{Source: "test", ActorIP: "unknown", AuthIdentity: "test-admin"})
	if err != nil {
		t.Fatal(err)
	}
	if out.AlreadyDeactivated || out.RevokedAPIKeys != 1 || out.RevokedRegistryCredentials != 1 || out.RevokedSessions != 1 || out.RemovedMemberships != 1 {
		t.Fatalf("cleanup=%+v", out)
	}
	if _, ok, err := s.AuthenticatePassword(ctx, user.Email, "password-for-test"); err != nil || ok {
		t.Fatalf("password still valid: ok=%t err=%v", ok, err)
	}
	if _, ok, err := s.AuthenticateUserAPIKey(ctx, rawKey); err != nil || ok {
		t.Fatalf("key still valid: ok=%t err=%v", ok, err)
	}
	if _, err = s.PrincipalForUserID(ctx, user.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("principal remains: %v", err)
	}
	if _, ok, err := auth.AuthenticateRequest(request); err != nil || ok {
		t.Fatalf("deactivated bearer accepted: ok=%t err=%v", ok, err)
	}
	keys, err := s.ListUserAPIKeys(ctx, user.ID)
	if err != nil || len(keys) != 1 || keys[0].ID != key.ID || !keys[0].Revoked {
		t.Fatalf("keys=%+v err=%v", keys, err)
	}
	registryKeys, err := s.ListRegistryCredentials(ctx, user.ID)
	if err != nil || len(registryKeys) != 1 || registryKeys[0].ID != registryKey.ID || !registryKeys[0].Revoked {
		t.Fatalf("registry=%+v err=%v", registryKeys, err)
	}
	stored, ok, err := s.GetSession(ctx, item.Namespace, string(item.Binding.Name))
	if err != nil || !ok || !stored.Binding.Revoked || stored.RevocationCause != "user_deactivated" {
		t.Fatalf("session=%+v err=%v", stored, err)
	}
	changes, err := s.SessionChanges(ctx, server, snapshot.Epoch, snapshot.Sequence)
	if err != nil || len(changes.Changes) != 1 || changes.Changes[0].Operation != "delete" {
		t.Fatalf("changes=%+v err=%v", changes, err)
	}
	var deletes int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM session_changes WHERE data->>'operation'='delete'`).Scan(&deletes); err != nil || deletes != 1 {
		t.Fatalf("delete changes=%d err=%v", deletes, err)
	}
	if _, err = s.PutSession(ctx, item); !errors.Is(err, ErrAccountInactive) {
		t.Fatalf("stale request restored session: %v", err)
	}
	if err = s.SetSessionRevoked(ctx, item.Namespace, string(item.Binding.Name), false, ""); !errors.Is(err, ErrAccountInactive) {
		t.Fatalf("unrevocation accepted: %v", err)
	}
	if _, _, err = s.CreateUserAPIKey(ctx, user.ID, "late"); !errors.Is(err, ErrAccountInactive) {
		t.Fatalf("late key issuance accepted: %v", err)
	}
	if _, _, err = s.CreateRegistryCredential(ctx, user.ID, "late"); !errors.Is(err, ErrAccountInactive) {
		t.Fatalf("late registry issuance accepted: %v", err)
	}
	if _, err = s.UpsertTeamMembership(ctx, team.Slug, user.ID, "member"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("inactive membership recreated: %v", err)
	}
	out, err = s.DeactivateUser(ctx, user.ID, admin.ID, AuditEvent{Source: "test"})
	if err != nil || !out.AlreadyDeactivated || out.RevokedSessions != 0 || out.RevokedAPIKeys != 0 {
		t.Fatalf("repeat=%+v err=%v", out, err)
	}
	var audit int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_logs WHERE action='user_deactivate' AND resource=$1 AND status='success'`, user.ID).Scan(&audit); err != nil || audit != 2 {
		t.Fatalf("audit=%d err=%v", audit, err)
	}
}

func TestPostgresAccountCleanupRollsBackIfAuditCannotBeSaved(t *testing.T) {
	s := lifecyclePostgresStore(t)
	ctx := t.Context()
	admin, err := s.CreatePasswordUser(ctx, "admin@example.test", "password-for-test", "admin")
	if err != nil {
		t.Fatal(err)
	}
	user, err := s.CreatePasswordUser(ctx, "user@example.test", "password-for-test", "user")
	if err != nil {
		t.Fatal(err)
	}
	_, raw, err := s.CreateUserAPIKey(ctx, user.ID, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `CREATE FUNCTION reject_lifecycle_audit() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RAISE EXCEPTION 'audit unavailable'; END$$;
 CREATE TRIGGER reject_lifecycle_audit BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION reject_lifecycle_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeactivateUser(ctx, user.ID, admin.ID, AuditEvent{}); err == nil {
		t.Fatal("account cleanup reported success without audit")
	}
	if _, ok, err := s.AuthenticateUserAPIKey(ctx, raw); err != nil || !ok {
		t.Fatalf("failed transaction revoked key: ok=%t err=%v", ok, err)
	}
	if _, err = s.PrincipalForUserID(ctx, user.ID); err != nil {
		t.Fatalf("failed transaction disabled account: %v", err)
	}
}

func TestPostgresAccountDeactivationProtectsOwnershipAndConcurrentIssuance(t *testing.T) {
	s := lifecyclePostgresStore(t)
	ctx := t.Context()
	owner, err := s.CreatePasswordUser(ctx, "owner@example.test", "password-for-test", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeactivateUser(ctx, owner.ID, owner.ID, AuditEvent{}); !errors.Is(err, ErrSelfDeactivation) {
		t.Fatalf("self=%v", err)
	}
	if _, err = s.DeactivateUser(ctx, owner.ID, "", AuditEvent{}); !errors.Is(err, ErrLastAdministrator) {
		t.Fatalf("last admin=%v", err)
	}
	admin, err := s.CreatePasswordUser(ctx, "other-admin@example.test", "password-for-test", "admin")
	if err != nil {
		t.Fatal(err)
	}
	team, err := s.CreateTeam(ctx, "owned", "Owned", owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeactivateUser(ctx, owner.ID, admin.ID, AuditEvent{}); !errors.Is(err, ErrLastTeamOwner) {
		t.Fatalf("last owner=%v", err)
	}
	if _, err = s.UpsertTeamMembership(ctx, team.Slug, admin.ID, "owner"); err != nil {
		t.Fatal(err)
	}
	item := sessionFixture()
	item.Binding.AgentID = ""
	item.Binding.TeamID = ""
	item.Binding.HumanID = policy.HumanID(owner.ID)
	var wg sync.WaitGroup
	start := make(chan struct{})
	issuanceErrors := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.PutSession(ctx, item)
			if err != nil && !errors.Is(err, ErrAccountInactive) {
				issuanceErrors <- err
			}
		}()
	}
	close(start)
	if _, err = s.DeactivateUser(ctx, owner.ID, admin.ID, AuditEvent{}); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(issuanceErrors)
	for err := range issuanceErrors {
		t.Error(err)
	}
	var active int
	if err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE data->'binding'->>'human_id'=$1 AND NOT revoked`, owner.ID).Scan(&active); err != nil || active != 0 {
		t.Fatalf("active=%d err=%v", active, err)
	}
	// Retained OIDC identities cannot be rebound under a changed email.
	if _, err = s.db.ExecContext(ctx, `INSERT INTO auth_identities(user_id,provider,subject) VALUES($1,'oidc:test','subject')`, owner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.EnsureOIDCUser(ctx, "oidc:test", "subject", "changed@example.test", "user"); !errors.Is(err, ErrAccountInactive) {
		t.Fatalf("OIDC reactivation=%v", err)
	}
}
