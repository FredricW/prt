package settings

import (
	"context"
	"encoding/json"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fredricw/prt/internal/config"
	"github.com/fredricw/prt/internal/proto"
	"github.com/fredricw/prt/internal/questions"
)

func TestValidate(t *testing.T) {
	good := FromConfig(config.Defaults(), config.DefaultRouting())
	if err := Validate(good); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
	if err := Validate(sampleValues()); err != nil {
		t.Fatalf("sample invalid: %v", err)
	}
	tests := []struct {
		name    string
		mut     func(*proto.SettingsValues)
		wantErr string
	}{
		{"empty auto_review", func(v *proto.SettingsValues) { v.AutoReview = " " }, "auto_review"},
		{"unknown auto_review", func(v *proto.SettingsValues) { v.AutoReview = "sometimes" }, "auto_review"},
		{"unsure_below > 1", func(v *proto.SettingsValues) { v.UnsureBelow = 1.1 }, "unsure_below must be between 0 and 1"},
		{"keep_pair_p < 0", func(v *proto.SettingsValues) { v.KeepPairP = -0.1 }, "keep_pair_p must be between 0 and 1"},
		{"embed_min_cosine NaN", func(v *proto.SettingsValues) { v.EmbedMinCosine = math.NaN() }, "embed_min_cosine must be between 0 and 1"},
		{"max_group_units 0", func(v *proto.SettingsValues) { v.MaxGroupUnits = 0 }, "max_group_units"},
		{"concurrency 0", func(v *proto.SettingsValues) { v.Concurrency = 0 }, "concurrency"},
		{"concurrency 65", func(v *proto.SettingsValues) { v.Concurrency = 65 }, "concurrency"},
		{"deep_slots 0", func(v *proto.SettingsValues) { v.DeepSlots = 0 }, "deep_slots"},
		{"cost cap 0", func(v *proto.SettingsValues) { v.CostCapUSD = 0 }, "cost_cap_usd"},
		{"cost cap inf", func(v *proto.SettingsValues) { v.CostCapUSD = math.Inf(1) }, "cost_cap_usd"},
		{"negative weight", func(v *proto.SettingsValues) { v.Rank.WAge = -1 }, "w_age"},
		{"NaN weight", func(v *proto.SettingsValues) { v.Rank.WReadyStale = math.NaN() }, "w_ready_stale"},
		{"empty glob", func(v *proto.SettingsValues) { v.Owns = []string{"web/**", " "} }, "owns"},
		{"bad glob", func(v *proto.SettingsValues) { v.Owns = []string{"web/[a"} }, "owns"},
		{"role not utf-8", func(v *proto.SettingsValues) { v.Role = "\xff" }, "role"},
		{"fast without provider", func(v *proto.SettingsValues) { v.Fast = "claude" }, "fast"},
		{"deep empty model", func(v *proto.SettingsValues) { v.Deep = "anthropic:" }, "deep"},
		{"embed empty", func(v *proto.SettingsValues) { v.Embed = "" }, "embed"},
		{"embed unknown adapter", func(v *proto.SettingsValues) { v.Embed = "openai:x" }, "embed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := good
			tt.mut(&v)
			err := Validate(v)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want mention of %q", err, tt.wantErr)
			}
		})
	}
	ok := good
	ok.Embed = "onnx:models/code.onnx"
	ok.AutoReview = " ALL "
	if err := Validate(ok); err != nil {
		t.Errorf("onnx embed / spaced strategy: %v", err)
	}
}

func TestEnsureQuestions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "prt", config.QuestionsFile)
	created, err := EnsureQuestions(path)
	if err != nil || !created {
		t.Fatalf("first call = %v, %v", created, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := questions.Load(data, path)
	if err != nil {
		t.Fatalf("starter does not load: %v", err)
	}
	if got, want := len(cat.Questions), len(questions.Default().Questions); got != want {
		t.Errorf("starter changes the catalogue: %d questions, want %d", got, want)
	}

	// The commented example must be valid once uncommented.
	var example []string
	inExample := false
	for _, line := range strings.Split(string(data), "\n") {
		if line == "# [[question]]" {
			inExample = true
		}
		if inExample {
			example = append(example, strings.TrimPrefix(line, "# "))
		}
	}
	if _, err := questions.Load([]byte(strings.Join(example, "\n")), "example"); err != nil || len(example) == 0 {
		t.Errorf("uncommented example does not load: %v", err)
	}

	if err := os.WriteFile(path, []byte("# edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	created, err = EnsureQuestions(path)
	if err != nil || created {
		t.Fatalf("second call = %v, %v", created, err)
	}
	if b, _ := os.ReadFile(path); string(b) != "# edited\n" {
		t.Errorf("existing file touched: %q", b)
	}
}

func TestHTTPProber(t *testing.T) {
	var gotPath string
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer ok.Close()
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer fail.Close()
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := l.Addr().String()
	_ = l.Close()

	okHost := strings.TrimPrefix(ok.URL, "http://")
	failHost := strings.TrimPrefix(fail.URL, "http://")
	tests := []struct {
		name string
		p    HTTPProber
		env  string
		want proto.Probe
	}{
		{"url", HTTPProber{Host: ok.URL}, "", proto.Probe{OK: true, Note: "reachable at " + okHost}},
		{"bare host:port", HTTPProber{Host: okHost}, "", proto.Probe{OK: true, Note: "reachable at " + okHost}},
		{"from OLLAMA_HOST", HTTPProber{}, okHost, proto.Probe{OK: true, Note: "reachable at " + okHost}},
		{"custom client", HTTPProber{Host: ok.URL, Client: ok.Client()}, "", proto.Probe{OK: true, Note: "reachable at " + okHost}},
		{"http 500", HTTPProber{Host: fail.URL}, "", proto.Probe{Note: "HTTP 500 from " + failHost}},
		{"closed port", HTTPProber{Host: closed}, "", proto.Probe{Note: "unreachable at " + closed}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OLLAMA_HOST", tt.env)
			if got := tt.p.Ollama(context.Background()); got != tt.want {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
	for _, host := range []string{"http://u:secretpw@" + closed, "http://u:secretpw@" + okHost} {
		if got := (HTTPProber{Host: host}).Ollama(context.Background()); strings.Contains(got.Note, "secretpw") {
			t.Errorf("userinfo echoed: %+v", got)
		}
	}
	if gotPath != "/api/tags" {
		t.Errorf("probed %q, want /api/tags", gotPath)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := (HTTPProber{Host: ok.URL}).Ollama(ctx); got.OK {
		t.Errorf("cancelled context probed OK: %+v", got)
	}
}

type fakeProber struct{ p proto.Probe }

func (f fakeProber) Ollama(context.Context) proto.Probe { return f.p }

func TestEnv(t *testing.T) {
	secret := map[string]string{
		"JEV_API_KEY":           "sk-live-SECRET-jev",
		"PRT_ANTHROPIC_API_KEY": "sk-live-SECRET-ant",
		"ANTHROPIC_API_KEY":     "sk-live-SECRET-wrong",
	}
	gh := proto.Probe{OK: true, Note: "gh token"}
	st := Env(context.Background(), func(k string) string { return secret[k] }, fakeProber{proto.Probe{OK: true, Note: "reachable at h:1"}}, gh)
	want := proto.EnvStatus{
		JevKey:       proto.Probe{OK: true, Note: "set"},
		AnthropicKey: proto.Probe{OK: true, Note: "set"},
		Ollama:       proto.Probe{OK: true, Note: "reachable at h:1"},
		GitHub:       gh,
	}
	if st != want {
		t.Errorf("got %+v, want %+v", st, want)
	}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "SECRET") {
		t.Errorf("secret leaked: %s", b)
	}

	onlyWrong := func(k string) string {
		if k == "ANTHROPIC_API_KEY" {
			return "sk-live-SECRET"
		}
		return "  "
	}
	st = Env(context.Background(), onlyWrong, nil, proto.Probe{})
	if st.AnthropicKey.OK || st.AnthropicKey.Note != "not set" || st.JevKey.OK || st.JevKey.Note != "not set" {
		t.Errorf("keys = %+v / %+v, want not set (ANTHROPIC_API_KEY ignored, blank is unset)", st.JevKey, st.AnthropicKey)
	}
	if st.Ollama != (proto.Probe{Note: "not checked"}) {
		t.Errorf("nil prober = %+v", st.Ollama)
	}

	t.Setenv("JEV_API_KEY", "x")
	t.Setenv("PRT_ANTHROPIC_API_KEY", "")
	st = Env(context.Background(), nil, nil, proto.Probe{})
	if !st.JevKey.OK || st.AnthropicKey.OK {
		t.Errorf("nil getenv uses os.Getenv: %+v", st)
	}
}
