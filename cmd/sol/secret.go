package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

func readKey(input io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(input, 16*1024+1))
	if err != nil {
		return "", errors.New("unable to read API key")
	}
	if len(data) > 16*1024 {
		return "", errors.New("API key input is too long")
	}
	key := strings.TrimSpace(string(data))
	if key == "" || strings.ContainsAny(key, "\r\n") {
		return "", errors.New("provide one nonempty API key line")
	}
	return key, nil
}

// readAPIKey accepts a closed pipe or a hidden terminal prompt. Cancellation
// restores terminal state before closing stdin to unblock the read.
func readAPIKey(ctx context.Context, input *os.File, prompt io.Writer, stdin bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	// Fd switches pipes to blocking I/O. SyscallConn preserves pollable reads,
	// so closing a canceled pipe actually unblocks the read goroutine.
	raw, err := input.SyscallConn()
	if err != nil {
		return "", errors.New("unable to inspect API key input")
	}
	fd := 0
	terminal := false
	if err := raw.Control(func(value uintptr) { fd = int(value); terminal = term.IsTerminal(fd) }); err != nil {
		return "", errors.New("unable to inspect API key input")
	}
	if stdin && terminal {
		return "", errors.New("--stdin requires piped input; omit it for a hidden prompt")
	}
	if !stdin && !terminal {
		return "", errors.New("use --stdin for piped API key input")
	}
	var state *term.State
	if terminal {
		var err error
		state, err = term.MakeRaw(fd)
		if err != nil {
			return "", errors.New("unable to secure terminal input")
		}
		defer term.Restore(fd, state)
	}
	type result struct {
		key string
		err error
	}
	done := make(chan result, 1)
	go func() {
		if !terminal {
			key, err := readKey(input)
			done <- result{key, err}
			return
		}
		terminal := term.NewTerminal(struct {
			io.Reader
			io.Writer
		}{input, prompt}, "")
		data, err := terminal.ReadPassword("API key (hidden): ")
		if err != nil {
			done <- result{err: errors.New("unable to read hidden API key")}
			return
		}
		key, err := readKey(strings.NewReader(data))
		done <- result{key, err}
	}()
	select {
	case value := <-done:
		if terminal {
			fmt.Fprintln(prompt)
		}
		return value.key, value.err
	case <-ctx.Done():
		if state != nil {
			_ = term.Restore(fd, state)
		}
		_ = input.Close()
		if !terminal {
			<-done
		}
		return "", ctx.Err()
	}
}
