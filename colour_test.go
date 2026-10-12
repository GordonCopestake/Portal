package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/sys/windows"
)

func TestConversationColoursAndPlainOutput(t *testing.T) {
	for _, field := range []string{"reasoning_content", "reasoning"} {
		for _, coloured := range []bool{false, true} {
			name := field + "/plain"
			if coloured {
				name = field + "/colour"
			}
			t.Run(name, func(t *testing.T) {
				var requests atomic.Int32
				command := "'probe\x1b[2J'"
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					if bytes.Contains(body, []byte("THINKING_ONLY")) || bytes.Contains(body, []byte("38;2;")) || bytes.Contains(body, []byte(`"reasoning`)) {
						t.Error("display data leaked into the model request")
					}
					switch requests.Add(1) {
					case 1:
						json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{
							"message":       map[string]any{"role": "assistant", "content": "Inspecting.", field: "THINKING_ONLY SECRET\x1b[2J", "tool_calls": []toolCall{call("probe", command)}},
							"finish_reason": "tool_calls",
						}}})
					case 2:
						reply(w, message{Role: "assistant", Content: "Done.\x1b[2J"})
					case 3:
						reply(w, message{Role: "assistant", Refusal: "No."})
					default:
						t.Error("unexpected request")
					}
				}))
				defer server.Close()
				c := validConfig()
				c.BaseURL, c.APIKey, c.ApprovalMode = server.URL, "SECRET", "ask"
				client := newModelClient(c)
				defer client.close()
				var out bytes.Buffer
				a := newAgent(client, "test", t.TempDir(), &out)
				a.colour = coloured
				executions := 0
				a.execute = func(_ context.Context, actual string) toolResult {
					if actual != command {
						t.Error("display changed the command")
					}
					executions++
					return toolResult{Stdout: "evidence"}
				}
				console := newConsole(strings.NewReader("inspect\ny\nagain\n/exit\n"))
				defer console.close()
				if err := interact(console, a, ""); err != nil {
					t.Fatal(err)
				}
				if executions != 1 || requests.Load() != 3 {
					t.Fatalf("executions %d; requests %d", executions, requests.Load())
				}
				output := out.String()
				if strings.Contains(output, "SECRET") || strings.Contains(output, "\x1b[2J") {
					t.Fatal("untrusted terminal controls or credential were displayed")
				}
				for _, expected := range []struct{ colour, text string }{
					{colourGrey, "Requesting model...\n"},
					{colourGrey, "THINKING_ONLY ******\\u001b[2J\n"},
					{colourGrey, "Inspecting.\n"},
					{colourOrange, "\nPowerShell command:\n'probe\\u001b[2J'\n"},
					{colourOrange, "Run this command? [y/N] "},
					{colourWhite, "Done.\\u001b[2J\n"},
					{colourWhite, "No.\n"},
				} {
					text := expected.text
					if coloured {
						text = expected.colour + text + colourReset
					}
					if !strings.Contains(output, text) {
						t.Errorf("missing output %q in %q", text, output)
					}
				}
				if coloured {
					if !strings.Contains(output, colourOrange+"Result: ") || strings.Count(output, "\x1b[38;2;") != strings.Count(output, colourReset) {
						t.Error("missing orange result or colour reset")
					}
				} else if strings.Contains(output, "\x1b") {
					t.Error("plain output contains colour sequences")
				}
				for _, m := range a.history {
					if m.Thinking != "" {
						t.Error("display-only thinking retained in history")
					}
				}
			})
		}
	}
}

func TestColourDisabledForFilesAndEnvironment(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "")
	file, err := os.Create(filepath.Join(t.TempDir(), "output.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if enabled, restore := enableColour(file); enabled {
		restore()
		t.Fatal("colour enabled for a file")
	} else {
		restore()
	}
	for _, setting := range []struct{ name, value string }{{"NO_COLOR", "1"}, {"TERM", "dumb"}} {
		t.Run(setting.name, func(t *testing.T) {
			t.Setenv(setting.name, setting.value)
			if enabled, restore := enableColour(os.Stdout); enabled {
				restore()
				t.Fatal("colour enabled despite environment setting")
			} else {
				restore()
			}
		})
	}
}

func TestWindowsConsoleColourMode(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "")
	console, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		t.Skip("no Windows console attached")
	}
	defer console.Close()
	handle := windows.Handle(console.Fd())
	var before, during, after uint32
	if err := windows.GetConsoleMode(handle, &before); err != nil {
		t.Fatal(err)
	}
	enabled, restore := enableColour(console)
	defer restore()
	if !enabled {
		t.Skip("virtual terminal output unavailable")
	}
	if err := windows.GetConsoleMode(handle, &during); err != nil || during&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING == 0 {
		t.Fatalf("virtual terminal output not enabled: mode %x; error %v", during, err)
	}
	restore()
	if err := windows.GetConsoleMode(handle, &after); err != nil || after != before {
		t.Fatalf("console mode not restored: before %x; after %x; error %v", before, after, err)
	}
}
