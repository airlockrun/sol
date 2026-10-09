package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestReadKey(t *testing.T) {
	for _, tc := range []struct {
		name, input, want string
		valid             bool
	}{{"line", "test-secret\n", "test-secret", true}, {"empty", "\n", "", false}, {"multiple", "test-secret\nother-secret\n", "", false}, {"too long", strings.Repeat("x", 16385), "", false}} {
		t.Run(tc.name, func(t *testing.T) {
			key, err := readKey(strings.NewReader(tc.input))
			if (err == nil) != tc.valid || key != tc.want {
				t.Fatal("unexpected key input result")
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("secret leaked")
			}
		})
	}
}

func TestReadAPIKeyFromPipe(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	writer.WriteString("pipe-secret\n")
	writer.Close()
	var prompt bytes.Buffer
	key, err := readAPIKey(t.Context(), reader, &prompt, true)
	if err != nil || key != "pipe-secret" || prompt.Len() != 0 {
		t.Fatal("incorrect pipe input")
	}
	if _, err := readAPIKey(t.Context(), reader, &prompt, false); err == nil {
		t.Fatal("accepted nonterminal interactive input")
	}
}

func TestReadAPIKeyCancellation(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	_, err = readAPIKey(ctx, reader, &bytes.Buffer{}, true)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
