package main

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

type promptRecorder struct{ writes chan string }

func (w promptRecorder) Write(data []byte) (int, error) {
	w.writes <- string(data)
	return len(data), nil
}

func TestInteractiveAPIKeyHiddenAndTerminalRestored(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "password", true: "Ctrl-C"}[cancel], func(t *testing.T) {
			master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer master.Close()
			fd := int(master.Fd())
			if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
				t.Fatal(err)
			}
			n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
			if err != nil {
				t.Fatal(err)
			}
			slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|unix.O_NOCTTY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer slave.Close()
			raw, err := slave.SyscallConn()
			if err != nil {
				t.Fatal(err)
			}
			var slaveFD int
			raw.Control(func(fd uintptr) { slaveFD = int(fd) })
			before, err := unix.IoctlGetTermios(slaveFD, unix.TCGETS)
			if err != nil {
				t.Fatal(err)
			}
			writes := make(chan string, 8)
			type result struct {
				key string
				err error
			}
			done := make(chan result, 1)
			go func() {
				key, err := readAPIKey(context.Background(), slave, promptRecorder{writes}, false)
				done <- result{key, err}
			}()
			select {
			case prompt := <-writes:
				if !strings.Contains(prompt, "hidden") {
					t.Fatal("missing hidden prompt")
				}
			case <-time.After(time.Second):
				t.Fatal("interactive prompt hung")
			}
			during, err := unix.IoctlGetTermios(slaveFD, unix.TCGETS)
			if err != nil {
				t.Fatal(err)
			}
			if during.Lflag&unix.ECHO != 0 {
				t.Fatal("terminal echoes secret input")
			}
			if cancel {
				master.Write([]byte{3})
			} else {
				master.WriteString("interactive-test-secret\r")
			}
			select {
			case result := <-done:
				if cancel {
					if result.err == nil {
						t.Fatal("Ctrl-C accepted key")
					}
				} else if result.err != nil || result.key != "interactive-test-secret" {
					t.Fatal("hidden prompt failed")
				}
			case <-time.After(time.Second):
				t.Fatal("interactive read hung")
			}
			after, err := unix.IoctlGetTermios(slaveFD, unix.TCGETS)
			if err != nil || after.Lflag != before.Lflag {
				t.Fatal("terminal state not restored", err)
			}
			close(writes)
			for output := range writes {
				if strings.Contains(output, "interactive-test-secret") {
					t.Fatal("secret echoed")
				}
			}
		})
	}
}
