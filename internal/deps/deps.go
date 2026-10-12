//go:build tools

// Package deps pins every third-party module the project uses, so that go.mod
// and go.sum stay complete even while some packages are still stubs. Parallel
// work on disjoint packages can then import these without running `go get`.
//
// This file is excluded from normal builds by the "tools" build tag;
// `go mod tidy` still sees it because tidy considers all build tags.
package deps

import (
	_ "github.com/BurntSushi/toml"
	_ "github.com/bmatcuk/doublestar/v4"
	_ "github.com/charmbracelet/bubbles/help"
	_ "github.com/charmbracelet/bubbles/key"
	_ "github.com/charmbracelet/bubbles/list"
	_ "github.com/charmbracelet/bubbles/spinner"
	_ "github.com/charmbracelet/bubbles/textarea"
	_ "github.com/charmbracelet/bubbles/textinput"
	_ "github.com/charmbracelet/bubbles/viewport"
	_ "github.com/charmbracelet/bubbletea"
	_ "github.com/charmbracelet/glamour"
	_ "github.com/charmbracelet/huh"
	_ "github.com/charmbracelet/lipgloss"
	_ "github.com/charmbracelet/log"
	_ "github.com/charmbracelet/x/exp/teatest"
	_ "github.com/google/go-github/v66/github"
	_ "github.com/sergi/go-diff/diffmatchpatch"
	_ "github.com/shurcooL/githubv4"
	_ "github.com/smacker/go-tree-sitter"
	_ "github.com/smacker/go-tree-sitter/golang"
	_ "github.com/smacker/go-tree-sitter/javascript"
	_ "github.com/smacker/go-tree-sitter/python"
	_ "github.com/smacker/go-tree-sitter/typescript/tsx"
	_ "github.com/smacker/go-tree-sitter/typescript/typescript"
	_ "golang.org/x/oauth2"
	_ "modernc.org/sqlite"
)
