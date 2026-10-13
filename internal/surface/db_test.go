package surface

import (
	"context"
	"strings"
	"testing"
)

func TestDatabase(t *testing.T) {
	runCases(t, []caseTest{
		{"sql", "added", []string{"added table: table audit_log", "added index: index audit_log_action_idx",
			"added column: users.last_seen", "added column: users.bio", "added view: view active_users",
			"added index: index on audit_log", "added table: table kv"}},
		{"sql", "removed", []string{"removed table: table legacy_sessions", "removed table: table legacy_tokens",
			"removed column: users.legacy_theme", "removed index: index users_email_idx"}},
		{"sql", "changed", []string{"changed column: users.display_name", "changed column: users.email",
			"changed table: table organisations", "changed column: orders.total", "added column: orders.currency"}},
		{"sql", "schema", []string{"changed column: users.name", "removed column: users.legacy_theme",
			"added column: users.avatar_url", "removed table: table sessions", "added table: table teams"}},
		{"sql", "edited", []string{"added column: users.nickname", "removed table: table scratch"}},
		{"sql", "negative", []string{"changed migration: migration 0006_data.sql"}},

		{"django-migrations", "added", []string{"added column: user.avatar_url", "added table: table Preference", "added index: index user_email_idx"}},
		{"django-migrations", "removed", []string{"removed column: user.legacy_theme", "removed table: table Session"}},
		{"django-migrations", "changed", []string{"changed column: user.display_name", "changed column: user.email"}},
		{"django-migrations", "negative", []string{"changed migration: migration 0005_data.py"}},

		{"alembic", "added", []string{"added table: table audit", "added column: users.avatar_url", "added index: index ix_users_email"}},
		{"alembic", "removed", []string{"removed column: users.legacy", "removed table: table sessions"}},
		{"alembic", "changed", []string{"changed column: users.display_name", "changed table: table orgs"}},
		{"alembic", "negative", []string{"changed migration: migration jkl000_noop.py"}},

		{"rails", "added", []string{"added column: users.avatar_url", "added table: table preferences",
			"added index: index on users(email)", "added column: posts.author_id"}},
		{"rails", "removed", []string{"removed column: users.legacy_theme", "removed table: table sessions"}},
		{"rails", "changed", []string{"changed column: users.display_name", "changed column: users.email"}},
		{"rails", "negative", []string{"changed migration: migration 20240104000000_noop.rb"}},

		{"knex", "added", []string{"added table: table audit", "added column: users.avatar_url", "added index: index users_email_idx"}},
		{"knex", "removed", []string{"removed column: users.legacy", "removed table: table sessions"}},
		{"knex", "changed", []string{"changed column: users.display_name", "changed table: table orgs"}},
		{"knex", "negative", nil},

		{"sequelize", "added", []string{"added column: users.avatar_url", "added table: table audit", "added index: index users_email_idx"}},
		{"sequelize", "removed", []string{"removed column: users.legacy", "removed table: table sessions"}},
		{"sequelize", "changed", []string{"changed column: users.display_name", "changed column: users.email"}},
		{"sequelize", "negative", []string{"changed migration: migration 20240104-data.js"}},

		{"prisma", "added", []string{"added column: User.avatarUrl", "added index: index user_email_idx", "added table: table Preference"}},
		{"prisma", "removed", []string{"removed column: User.legacyTheme", "removed table: table Session"}},
		{"prisma", "changed", []string{"changed column: User.name"}},
		{"prisma", "negative", []string{"changed schema: schema schema.prisma"}},

		{"models", "added", []string{"added column: User.avatar_url", "added table: table Preference", "added column: Account.bio"}},
		{"models", "removed", []string{"removed column: User.legacy_theme"}},
		{"models", "changed", []string{"changed column: User.name"}},
		{"models", "negative", nil},
	})
}

// TestDatabaseDetails checks the one-way marks, rename details and the
// edited-migration words.
func TestDatabaseDetails(t *testing.T) {
	detailOf := func(dir, name, words string) string {
		cs := build(context.Background(), patchInput(t, loadCases(t, dir)[name]))
		return find(t, cs, words).Detail
	}
	tests := []struct {
		dir, name, words, detail string
	}{
		{"sql", "removed", "removed table: table legacy_sessions", "one-way"},
		{"sql", "removed", "removed column: users.legacy_theme", "one-way"},
		{"sql", "changed", "changed column: users.display_name", "was users.name · one-way"},
		{"sql", "changed", "changed table: table organisations", "was table accounts · one-way"},
		{"sql", "changed", "changed column: users.email", "SET NOT NULL"},
		{"sql", "changed", "changed column: orders.total", "TYPE NUMERIC(12, 2)"},
		{"sql", "schema", "changed column: users.name", "was TEXT"},
		{"sql", "schema", "removed table: table sessions", "one-way"},
		{"sql", "edited", "added column: users.nickname", "statement edited"},
		{"sql", "edited", "removed table: table scratch", "no longer in migration"},
		{"alembic", "changed", "changed column: users.display_name", "was users.name · one-way"},
		{"rails", "removed", "removed column: users.legacy_theme", "one-way"},
		{"django-migrations", "changed", "changed column: user.display_name", "was user.name · one-way"},
		{"prisma", "changed", "changed column: User.name", "was String"},
	}
	for _, tc := range tests {
		t.Run(tc.dir+"/"+tc.words, func(t *testing.T) {
			if got := detailOf(tc.dir, tc.name, tc.words); got != tc.detail {
				t.Errorf("detail = %q, want %q", got, tc.detail)
			}
		})
	}
}

// TestDatabaseConfidence checks DDL in .sql files is near-certain and DDL in
// application code strings is a heuristic.
func TestDatabaseConfidence(t *testing.T) {
	cs := build(context.Background(), patchInput(t, loadCases(t, "sql")["added"]))
	if e := find(t, cs, "added table: table audit_log"); e.Confidence < 0.95 || e.Framework != fwSQL {
		t.Errorf("audit_log = %+v, want sql at >= .95", e)
	}
	if e := find(t, cs, "added table: table kv"); e.Confidence >= 0.95 || !strings.HasSuffix(e.Path, "db.go") {
		t.Errorf("kv = %+v, want a lower-confidence entry in db.go", e)
	}
}
