package surface

import (
	"path"
	"strings"
)

// Languages the detectors know (langOf).
const (
	langGo     = "go"
	langJS     = "js" // JavaScript and TypeScript, JSX and TSX included
	langPy     = "py"
	langSQL    = "sql"
	langRuby   = "rb"
	langPrisma = "prisma"
	langEnv    = "env" // .env files
)

// langOf returns the detector language of a path ("" when none applies).
func langOf(p string) string {
	base := strings.ToLower(path.Base(p))
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return langEnv
	}
	switch path.Ext(base) {
	case ".go":
		return langGo
	case ".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts":
		return langJS
	case ".py":
		return langPy
	case ".sql":
		return langSQL
	case ".rb":
		return langRuby
	case ".prisma":
		return langPrisma
	}
	return ""
}

// isJSX reports whether p is a JSX or TSX file (UI code: never a route
// module).
func isJSX(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".jsx", ".tsx":
		return true
	}
	return false
}

// testDirs are directory names whose files are tests, mocks or fixtures.
var testDirs = map[string]bool{
	"test": true, "tests": true, "__tests__": true, "__mocks__": true, "testdata": true,
	"fixtures": true, "__fixtures__": true, "spec": true, "e2e": true, "cypress": true,
	"testutil": true, "testutils": true, "mocks": true,
}

// isTestPath reports whether p is a test file, a mock or a test fixture.
// Their units are left out of every block (DECISIONS: test files).
func isTestPath(p string) bool {
	lp := strings.ToLower(p)
	base := path.Base(lp)
	switch {
	case strings.HasSuffix(base, "_test.go"),
		strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py"),
		strings.HasSuffix(base, "_test.py"), base == "conftest.py",
		strings.Contains(base, ".test."), strings.Contains(base, ".spec."),
		strings.HasSuffix(base, "_spec.rb"), strings.HasSuffix(base, "_test.rb"):
		return true
	}
	for seg := range strings.SplitSeq(path.Dir(lp), "/") {
		if testDirs[seg] {
			return true
		}
	}
	return false
}

// dbKind classifies a path for the Database block.
type dbKind int

const (
	dbNone      dbKind = iota
	dbMigration        // an imperative migration (or any .sql file)
	dbDown             // a down migration file: its DDL undoes the up file
	dbSchema           // a declarative schema (schema.sql, structure.sql, schema.rb, *.prisma)
	dbModels           // ORM model definitions (Django or SQLAlchemy models.py)
)

// dbFile is what the path of a file says about its database role.
type dbFile struct {
	kind dbKind
	// migrationDir is set for files under a migrations directory: such a
	// file is reported as a "migration" entry when nothing finer is found.
	migrationDir bool
}

// migrationDirs are directory names that hold migrations (alembic's
// "versions" only under "alembic").
var migrationDirs = map[string]bool{"migrations": true, "migration": true, "migrate": true}

// dbFileOf classifies p.
func dbFileOf(p string) dbFile {
	lp := strings.ToLower(p)
	base := path.Base(lp)
	if base == "__init__.py" {
		return dbFile{}
	}
	var f dbFile
	inModels, prev := false, ""
	for seg := range strings.SplitSeq(path.Dir(lp), "/") {
		if migrationDirs[seg] || seg == "versions" && prev == "alembic" {
			f.migrationDir = true
		}
		inModels = inModels || seg == "models"
		prev = seg
	}
	lang := langOf(lp)
	switch {
	case base == "schema.sql" || base == "structure.sql" || base == "schema.rb" || lang == langPrisma:
		f.kind = dbSchema
	case isDownFile(base):
		f.kind = dbDown
	case f.migrationDir && lang != "" && lang != langEnv, lang == langSQL:
		f.kind = dbMigration
	case lang == langPy && (base == "models.py" || inModels):
		f.kind = dbModels
	}
	return f
}

// isDownFile reports whether a migration file name is a down migration
// ("0001_x.down.sql", "down.sql", "20240101_x_down.sql").
func isDownFile(base string) bool {
	stem := strings.TrimSuffix(base, path.Ext(base))
	return strings.HasSuffix(stem, ".down") || stem == "down" || strings.HasSuffix(stem, "_down")
}

// stem returns p without its extension.
func stem(p string) string { return strings.TrimSuffix(p, path.Ext(p)) }

// dirBase returns the last directory element of p ("server/internal/users/x.go"
// → "users"; "" at the root).
func dirBase(p string) string {
	d := path.Dir(p)
	if d == "." || d == "/" {
		return ""
	}
	return path.Base(d)
}
