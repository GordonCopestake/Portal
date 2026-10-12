package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const maxConfigBytes = 256 * 1024

type config struct {
	SchemaVersion         int    `json:"schema_version"`
	BaseURL               string `json:"base_url"`
	Model                 string `json:"model"`
	SystemPrompt          string `json:"system_prompt"`
	APIKeyEnv             string `json:"api_key_env"`
	APIKey                string `json:"api_key"`
	ApprovalMode          string `json:"approval_mode"`
	RequestTimeoutSeconds int    `json:"request_timeout_seconds"`
	CommandTimeoutSeconds int    `json:"command_timeout_seconds"`
	MaxToolCallsPerTurn   int    `json:"max_tool_calls_per_turn"`
}

func configPath(executable, override string) (string, error) {
	if override != "" {
		return filepath.Abs(override)
	}
	return filepath.Join(filepath.Dir(executable), "portal.json"), nil
}

func loadConfig(path string) (config, error) {
	c := config{
		ApprovalMode: "ask", RequestTimeoutSeconds: 120,
		CommandTimeoutSeconds: 60, MaxToolCallsPerTurn: 20,
	}
	f, err := os.Open(path)
	if err != nil {
		return c, fmt.Errorf("cannot open config %q: %w; use --config to select a file", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return c, errors.New("cannot read config; check file access")
	}
	if len(data) > maxConfigBytes {
		return c, errors.New("config exceeds 256 KiB; reduce its size")
	}
	// Windows editors can add a UTF-8 byte order mark when saving JSON.
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		// Do not print parser input: the config can contain a key.
		return c, errors.New("invalid config JSON or unknown field; check portal.json")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return c, errors.New("config must contain exactly one JSON object")
	}
	if err := c.validate(); err != nil {
		return c, err
	}
	return c, nil
}

func (c config) validate() error {
	if c.SchemaVersion != 1 {
		return errors.New("unsupported schema_version; use version 1")
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("base_url must be an absolute HTTP or HTTPS API base URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return errors.New("base_url must not contain credentials, a query, or a fragment")
	}
	if strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/chat/completions") {
		return errors.New("base_url must name the API base, not /chat/completions")
	}
	if p := u.Port(); p != "" {
		port, err := strconv.Atoi(p)
		if err != nil || port < 1 || port > 65535 {
			return errors.New("base_url port must be between 1 and 65535")
		}
	}
	if strings.TrimSpace(c.Model) == "" || len(c.Model) > 256 {
		return errors.New("model must contain 1 to 256 bytes")
	}
	if strings.TrimSpace(c.SystemPrompt) == "" || len(c.SystemPrompt) > 128*1024 {
		return errors.New("system_prompt must contain 1 to 131072 bytes")
	}
	if c.ApprovalMode != "ask" && c.ApprovalMode != "automatic" {
		return errors.New("approval_mode must be ask or automatic")
	}
	if c.RequestTimeoutSeconds < 1 || c.RequestTimeoutSeconds > 600 ||
		c.CommandTimeoutSeconds < 1 || c.CommandTimeoutSeconds > 3600 ||
		c.MaxToolCallsPerTurn < 1 || c.MaxToolCallsPerTurn > 100 {
		return errors.New("limits must be positive: request timeout <= 600, command timeout <= 3600, tool calls <= 100")
	}
	key := c.APIKey
	if c.APIKeyEnv != "" {
		if len(c.APIKeyEnv) > 256 || strings.ContainsAny(c.APIKeyEnv, "=\x00\r\n") {
			return errors.New("invalid API key variable name")
		}
		var exists bool
		key, exists = os.LookupEnv(c.APIKeyEnv)
		if !exists || strings.TrimSpace(key) == "" {
			return errors.New("configured API key variable is missing or empty; set it before startup")
		}
	}
	if len(key) > 8192 || strings.IndexFunc(key, func(r rune) bool { return r <= 32 || r == 127 }) >= 0 {
		return errors.New("API key has an invalid length or control character")
	}
	// The literal local token is a non-secret compatibility token requested for LAN inference.
	if key != "" && key != "local" && u.Scheme != "https" {
		return errors.New("an endpoint with an API key requires HTTPS")
	}
	return nil
}

func (c config) resolvedKey() string {
	if c.APIKeyEnv != "" {
		return os.Getenv(c.APIKeyEnv)
	}
	return c.APIKey
}

func workingDirectory(override string) (string, error) {
	dir := override
	if dir == "" {
		var err error
		dir, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cannot read working folder: %w", err)
		}
		if isNetworkPath(dir) {
			dir, err = os.UserHomeDir()
			if err != nil || isNetworkPath(dir) {
				return "", errors.New("no local working folder is available; select one with --cwd")
			}
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("cannot resolve working folder: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", errors.New("working folder is missing or inaccessible; select a folder with --cwd")
	}
	return abs, nil
}
