package surface

import (
	"regexp"
	"strings"
)

// opCtx is a changed line of a code migration, with its doc.
type opCtx struct {
	v    *view
	s    side
	d    *doc
	i    int // index of the line in d
	l    line
	seen map[int]bool // operation starts already read on this side
}

func (c opCtx) at() at { return at{v: c.v, l: c.l, s: c.s} }

// fact returns a statement fact at the context's line.
func (c opCtx) fact(what, name, op, detail, fw string) dbFact {
	return dbFact{what: what, name: name, op: op, detail: detail, fw: fw, conf: confORM, a: c.at()}
}

// Bounds for multi-line migration operations.
const (
	opScanBack = 15 // lines searched upwards for an operation's start
	opMaxLines = 30 // lines one operation call may span
)

// opStart returns the index of the line where the operation call that
// covers line i starts (i itself or up to opScanBack lines above), or -1.
func opStart(d *doc, i int, re *regexp.Regexp) int {
	for k := i; k >= 0 && k >= i-opScanBack; k-- {
		m := re.FindStringIndex(d.lines[k].code)
		if m == nil || d.lines[k].inString(m[0]) {
			continue
		}
		if _, end := gatherCall(d, k, m[0]); end >= i {
			return k
		}
		return -1
	}
	return -1
}

// gatherCall joins the code of the call that starts at offset from on line
// k, up to its closing parenthesis (or opMaxLines lines); end is the index
// of its last line.
func gatherCall(d *doc, k, from int) (string, int) {
	var b strings.Builder
	depth, opened := 0, false
	for j := k; j < len(d.lines) && j < k+opMaxLines; j++ {
		l := d.lines[j]
		start := 0
		if j == k {
			start = from
		}
		for p := start; p < len(l.code); p++ {
			if l.inString(p) {
				continue
			}
			switch l.code[p] {
			case '(':
				depth, opened = depth+1, true
			case ')':
				depth--
			}
		}
		b.WriteString(l.code[min(start, len(l.code)):])
		b.WriteByte(' ')
		if opened && depth <= 0 {
			return b.String(), j
		}
	}
	return b.String(), min(len(d.lines)-1, k+opMaxLines-1)
}

// once gathers the operation covering the context's line, unless it was
// already read; "" when none covers it.
func (c opCtx) once(re *regexp.Regexp) string {
	k := opStart(c.d, c.i, re)
	if k < 0 || c.seen[k] {
		return ""
	}
	c.seen[k] = true
	m := re.FindStringIndex(c.d.lines[k].code)
	text, _ := gatherCall(c.d, k, m[0])
	return text
}

var (
	reDjangoOp  = regexp.MustCompile(`\bmigrations\.(AddField|RemoveField|AlterField|RenameField|CreateModel|DeleteModel|RenameModel|AddIndex|RemoveIndex)\(`)
	reKwStrings = map[string]*regexp.Regexp{}
)

func init() {
	for _, k := range []string{"model_name", "name", "old_name", "new_name", "new_column_name"} {
		reKwStrings[k] = regexp.MustCompile(`\b` + k + `\s*=\s*[rRuU]?["']([^"']+)["']`)
	}
}

// kwString returns the string value of keyword argument k in text.
func kwString(text, k string) string {
	if m := reKwStrings[k].FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

// djangoOps returns the fact of the Django migration operation covering
// the context's line.
func djangoOps(c opCtx) []dbFact {
	text := c.once(reDjangoOp)
	if text == "" {
		return nil
	}
	op := reDjangoOp.FindStringSubmatch(text)[1]
	mdl, name := kwString(text, "model_name"), kwString(text, "name")
	oldN, newN := kwString(text, "old_name"), kwString(text, "new_name")
	col := func(n string) string { return mdl + "." + n }
	var f dbFact
	switch op {
	case "AddField":
		f = c.fact(whatColumn, col(name), opCreate, "", fwDjango)
	case "RemoveField":
		f = c.fact(whatColumn, col(name), opDrop, detailOneWay, fwDjango)
	case "AlterField":
		f = c.fact(whatColumn, col(name), opAlter, "field altered", fwDjango)
	case "RenameField":
		f = c.fact(whatColumn, col(newN), opRename, joinDetail("was "+col(oldN), detailOneWay), fwDjango)
	case "CreateModel":
		f = c.fact(whatTable, "table "+name, opCreate, "", fwDjango)
	case "DeleteModel":
		f = c.fact(whatTable, "table "+name, opDrop, detailOneWay, fwDjango)
	case "RenameModel":
		f = c.fact(whatTable, "table "+newN, opRename, "was table "+oldN, fwDjango)
	case "AddIndex":
		f = c.fact(whatIndex, "index "+name, opCreate, "", fwDjango)
	case "RemoveIndex":
		f = c.fact(whatIndex, "index "+name, opDrop, "", fwDjango)
	}
	if !validFactName(f.name) {
		return nil
	}
	return []dbFact{f}
}

var (
	reAlembicOp   = regexp.MustCompile(`\bop\.(add_column|drop_column|create_table|drop_table|alter_column|create_index|drop_index|rename_table)\(`)
	reAlembicArgs = regexp.MustCompile(`\(\s*(?:op\.f\(\s*)?[rRuU]?["']([^"']+)["']\)?(?:\s*,\s*(?:(?:sa|sqlalchemy)\.Column\(\s*)?(?:op\.f\(\s*)?[rRuU]?["']([^"']+)["'])?`)
)

// alembicOps returns the fact of the Alembic operation covering the
// context's line.
func alembicOps(c opCtx) []dbFact {
	text := c.once(reAlembicOp)
	if text == "" {
		return nil
	}
	op := reAlembicOp.FindStringSubmatch(text)[1]
	args := reAlembicArgs.FindStringSubmatch(text[strings.Index(text, op)+len(op):])
	if args == nil {
		return nil
	}
	a1, a2 := args[1], args[2]
	var f dbFact
	switch op {
	case "add_column":
		f = c.fact(whatColumn, a1+"."+a2, opCreate, "", fwAlembic)
	case "drop_column":
		f = c.fact(whatColumn, a1+"."+a2, opDrop, detailOneWay, fwAlembic)
	case "create_table":
		f = c.fact(whatTable, "table "+a1, opCreate, "", fwAlembic)
	case "drop_table":
		f = c.fact(whatTable, "table "+a1, opDrop, detailOneWay, fwAlembic)
	case "alter_column":
		f = c.fact(whatColumn, a1+"."+a2, opAlter, "altered", fwAlembic)
		if n := kwString(text, "new_column_name"); n != "" {
			f = c.fact(whatColumn, a1+"."+n, opRename, joinDetail("was "+a1+"."+a2, detailOneWay), fwAlembic)
		}
	case "create_index":
		f = c.fact(whatIndex, "index "+a1, opCreate, "", fwAlembic)
	case "drop_index":
		f = c.fact(whatIndex, "index "+a1, opDrop, "", fwAlembic)
	case "rename_table":
		f = c.fact(whatTable, "table "+a2, opRename, joinDetail("was table "+a1, detailOneWay), fwAlembic)
	}
	if a1 == "" || f.what == whatColumn && a2 == "" || op == "rename_table" && a2 == "" {
		return nil
	}
	return []dbFact{f}
}

var (
	reRailsOp    = regexp.MustCompile(`^\s*(create_table|drop_table|add_column|remove_column|rename_column|change_column|change_column_null|change_column_default|add_index|remove_index|add_reference|remove_reference|add_belongs_to|remove_belongs_to|rename_table)\b\s*\(?\s*:?["']?(\w+)["']?(?:\s*,\s*:?["']?(\w+)["']?)?(?:\s*,\s*:?["']?(\w+)["']?)?(.*)`)
	reRailsName  = regexp.MustCompile(`\bname:\s*["'](\w+)["']`)
	reRailsTCol  = regexp.MustCompile(`^\s*t\.(\w+)\s+(?::(\w+)|["'](\w+)["']|\[\s*(?::(\w+)|["'](\w+)["']))(.*)`)
	reRailsBlock = regexp.MustCompile(`^\s*(create_table|change_table)\s*\(?\s*:?["']?(\w+)`)
	reRailsEnd   = regexp.MustCompile(`^\s{0,2}end\b`)
)

// railsOps returns the facts of a Rails migration line: a schema statement,
// or a t.<type> column inside an existing create_table/change_table block.
func railsOps(c opCtx) []dbFact {
	code := c.l.code
	if m := reRailsOp.FindStringSubmatch(code); m != nil {
		return railsStatement(c, m)
	}
	m := reRailsTCol.FindStringSubmatch(code)
	if m == nil {
		return nil
	}
	table, hdr := railsBlock(c.d, c.i)
	if table == "" || c.v.f.isChanged(c.s, hdr) {
		return nil
	}
	return railsColumn(c, table, m, false)
}

// railsStatement maps a Rails schema statement.
func railsStatement(c opCtx, m []string) []dbFact {
	op, t, a, b, rest := m[1], m[2], m[3], m[4], m[5]
	idx := func() string {
		if n := reRailsName.FindStringSubmatch(rest); n != nil {
			return "index " + n[1]
		}
		if a != "" {
			return "index on " + t + "(" + a + ")"
		}
		return "index on " + t
	}
	var f dbFact
	switch op {
	case "create_table":
		f = c.fact(whatTable, "table "+t, opCreate, "", fwRails)
	case "drop_table":
		f = c.fact(whatTable, "table "+t, opDrop, detailOneWay, fwRails)
	case "rename_table":
		f = c.fact(whatTable, "table "+a, opRename, joinDetail("was table "+t, detailOneWay), fwRails)
	case "add_column":
		f = c.fact(whatColumn, t+"."+a, opCreate, "", fwRails)
	case "remove_column":
		f = c.fact(whatColumn, t+"."+a, opDrop, detailOneWay, fwRails)
	case "rename_column":
		f = c.fact(whatColumn, t+"."+b, opRename, joinDetail("was "+t+"."+a, detailOneWay), fwRails)
	case "change_column", "change_column_null", "change_column_default":
		f = c.fact(whatColumn, t+"."+a, opAlter, strings.ReplaceAll(op, "_", " "), fwRails)
	case "add_reference", "add_belongs_to":
		f = c.fact(whatColumn, t+"."+a+"_id", opCreate, "", fwRails)
	case "remove_reference", "remove_belongs_to":
		f = c.fact(whatColumn, t+"."+a+"_id", opDrop, detailOneWay, fwRails)
	case "add_index":
		f = c.fact(whatIndex, idx(), opCreate, "", fwRails)
	case "remove_index":
		f = c.fact(whatIndex, idx(), opDrop, "", fwRails)
	}
	if t == "" || !validFactName(f.name) || strings.Contains(op, "reference") && a == "" || strings.Contains(op, "belongs") && a == "" {
		return nil
	}
	return []dbFact{f}
}

// validFactName reports whether a fact's name has all its parts (a missing
// table, column or index name leaves "users.", ".x" or "table ").
func validFactName(n string) bool {
	return n != "" && !strings.HasSuffix(n, ".") && !strings.HasSuffix(n, " ") && !strings.HasPrefix(n, ".")
}

// railsColumn maps a t.<type> line of table; decl marks schema.rb.
func railsColumn(c opCtx, table string, m []string, decl bool) []dbFact {
	method, name := m[1], m[2]+m[3]+m[4]+m[5]
	if name == "" {
		return nil
	}
	f := c.fact(whatColumn, table+"."+name, opCreate, "", fwRails)
	switch method {
	case "timestamps":
		return nil
	case "remove":
		f.op, f.detail = opDrop, detailOneWay
	case "rename":
		return nil
	case "index":
		f.what, f.name = whatIndex, "index on "+table+"("+name+")"
		if n := reRailsName.FindStringSubmatch(m[6]); n != nil {
			f.name = "index " + n[1]
		}
	case "references", "belongs_to":
		f.name += "_id"
	}
	if decl {
		f.decl, f.sig = true, method+" "+normSpace(m[6])
	}
	return []dbFact{f}
}

// railsBlock returns the table and header line number of the
// create_table/change_table block enclosing line i.
func railsBlock(d *doc, i int) (string, int) {
	for k := i - 1; k >= 0 && k >= i-shapeScan; k-- {
		c := d.lines[k].code
		if m := reRailsBlock.FindStringSubmatch(c); m != nil {
			return m[2], d.lines[k].no
		}
		if reRailsEnd.MatchString(c) || strings.Contains(c, "def ") {
			return "", 0
		}
	}
	return "", 0
}

// railsSchemaFacts returns the declarations of schema.rb lines.
func railsSchemaFacts(v *view, s side, ls []line, get func() *doc) []dbFact {
	var out []dbFact
	for _, l := range ls {
		d := get()
		c := opCtx{v: v, s: s, d: d, i: d.idx(l.no), l: l}
		if c.i < 0 {
			continue
		}
		if m := reRailsBlock.FindStringSubmatch(l.code); m != nil && m[1] == "create_table" {
			f := c.fact(whatTable, "table "+m[2], opCreate, "", fwRails)
			f.decl = true
			out = append(out, f)
			continue
		}
		m := reRailsTCol.FindStringSubmatch(l.code)
		if m == nil {
			continue
		}
		table, hdr := railsBlock(d, c.i)
		if table == "" || v.f.isChanged(s, hdr) {
			continue
		}
		out = append(out, railsColumn(c, table, m, true)...)
	}
	return out
}

var (
	reKnexTable = regexp.MustCompile(`\.(createTable|createTableIfNotExists|dropTable|dropTableIfExists|renameTable|alterTable|table)\(\s*["'](\w+)["'](?:\s*,\s*["'](\w+)["'])?`)
	reKnexCol   = regexp.MustCompile(`\b\w+\.(string|text|integer|bigInteger|bigint|boolean|timestamp|datetime|date|uuid|json|jsonb|decimal|float|double|enum|enu|increments|bigIncrements|specificType|binary|time|tinyint|smallint|mediumint)\(\s*["'](\w+)["']`)
	reKnexColOp = regexp.MustCompile(`\.(dropColumn|renameColumn|dropIndex|index|unique)\(\s*(?:\[\s*["'](\w+)["'][^\]]*\]|["'](\w+)["'])(?:\s*,\s*["'](\w+)["'])?`)
)

// knexOps returns the facts of a knex migration line.
func knexOps(c opCtx) []dbFact {
	code := c.l.code
	if m := reKnexTable.FindStringSubmatch(code); m != nil {
		switch m[1] {
		case "createTable", "createTableIfNotExists":
			return []dbFact{c.fact(whatTable, "table "+m[2], opCreate, "", fwKnex)}
		case "dropTable", "dropTableIfExists":
			return []dbFact{c.fact(whatTable, "table "+m[2], opDrop, detailOneWay, fwKnex)}
		case "renameTable":
			if m[3] != "" {
				return []dbFact{c.fact(whatTable, "table "+m[3], opRename, joinDetail("was table "+m[2], detailOneWay), fwKnex)}
			}
		}
		return nil
	}
	table, ok := knexTable(c)
	if !ok {
		return nil
	}
	if m := reKnexCol.FindStringSubmatch(code); m != nil {
		return []dbFact{c.fact(whatColumn, table+"."+m[2], opCreate, "", fwKnex)}
	}
	m := reKnexColOp.FindStringSubmatch(code)
	if m == nil {
		return nil
	}
	name := m[2] + m[3]
	switch m[1] {
	case "dropColumn":
		return []dbFact{c.fact(whatColumn, table+"."+name, opDrop, detailOneWay, fwKnex)}
	case "renameColumn":
		if m[4] != "" {
			return []dbFact{c.fact(whatColumn, table+"."+m[4], opRename, joinDetail("was "+table+"."+name, detailOneWay), fwKnex)}
		}
	case "index", "unique":
		idx := "index on " + table + "(" + name + ")"
		if m[4] != "" {
			idx = "index " + m[4]
		}
		return []dbFact{c.fact(whatIndex, idx, opCreate, "", fwKnex)}
	case "dropIndex":
		idx := "index on " + table + "(" + name + ")"
		if m[4] != "" {
			idx = "index " + m[4]
		}
		return []dbFact{c.fact(whatIndex, idx, opDrop, "", fwKnex)}
	}
	return nil
}

// knexTable returns the table of the createTable/alterTable/table callback
// enclosing the context's line; ok is false when there is none, or when it
// is a table created on this side (its columns come with it).
func knexTable(c opCtx) (string, bool) {
	for k := c.i - 1; k >= 0 && k >= c.i-shapeScan; k-- {
		m := reKnexTable.FindStringSubmatch(c.d.lines[k].code)
		if m == nil {
			continue
		}
		switch m[1] {
		case "createTable", "createTableIfNotExists":
			return m[2], !c.v.f.isChanged(c.s, c.d.lines[k].no)
		case "alterTable", "table":
			return m[2], true
		}
		return "", false
	}
	return "", false
}

var (
	reSeqOp   = regexp.MustCompile(`\.(addColumn|removeColumn|createTable|dropTable|renameColumn|changeColumn|addIndex|removeIndex|renameTable)\(`)
	reSeqArgs = regexp.MustCompile(`\(\s*["'](\w+)["'](?:\s*,\s*(?:\[\s*["']?(\w+)["']?[^\]]*\]|["'](\w+)["']))?(?:\s*,\s*["'](\w+)["'])?`)
	reSeqName = regexp.MustCompile(`\bname\s*:\s*["'](\w+)["']`)
)

// sequelizeOps returns the fact of the sequelize queryInterface operation
// covering the context's line.
func sequelizeOps(c opCtx) []dbFact {
	text := c.once(reSeqOp)
	if text == "" {
		return nil
	}
	op := reSeqOp.FindStringSubmatch(text)[1]
	args := reSeqArgs.FindStringSubmatch(text[strings.Index(text, op)+len(op):])
	if args == nil {
		return nil
	}
	t, a, b := args[1], args[2]+args[3], args[4]
	idx := "index on " + t
	if n := reSeqName.FindStringSubmatch(text); n != nil {
		idx = "index " + n[1]
	}
	var f dbFact
	switch op {
	case "addColumn":
		f = c.fact(whatColumn, t+"."+a, opCreate, "", fwSequelize)
	case "removeColumn":
		f = c.fact(whatColumn, t+"."+a, opDrop, detailOneWay, fwSequelize)
	case "renameColumn":
		f = c.fact(whatColumn, t+"."+b, opRename, joinDetail("was "+t+"."+a, detailOneWay), fwSequelize)
	case "changeColumn":
		f = c.fact(whatColumn, t+"."+a, opAlter, "altered", fwSequelize)
	case "createTable":
		f = c.fact(whatTable, "table "+t, opCreate, "", fwSequelize)
	case "dropTable":
		f = c.fact(whatTable, "table "+t, opDrop, detailOneWay, fwSequelize)
	case "renameTable":
		f = c.fact(whatTable, "table "+a, opRename, joinDetail("was table "+t, detailOneWay), fwSequelize)
	case "addIndex":
		f = c.fact(whatIndex, idx, opCreate, "", fwSequelize)
	case "removeIndex":
		if a != "" {
			idx = "index " + a
		}
		f = c.fact(whatIndex, idx, opDrop, "", fwSequelize)
	}
	if !validFactName(f.name) {
		return nil
	}
	return []dbFact{f}
}

var (
	rePrismaBlock = regexp.MustCompile(`^\s*(model|enum|type|view|generator|datasource)\s+(\w+)\s*\{`)
	rePrismaField = regexp.MustCompile(`^\s*(\w+)\s+([A-Za-z]\w*(?:\[\])?\??)(.*)$`)
	rePrismaIndex = regexp.MustCompile(`^\s*@@(index|unique)\(\s*(?:fields:\s*)?\[([^\]]*)\](.*)`)
	rePrismaMap   = regexp.MustCompile(`\b(?:name|map)\s*:\s*"(\w+)"`)
)

// prismaFacts returns the declarations of schema.prisma lines: models,
// their fields and indexes.
func prismaFacts(v *view, s side, ls []line, get func() *doc) []dbFact {
	var out []dbFact
	for _, l := range ls {
		if strings.TrimSpace(l.code) == "" {
			continue
		}
		d := get()
		c := opCtx{v: v, s: s, d: d, i: d.idx(l.no), l: l}
		if c.i < 0 {
			continue
		}
		f, ok := prismaLine(c)
		if ok {
			f.decl = true
			out = append(out, f)
		}
	}
	return out
}

// prismaLine maps one changed schema.prisma line.
func prismaLine(c opCtx) (dbFact, bool) {
	code := c.l.code
	if m := rePrismaBlock.FindStringSubmatch(code); m != nil {
		return c.fact(whatTable, "table "+m[2], opCreate, "", fwPrisma), m[1] == "model"
	}
	mdl, hdr := prismaModel(c.d, c.i)
	if mdl == "" || c.v.f.isChanged(c.s, hdr) {
		return dbFact{}, false
	}
	if m := rePrismaIndex.FindStringSubmatch(code); m != nil {
		name := "index on " + mdl + "(" + normSpace(m[2]) + ")"
		if n := rePrismaMap.FindStringSubmatch(m[3]); n != nil {
			name = "index " + n[1]
		}
		f := c.fact(whatIndex, name, opCreate, "", fwPrisma)
		f.sig = m[1] + normSpace(m[3])
		return f, true
	}
	m := rePrismaField.FindStringSubmatch(code)
	if m == nil || strings.HasPrefix(strings.TrimSpace(code), "@") {
		return dbFact{}, false
	}
	f := c.fact(whatColumn, mdl+"."+m[1], opCreate, "", fwPrisma)
	f.sig = m[2] + " " + normSpace(m[3])
	return f, true
}

// prismaModel returns the model enclosing line i and its header line
// number ("" inside other blocks or outside any).
func prismaModel(d *doc, i int) (string, int) {
	for k := i - 1; k >= 0 && k >= i-shapeScan; k-- {
		c := d.lines[k].code
		if m := rePrismaBlock.FindStringSubmatch(c); m != nil {
			if m[1] != "model" {
				return "", 0
			}
			return m[2], d.lines[k].no
		}
		if strings.HasPrefix(c, "}") {
			return "", 0
		}
	}
	return "", 0
}

var (
	rePyClass   = regexp.MustCompile(`^class\s+(\w+)\s*\(([^)]*)\)\s*:`)
	reDjField   = regexp.MustCompile(`^\s+(\w+)\s*=\s*models\.(\w+)\((.*)`)
	reSAField   = regexp.MustCompile(`^\s+(\w+)\s*(?::\s*Mapped\[[^\]]*\]\s*)?=\s*(?:sa\.|db\.|sqlalchemy\.)?(Column|mapped_column)\((.*)`)
	reModelBase = regexp.MustCompile(`Model\b|Base\b|DeclarativeBase\b`)
)

// modelFacts returns the declarations of ORM model lines: model classes
// (tables) and their fields (columns).
func modelFacts(v *view, s side, ls []line, get func() *doc) []dbFact {
	var out []dbFact
	for _, l := range ls {
		if l.code == "" {
			continue
		}
		d := get()
		c := opCtx{v: v, s: s, d: d, i: d.idx(l.no), l: l}
		if c.i < 0 {
			continue
		}
		if m := rePyClass.FindStringSubmatch(l.code); m != nil {
			if reModelBase.MatchString(m[2]) {
				f := c.fact(whatTable, "table "+m[1], opCreate, "", modelFw(m[2]))
				f.decl, f.conf = true, confModels
				out = append(out, f)
			}
			continue
		}
		m, fw := reDjField.FindStringSubmatch(l.code), fwDjango
		if m == nil {
			m, fw = reSAField.FindStringSubmatch(l.code), fwSQLAlchemy
		}
		if m == nil {
			continue
		}
		cls, hdr := pyModelClass(d, c.i)
		if cls == "" || v.f.isChanged(s, hdr) {
			continue
		}
		f := c.fact(whatColumn, cls+"."+m[1], opCreate, "", fw)
		f.decl, f.conf, f.sig = true, confModels, m[2]+"("+normSpace(m[3])
		out = append(out, f)
	}
	return out
}

func modelFw(bases string) string {
	if strings.Contains(bases, "models.Model") {
		return fwDjango
	}
	return fwSQLAlchemy
}

// pyModelClass returns the model class enclosing line i of d and its
// header line number.
func pyModelClass(d *doc, i int) (string, int) {
	for k := i - 1; k >= 0 && k >= i-shapeScan; k-- {
		c := d.lines[k].code
		if m := rePyClass.FindStringSubmatch(c); m != nil {
			if !reModelBase.MatchString(m[2]) {
				return "", 0
			}
			return m[1], d.lines[k].no
		}
		if c != "" && c[0] != ' ' && c[0] != '\t' && strings.TrimSpace(c) != "" {
			return "", 0
		}
	}
	return "", 0
}
