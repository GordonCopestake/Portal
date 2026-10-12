package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validConfig() config {
	return config{SchemaVersion: 1, BaseURL: "http://localhost:8080/v1", Model: "test",
		SystemPrompt: "Inspect this workstation.", ApprovalMode: "ask",
		RequestTimeoutSeconds: 120, CommandTimeoutSeconds: 60, MaxToolCallsPerTurn: 20}
}

func TestConfigPathUsesExecutableFolder(t *testing.T) {
	for _, executable := range []string{`C:\folder with spaces\portal.exe`, `\\server\share\folder with spaces\portal.exe`} {
		got, err := configPath(executable, "")
		want := filepath.Join(filepath.Dir(executable), "portal.json")
		if err != nil || got != want {
			t.Fatalf("configPath(%q) = %q, %v; want %q", executable, got, err, want)
		}
	}
	got, err := configPath(`C:\unused\portal.exe`, "explicit.json")
	want, _ := filepath.Abs("explicit.json")
	if err != nil || got != want {
		t.Fatalf("relative override = %q, %v; want %q", got, err, want)
	}
}

func TestConfigValidation(t *testing.T) {
	t.Setenv("PORTAL_TEST_MISSING_KEY", "")
	for _, test := range []struct {
		name   string
		change func(*config)
	}{
		{"schema", func(c *config) { c.SchemaVersion = 2 }},
		{"endpoint", func(c *config) { c.BaseURL = "invalid" }},
		{"credentials in URL", func(c *config) { c.BaseURL = "https://user:SECRET@host/v1" }},
		{"query", func(c *config) { c.BaseURL = "https://host/v1?key=SECRET" }},
		{"completion path", func(c *config) { c.BaseURL += "/chat/completions/" }},
		{"model", func(c *config) { c.Model = " " }},
		{"prompt", func(c *config) { c.SystemPrompt = "" }},
		{"approval", func(c *config) { c.ApprovalMode = "invalid" }},
		{"timeout", func(c *config) { c.CommandTimeoutSeconds = 0 }},
		{"tool limit", func(c *config) { c.MaxToolCallsPerTurn = 101 }},
		{"HTTP key", func(c *config) { c.APIKey = "SECRET" }},
		{"missing env", func(c *config) { c.APIKeyEnv = "PORTAL_TEST_MISSING_KEY"; c.APIKey = "SECRET" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := validConfig()
			test.change(&c)
			err := c.validate()
			if err == nil || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("expected a safe validation error; got %v", err)
			}
		})
	}
	if err := validConfig().validate(); err != nil {
		t.Fatal(err)
	}
	c := validConfig()
	c.BaseURL, c.APIKey = "https://host/v1", "SECRET"
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestConfigFileParsingAndLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "portal.json")
	good, _ := json.Marshal(validConfig())
	for _, data := range []string{
		`{"api_key":"SECRET",`,
		strings.TrimSuffix(string(good), "}") + `,"unknown":"SECRET"}`,
		string(good) + `{}`,
		"null",
		strings.Repeat(" ", maxConfigBytes+1),
	} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := loadConfig(path)
		if err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("expected safe parsing error; got %v", err)
		}
	}
	if err := os.WriteFile(path, good, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := loadConfig(path)
	if err != nil || c.SystemPrompt != validConfig().SystemPrompt {
		t.Fatalf("prompt not preserved: %v", err)
	}
}

func TestHelpAndVersionNeedNoConfig(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"--version"}} {
		var out, errOut bytes.Buffer
		if err := run(args, &out, &errOut); err != nil {
			t.Fatal(err)
		}
		if out.Len()+errOut.Len() == 0 {
			t.Fatal("no output")
		}
	}
}

func TestConfigAcceptsUTF8BOMWithoutChangingFile(t *testing.T) {
	want := validConfig()
	want.Model = "Strata-Qwen3.8-Flash-Next-Abliterated-IQ3_XXS-LowRAM-Dual-256K"
	want.SystemPrompt = "Inspect the workstation. café 中文"
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "portal.json")
	for _, prefix := range [][]byte{nil, {0xEF, 0xBB, 0xBF}} {
		original := append(append([]byte{}, prefix...), data...)
		if err := os.WriteFile(path, original, 0600); err != nil {
			t.Fatal(err)
		}
		got, err := loadConfig(path)
		if err != nil || got != want {
			t.Fatalf("config with prefix %x was not preserved: %v", prefix, err)
		}
		unchanged, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(original, unchanged) {
			t.Fatal("loader changed the config file")
		}
	}
}

func TestStartupUsesExplicitConfigAndFolder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "portal.json")
	data, _ := json.Marshal(validConfig())
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := run([]string{"--startup-check", "--config", path, "--cwd", dir}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{path, dir, "Computer:", "Elevated:", "Execution mode: ask", "Startup check passed"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in %s", want, out.String())
		}
	}
}

func TestNetworkPath(t *testing.T) {
	for _, path := range []string{`\\server\share\folder`, `\\?\UNC\server\share\folder`, `//server/share`} {
		if !isNetworkPath(path) {
			t.Fatalf("network path not detected: %s", path)
		}
	}
	for _, path := range []string{`C:\Windows`, `\\?\C:\Windows`} {
		if isNetworkPath(path) {
			t.Fatalf("local path classified as remote: %s", path)
		}
	}
}
