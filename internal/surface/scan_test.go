package surface

import (
	"slices"
	"testing"
)

func TestLexer(t *testing.T) {
	tests := []struct {
		lang  string
		lines []string
		code  []string   // comment-free code per line
		strs  [][]string // string literals per line (quotes included)
	}{
		{langGo, []string{`r.Get("/x", h) // r.Get("/y", h)`, `s := "a // b" /* c`, `still comment */ x := 1`},
			[]string{`r.Get("/x", h)                  `, `s := "a // b"     `, `                 x := 1`},
			[][]string{{`"/x"`}, {`"a // b"`}, nil}},
		{langGo, []string{"q := `multi", "line", "end` + x"},
			[]string{"q := `multi", "line", "end` + x"},
			[][]string{{"`multi"}, {"line"}, {"end`"}}},
		{langJS, []string{`const a = 'it\'s'; // note`, "const t = `x ${y}`;"},
			[]string{`const a = 'it\'s';        `, "const t = `x ${y}`;"},
			[][]string{{`'it\'s'`}, {"`x ${y}`"}}},
		{langPy, []string{`x = "a # b"  # comment`, `"""doc`, `@app.route("/x")`, `end"""`},
			[]string{`x = "a # b"           `, `"""doc`, `@app.route("/x")`, `end"""`},
			[][]string{{`"a # b"`}, {`"""doc`}, {`@app.route("/x")`}, {`end"""`}}},
		{langSQL, []string{`SELECT 'it''s -- x' -- comment`, `CREATE TABLE "t" (id int); /* x */`},
			[]string{`SELECT 'it''s -- x'           `, `CREATE TABLE "t" (id int);        `},
			[][]string{{`'it''s -- x'`}, nil}},
		{langRuby, []string{`add_column :users, :x # note`}, []string{`add_column :users, :x       `}, [][]string{nil}},
	}
	for _, tc := range tests {
		lx := lexer{lang: tc.lang}
		for i, text := range tc.lines {
			l := scanLine(&lx, i+1, ' ', text)
			if l.code != tc.code[i] {
				t.Errorf("%s line %q: code = %q, want %q", tc.lang, text, l.code, tc.code[i])
			}
			var strs []string
			for _, s := range l.strs {
				strs = append(strs, text[s.start:s.end])
			}
			if !slices.Equal(strs, tc.strs[i]) {
				t.Errorf("%s line %q: strings = %q, want %q", tc.lang, text, strs, tc.strs[i])
			}
		}
	}
}

func TestMaskStrings(t *testing.T) {
	lx := lexer{lang: langSQL}
	l := scanLine(&lx, 1, '+', `INSERT INTO t VALUES ('create table x');`)
	if got := maskStrings(l); got != `INSERT INTO t VALUES ('              ');` {
		t.Errorf("masked = %q", got)
	}
}

func TestLongLineNotScanned(t *testing.T) {
	lx := lexer{lang: langJS, block: true}
	long := make([]byte, maxLineLen+1)
	for i := range long {
		long[i] = 'a'
	}
	if l := scanLine(&lx, 1, '+', string(long)); l.code != "" || lx.block {
		t.Errorf("long line scanned: code %d bytes, block %v", len(l.code), lx.block)
	}
}

func TestPaths(t *testing.T) {
	tests := []struct {
		path string
		lang string
		test bool
		db   dbKind
	}{
		{"server/main.go", langGo, false, dbNone},
		{"server/main_test.go", langGo, true, dbNone},
		{"web/src/__tests__/a.ts", langJS, true, dbNone},
		{"web/src/a.spec.tsx", langJS, true, dbNone},
		{"scripts/test_report.py", langPy, true, dbNone},
		{"server/migrations/20261010_x.sql", langSQL, false, dbMigration},
		{"db/migrations/0001_x.down.sql", langSQL, false, dbDown},
		{"queries/users.sql", langSQL, false, dbMigration},
		{"db/schema.sql", langSQL, false, dbSchema},
		{"prisma/schema.prisma", langPrisma, false, dbSchema},
		{"db/migrate/2024_x.rb", langRuby, false, dbMigration},
		{"alembic/versions/abc_x.py", langPy, false, dbMigration},
		{"api/versions/v1.py", langPy, false, dbNone},
		{"users/migrations/0002_x.py", langPy, false, dbMigration},
		{"users/migrations/__init__.py", langPy, false, dbNone},
		{"users/models.py", langPy, false, dbModels},
		{".env.example", langEnv, false, dbNone},
		{"README.md", "", false, dbNone},
	}
	for _, tc := range tests {
		if got := langOf(tc.path); got != tc.lang {
			t.Errorf("langOf(%q) = %q, want %q", tc.path, got, tc.lang)
		}
		if got := isTestPath(tc.path); got != tc.test {
			t.Errorf("isTestPath(%q) = %v, want %v", tc.path, got, tc.test)
		}
		if got := dbFileOf(tc.path).kind; got != tc.db {
			t.Errorf("dbFileOf(%q) = %v, want %v", tc.path, got, tc.db)
		}
	}
}
