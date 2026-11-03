package integration

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Ember-Moth/aws-panel/internal/app/migration/schema"
	"github.com/Ember-Moth/aws-panel/internal/auth"
	"github.com/Ember-Moth/aws-panel/internal/auth/token"
	"github.com/Ember-Moth/aws-panel/internal/config"
	"github.com/Ember-Moth/aws-panel/internal/infra/postgres"
	"github.com/Ember-Moth/aws-panel/internal/module/identity"
	"github.com/Ember-Moth/aws-panel/internal/module/identity/contract"
	"github.com/Ember-Moth/aws-panel/internal/module/platform"
	"github.com/Ember-Moth/aws-panel/internal/repository/kernel"
	"gorm.io/gorm"
)

type allowLogin struct{}

func (allowLogin) Allow(context.Context, string, string) (bool, error) { return true, nil }

type rejectAudit struct{}

func (rejectAudit) AppendAudit(context.Context, kernel.AuditEntry) error {
	return errors.New("audit unavailable")
}

func identityDatabase(t *testing.T) (*gorm.DB, *sql.DB, *sql.DB) {
	t.Helper()
	value := os.Getenv("AWS_PANEL_TEST_DB_PORT")
	if value == "" {
		t.Skip("isolated PostgreSQL not configured")
	}
	port, err := strconv.Atoi(value)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults().Database
	cfg.Port = port
	cfg.Name = "aws_panel_test"
	cfg.Password = os.Getenv("AWS_PANEL_TEST_DB_PASSWORD")
	if name := os.Getenv("AWS_PANEL_TEST_DB_NAME"); name != "" {
		cfg.Name = name
	}
	if !strings.HasPrefix(cfg.Name, "aws_panel_test") {
		t.Fatal("refusing to mutate a non-test database")
	}
	migration := cfg
	if user := os.Getenv("AWS_PANEL_TEST_MIGRATION_USER"); user != "" {
		migration.User = user
		migration.Password = os.Getenv("AWS_PANEL_TEST_MIGRATION_PASSWORD")
	}
	if err := schema.Up(migration); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	db, pool, err := postgres.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, owner, err := postgres.Open(ctx, migration)
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	clear := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := owner.ExecContext(ctx, "TRUNCATE TABLE dashboard_runs, dashboard_snapshots, operation_effects, operation_target_locks, lightsail_creations, inventory_schedules, resource_snapshots, inventory_runs, inventory_scopes, operations_diagnostic_effects, operations_outbox, job_events, job_attempts, job_steps, job_items, jobs, plans, account_grants, connections, proxy_profiles, secrets, aws_accounts, sessions, members, audit_events RESTART IDENTITY"); err != nil {
			t.Error("cannot reset identity fixture")
		}
	}
	t.Cleanup(func() { clear(); pool.Close(); owner.Close() })
	clear()
	return db, pool, owner
}

func TestIdentityTransactions(t *testing.T) {
	db, pool, owner := identityDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	build := func(factory kernel.AuditFactory) identity.Service {
		return identity.New(identity.Deps{Store: identity.NewRepository(kernel.Connection{DB: db}, factory), Limiter: allowLogin{}, SessionTTL: time.Hour})
	}
	service := build(platform.NewAuditAppender)
	meta := auth.RequestMeta{RequestID: "identity-integration", ClientIP: "127.0.0.1"}
	adminPassword := "integration-admin-passphrase"
	memberPassword := "integration-member-passphrase"
	newPassword := "integration-updated-passphrase"
	admin, err := service.Bootstrap(ctx, contract.CreateMemberRequest{Username: "Admin", DisplayName: "管理员", Password: adminPassword}, meta)
	if err != nil {
		t.Fatal(err)
	}
	if admin.Username != "admin" || admin.Role != contract.RoleAdmin {
		t.Fatal("bootstrap did not normalize administrator")
	}
	if _, err := service.Bootstrap(ctx, contract.CreateMemberRequest{Username: "another", DisplayName: "另一个", Password: adminPassword}, meta); !errors.Is(err, contract.ErrInitialized) {
		t.Fatal("bootstrap is not one-time")
	}
	login := func(username, password string) contract.LoginGrant {
		t.Helper()
		grant, err := service.Login(ctx, contract.LoginRequest{Username: username, Password: password}, meta)
		if err != nil {
			t.Fatal(err)
		}
		return grant
	}
	adminSession := login("admin", adminPassword)
	otherAdminSession := login("admin", adminPassword)
	if adminSession.Token == otherAdminSession.Token {
		t.Fatal("login reused a token")
	}
	if _, err := service.Authenticate(ctx, token.Hash(adminSession.Token)); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatal("stored token hash accepted as a credential")
	}
	if _, err := service.Login(ctx, contract.LoginRequest{Username: "unknown", Password: adminPassword}, meta); !errors.Is(err, contract.ErrInvalidCredentials) {
		t.Fatal("unknown account response differs")
	}
	active := true
	if _, err := service.UpdateMember(ctx, admin.ID, admin.ID, contract.UpdateMemberRequest{DisplayName: admin.DisplayName, Role: contract.RoleViewer, Active: &active, Version: admin.Version}, meta); !errors.Is(err, contract.ErrLastAdmin) {
		t.Fatal("last administrator could be demoted")
	}
	create := func(name, role string) contract.Member {
		t.Helper()
		member, err := service.CreateMember(ctx, admin.ID, contract.CreateMemberRequest{Username: name, DisplayName: name, Password: memberPassword, Role: role}, meta)
		if err != nil {
			t.Fatal(err)
		}
		return member
	}
	viewer := create("viewer", contract.RoleViewer)
	operator := create("operator", contract.RoleOperator)
	if _, err := service.CreateMember(ctx, viewer.ID, contract.CreateMemberRequest{Username: "forbidden", DisplayName: "bad", Password: memberPassword, Role: contract.RoleAdmin}, meta); !errors.Is(err, contract.ErrForbidden) {
		t.Fatal("viewer created a member")
	}
	viewerSession := login(viewer.Username, memberPassword)
	otherViewerSession := login(viewer.Username, memberPassword)
	if err := service.ChangePassword(ctx, viewer.ID, contract.ChangePasswordRequest{CurrentPassword: memberPassword, NewPassword: newPassword}, meta); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{viewerSession.Token, otherViewerSession.Token} {
		if _, err := service.Authenticate(ctx, raw); !errors.Is(err, contract.ErrUnauthenticated) {
			t.Fatal("password change left an active session")
		}
	}
	if _, err := service.Login(ctx, contract.LoginRequest{Username: viewer.Username, Password: memberPassword}, meta); !errors.Is(err, contract.ErrInvalidCredentials) {
		t.Fatal("old password is still accepted")
	}
	freshViewer := login(viewer.Username, newPassword)
	viewer, err = service.Member(ctx, admin.ID, viewer.ID)
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	viewer, err = service.UpdateMember(ctx, admin.ID, viewer.ID, contract.UpdateMemberRequest{DisplayName: viewer.DisplayName, Role: viewer.Role, Active: &disabled, Version: viewer.Version}, meta)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, freshViewer.Token); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatal("disabled member session remained valid")
	}
	if _, err := service.Login(ctx, contract.LoginRequest{Username: viewer.Username, Password: newPassword}, meta); !errors.Is(err, contract.ErrInvalidCredentials) {
		t.Fatal("disabled member could log in")
	}
	viewer, err = service.UpdateMember(ctx, admin.ID, viewer.ID, contract.UpdateMemberRequest{DisplayName: viewer.DisplayName, Role: viewer.Role, Active: &active, Version: viewer.Version}, meta)
	if err != nil {
		t.Fatal(err)
	}
	expiring := login(viewer.Username, newPassword)
	if _, err := owner.ExecContext(ctx, "UPDATE sessions SET created_at = now() - interval '2 hours', expires_at = now() - interval '1 hour' WHERE token_hash = $1", token.Hash(expiring.Token)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, expiring.Token); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatal("expired session accepted")
	}
	operatorSession := login(operator.Username, memberPassword)
	if _, err := service.UpdateMember(ctx, admin.ID, operator.ID, contract.UpdateMemberRequest{DisplayName: operator.DisplayName, Role: contract.RoleViewer, Active: &active, Version: operator.Version}, meta); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, operatorSession.Token); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatal("role change left an active session")
	}
	broken := build(func(kernel.Connection) kernel.AuditAppender { return rejectAudit{} })
	if _, err := broken.CreateMember(ctx, admin.ID, contract.CreateMemberRequest{Username: "audit-failure", DisplayName: "audit failure", Role: contract.RoleViewer, Password: memberPassword}, meta); err == nil {
		t.Fatal("audit failure did not fail mutation")
	}
	var exists bool
	if err := pool.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM members WHERE username='audit-failure')").Scan(&exists); err != nil || exists {
		t.Fatal("failed audit did not roll back member creation")
	}
	if err := service.Logout(ctx, adminSession.Token, meta); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Authenticate(ctx, adminSession.Token); !errors.Is(err, contract.ErrUnauthenticated) {
		t.Fatal("logged-out session remained active")
	}
	if _, err := build(platform.NewAuditAppender).Authenticate(ctx, otherAdminSession.Token); err != nil {
		t.Fatal("session depended on process-local state")
	}
	var leaked bool
	if err := pool.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM sessions WHERE token_hash=$1)", adminSession.Token).Scan(&leaked); err != nil || leaked {
		t.Fatal("raw session token persisted")
	}
	rows, err := pool.QueryContext(ctx, "SELECT row_to_json(audit_events)::text FROM audit_events")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{adminPassword, memberPassword, newPassword, adminSession.Token, viewerSession.Token, "$argon2id$"} {
			if strings.Contains(line, secret) {
				t.Fatal("secret leaked into audit")
			}
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	secondAdmin := create("admin-two", contract.RoleAdmin)
	results := make(chan error, 2)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for _, member := range []contract.Member{admin, secondAdmin} {
		wait.Add(1)
		go func(member contract.Member) {
			defer wait.Done()
			<-start
			_, err := service.UpdateMember(ctx, member.ID, member.ID, contract.UpdateMemberRequest{DisplayName: member.DisplayName, Role: contract.RoleViewer, Active: &active, Version: member.Version}, meta)
			results <- err
		}(member)
	}
	close(start)
	wait.Wait()
	close(results)
	success, protected := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, contract.ErrLastAdmin) {
			protected++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || protected != 1 {
		t.Fatal("concurrent demotions did not preserve exactly one administrator")
	}
	var count int
	if err := pool.QueryRowContext(ctx, "SELECT count(*) FROM members WHERE active AND role='admin'").Scan(&count); err != nil || count != 1 {
		t.Fatal("administrator invariant broken")
	}
	var isSuper bool
	if err := pool.QueryRowContext(ctx, "SELECT rolsuper FROM pg_roles WHERE rolname=current_user").Scan(&isSuper); err != nil {
		t.Fatal(err)
	}
	if !isSuper {
		var canRewrite bool
		if err := pool.QueryRowContext(ctx, "SELECT has_table_privilege(current_user,'audit_events','UPDATE') OR has_table_privilege(current_user,'audit_events','DELETE')").Scan(&canRewrite); err != nil || canRewrite {
			t.Fatal("runtime role can rewrite audit history")
		}
	}
}
