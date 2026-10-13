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

// Two processes resizing different panes keep each other's widths: each
// writes only the width it changed, over the file's current contents.
func TestSaversInterleaved(t *testing.T) {
	p := filepath.Join(t.TempDir(), FileName)
	a, b := NewSaver(p, Layout{}), NewSaver(p, Layout{})
	if err := a.Save(Layout{CodeListWidth: 30}); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(Layout{SidebarWidth: 38}); err != nil {
		t.Fatal(err)
	}
	if got, _ := Load(p); got != (Layout{SidebarWidth: 38, CodeListWidth: 30}) {
		t.Fatalf("after A's code list and B's picker: %+v", got)
	}
	// A saves again with its picker unchanged since start: B's width stays.
	if err := a.Save(Layout{CodeListWidth: 26}); err != nil {
		t.Fatal(err)
	}
	if got, _ := Load(p); got != (Layout{SidebarWidth: 38, CodeListWidth: 26}) {
		t.Fatalf("after A's second save: %+v", got)
	}
	// Back to automatic (0) is a change too.
	if err := b.Save(Layout{}); err != nil {
		t.Fatal(err)
	}
	if got, _ := Load(p); got != (Layout{CodeListWidth: 26}) {
		t.Fatalf("after B's reset: %+v", got)
	}
	// A file that does not decode is replaced by the saver's layout.
	if err := os.WriteFile(p, []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Save(Layout{SidebarWidth: 50, CodeListWidth: 26}); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(p); err != nil || got != (Layout{SidebarWidth: 50, CodeListWidth: 26}) {
		t.Fatalf("after a bad file: %+v, %v", got, err)
	}
}

func TestSaveEmptyPathIsNoop(t *testing.T) {
	if err := Save("", Layout{SidebarWidth: 1}); err != nil {
		t.Fatal(err)
	}
}
