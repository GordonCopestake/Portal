package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestPowerShellCommands(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PORTAL_TOOL_TEST_KEY", "SECRET")
	c := validConfig()
	c.APIKeyEnv = "PORTAL_TOOL_TEST_KEY"
	for _, test := range []struct {
		name, command, stdout, stderr string
		code                          uint32
	}{
		{"encoding", "$text = 'café 中文 😀';\n[Console]::WriteLine($text + ' $ ` " + `"quoted"` + "')", "café 中文 😀 $ ` \"quoted\"", "", 0},
		{"streams", "[Console]::WriteLine('OUT'); [Console]::Error.WriteLine('ERR')", "OUT", "ERR", 0},
		{"native failure", "& $env:ComSpec /d /c 'exit 7'", "", "", 7},
		{"PS failure", "Write-Error 'INTENDED_FAILURE'", "", "INTENDED_FAILURE", 1},
		{"cwd", "[Console]::WriteLine((Get-Location).Path)", "", "", 0},
		{"secret stripped", "if (Test-Path Env:PORTAL_TOOL_TEST_KEY) { throw 'key leaked' }; [Console]::WriteLine('NO_KEY')", "NO_KEY", "", 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := runPowerShell(context.Background(), c, dir, test.command)
			if test.name == "cwd" {
				want, err1 := os.Stat(dir)
				got, err2 := os.Stat(strings.TrimSpace(r.Stdout))
				if err1 != nil || err2 != nil || !os.SameFile(want, got) {
					t.Fatal("incorrect command working folder")
				}
			}
			if r.Error != "" || r.ExitCode == nil || *r.ExitCode != test.code || !strings.Contains(r.Stdout, test.stdout) || !strings.Contains(r.Stderr, test.stderr) {
				data, _ := json.Marshal(r)
				t.Fatalf("%s", data)
			}
		})
	}
	large := runPowerShell(context.Background(), c, dir, "[Console]::Write('x' * 100000); [Console]::Error.Write('y' * 100000)")
	if large.Error != "" || large.ExitCode == nil || *large.ExitCode != 0 || !large.Truncated || len(large.Stdout)+len(large.Stderr) != maxOutputBytes {
		t.Fatalf("large output: %#v", large)
	}
	if r := runPowerShell(context.Background(), c, dir, "if (Test-Path Variable:text) { throw 'state leaked' }"); r.Error != "" || r.ExitCode == nil || *r.ExitCode != 0 {
		t.Fatal(r)
	}
	command := "[Console]::WriteLine('MAX_INPUT_OK')"
	command += strings.Repeat(" ", maxCommandBytes-len(command))
	if r := runPowerShell(context.Background(), c, dir, command); r.Error != "" || r.ExitCode == nil || *r.ExitCode != 0 || !strings.Contains(r.Stdout, "MAX_INPUT_OK") {
		t.Fatal(r)
	}
}

func TestPowerShellTimeoutAndDescendants(t *testing.T) {
	c := validConfig()
	c.CommandTimeoutSeconds = 2
	dir := t.TempDir()
	for _, mode := range []string{"timeout", "cancel", "parent exit"} {
		t.Run(mode, func(t *testing.T) {
			pidFile := filepath.Join(dir, mode+".pid")
			command := "$p = Start-Process -FilePath ($PSHOME + '\\powershell.exe') -ArgumentList '-NoLogo -NoProfile -NonInteractive -Command Start-Sleep -Seconds 30' -PassThru; [IO.File]::WriteAllText('" + strings.ReplaceAll(pidFile, "'", "''") + "', [string]$p.Id)"
			if mode != "parent exit" {
				command += "; Start-Sleep -Seconds 30"
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				go func() {
					for i := 0; i < 100; i++ {
						if _, err := os.Stat(pidFile); err == nil {
							cancel()
							return
						}
						time.Sleep(25 * time.Millisecond)
					}
					cancel()
				}()
			}
			start := time.Now()
			r := runPowerShell(ctx, c, dir, command)
			if mode != "parent exit" && (r.ExitCode != nil || r.DurationMS <= 0) {
				t.Fatal("forced termination code reported as command exit status")
			}
			if time.Since(start) > 6*time.Second || (mode == "timeout" && !r.TimedOut) || (mode == "cancel" && !r.Cancelled) || (mode == "parent exit" && (r.Error != "" || r.ExitCode == nil || *r.ExitCode != 0)) {
				t.Fatalf("%s: %#v", mode, r)
			}
			data, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.ParseUint(string(data), 10, 32)
			if err != nil {
				t.Fatal(err)
			}
			handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
			if err == nil {
				defer windows.CloseHandle(handle)
				state, err := windows.WaitForSingleObject(handle, 2000)
				if err != nil || state != windows.WAIT_OBJECT_0 {
					t.Fatal("descendant still running")
				}
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := runPowerShell(ctx, c, dir, "throw 'must not start'")
	if !r.Cancelled || r.ExitCode != nil {
		t.Fatal(r)
	}
}
