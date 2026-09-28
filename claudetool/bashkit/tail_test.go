package bashkit

import "testing"

func TestTailPipe(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		stripped string
		lines    int
	}{
		{
			name:     "short flag with value",
			command:  "go test ./... 2>&1 | tail -40",
			stripped: "go test ./... 2>&1",
			lines:    40,
		},
		{
			name:     "attached value",
			command:  "make | tail -n5",
			stripped: "make",
			lines:    5,
		},
		{
			name:     "separate value",
			command:  "make | tail -n 5",
			stripped: "make",
			lines:    5,
		},
		{
			name:     "long flag",
			command:  "make | tail --lines=5",
			stripped: "make",
			lines:    5,
		},
		{
			name:     "long flag with separate value",
			command:  "make | tail --lines 5",
			stripped: "make",
			lines:    5,
		},
		{
			name:     "bare tail keeps its default",
			command:  "make | tail",
			stripped: "make",
			lines:    10,
		},
		{
			name:     "longer pipeline",
			command:  "kubectl logs pod | grep -i error | tail -20",
			stripped: "kubectl logs pod | grep -i error",
			lines:    20,
		},
		{
			name:     "pipe all",
			command:  "make |& tail -3",
			stripped: "make",
			lines:    3,
		},
		{
			name:     "block upstream",
			command:  "{ echo a; echo b; } | tail -1",
			stripped: "{\n\techo a\n\techo b\n}",
			lines:    1,
		},
		{
			name:     "heredoc downstream of the pipe",
			command:  "cat <<EOF | tail -1\nhello\nEOF\n",
			stripped: "cat <<EOF\nhello\nEOF",
			lines:    1,
		},
		{
			name:     "line continuation before the pipe",
			command:  "echo hi \\\n| tail -1",
			stripped: "echo hi",
			lines:    1,
		},
		{
			name:     "cd prefix in a chain",
			command:  "cd /tmp && make 2>&1 | tail -5",
			stripped: "cd /tmp && make 2>&1",
			lines:    5,
		},
		{
			name:     "cd prefix as its own statement",
			command:  "cd /tmp; make | tail -5",
			stripped: "cd /tmp\nmake",
			lines:    5,
		},
		{
			name:     "several cd prefixes",
			command:  "cd ~/x; cd /y && cd z && make | tail -2",
			stripped: "cd ~/x\ncd /y && cd z && make",
			lines:    2,
		},
		{
			name:     "cd with no argument",
			command:  "cd && make | tail -5",
			stripped: "cd && make",
			lines:    5,
		},
		{
			name:     "real world cd prefix",
			command:  "cd ui && pnpm install --frozen-lockfile 2>&1 | tail -8",
			stripped: "cd ui && pnpm install --frozen-lockfile 2>&1",
			lines:    8,
		},

		{name: "cd - prints the directory", command: "cd - && make | tail -5"},
		{name: "pushd prints the stack", command: "pushd /tmp && make | tail -5"},
		{name: "echo prefix", command: "echo hi && make | tail -5"},
		{name: "mkdir prefix", command: "mkdir -p x && make | tail -5"},
		{name: "cd prefix, more output after the tail", command: "cd /tmp && make | tail -5; echo done"},
		{name: "cd prefix, nested pipeline", command: "cd /tmp && (make | tail -5)"},
		{name: "tail is not the last stage", command: "make | tail -5 | grep -i error"},
		{name: "second statement", command: "make | tail -5; echo done"},
		{name: "printing prefix in a chain", command: "make && other | tail -5"},
		{name: "or joined", command: "make | tail -5 || echo failed"},
		{name: "backgrounded", command: "make | tail -5 &"},
		{name: "negated", command: "! make | tail -5"},
		{name: "redirect on the pipeline", command: "make | tail -5 > out.txt"},
		{name: "redirect on the tail", command: "make | tail -5 2>/dev/null"},
		{name: "byte count", command: "make | tail -c 100"},
		{name: "follow", command: "make | tail -f"},
		{name: "from line", command: "make | tail -n +5"},
		{name: "count zero", command: "make | tail -0"},
		{name: "file operand", command: "make | tail -5 app.log"},
		{name: "unknown flag", command: "make | tail -q -5"},
		{name: "assignment prefix", command: "make | x=$(date) tail -5"},
		{name: "environment prefix", command: "make | LC_ALL=C tail -5"},
		{name: "timed", command: "time make | tail -5"},
		{name: "expanded count", command: `make | tail -n "$N"`},
		{name: "missing count", command: "make | tail -n"},
		{name: "no pipeline", command: "make"},
		{name: "another last stage", command: "make | head -5"},
		{name: "pipeline in a substitution", command: "echo $(make | tail -5)"},
		{name: "unparseable", command: "make | tail -5 |"},
		{name: "empty", command: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stripped, lines, ok := TailPipe(tt.command)
			if tt.stripped == "" && tt.lines == 0 {
				if ok {
					t.Fatalf("TailPipe(%q) = (%q, %d, true), want not ok", tt.command, stripped, lines)
				}
				return
			}
			if !ok {
				t.Fatalf("TailPipe(%q) = not ok, want (%q, %d)", tt.command, tt.stripped, tt.lines)
			}
			if stripped != tt.stripped || lines != tt.lines {
				t.Errorf("TailPipe(%q) = (%q, %d), want (%q, %d)", tt.command, stripped, lines, tt.stripped, tt.lines)
			}
		})
	}
}
