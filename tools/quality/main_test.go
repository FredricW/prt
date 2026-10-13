package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// TestRunPrintsDeterministicBoard runs the CLI twice offline: the markdown
// board is byte-identical, and -json decodes with one row per ground-truth
// item.
func TestRunPrintsDeterministicBoard(t *testing.T) {
	ctx := context.Background()
	var first, second, errOut bytes.Buffer
	if code := run(ctx, nil, &first, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if code := run(ctx, nil, &second, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if first.String() != second.String() {
		t.Fatalf("board differs between runs:\n%s\n---\n%s", first.String(), second.String())
	}
	for _, item := range []string{"pr:101", "pr:102", "pr:103", "pr:104", "pr:106", "branch:05-spike-dark-mode", "**totals**"} {
		if !strings.Contains(first.String(), "| "+item+" |") {
			t.Errorf("board lacks %s:\n%s", item, first.String())
		}
	}
	var js bytes.Buffer
	if code := run(ctx, []string{"-json"}, &js, &errOut); code != 0 {
		t.Fatalf("-json exit %d: %s", code, errOut.String())
	}
	var board struct {
		Items []struct {
			Item string `json:"item"`
		} `json:"items"`
	}
	if err := json.Unmarshal(js.Bytes(), &board); err != nil || len(board.Items) != 6 {
		t.Fatalf("-json: %d items, %v", len(board.Items), err)
	}
}

func TestRunErrors(t *testing.T) {
	if code := run(context.Background(), []string{"-nope"}, io.Discard, io.Discard); code != 2 {
		t.Errorf("bad flag: exit %d, want 2", code)
	}
	var errOut bytes.Buffer
	if code := run(context.Background(), []string{"-groundtruth", t.TempDir()}, io.Discard, &errOut); code != 1 || !strings.Contains(errOut.String(), "no ground-truth files") {
		t.Errorf("empty ground truth: exit %d, %q", code, errOut.String())
	}
}
