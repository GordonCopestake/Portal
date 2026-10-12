package main

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

// Opt-in integration check. Only this exact harmless command can execute.
func TestLiveEndpoint(t *testing.T) {
	if os.Getenv("PORTAL_LIVE_TEST") != "1" {
		t.Skip("set PORTAL_LIVE_TEST=1 to test the configured endpoint")
	}
	c, err := loadConfig("portal.json")
	if err != nil {
		t.Fatal(err)
	}
	client := newModelClient(c)
	defer client.close()
	if err := client.discoverContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Logf("Provider context: %d tokens", client.contextTokens)
	a := newAgent(client, "Integration test workstation", t.TempDir(), io.Discard)
	const command = "[Console]::WriteLine('PORTAL_TOOL_OK')"
	calls := 0
	a.approve = func(_ context.Context, actual string) (bool, error) {
		if strings.TrimSpace(actual) != command || calls >= 2 {
			t.Errorf("unexpected live command: %q", actual)
			return false, nil
		}
		calls++
		return true, nil
	}
	for i := 0; i < 2; i++ {
		if i == 1 {
			clear(a.history[2:])
			a.history = a.history[:2]
		}
		if err := a.turn(context.Background(), "Use powershell_call exactly once with this exact command: "+command+". Do not use any other command. After the result, report the marker from stdout."); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatal("model did not call the single PowerShell tool")
	}
	last := a.history[len(a.history)-1]
	if last.Role != "assistant" || !strings.Contains(last.Content, "PORTAL_TOOL_OK") {
		t.Fatal("model did not use tool evidence")
	}
	t.Log("Live tool call and final evidence report passed:", c.Model)
}
