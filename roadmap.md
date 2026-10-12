# Portal agent roadmap

Status: `1.0.0-rc.6` adds `--prompt` for the first session prompt. It retains the session commands, statistics, approval controls, and streamed thinking from rc.5. Tests, vet, and a live startup prompt check passed. Fleet validation remains open. See [release-notes.md](release-notes.md) for release evidence.

The product contract is in [agent.md](agent.md). The user has specified that the adjacent config contains the system prompt and that the model has exactly one tool: a PowerShell call.

## Intended result

Run `\\ws-gc\portal\portal.exe` from CMD on a workstation. Read `portal.json` beside the executable. Prompt the agent to diagnose and repair that workstation. Keep the conversation in memory and use the configured inference endpoint.

Required deployment files:

```text
portal.exe
portal.json
```

No installer, copied program, extracted runtime, package download, local application cache, startup update, or browser sign-in is part of normal operation. Windows and approved tools can still create OS data or repair files.

## Decisions

| Area | Version 1 decision | Reason |
| --- | --- | --- |
| Language | Go, Windows x64, CGO disabled | Produce one native executable without a separate runtime |
| Reuse | Small custom agent; Pi and OpenCode are references only | One protocol and one tool do not need a framework |
| Config | Adjacent `portal.json`, with explicit override | Keep endpoint and prompt with the deployed program |
| System prompt | Required config string | Operator can change agent instructions without a build |
| Model API | OpenAI-compatible Chat Completions with function tools | Works with suitable cloud and LAN services after testing |
| Tool | `powershell_call(command)` only | Windows PowerShell can inspect files, services, events, registry, and network state |
| Shell | OS Windows PowerShell 5.1 | No PowerShell 7 installation required |
| Interface | Text console with optional colour and streamed thinking | Keep plain output available without a terminal library |
| State | Memory only | No local session store, SQLite, cache, or shared-state conflicts |
| Authentication | Optional config key or environment variable | No OAuth or interactive login dependency |
| Execution policy | Config chooses `ask` or `automatic` | Host controls execution; model does not choose permissions |
| Cancellation | Windows Job Object and bounded pipe waits | Stop commands and descendants without leaving the console blocked |
| Recovery | Clear errors; restart with another config | Keep endpoint switching explicit and small |

Go is the proposed implementation choice, not a completed portability proof. If Phase 1 finds a real blocker, record it before changing language. Native AOT C# is an alternative. Do not use a self-extracting package to pass the one-executable requirement.

## Phase 1: prove UNC execution and config discovery

- [x] Create the smallest Go CLI with `--help`, `--version`, `--config`, and `--cwd`.
- [x] Add `--prompt` with quoted and unquoted text. Send it once, keep tool approval, continue the session, and include it in session totals.
- [x] Find `portal.json` through the running executable's directory, not the working folder.
- [x] Validate the proposed config from `agent.md`, including its required system prompt.
- [x] Show local computer identity, current rights, endpoint, model, working folder, and execution mode without secrets.
- [x] Build with CGO disabled. Confirm that the release needs no non-OS DLL or runtime extraction. Build metadata confirms CGO is disabled. Static PE imports contain only `kernel32.dll`; platform inspection loads OS DLLs. Startup code does not extract files.
- [ ] Start the binary from a real read-only SMB share on a clean target machine. Use a different local working folder and a path containing spaces.
- [ ] Observe file and process activity with developer diagnostics, such as Process Monitor. Confirm no program copy, extracted files, app state, or hidden package download. Do not require the diagnostics tool on normal workstations.

Acceptance: `--help` and config startup work through a UNC path with no model connection, no writes to the executable folder, and no local application files. Missing or invalid config gives a clear error. Record the tested OS, architecture, share setup, account rights, and build version.

The user later authorised complete production implementation. Local UNC evidence permits development to continue. The clean remote-machine and file-activity checks remain release gates.

## Phase 2: connect to the inference endpoint

- [x] Implement the one HTTP request format with the standard library.
- [x] Send the configured system prompt and separately labelled workstation context.
- [x] Support no authentication, an environment key, or a config key as specified in `agent.md`.
- [x] Enforce TLS verification, bounded bodies, time limits, safe redirect handling, and secret-free errors. Reject all redirects.
- [x] Add `--check`: validate config, inspect local prerequisites, and make a harmless model request with tools disabled. Report that this check does not prove tool compatibility.
- [x] Test endpoint path, missing key, invalid JSON, empty and incomplete responses, authentication errors, limits, and slow responses with a fake server.
- [x] Test `http://ws-p3:8080/v1`, token `local`, model `Qwen3.8-27B-UD-Q5_K_XL-MTP`.
- [ ] Test a cloud endpoint when credentials are available.
- [ ] Inject DNS and connection-refusal failures on the fleet test workstation.

Acceptance: the agent produces a model reply from the configured endpoint. The config prompt reaches the server. The program returns within its deadline for each tested failure. Authentication errors never open a browser or trigger a download.

## Phase 3: implement the single PowerShell tool

- [x] Declare `powershell_call` with a required command string and no other model tools.
- [x] Validate tool names and JSON arguments before process creation.
- [x] Invoke OS PowerShell with no profile, no interactive input, explicit working folder, and command input through stdin.
- [x] Test multiline commands, quotes, backticks, dollar signs, accents, Chinese text, and emoji. No temporary scripts or policy bypass.
- [x] Apply approval mode. Show full commands and return explicit denials.
- [x] Capture separate streams and status. Test PowerShell errors and native exit code 7.
- [x] Drain output after the combined 64 KiB cap. Test 200,000 output bytes.
- [x] Implement timeout, interrupt routing, descendant termination, and bounded pipe closure. Assign the Job Object during process creation. Real PowerShell timeout, cancellation, and descendant cleanup tests pass.
- [x] Remove known inference key variables from child environments; test configured-variable removal.
- [x] Test commands with an ordinary user token on the build machine.
- [ ] Test commands with an administrator token and under fleet application policy.
- [ ] Verify Ctrl+C returns to a usable prompt in a direct operator CMD session. The test terminal closes its host session after Ctrl+C; Portal emits a cancelled result and a new prompt before that closure.

Acceptance: one tool call runs on the client workstation and returns usable results. A denied, invalid, timed-out, or cancelled call gives an explicit result. A command that spawns a child is stopped within a bounded time. No timeout or transport retry repeats a repair command.

## Phase 4: complete the interactive tool-call loop

- [x] Keep history in memory; implement `/new`, `/exit`, EOF, and cancellation.
- [x] Preserve assistant calls, IDs, and results in message order.
- [x] Run multiple calls serially.
- [x] Handle text, calls, malformed arguments, unknown tools, duplicate IDs, refusal, and interruption.
- [x] Validate the entire call-ID batch before execution. Return argument and tool-name errors without invented exit status.
- [x] Enforce tool count, bounded JSON requests and responses, actual result-size fitting, and a 4096-ID session cap. Reject oversized prompts before adding them to history. Tests cover 20 and 100 calls, full outputs, and output truncation near the byte limit.
- [x] Read provider context metadata and show actual reported token use. Test ID and alias matching, missing metadata, conflicting runtime limits, and large conversations. Keep memory budgets separate from model token limits.
- [x] Retry inference within its deadline. A test repairs and verifies a temporary file, receives HTTP 503, then completes without repeated commands.
- [x] Read explicit command approval separately from prompts. Test an empty answer as denial.
- [x] Add grey provider thinking and interim text, orange commands and results, and white final answers. Test plain output, `NO_COLOR`, control escaping, credential redaction, and console-mode restoration. Keep provider thinking out of request history. A live Windows terminal exchange confirmed the colours.
- [x] Complete deterministic inspection, repair, and verification with a fake server and a disposable test folder.
- [x] Add `/help`, `/status`, and session-only `/permissions ask|automatic`. Keep cumulative counters through `/new`. Print one summary on exit, EOF, and idle interrupt. Tests verify token totals, missing usage, retry counts, permission changes, and approval commands.
- [x] Show readable stdout, stderr, status, duration, and five-second command progress. Keep structured results for the model. Do not report forced termination codes as command exit codes.
- [x] Approve an exact displayed command group once. Test that approval cannot apply to a later response. Retain individual approval and default denial.
- [x] Stream thinking while buffering assistant text and assembling tool calls. Tests cover early thinking, split credential redaction, Unicode, usage, malformed and interrupted streams, duplicate IDs, output caps, and prevention of partial execution.
- [x] Require the expected text marker for `--check`. Add `--tool-check` with only one fixed marker command. Test refusals, wrong text, missing calls, extra calls, and unsupported commands.
- [x] Update the config prompt for short diagnostic paths and reports supported by evidence. Keep runtime policy in host code.
- [x] Validate session commands, progress, totals through `/new`, and `/exit` and idle Ctrl+C summaries in a live Windows terminal through `\\ws-gc\temp`. A RAM question used one inspection call instead of six. The share maps to `T:\temp`; staging used that local path. The isolated test folder passed a denied write probe and a read-only tool check. Its permissions were restored. A separate remote workstation remains untested.

Acceptance: an operator prompt produces a local inspection, an approved change, a separate verification command, and a final report. `/new` clears session history. Provider or tool failure returns control to the prompt without corrupting the remaining conversation.

## Phase 5: test real faults and recovery

Use disposable Windows test machines. Do not create faults on production workstations to test this agent.

- [x] Test the configured LAN model's tool selection and use of evidence. Two live exchanges pass, including cleared history.
- [ ] Test the configured cloud model through the same protocol where available. Compatibility must include tool calls, not only text chat.
- [ ] Diagnose an event-log error, a failed test service, and a network configuration problem. Use reversible test faults and verify each repair independently.
- [ ] Test standard-user access failures and an elevated run. Make privilege errors clear without collecting administrator passwords.
- [ ] Test a missing share at launch and share interruption during a session. Document the result; do not assume the OS has loaded all executable pages.
- [ ] Test DNS failure, refused connection, TLS failure, 401/403, 429 with Retry-After, 5xx, a slow response, oversized response, and lost connection after a tool ran.
- [x] Test the agent from a read-only program folder with spaces on the build machine. A live approved tool exchange passed; only the two runtime files remained.
- [ ] Test two remote workstations concurrently. Confirm no shared application state or credential-file writes.
- [ ] Test use after a period without launches. Confirm startup has no client token refresh, version check, or forced upgrade path.
- [ ] Inspect default file activity again with the complete agent. Attribute child-command and OS writes separately from application writes.

Acceptance: record the exact client build, OS versions, endpoint, model, and each test result. No application claim of permanent availability is permitted. Failed checks remain open until fixed or recorded as a clear release limit.

## Phase 6: package a maintainable release

- [x] Pin Go 1.27.2 and `golang.org/x/sys v0.49.0`. Include third-party notices.
- [x] Provide `build.cmd`; run tests and vet before the build.
- [x] Produce the release candidate and config with only the non-secret `local` token. Record version and hashes in release notes.
- [x] Document launch, config, prompt, approval, elevation, errors, and endpoint replacement.
- [x] Supply a LAN config and HTTPS key-variable example.
- [ ] Test a config made for the previous release. Reject unsupported schema versions clearly; do not rewrite config silently.
- [ ] Test a candidate in a separate share folder. Stop relevant sessions before replacing a deployed executable. Keep the previous release for recovery.
- [ ] Assess code signing and application-control approval against the actual fleet. A blocked executable must be resolved through deployment policy, not a runtime bypass.
- [x] Document an external maintenance check, staged updates, and retention of the previous release.

Acceptance: distribute only the two required runtime files, with documentation and release evidence kept separately. A clean workstation can complete the agreed repair scenario through UNC execution. Failed availability checks have documented recovery steps.

## Release test matrix

This is the final release matrix. Local release-candidate evidence does not replace the clean remote-workstation gate.

| Check | Required result |
| --- | --- |
| UNC executable and adjacent config | Launch from a real share, with no local program copy |
| Read-only deployment share | Normal operation without writes to that share |
| No extra runtime | Clean target without Go, Node, Python, WSL, Git, or a separately installed .NET runtime |
| No default app files | No extracted support files, cache, logs, sessions, or saved keys |
| Prompt in config | Prompt received by the endpoint; change works after restart |
| One model tool | Schema exposes only `powershell_call` |
| Local execution | Identity and effects belong to the client workstation |
| Tool protocol | Correct call IDs, serial execution, and complete results |
| Retry after command completion | Completed command is not executed again |
| Command error | Distinct exit status, output, and error state |
| Large output | Bounded memory, drained pipes, visible truncation |
| Timeout and Ctrl+C | PowerShell and descendants stop; prompt becomes usable |
| Approval refusal | No process starts; denial is returned |
| Automatic mode | Commands run without per-call approval, with visible active mode |
| Standard-user run | Useful inspection and clear privilege failures |
| Concurrent clients | No shared session store or auth-file changes |
| Bad endpoint or credential | Bounded failure with no login or update flow |
| Model context limit | Clear stop; no silent loss of instructions |
| Repair verification | Separate observed check supports the final report |

## Deferred work

Add these only for an observed need:

- Streaming assistant answer text and improved terminal editing. Thinking is already streamed; answer text stays buffered for correct colour and validation.
- A separate native build for Windows ARM64.
- Another inference protocol, including Responses, if the chosen service requires it.
- Explicit session export to an operator-selected path; no default persistence.
- Supported subscription access through a gateway or provider integration. Define credential ownership and recovery before implementation.
- A selected backup endpoint and explicit operator switching within the console.

No plugins, MCP, additional tools, desktop UI, remote command server, persistent PowerShell session, agent database, automatic updater, or bundled local model is planned for version 1.

## Evidence and limits

Maintain this table as implementation proceeds. Do not mark a phase complete on the basis of a successful compilation alone.

| Item | Current evidence |
| --- | --- |
| Repository | Full CLI, model client, single PowerShell tool, tests, config, and operator docs now exist |
| Design | Go, adjacent config prompt, one PowerShell tool, memory-only state |
| Build | `build.cmd` passed: `go test ./...`, `go vet ./...`, and a CGO-disabled Windows x64 build with Go 1.27.2 |
| Config checks | Tests passed for executable-relative paths, UNC paths, explicit overrides, schema, unknown fields, prompt preservation, limits, missing key variables, and secret-free errors |
| UNC execution | Passed on 11 October 2026 through `\\ws-gc\C$\SOURCE\portal\portal.exe`, with `C:\Windows` as the caller's folder. Config was read from the UNC executable directory. Help and version also passed through UNC |
| Read-only and spaces | Passed through `\\ws-gc\C$\SOURCE\portal\build\UNC startup with spaces\portal.exe`. A write probe was denied. The folder contained only the staged executable and config after startup. Test-folder inherited permissions were restored |
| Windows versions | Build machine only: Windows kernel 10.0, build 26300, x64; ordinary user token, not elevated. No clean remote workstation has been tested |
| Default file activity | Source has no default application-state writes or extraction code. Commands use pipes, not temporary scripts. Full Process Monitor verification remains pending |
| Requested share | `\\ws-gc\portal` was not present. No share was created or changed; an existing administrative share was used for the local UNC test |
| Candidate checksum | See [release-notes.md](release-notes.md); the startup prototype is superseded |
| Model endpoint | `ws-p3:8080`: text and live tool exchanges passed with the initial Qwen model and selected Strata model. Strata context metadata reports 262,144 tokens |
| Repair and cancellation | Temporary-file inspection, repair, verification, HTTP retry, real PowerShell timeout, cancellation, and descendant cleanup passed |
| Full UNC candidate | Local loopback via C$; a read-only folder with spaces passed startup, live text inference, and an approved live PowerShell exchange. Remote client validation is pending |

## Technical references

These references support the design. They do not replace tests of the final executable.

- [Go build command](https://pkg.go.dev/cmd/go): Windows executable output and build controls.
- [Go process execution](https://pkg.go.dev/os/exec): direct process invocation, streams, and cancellation facilities. Whole-process-tree cancellation needs Windows handling in addition to the base command context.
- [Windows Job Objects](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects): child-process control.
- [Windows PowerShell command-line options](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_powershell_exe?view=powershell-5.1): profile, input, and noninteractive execution.
- [Ollama OpenAI compatibility](https://docs.ollama.com/api/openai-compatibility): supported subset of the protocol, including tools. A selected model still needs a live tool test.
- [Chat Completions API](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create): messages and function tools.
- [.NET single-file deployment](https://learn.microsoft.com/en-us/dotnet/core/deploying/single-file/overview): some single-file publishing modes extract native libraries. A single distribution file alone does not prove the required behaviour.

## Next task

Validate the candidate on a clean remote workstation from the intended read-only share. Check application policy, ordinary and elevated use, direct CMD cancellation, and file activity. Test reversible service and network faults on disposable machines. Keep these release gates open until they pass. Subscription auth remains deferred.
