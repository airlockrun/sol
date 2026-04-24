package tools

import (
	"testing"
)

func TestExtractCommandPattern(t *testing.T) {
	tests := []struct {
		command string
		want    string
	}{
		{"git commit -m 'message'", "git "},
		{"npm install package", "npm "},
		{"rm -rf /tmp/foo", "rm "},
		{"ls", "ls "},
		{"", ""},
		{"git && npm install", "git "},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			got := extractCommandPattern(tt.command)
			if got != tt.want {
				t.Errorf("extractCommandPattern(%q) = %q, want %q", tt.command, got, tt.want)
			}
		})
	}
}

func TestParseCommandArgs(t *testing.T) {
	tests := []struct {
		cmd  string
		want []string
	}{
		{"ls -la", []string{"ls", "-la"}},
		{"rm 'file with spaces'", []string{"rm", "file with spaces"}},
		{`echo "hello world"`, []string{"echo", "hello world"}},
		{"git commit -m 'fix bug'", []string{"git", "commit", "-m", "fix bug"}},
		{"", nil},
	}

	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			got := parseCommandArgs(tt.cmd)
			if len(got) != len(tt.want) {
				t.Errorf("parseCommandArgs(%q) = %v, want %v", tt.cmd, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parseCommandArgs(%q)[%d] = %q, want %q", tt.cmd, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestExtractExternalDirectories(t *testing.T) {
	workDir := "/home/user/project"

	tests := []struct {
		name    string
		command string
		want    []string
	}{
		{
			name:    "no external dirs",
			command: "ls src/",
			want:    nil,
		},
		{
			name:    "rm with absolute external path",
			command: "rm /tmp/testfile",
			want:    []string{"/tmp/testfile"},
		},
		{
			name:    "cd to external dir",
			command: "cd /var/log",
			want:    []string{"/var/log"},
		},
		{
			name:    "multiple commands with external paths",
			command: "cp /etc/hosts . && rm /tmp/foo",
			want:    []string{"/etc/hosts", "/tmp/foo"},
		},
		{
			name:    "command with flags and external path",
			command: "rm -rf /tmp/testdir",
			want:    []string{"/tmp/testdir"},
		},
		{
			name:    "chmod should skip mode argument",
			command: "chmod 755 /tmp/script.sh",
			want:    []string{"/tmp/script.sh"},
		},
		{
			name:    "relative path stays inside",
			command: "rm ./src/file.txt",
			want:    nil,
		},
		{
			name:    "git command no paths",
			command: "git status",
			want:    nil,
		},
		{
			name:    "parent directory escape",
			command: "rm ../../etc/passwd",
			want:    []string{"/home/etc/passwd"}, // /home/user/project/../../etc/passwd = /home/etc/passwd
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractExternalDirectories(tt.command, workDir)
			if len(got) != len(tt.want) {
				t.Errorf("extractExternalDirectories(%q, %q) = %v, want %v", tt.command, workDir, got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("extractExternalDirectories(%q, %q)[%d] = %q, want %q", tt.command, workDir, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestIsInsideDir(t *testing.T) {
	tests := []struct {
		path string
		dir  string
		want bool
	}{
		{"/home/user/project/src", "/home/user/project", true},
		{"/home/user/project", "/home/user/project", true},
		{"/home/user/other", "/home/user/project", false},
		{"/tmp/file", "/home/user/project", false},
		{"/home/user/project/../other", "/home/user/project", false},
	}

	for _, tt := range tests {
		t.Run(tt.path+"_in_"+tt.dir, func(t *testing.T) {
			got := isInsideDir(tt.path, tt.dir)
			if got != tt.want {
				t.Errorf("isInsideDir(%q, %q) = %v, want %v", tt.path, tt.dir, got, tt.want)
			}
		})
	}
}

func TestIsNumeric(t *testing.T) {
	tests := []struct {
		s    string
		want bool
	}{
		{"755", true},
		{"123", true},
		{"0", true},
		{"+x", false},
		{"abc", false},
		{"12a", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.s, func(t *testing.T) {
			got := isNumeric(tt.s)
			if got != tt.want {
				t.Errorf("isNumeric(%q) = %v, want %v", tt.s, got, tt.want)
			}
		})
	}
}
