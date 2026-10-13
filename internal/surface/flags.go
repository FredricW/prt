package surface

import (
	"cmp"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/fredricw/prt/internal/model"
)

// Frameworks of flag, env and config entries.
const (
	fwLaunchDarkly = "launchdarkly"
	fwUnleash      = "unleash"
	fwGrowthBook   = "growthbook"
	fwOpenFeature  = "openfeature"
	fwCustom       = "custom"
	fwEnv          = "env"
	fwDotenv       = "dotenv"
	fwViper        = "viper"
	fwConfig       = "config"
	fwGoFlag       = "flag"
	fwCobra        = "cobra"
	fwArgparse     = "argparse"
)

// Flag confidences.
const (
	confLookup    = 0.95 // a flag SDK lookup with a literal key
	confEnvExact  = 0.97 // os.Getenv("X") and the like
	confEnvLoose  = 0.85 // struct tags, django-environ
	confFlagProp  = 0.8  // flags.someFlag (property access)
	confConfigKey = 0.9
	confDotenv    = 0.8
)

// flagPattern is one lookup pattern; key is the submatch holding the key.
type flagPattern struct {
	re    *regexp.Regexp
	what  string
	fw    string
	conf  float64
	langs string // languages it applies to (each a langOf value), "" any code language
	key   int
	name  func(key string) string // display name ("-addr" for a Go flag); nil = key
	// inStr allows matches that start inside a string literal (struct
	// tags); check vets a match (receiver, what follows it).
	inStr bool
	check func(line string, m []int) (fw string, ok bool)
	// hints are literals one of which a line must contain for re to be
	// tried (a cheap pre-filter; nil = always try).
	hints []string
}

const flagQ = "[\"'`]"

// hints of the custom flag-object patterns.
var flagObjHints = []string{"lags", "LAGS", "eatures", "EATURES", "oggles"}

var flagPatterns = []flagPattern{
	// Feature flag SDKs.
	{re: regexp.MustCompile(`[\w)\]]\.(?:bool|string|number|int|float64|json|Bool|String|Number|Int|Float64|JSON|Json)Variation(?:Detail)?(?:Ctx)?\(\s*` + flagQ + "([^\"'`]+)" + flagQ), what: whatFlag, fw: fwLaunchDarkly, conf: confLookup, key: 1, hints: []string{"Variation"}},
	{re: regexp.MustCompile(`(\w*\)?)\.(?:v|V)ariation(?:Detail)?(?:Ctx)?\(\s*` + flagQ + "([^\"'`]+)" + flagQ), what: whatFlag, fw: fwLaunchDarkly, conf: confLookup, key: 2, check: ldReceiver, hints: []string{"ariation"}},
	{re: regexp.MustCompile(`\b\w*[uU]nleash\w*\.(?:isEnabled|IsEnabled|getVariant|GetVariant)\(\s*` + flagQ + "([^\"'`]+)"), what: whatFlag, fw: fwUnleash, conf: confLookup, key: 1, hints: []string{"nleash"}},
	{re: regexp.MustCompile(`\buse(?:Flag|Variant)\(\s*` + flagQ + "([^\"'`]+)"), what: whatFlag, fw: fwUnleash, conf: confLookup, langs: langJS, key: 1, hints: []string{"useFlag(", "useVariant("}},
	{re: regexp.MustCompile(`\.(?:isOn|isOff|getFeatureValue|evalFeature)\(\s*` + flagQ + "([^\"'`]+)"), what: whatFlag, fw: fwGrowthBook, conf: confLookup, key: 1, hints: []string{".isO", "FeatureValue(", "evalFeature("}},
	{re: regexp.MustCompile(`\buseFeature(?:IsOn|Value)?\(\s*` + flagQ + "([^\"'`]+)"), what: whatFlag, fw: fwGrowthBook, conf: confLookup, langs: langJS, key: 1, hints: []string{"useFeature"}},
	{re: regexp.MustCompile(`\b(?:gb|growthbook|growthBook|GrowthBook)\.(?:feature|Feature|EvalFeature)\(\s*` + flagQ + "([^\"'`]+)"), what: whatFlag, fw: fwGrowthBook, conf: confLookup, key: 1, hints: []string{"gb.", "rowth"}},
	{re: regexp.MustCompile(`\.get(?:Boolean|String|Number|Object)(?:Value|Details)\(\s*` + flagQ + "([^\"'`]+)"), what: whatFlag, fw: fwOpenFeature, conf: confLookup, key: 1, hints: []string{"Value(", "Details("}},
	{re: regexp.MustCompile(`\.(?:Boolean|String|Int|Float|Object)Value(?:Details)?\(\s*\w+\s*,\s*"([^"]+)"`), what: whatFlag, fw: fwOpenFeature, conf: confLookup, langs: langGo, key: 1, hints: []string{"Value(", "Details("}},
	{re: regexp.MustCompile(`\.get_(?:boolean|string|integer|float|object)_(?:value|details)\(\s*["']([^"']+)`), what: whatFlag, fw: fwOpenFeature, conf: confLookup, langs: langPy, key: 1, hints: []string{"_value(", "_details("}},
	{re: regexp.MustCompile(`\buse(?:Boolean|String|Number|Object)Flag(?:Value|Details)\(\s*` + flagQ + "([^\"'`]+)"), what: whatFlag, fw: fwOpenFeature, conf: confLookup, langs: langJS, key: 1, hints: []string{"Flag"}},
	// Custom helpers: a bare call, or a method on a flag-ish receiver.
	{re: regexp.MustCompile(`(?:^|[^\w.$])(?:isEnabled|isFeatureEnabled|featureEnabled|isFeatureOn|featureFlag|flagEnabled|isFlagEnabled|is_enabled|is_feature_enabled|feature_enabled|flag_enabled|flag)\(\s*` + flagQ + "([^\"'`]+)" + flagQ), what: whatFlag, fw: fwCustom, conf: confLookup, key: 1, hints: []string{"nabled(", "lag(", "eatureOn("}},
	{re: regexp.MustCompile(`\b(\w+)\.(?:isEnabled|isFeatureEnabled|is_enabled|IsEnabled|Enabled|enabled)\(\s*` + flagQ + "([^\"'`]+)" + flagQ), what: whatFlag, fw: fwCustom, conf: confLookup, key: 2, check: flagReceiver, hints: []string{"nabled("}},
	{re: regexp.MustCompile(`\b(?:flags|featureFlags|features|FLAGS|FEATURES|feature_flags|featureToggles)\[\s*` + flagQ + "([^\"'`]+)" + flagQ + `\s*\]`), what: whatFlag, fw: fwCustom, conf: confConfigKey, langs: langJS + langPy, key: 1, hints: flagObjHints},
	{re: regexp.MustCompile(`\b(?:flags|featureFlags|features|FLAGS|FEATURES|feature_flags|featureToggles)\.([A-Za-z_$][\w$]*)`), what: whatFlag, fw: fwCustom, conf: confFlagProp, langs: langJS + langPy, key: 1, check: flagProperty, hints: flagObjHints},
	// Environment variables.
	{re: regexp.MustCompile(`\b(?:os|syscall)\.(?:Getenv|LookupEnv)\(\s*"([^"]+)"`), what: whatEnv, fw: fwEnv, conf: confEnvExact, langs: langGo, key: 1, hints: []string{"etenv(", "ookupEnv("}},
	{re: regexp.MustCompile(`\b(?:env|envconfig)\s*:\s*"([A-Za-z_][A-Za-z0-9_]*)`), what: whatEnv, fw: fwEnv, conf: confEnvLoose, langs: langGo, key: 1, inStr: true, check: inStructTag, hints: []string{"env"}},
	{re: regexp.MustCompile(`\bprocess\.env\.([A-Za-z_$][\w$]*)`), what: whatEnv, fw: fwEnv, conf: confEnvExact, langs: langJS, key: 1, hints: []string{"process.env"}},
	{re: regexp.MustCompile(`\bprocess\.env\[\s*` + flagQ + "([^\"'`]+)" + flagQ + `\s*\]`), what: whatEnv, fw: fwEnv, conf: confEnvExact, langs: langJS, key: 1, hints: []string{"process.env"}},
	{re: regexp.MustCompile(`\bimport\.meta\.env\.([A-Za-z_$][\w$]*)`), what: whatEnv, fw: fwEnv, conf: confLookup, langs: langJS, key: 1, hints: []string{"import.meta"}},
	{re: regexp.MustCompile(`\b(?:Deno|Bun)\.env\.get\(\s*["']([^"']+)`), what: whatEnv, fw: fwEnv, conf: confLookup, langs: langJS, key: 1, hints: []string{".env.get("}},
	{re: regexp.MustCompile(`\bos\.environ\[\s*["']([^"']+)["']\s*\]`), what: whatEnv, fw: fwEnv, conf: confEnvExact, langs: langPy, key: 1, hints: []string{"environ"}},
	{re: regexp.MustCompile(`\bos\.environ\.(?:get|setdefault|pop)\(\s*["']([^"']+)`), what: whatEnv, fw: fwEnv, conf: confEnvExact, langs: langPy, key: 1, hints: []string{"environ"}},
	{re: regexp.MustCompile(`\bos\.getenv\(\s*["']([^"']+)`), what: whatEnv, fw: fwEnv, conf: confEnvExact, langs: langPy, key: 1, hints: []string{"getenv("}},
	{re: regexp.MustCompile(`(?:^|[^\w.])env(?:\.(?:str|bool|int|float|list|json|url|db|cache))?\(\s*["']([A-Z][A-Z0-9_]*)["']`), what: whatEnv, fw: fwEnv, conf: confEnvLoose, langs: langPy, key: 1, hints: []string{"env"}},
	{re: regexp.MustCompile(`\bENV\[\s*["']([^"']+)["']\s*\]`), what: whatEnv, fw: fwEnv, conf: confEnvExact, langs: langRuby, key: 1, hints: []string{"ENV"}},
	{re: regexp.MustCompile(`\bENV\.fetch\(\s*["']([^"']+)`), what: whatEnv, fw: fwEnv, conf: confEnvExact, langs: langRuby, key: 1, hints: []string{"ENV"}},
	{re: regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=`), what: whatEnv, fw: fwDotenv, conf: confDotenv, langs: langEnv, key: 1, hints: []string{"="}},
	// Config keys and CLI flags.
	{re: regexp.MustCompile(`\bviper\.(?:Get\w*|IsSet|SetDefault|BindEnv|BindPFlag|Sub)\(\s*"([^"]+)"`), what: whatConfig, fw: fwViper, conf: confLookup, langs: langGo, key: 1, hints: []string{"viper."}},
	{re: regexp.MustCompile(`\b(?:config|conf|cfg|nconf|convict)\.(?:get|has)\(\s*` + flagQ + "([^\"'`]+)" + flagQ), what: whatConfig, fw: fwConfig, conf: confConfigKey, langs: langJS + langPy, key: 1, hints: []string{".get(", ".has("}},
	{re: regexp.MustCompile(`\bsettings\.([A-Z][A-Z0-9_]+)\b`), what: whatConfig, fw: fwDjango, conf: confConfigKey, langs: langPy, key: 1, name: settingsName, hints: []string{"settings."}},
	{re: regexp.MustCompile(`\bflag\.(?:String|Int|Int64|Uint|Uint64|Bool|Duration|Float64|Func|TextVar)\(\s*"([^"]+)"`), what: whatConfig, fw: fwGoFlag, conf: confConfigKey, langs: langGo, key: 1, name: dashName, hints: []string{"flag."}},
	{re: regexp.MustCompile(`\bflag\.(?:StringVar|IntVar|Int64Var|UintVar|Uint64Var|BoolVar|DurationVar|Float64Var|Var)\(\s*[^,]+,\s*"([^"]+)"`), what: whatConfig, fw: fwGoFlag, conf: confConfigKey, langs: langGo, key: 1, name: dashName, hints: []string{"flag."}},
	{re: regexp.MustCompile(`\.(?:Persistent)?Flags\(\)\.\w+\(\s*(?:&[\w.]+\s*,\s*)?"([^"]+)"`), what: whatConfig, fw: fwCobra, conf: confConfigKey, langs: langGo, key: 1, name: func(k string) string { return "--" + k }, hints: []string{"Flags()"}},
	{re: regexp.MustCompile(`\.add_argument\(\s*["'](--?[\w-]+)["']`), what: whatConfig, fw: fwArgparse, conf: confConfigKey, langs: langPy, key: 1, hints: []string{"add_argument("}},
}

// containsAny reports whether s contains one of subs (true for none).
func containsAny(s string, subs []string) bool {
	if len(subs) == 0 {
		return true
	}
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// reSettingsDef is a top-level setting in a Django settings module.
var reSettingsDef = regexp.MustCompile(`^([A-Z][A-Z0-9_]+)\s*(?::\s*[^=]+)?=[^=]`)

func settingsName(k string) string { return "settings." + k }
func dashName(k string) string     { return "-" + k }

// ldReceiver vets a plain variation() call: a LaunchDarkly-looking client
// ("ldClient", "client", ldclient.get()).
func ldReceiver(line string, m []int) (string, bool) {
	recv := strings.ToLower(line[m[2]:m[3]])
	ok := strings.HasSuffix(recv, ")") || strings.Contains(recv, "ld") || strings.Contains(recv, "launch") ||
		strings.Contains(recv, "client") || strings.Contains(recv, "flag")
	return fwLaunchDarkly, ok
}

// flagReceiver vets a method-style flag check by its receiver: an Unleash
// client, or a receiver named like flags ("features.isEnabled").
func flagReceiver(line string, m []int) (string, bool) {
	recv := strings.ToLower(line[m[2]:m[3]])
	switch {
	case strings.Contains(recv, "unleash"):
		return fwUnleash, true
	case strings.Contains(recv, "flag") || strings.Contains(recv, "feature") || strings.Contains(recv, "toggle") || recv == "ff":
		return fwCustom, true
	}
	return "", false
}

// flagPropertyStop are properties of a flags object that are not flags.
var flagPropertyStop = map[string]bool{
	"length": true, "size": true, "keys": true, "values": true, "entries": true, "items": true,
	"prototype": true, "constructor": true, "get": true, "set": true, "has": true, "map": true,
	"forEach": true, "filter": true, "includes": true, "toString": true, "push": true,
}

// flagProperty vets flags.someFlag: a read (not a call, not an assignment)
// of a property that is not a container method.
func flagProperty(line string, m []int) (string, bool) {
	if flagPropertyStop[line[m[2]:m[3]]] {
		return "", false
	}
	rest := strings.TrimLeft(line[m[1]:], " \t")
	switch {
	case strings.HasPrefix(rest, "("):
		return "", false
	case strings.HasPrefix(rest, "=") && !strings.HasPrefix(rest, "=="):
		return "", false
	}
	return fwCustom, true
}

// inStructTag vets an env:"X" match: it must sit in a Go struct tag.
func inStructTag(line string, m []int) (string, bool) {
	return fwEnv, strings.Count(line[:m[0]], "`")%2 == 1
}

// flagHit is one lookup on one side.
type flagHit struct {
	what, key, name, fw string
	conf                float64
	a                   at
}

// detectFlags finds feature flag, environment variable and config key
// changes.
func (b *builder) detectFlags() []model.SurfaceEntry {
	var hits [2][]flagHit
	for _, v := range b.live() {
		if isNoFlagFile(v.f) {
			continue
		}
		for _, s := range sides {
			for _, l := range v.changedLines(s) {
				hits[s] = append(hits[s], lineFlags(v, s, l)...)
			}
		}
	}
	return pairFlags(hits[sideOld], hits[sideNew])
}

// isNoFlagFile reports files no lookup pattern applies to: unknown
// languages, SQL and Prisma schemas.
func isNoFlagFile(f *fileInfo) bool {
	return f.lang == "" || f.lang == langSQL || f.lang == langPrisma
}

// lineFlags returns the lookups on one changed line.
func lineFlags(v *view, s side, l line) []flagHit {
	if l.code == "" {
		return nil
	}
	var out []flagHit
	seen := map[string]bool{}
	add := func(what, key, name, fw string, conf float64) {
		if key == "" || seen[what+"\x00"+key] {
			return
		}
		seen[what+"\x00"+key] = true
		out = append(out, flagHit{what: what, key: key, name: name, fw: fw, conf: conf, a: at{v: v, l: l, s: s}})
	}
	for _, p := range flagPatterns {
		if p.langs != "" && !strings.Contains(p.langs, v.f.lang) || p.langs == "" && v.f.lang == langEnv || !containsAny(l.code, p.hints) {
			continue
		}
		for _, m := range p.re.FindAllStringSubmatchIndex(l.code, -1) {
			if !p.inStr && l.inString(anchor(l.code, m[0])) {
				continue
			}
			fw := p.fw
			if p.check != nil {
				f, ok := p.check(l.code, m)
				if !ok {
					continue
				}
				fw = f
			}
			key := sub(l.code, m, p.key)
			name := key
			if p.name != nil {
				name = p.name(key)
			}
			add(p.what, key, name, fw, p.conf)
		}
	}
	if isSettingsModule(v.f.path) {
		if m := reSettingsDef.FindStringSubmatch(l.code); m != nil {
			add(whatConfig, m[1], settingsName(m[1]), fwDjango, confConfigKey)
		}
	}
	return out
}

// anchor is where a match really starts: past a leading context character
// such as the "(" or space a bare-call pattern matches before the name.
func anchor(code string, i int) int {
	if i < len(code)-1 && !isIdentByte(code[i]) && code[i] != '.' && code[i] != ')' && code[i] != ']' {
		return i + 1
	}
	return i
}

// isSettingsModule reports whether p is a Django settings module.
func isSettingsModule(p string) bool {
	return path.Base(p) == "settings.py" || dirBase(p) == "settings" && path.Ext(p) == ".py"
}

// pairFlags turns both sides' lookups into entries. A key looked up on both
// sides anywhere in the item is no surface change (a moved or edited
// read); within one unit a removed key and an added key of the same kind
// and framework pair as a rename; the rest are removals and additions.
func pairFlags(before, after []flagHit) []model.SurfaceEntry {
	type k struct{ what, key string }
	inOld, inNew := map[k]bool{}, map[k]bool{}
	for _, h := range before {
		inOld[k{h.what, h.key}] = true
	}
	for _, h := range after {
		inNew[k{h.what, h.key}] = true
	}
	keep := func(hs []flagHit, other map[k]bool) []flagHit {
		var out []flagHit
		for _, h := range hs {
			if !other[k{h.what, h.key}] {
				out = append(out, h)
			}
		}
		slices.SortStableFunc(out, func(a, b flagHit) int {
			return cmp.Or(cmp.Compare(a.a.v.u.ID, b.a.v.u.ID), cmp.Compare(a.a.l.no, b.a.l.no), cmp.Compare(a.key, b.key))
		})
		return out
	}
	before, after = keep(before, inNew), keep(after, inOld)
	usedOld := make([]bool, len(before))
	rename := func(n flagHit) int {
		for i, o := range before {
			if !usedOld[i] && o.a.v == n.a.v && o.what == n.what && o.fw == n.fw {
				return i
			}
		}
		return -1
	}
	var out []model.SurfaceEntry
	for _, n := range after {
		i := rename(n)
		if i < 0 {
			out = append(out, flagEntry(n, model.SurfaceAdded, ""))
			continue
		}
		usedOld[i] = true
		out = append(out, flagEntry(n, model.SurfaceChanged, "was "+before[i].name))
	}
	for i, o := range before {
		if !usedOld[i] {
			out = append(out, flagEntry(o, model.SurfaceRemoved, ""))
		}
	}
	return out
}

func flagEntry(h flagHit, change, detail string) model.SurfaceEntry {
	e := newEntry(model.SurfaceFlag, change, h.what, h.name, h.a, h.conf, h.fw)
	e.Detail = detail
	return e
}
