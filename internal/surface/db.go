package surface

import (
	"cmp"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/fredricw/prt/internal/model"
)

// Database confidences.
const (
	confDDL        = 0.97 // DDL in a .sql file
	confORM        = 0.95 // an ORM migration operation, a declarative schema
	confModels     = 0.8  // an ORM model field
	confDDLCode    = 0.75 // DDL in a string literal of code
	confDDLCodeMig = 0.9  // DDL in a string literal of a migration file
	confMigration  = 0.9  // a migration file with nothing finer found
)

// Operations of a database fact.
const (
	opCreate = "create"
	opDrop   = "drop"
	opRename = "rename"
	opAlter  = "alter"
)

// Frameworks of database entries.
const (
	fwSQL        = "sql"
	fwAlembic    = "alembic"
	fwRails      = "rails"
	fwKnex       = "knex"
	fwSequelize  = "sequelize"
	fwPrisma     = "prisma"
	fwSQLAlchemy = "sqlalchemy"
)

// dbFact is one schema fact found on one side of a unit.
type dbFact struct {
	what   string // whatTable, whatColumn, whatIndex, whatView
	name   string // display name ("table x", "users.c", "index i")
	op     string // opCreate, opDrop, opRename, opAlter (statements)
	detail string // "was users.a", "TYPE text"
	fw     string
	conf   float64
	// decl marks a declaration (a declarative schema, a model, a column of
	// an existing CREATE TABLE) rather than a statement: declarations pair
	// by name across sides, and sig tells whether one changed.
	decl bool
	sig  string
	a    at
}

// detectDB finds schema and migration changes.
func (b *builder) detectDB() []model.SurfaceEntry {
	var facts [2][]dbFact
	for _, v := range b.live() {
		for _, s := range sides {
			facts[s] = append(facts[s], b.unitDB(v, s)...)
		}
	}
	out := dbEntries(facts[sideOld], facts[sideNew])
	return append(out, b.migrationFiles(out)...)
}

// unitDB returns the schema facts on v's changed lines on side s.
func (b *builder) unitDB(v *view, s side) []dbFact {
	ls := v.changedLines(s)
	if len(ls) == 0 || v.f.db.kind == dbDown {
		return nil
	}
	kind, lang := v.f.db.kind, v.f.lang
	var d *doc
	get := func() *doc {
		if d == nil {
			d = b.sideDoc(v, s)
		}
		return d
	}
	switch {
	case lang == langSQL:
		return sqlFacts(v, s, ls, get, kind == dbSchema)
	case lang == langPrisma:
		return prismaFacts(v, s, ls, get)
	case kind == dbSchema && lang == langRuby:
		return railsSchemaFacts(v, s, ls, get)
	case kind == dbModels:
		return modelFacts(v, s, ls, get)
	case kind == dbMigration:
		return ormFacts(v, s, ls, get)
	case (lang == langGo || lang == langJS || lang == langPy || lang == langRuby) && !isJSX(v.f.path):
		return stringDDL(v, s, ls, confDDLCode, true)
	}
	return nil
}

// ormFacts returns the facts of a code migration: its framework's
// operations, and DDL in its string literals; nothing in a down section.
func ormFacts(v *view, s side, ls []line, get func() *doc) []dbFact {
	var out []dbFact
	seen := map[int]bool{}
	for _, l := range ls {
		if l.code == "" {
			continue
		}
		d := get()
		i := d.idx(l.no)
		if i < 0 || inDownSection(d, i) {
			continue
		}
		c := opCtx{v: v, s: s, d: d, i: i, l: l, seen: seen}
		switch v.f.lang {
		case langPy:
			out = append(out, djangoOps(c)...)
			out = append(out, alembicOps(c)...)
		case langRuby:
			out = append(out, railsOps(c)...)
		case langJS:
			if usesSequelize(d) {
				out = append(out, sequelizeOps(c)...)
			} else {
				out = append(out, knexOps(c)...)
			}
		}
		out = append(out, stringDDL(v, s, []line{l}, confDDLCodeMig, false)...)
	}
	return out
}

// usesSequelize reports whether a JS migration is a sequelize one (else
// knex).
func usesSequelize(d *doc) bool {
	if d.seq == 0 {
		d.seq = 1
		for _, l := range d.lines {
			if strings.Contains(l.code, "queryInterface") || strings.Contains(l.code, "Sequelize") {
				d.seq = 2
				break
			}
		}
	}
	return d.seq == 2
}

// SQL statement patterns (case-insensitive). A name is a plain, quoted,
// backticked or bracketed identifier, optionally schema-qualified.
const sqlName = "((?:[\\w$]+|\"[^\"]+\"|`[^`]+`|\\[[^\\]]+\\])(?:\\.(?:[\\w$]+|\"[^\"]+\"|`[^`]+`|\\[[^\\]]+\\]))?)"

var (
	reCreateTable = regexp.MustCompile(`(?i)\bCREATE\s+(?:OR\s+REPLACE\s+)?(?:(?:GLOBAL|LOCAL)\s+)?(?:TEMP(?:ORARY)?\s+|UNLOGGED\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?` + sqlName)
	reDropTable   = regexp.MustCompile(`(?i)\bDROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?` + sqlName + `((?:\s*,\s*` + sqlName + `)*)`)
	reAlterTable  = regexp.MustCompile(`(?i)\bALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?` + sqlName + `(.*)`)
	reCreateIndex = regexp.MustCompile(`(?i)\bCREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+NOT\s+EXISTS\s+)?(?:` + sqlName + `\s+)?ON\s+(?:ONLY\s+)?` + sqlName)
	reDropIndex   = regexp.MustCompile(`(?i)\bDROP\s+INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+EXISTS\s+)?` + sqlName)
	reCreateView  = regexp.MustCompile(`(?i)\bCREATE\s+(?:OR\s+REPLACE\s+)?(?:MATERIALIZED\s+)?VIEW\s+(?:IF\s+NOT\s+EXISTS\s+)?` + sqlName)
	reDropView    = regexp.MustCompile(`(?i)\bDROP\s+(?:MATERIALIZED\s+)?VIEW\s+(?:IF\s+EXISTS\s+)?` + sqlName)

	reActAdd       = regexp.MustCompile(`(?i)^ADD\s+(?:COLUMN\s+)?(?:IF\s+NOT\s+EXISTS\s+)?` + sqlName + `(.*)`)
	reActDrop      = regexp.MustCompile(`(?i)^DROP\s+(?:COLUMN\s+)?(?:IF\s+EXISTS\s+)?` + sqlName)
	reActRenameCol = regexp.MustCompile(`(?i)^RENAME\s+(?:COLUMN\s+)?` + sqlName + `\s+TO\s+` + sqlName)
	reActRenameTo  = regexp.MustCompile(`(?i)^RENAME\s+TO\s+` + sqlName)
	reActAlter     = regexp.MustCompile(`(?i)^(?:ALTER|MODIFY)\s+(?:COLUMN\s+)?` + sqlName + `\s*(.*)`)
	reActChange    = regexp.MustCompile(`(?i)^CHANGE\s+(?:COLUMN\s+)?` + sqlName + `\s+` + sqlName + `(.*)`)
	reColDef       = regexp.MustCompile(`^\s*` + sqlName + `\s+([A-Za-z]\w*(?:\s*\([^)]*\))?(?:\[\])?)(.*)`)

	// reUpperDDL is the bar for DDL in string literals outside migrations:
	// upper-case keywords, so UI text ("Drop table rows") does not match.
	reUpperDDL = regexp.MustCompile(`\b(?:CREATE|DROP|ALTER)\s+(?:(?:OR\s+REPLACE|UNIQUE|MATERIALIZED|TEMP|TEMPORARY|UNLOGGED|GLOBAL|LOCAL)\s+)*(?:TABLE|INDEX|VIEW)\b`)
)

// downMarkers find the start of a migration's up or down part, per
// language (the first non-empty group is "up"/"down"/...).
var downMarkers = map[string]*regexp.Regexp{
	langSQL:  regexp.MustCompile(`(?i)--\s*(?:\+goose|\+migrate|migrate:)\s*(up|down)\b`),
	langPy:   regexp.MustCompile(`^def\s+(upgrade|downgrade)\b`),
	langRuby: regexp.MustCompile(`^\s*def\s+(?:self\.)?(up|down|change)\b`),
	langJS:   regexp.MustCompile(`\bexports\.(up|down)\b|\bfunction\s+(up|down)\b|^\s*(?:async\s+)?(up|down)\s*(?::|\()|^\s*(?:export\s+)?(?:const|let)\s+(up|down)\s*=`),
	langGo:   regexp.MustCompile(`^func\s+(up|down|Up|Down)`),
}

// sqlKeywords are words that start a table element or action other than a
// column.
var sqlKeywords = map[string]bool{
	"CONSTRAINT": true, "PRIMARY": true, "FOREIGN": true, "UNIQUE": true, "CHECK": true,
	"INDEX": true, "KEY": true, "EXCLUDE": true, "LIKE": true, "PERIOD": true, "FULLTEXT": true,
	"SPATIAL": true, "DEFAULT": true, "NOT": true, "NULL": true, "COLUMN": true, "TABLE": true,
	"IF": true, "ON": true, "REFERENCES": true, "IDENTITY": true, "EXPRESSION": true,
}

// sqlIdent strips identifier quoting ("public"."users" → public.users).
func sqlIdent(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '"', '`', '[', ']':
			return -1
		}
		return r
	}, s)
}

// sqlFacts returns the facts of SQL lines: statements, actions of an
// enclosing ALTER TABLE and columns of an enclosing CREATE TABLE.
func sqlFacts(v *view, s side, ls []line, get func() *doc, schema bool) []dbFact {
	var out []dbFact
	for _, l := range ls {
		if strings.TrimSpace(l.code) == "" {
			continue
		}
		d := get()
		i := d.idx(l.no)
		if i < 0 || inDownSection(d, i) {
			continue
		}
		a := at{v: v, l: l, s: s}
		fs := sqlStatement(maskStrings(l), a, confDDL)
		if len(fs) == 0 {
			fs = sqlInStatement(v, s, d, i, a)
		}
		for _, f := range fs {
			if schema && f.op == opCreate {
				// A declarative schema states what exists: its CREATE
				// statements pair by name across the two sides.
				f.decl = true
			}
			out = append(out, f)
		}
	}
	return out
}

// sqlStatement returns the facts of statements that start on text.
func sqlStatement(text string, a at, conf float64) []dbFact {
	if lt := strings.ToLower(text); !strings.Contains(lt, "create") && !strings.Contains(lt, "drop") && !strings.Contains(lt, "alter") {
		return nil
	}
	var out []dbFact
	add := func(what, name, op, detail string) {
		out = append(out, dbFact{what: what, name: name, op: op, detail: detail, fw: fwSQL, conf: conf, a: a})
	}
	if m := reCreateTable.FindStringSubmatch(text); m != nil {
		add(whatTable, "table "+sqlIdent(m[1]), opCreate, "")
	}
	if m := reDropTable.FindStringSubmatch(text); m != nil {
		add(whatTable, "table "+sqlIdent(m[1]), opDrop, detailOneWay)
		for t := range strings.SplitSeq(m[2], ",") {
			if t = strings.TrimSpace(t); t != "" {
				add(whatTable, "table "+sqlIdent(t), opDrop, detailOneWay)
			}
		}
	}
	if m := reAlterTable.FindStringSubmatch(text); m != nil {
		table := sqlIdent(m[1])
		for _, act := range splitActions(m[2]) {
			out = append(out, sqlAction(table, act, a, conf)...)
		}
	}
	if m := reCreateIndex.FindStringSubmatch(text); m != nil {
		name := "index " + sqlIdent(m[1])
		if m[1] == "" {
			name = "index on " + sqlIdent(m[2])
		}
		add(whatIndex, name, opCreate, "")
	}
	if m := reDropIndex.FindStringSubmatch(text); m != nil {
		add(whatIndex, "index "+sqlIdent(m[1]), opDrop, "")
	}
	if m := reCreateView.FindStringSubmatch(text); m != nil {
		add(whatView, "view "+sqlIdent(m[1]), opCreate, "")
	}
	if m := reDropView.FindStringSubmatch(text); m != nil {
		add(whatView, "view "+sqlIdent(m[1]), opDrop, "")
	}
	return out
}

// splitActions splits the actions of an ALTER TABLE at top-level commas.
func splitActions(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',', ';':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, s[start:])
	for i := range out {
		out[i] = strings.TrimSpace(out[i])
	}
	return out
}

// sqlAction returns the fact of one ALTER TABLE action on table.
func sqlAction(table, act string, a at, conf float64) []dbFact {
	f := dbFact{what: whatColumn, fw: fwSQL, conf: conf, a: a}
	col := func(n string) string { return table + "." + sqlIdent(n) }
	m := matchAction(act)
	switch m.re {
	case reActRenameTo:
		f.what, f.name, f.op, f.detail = whatTable, "table "+sqlIdent(m.g[1]), opRename, joinDetail("was table "+table, detailOneWay)
	case reActRenameCol:
		f.name, f.op, f.detail = col(m.g[2]), opRename, joinDetail("was "+col(m.g[1]), detailOneWay)
	case reActChange:
		f.name, f.op = col(m.g[2]), opAlter
		if !strings.EqualFold(m.g[1], m.g[2]) {
			f.op, f.detail = opRename, joinDetail("was "+col(m.g[1]), detailOneWay)
		}
	case reActAdd:
		f.name, f.op, f.sig = col(m.g[1]), opCreate, normSpace(m.g[2])
	case reActDrop:
		f.name, f.op, f.detail = col(m.g[1]), opDrop, detailOneWay
	case reActAlter:
		f.name, f.op, f.detail = col(m.g[1]), opAlter, clip(normSpace(strings.TrimRight(m.g[2], ",;")), 60)
	default:
		return nil
	}
	return []dbFact{f}
}

// actionMatch is the pattern an ALTER TABLE action matched and its groups.
type actionMatch struct {
	re *regexp.Regexp
	g  []string
}

// matchAction matches an action against the action patterns, skipping
// constraint and index actions.
func matchAction(act string) actionMatch {
	for _, re := range []*regexp.Regexp{reActRenameTo, reActRenameCol, reActChange, reActAdd, reActDrop, reActAlter} {
		g := re.FindStringSubmatch(act)
		if g == nil || sqlKeywords[strings.ToUpper(sqlIdent(g[1]))] {
			continue
		}
		return actionMatch{re: re, g: g}
	}
	return actionMatch{}
}

// sqlStmtScan is how far back a statement's start is searched.
const sqlStmtScan = 200

// sqlInStatement returns the facts of a line inside a statement that starts
// earlier: an action of an ALTER TABLE, a column of a CREATE TABLE whose
// header is not itself changed (a new table's columns come with it).
func sqlInStatement(v *view, s side, d *doc, i int, a at) []dbFact {
	for k := i - 1; k >= 0 && k >= i-sqlStmtScan; k-- {
		c := maskStrings(d.lines[k])
		if strings.Contains(c, ";") {
			return nil
		}
		if m := reAlterTable.FindStringSubmatch(c); m != nil {
			var out []dbFact
			for _, act := range splitActions(maskStrings(a.l)) {
				out = append(out, sqlAction(sqlIdent(m[1]), act, a, confDDL)...)
			}
			return out
		}
		if m := reCreateTable.FindStringSubmatch(c); m != nil {
			if v.f.isChanged(s, d.lines[k].no) {
				return nil
			}
			return sqlColumn(sqlIdent(m[1]), a)
		}
	}
	return nil
}

// sqlColumn returns the column a CREATE TABLE body line defines.
func sqlColumn(table string, a at) []dbFact {
	m := reColDef.FindStringSubmatch(a.l.code)
	if m == nil || sqlKeywords[strings.ToUpper(sqlIdent(m[1]))] {
		return nil
	}
	return []dbFact{{what: whatColumn, name: table + "." + sqlIdent(m[1]), op: opCreate, fw: fwSQL, conf: confDDL,
		decl: true, sig: normSpace(strings.TrimRight(m[2]+m[3], ", ")), a: a}}
}

// inDownSection reports whether line i of a migration doc is in its down
// part (goose/sql-migrate/dbmate markers, downgrade(), def down, down:).
// Without a marker in reach, a unit's symbol decides.
func inDownSection(d *doc, i int) bool {
	if d.downs == nil {
		d.downs = make([]int8, len(d.lines))
		re, state := downMarkers[d.lang], int8(0)
		for k, l := range d.lines {
			if m := matchGroup(re, l.text); m != "" {
				state = 1
				if m == "down" || m == "downgrade" {
					state = 2
				}
			}
			d.downs[k] = state
		}
	}
	if i >= 0 && i < len(d.downs) && d.downs[i] != 0 {
		return d.downs[i] == 2
	}
	switch strings.ToLower(symbolName(d.sym)) {
	case "down", "downgrade":
		return true
	}
	return false
}

// matchGroup returns the first non-empty group of re's match in s,
// lower-cased; "" when re is nil or does not match.
func matchGroup(re *regexp.Regexp, s string) string {
	if re == nil {
		return ""
	}
	m := re.FindStringSubmatch(s)
	for _, g := range m[min(1, len(m)):] {
		if g != "" {
			return strings.ToLower(g)
		}
	}
	return ""
}

// stringDDL returns the DDL statements in the string literals of code
// lines; strict asks for upper-case keywords.
func stringDDL(v *view, s side, ls []line, conf float64, strict bool) []dbFact {
	var out []dbFact
	for _, l := range ls {
		for _, str := range l.strContents() {
			if strict && !reUpperDDL.MatchString(str) {
				continue
			}
			out = append(out, sqlStatement(str, at{v: v, l: l, s: s}, conf)...)
		}
	}
	return out
}

// dbEntries turns both sides' facts into entries.
func dbEntries(before, after []dbFact) []model.SurfaceEntry {
	var out []model.SurfaceEntry
	declOld, stmtOld := splitDecl(before)
	declNew, stmtNew := splitDecl(after)
	out = append(out, pairDecls(declOld, declNew)...)
	type key struct{ what, name, op, path string }
	keyOf := func(f dbFact) key { return key{f.what, f.name, f.op, f.a.v.f.path} }
	newKeys, oldKeys := map[key]bool{}, map[key]bool{}
	for _, f := range stmtOld {
		oldKeys[keyOf(f)] = true
	}
	for _, f := range stmtNew {
		newKeys[keyOf(f)] = true
		e := stmtEntry(f)
		if oldKeys[keyOf(f)] {
			e.Detail = joinDetail(e.Detail, "statement edited")
		}
		out = append(out, e)
	}
	for _, f := range stmtOld {
		if !newKeys[keyOf(f)] {
			out = append(out, removedStmtEntry(f))
		}
	}
	return out
}

func splitDecl(fs []dbFact) (decl, stmt []dbFact) {
	for _, f := range fs {
		if f.decl {
			decl = append(decl, f)
		} else {
			stmt = append(stmt, f)
		}
	}
	return decl, stmt
}

// stmtEntry is the entry of a statement on the new side.
func stmtEntry(f dbFact) model.SurfaceEntry {
	change := model.SurfaceAdded
	switch f.op {
	case opDrop:
		change = model.SurfaceRemoved
	case opRename, opAlter:
		change = model.SurfaceChanged
	}
	e := newEntry(model.SurfaceDB, change, f.what, f.name, f.a, f.conf, f.fw)
	e.Detail = f.detail
	return e
}

// removedStmtEntry is the entry of a statement taken out of a migration.
func removedStmtEntry(f dbFact) model.SurfaceEntry {
	change, detail := model.SurfaceChanged, "no longer in migration"
	switch f.op {
	case opCreate:
		change = model.SurfaceRemoved
	case opDrop:
		detail = "drop no longer in migration"
	}
	e := newEntry(model.SurfaceDB, change, f.what, f.name, f.a, f.conf, f.fw)
	e.Detail = detail
	return e
}

// pairDecls pairs declarations by file and name: the same signature on both
// sides is no change, another is a change; the rest are added or removed
// (a removed table or column is one-way).
func pairDecls(before, after []dbFact) []model.SurfaceEntry {
	key := func(f dbFact) string { return f.a.v.f.path + "\x00" + f.what + "\x00" + f.name }
	order := func(a, b dbFact) int { return cmp.Or(cmp.Compare(key(a), key(b)), cmp.Compare(a.a.l.no, b.a.l.no)) }
	slices.SortStableFunc(before, order)
	slices.SortStableFunc(after, order)
	byKey := map[string][]int{}
	for i, o := range before {
		byKey[key(o)] = append(byKey[key(o)], i)
	}
	used := make([]bool, len(before))
	var out []model.SurfaceEntry
	for _, n := range after {
		idx := byKey[key(n)]
		if len(idx) == 0 {
			e := newEntry(model.SurfaceDB, model.SurfaceAdded, n.what, n.name, n.a, n.conf, n.fw)
			e.Detail = n.detail
			out = append(out, e)
			continue
		}
		byKey[key(n)], used[idx[0]] = idx[1:], true
		if o := before[idx[0]]; normSpace(o.sig) != normSpace(n.sig) {
			e := newEntry(model.SurfaceDB, model.SurfaceChanged, n.what, n.name, n.a, n.conf, n.fw)
			e.Detail = joinDetail("was "+clip(normSpace(o.sig), 60), n.detail)
			out = append(out, e)
		}
	}
	for i, o := range before {
		if used[i] {
			continue
		}
		e := newEntry(model.SurfaceDB, model.SurfaceRemoved, o.what, o.name, o.a, o.conf, o.fw)
		if o.what == whatTable || o.what == whatColumn {
			e.Detail = detailOneWay
		}
		out = append(out, e)
	}
	return out
}

// migrationFiles reports changed migration and schema files in which no
// finer entry was found (one entry per file).
func (b *builder) migrationFiles(found []model.SurfaceEntry) []model.SurfaceEntry {
	have := map[string]bool{}
	for _, e := range found {
		have[e.Path] = true
	}
	var out []model.SurfaceEntry
	for _, f := range b.files {
		mig := f.db.migrationDir && f.db.kind == dbMigration
		if f.test || have[f.path] || !mig && f.db.kind != dbSchema || len(f.views) == 0 {
			continue
		}
		v := f.views[0]
		a := firstChange(v)
		change := model.SurfaceChanged
		switch {
		case f.file.Added || missingOn(f, sideOld):
			change = model.SurfaceAdded
		case f.file.Deleted || missingOn(f, sideNew):
			change = model.SurfaceRemoved
		}
		what, name := whatMigration, "migration "+path.Base(f.path)
		if !mig {
			what, name = "schema", "schema "+path.Base(f.path)
		}
		out = append(out, newEntry(model.SurfaceDB, change, what, name, a, confMigration, ""))
	}
	return out
}

// missingOn reports whether f does not exist on side s: every unit has an
// empty span at line 0 there (a new file's units start at old line 0).
func missingOn(f *fileInfo, s side) bool {
	for _, v := range f.views {
		sp := v.u.Range.Old
		if s == sideNew {
			sp = v.u.Range.New
		}
		if sp.Count != 0 || sp.Start != 0 {
			return false
		}
	}
	return len(f.views) > 0
}

// firstChange is a unit's first changed line: the first added line, else
// the first removed one (old side), else its new start.
func firstChange(v *view) at {
	if ls := v.changedLines(sideNew); len(ls) > 0 {
		return at{v: v, l: ls[0], s: sideNew}
	}
	if ls := v.changedLines(sideOld); len(ls) > 0 {
		return at{v: v, l: ls[0], s: sideOld}
	}
	return at{v: v, l: line{no: v.u.Range.New.Start}, s: sideNew}
}
