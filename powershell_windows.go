package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const maxCommandBytes = 64 * 1024
const maxOutputBytes = 64 * 1024

type toolResult struct {
	ExitCode   *uint32 `json:"exit_code"`
	Stdout     string  `json:"stdout"`
	Stderr     string  `json:"stderr"`
	Error      string  `json:"error,omitempty"`
	TimedOut   bool    `json:"timed_out"`
	Cancelled  bool    `json:"cancelled"`
	Truncated  bool    `json:"truncated"`
	DurationMS int64   `json:"duration_ms"`
}

type commandOutput struct {
	mu             sync.Mutex
	stdout, stderr bytes.Buffer
	remaining      int
	truncated      bool
}

func (o *commandOutput) drain(reader io.Reader, stderr bool) {
	buf := make([]byte, 4096)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			o.mu.Lock()
			keep := min(n, o.remaining)
			if stderr {
				o.stderr.Write(buf[:keep])
			} else {
				o.stdout.Write(buf[:keep])
			}
			o.remaining -= keep
			o.truncated = o.truncated || keep < n
			o.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func powershellPath() (string, error) {
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "WindowsPowerShell", "v1.0", "powershell.exe")
	if _, err := os.Stat(path); err != nil {
		return "", errors.New("Windows PowerShell is not available")
	}
	return path, nil
}

func childEnvironment(keyEnv string) []uint16 {
	var env []string
	for _, value := range os.Environ() {
		name, _, _ := strings.Cut(value, "=")
		name = strings.ToUpper(name)
		if name == strings.ToUpper(keyEnv) && keyEnv != "" {
			continue
		}
		switch name {
		case "OPENAI_API_KEY", "CODEX_API_KEY", "ANTHROPIC_API_KEY", "PORTAL_API_KEY":
			continue
		}
		env = append(env, value)
	}
	sort.Slice(env, func(i, j int) bool { return strings.ToUpper(env[i]) < strings.ToUpper(env[j]) })
	var block []uint16
	for _, entry := range env {
		data, _ := windows.UTF16FromString(entry)
		block = append(block, data...)
	}
	block = append(block, 0)
	if len(block) == 1 {
		block = append(block, 0)
	}
	return block
}

func runPowerShell(parent context.Context, c config, cwd, command string) (result toolResult) {
	started := time.Now()
	defer func() { result.DurationMS = time.Since(started).Milliseconds() }()
	if strings.TrimSpace(command) == "" || len(command) > maxCommandBytes || strings.ContainsRune(command, 0) {
		result.Error = "command must contain 1 to 65536 bytes and no NUL character"
		return result
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(c.CommandTimeoutSeconds)*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		result.Cancelled = true
		result.Error = "command was not started"
		return result
	}
	path, err := powershellPath()
	if err != nil {
		result.Error = err.Error()
		return result
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		result.Error = "cannot create a command Job Object"
		return result
	}
	defer windows.CloseHandle(job)
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		result.Error = "cannot set command containment; command was not started"
		return result
	}
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		result.Error = "cannot create command input pipe"
		return result
	}
	defer stdinR.Close()
	defer stdinW.Close()
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		result.Error = "cannot create command output pipe"
		return result
	}
	defer stdoutR.Close()
	defer stdoutW.Close()
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		result.Error = "cannot create command error pipe"
		return result
	}
	defer stderrR.Close()
	defer stderrW.Close()
	stdHandles := []windows.Handle{windows.Handle(stdinR.Fd()), windows.Handle(stdoutW.Fd()), windows.Handle(stderrW.Fd())}
	for _, handle := range stdHandles {
		if err := windows.SetHandleInformation(handle, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
			result.Error = "cannot prepare command pipes"
			return result
		}
	}
	attrs, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		result.Error = "cannot prepare process containment"
		return result
	}
	defer attrs.Delete()
	// Assign the job during CreateProcess, before any command code can run.
	const jobListAttribute = 0x0002000d
	if err := attrs.Update(jobListAttribute, unsafe.Pointer(&job), unsafe.Sizeof(job)); err != nil {
		result.Error = "Windows does not support command containment at creation"
		return result
	}
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&stdHandles[0]), uintptr(len(stdHandles))*unsafe.Sizeof(stdHandles[0])); err != nil {
		result.Error = "cannot restrict inherited command handles"
		return result
	}
	startup := windows.StartupInfoEx{}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	startup.Flags = windows.STARTF_USESTDHANDLES
	startup.StdInput, startup.StdOutput, startup.StdErr = stdHandles[0], stdHandles[1], stdHandles[2]
	startup.ProcThreadAttributeList = attrs.List()
	app, _ := windows.UTF16PtrFromString(path)
	cmdline, _ := windows.UTF16PtrFromString(windows.ComposeCommandLine([]string{path, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "-"}))
	directory, err := windows.UTF16PtrFromString(cwd)
	if err != nil {
		result.Error = "invalid working folder"
		return result
	}
	env := childEnvironment(c.APIKeyEnv)
	var process windows.ProcessInformation
	if err := windows.CreateProcess(app, cmdline, nil, nil, true,
		windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_NO_WINDOW,
		&env[0], directory, &startup.StartupInfo, &process); err != nil {
		result.Error = fmt.Sprintf("cannot start contained PowerShell (Windows error: %v)", err)
		return result
	}
	defer windows.CloseHandle(process.Process)
	windows.CloseHandle(process.Thread)
	stdinR.Close()
	stdoutW.Close()
	stderrW.Close()
	capture := &commandOutput{remaining: maxOutputBytes}
	var drains sync.WaitGroup
	drains.Add(2)
	go func() { defer drains.Done(); capture.drain(stdoutR, false) }()
	go func() { defer drains.Done(); capture.drain(stderrR, true) }()
	encoded := base64.StdEncoding.EncodeToString([]byte(command))
	// ASCII wrapper input avoids PowerShell 5.1 stdin code-page differences.
	wrapper := "[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding $false; $OutputEncoding = [Console]::OutputEncoding; $ErrorActionPreference = 'Stop'; $global:LASTEXITCODE = 0; try { & ([ScriptBlock]::Create([Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('" + encoded + "')))); if (-not $?) { exit 1 }; exit $global:LASTEXITCODE } catch { [Console]::Error.WriteLine($_.ToString()); exit 1 }\r\n"
	written := make(chan error, 1)
	go func() { _, err := io.WriteString(stdinW, wrapper); stdinW.Close(); written <- err }()
	completed := false
	for {
		state, waitErr := windows.WaitForSingleObject(process.Process, 50)
		if waitErr != nil {
			result.Error = "cannot wait for the command; outcome is unknown"
			break
		}
		if state == windows.WAIT_OBJECT_0 {
			completed = true
			break
		}
		if ctx.Err() != nil {
			result.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
			result.Cancelled = !result.TimedOut
			result.Error = "command interrupted; changes already made are not undone"
			break
		}
	}
	// Also stop ordinary descendants that outlive a successful parent command.
	if err := windows.TerminateJobObject(job, 1); err != nil && result.Error == "" {
		result.Error = "cannot stop command descendants; check for remaining processes"
	}
	state, waitErr := windows.WaitForSingleObject(process.Process, 2000)
	if waitErr == nil && state == windows.WAIT_OBJECT_0 {
		var code uint32
		if completed && windows.GetExitCodeProcess(process.Process, &code) == nil {
			result.ExitCode = &code
		}
	} else {
		result.Error = "command termination is not confirmed; outcome is unknown"
	}
	done := make(chan struct{})
	go func() { drains.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		stdoutR.Close()
		stderrR.Close()
		result.Error = "output pipe did not close; output may be incomplete"
	}
	stdinW.Close()
	select {
	case err := <-written:
		if err != nil && result.Error == "" {
			result.Error = "command input was interrupted; outcome is unknown"
		}
	case <-time.After(time.Second):
		result.Error = "command input pipe did not close; outcome is unknown"
	}
	capture.mu.Lock()
	result.Stdout = strings.ToValidUTF8(capture.stdout.String(), "\uFFFD")
	result.Stderr = strings.ToValidUTF8(capture.stderr.String(), "\uFFFD")
	result.Truncated = capture.truncated
	capture.mu.Unlock()
	return result
}
