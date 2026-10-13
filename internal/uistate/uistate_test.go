package uistate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingIsZero(t *testing.T) {
	for _, p := range []string{"", filepath.Join(t.TempDir(), "nope", FileName)} {
		l, err := Load(p)
		if err != nil || l != (Layout{}) {
			t.Errorf("Load(%q) = %+v, %v; want zero, nil", p, l, err)
		}
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state", "prt", FileName)
	want := Layout{SidebarWidth: 48, CodeListWidth: 30}
	if err := Save(p, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil || got != want {
		t.Fatalf("Load = %+v, %v; want %+v", got, err, want)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", fi.Mode().Perm())
	}
	// Overwrite keeps working and leaves no temp files behind.
	if err := Save(p, Layout{SidebarWidth: 40}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(p))
	if err != nil || len(entries) != 1 {
		t.Fatalf("dir entries %v, %v; want only %s", entries, err, FileName)
	}
}

func TestLoadBadFile(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name, body string
		want       Layout
		wantErr    bool
	}{
		{"empty", "", Layout{}, false},
		{"garbage", "{not json", Layout{}, true},
		{"negative", `{"sidebarWidth": -3, "codeListWidth": 22}`, Layout{CodeListWidth: 22}, false},
		{"unknown keys", `{"sidebarWidth": 50, "theme": "dark"}`, Layout{SidebarWidth: 50}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := filepath.Join(dir, tt.name+".json")
			if err := os.WriteFile(p, []byte(tt.body), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := Load(p)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("Load = %+v, %v; want %+v (err %v)", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestDefaultPath(t *testing.T) {
	st := t.TempDir()
	t.Setenv("XDG_STATE_HOME", st)
	if got := DefaultPath(); got != filepath.Join(st, "prt", FileName) {
		t.Errorf("DefaultPath = %q", got)
	}
}

func TestSaveEmptyPathIsNoop(t *testing.T) {
	if err := Save("", Layout{SidebarWidth: 1}); err != nil {
		t.Fatal(err)
	}
}
