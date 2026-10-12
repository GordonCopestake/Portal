# Portal

Portal is a Windows x64 CLI agent for local fault diagnosis and repair. Run it directly from an SMB share. No install, runtime extraction, update check, or model login is part of startup. The model has one tool: `powershell_call`.

Version `1.0.0-rc.6` is a release candidate. It adds `--prompt` to start a session with a supplied prompt. A clean remote workstation and fleet application-control policy still need validation. See [release-notes.md](release-notes.md).

## Build

Use Go 1.27.2 on the build machine. `golang.org/x/sys v0.49.0` is pinned in `go.mod` and `go.sum`. Run `build.cmd` from CMD. It runs tests and vet, then builds `portal.exe` with CGO disabled. If `.tools/go` contains the portable compiler, the script uses it. Build caches stay in `.tools`. Dependency downloads happen only during development builds. Go is not needed on client machines.

## Run

Put `portal.exe` and `portal.json` in the same share folder. From CMD on a workstation, run:

```cmd
"\\ws-gc\portal\portal.exe"
```

The process and PowerShell commands run on that workstation, with your account rights. The share supplies only the program and config. Give operators read and execute access. Restrict write access to the release maintainer.

The requested `\\ws-gc\portal` share was not present during development. Create or select the deployment share before using this example. No share was changed by the build.

To send the first prompt at startup, run:

```cmd
portal.exe --prompt "What is my free RAM and what is using it?"
portal.exe --prompt this is the prompt I want to start with
"\\ws-gc\portal\portal.exe" --cwd "C:\Windows" --prompt "Diagnose this workstation"
```

Portal sends this text to the model once, then waits for further prompts. Tool permissions and session statistics apply as usual. Put other options before `--prompt`. Quote the text to preserve spaces and shell characters, or when words start with `-`. Unquoted words left after option parsing are joined with spaces. The text must not be empty or exceed 64 KiB. Do not combine `--prompt` with a check option or `--version`. Slash text supplied through `--prompt` is model input; it is not a session command.

Describe the fault at the prompt. In the supplied `ask` mode, inspect each full command and enter `y` or `yes` to run it. All other answers, including an empty answer, deny execution. For a command group, inspect all displayed commands. Enter `a` to approve that exact group, `i` to approve each command separately, or `n` to deny the group. An empty answer denies the group. Group approval does not apply to a later model response.

Use these commands inside a session:

| Command | Result |
| --- | --- |
| `/help` | List session commands |
| `/status` | Show current session statistics |
| `/permissions` | Show the tool permission mode |
| `/permissions ask` | Ask before tool execution |
| `/permissions automatic` | Run pending and future tools without approval |
| `/new` | Clear the conversation; keep statistics, permissions, and call IDs |
| `/exit` | End the session and print its summary |

Permission changes apply only to the current session. They do not change `portal.json`. `/help`, `/status`, `/permissions`, and `/exit` also work at a tool approval prompt. Use `/new` only at the main prompt. Unknown slash commands are handled locally, not sent to the model.

`/exit`, EOF, and Ctrl+C at an idle prompt print one session summary. Ctrl+C during a request or command stops that turn and returns to the prompt. The summary appears when the session ends. Forced process termination cannot print a summary.

Statistics include elapsed session time, turns, model requests and HTTP attempts, errors, tool run attempts, denials, timeouts, cancellations, output truncation, and token totals. Sent tokens are provider prompt tokens and include history sent again. Received tokens are provider completion tokens and can include thinking and tool calls. Used tokens are their sum. Missing usage is not estimated. Failed or interrupted requests can have unreported usage.

The reply rate uses received tokens divided by the time taken by replies with reported usage. It includes network and prompt processing. The generation rate uses matching provider `predicted_n` and `predicted_ms` values when available. It applies only to the replies with those timing fields. Neither rate includes operator approval time or PowerShell run time.

In a supported Windows console, thinking and text before tool calls are grey. Commands, approval requests, and tool results are orange. Final answers are white. Request and token-use messages are grey. Portal uses plain text when output is redirected or console colour is unavailable. Set `NO_COLOR=1` to disable colour. The console mode is restored on exit.

Thinking is shown only when the provider sends `reasoning_content` or `reasoning`. Portal requests a stream with usage and shows thinking as it arrives. It does not request hidden thinking or send it back in conversation history. Assistant reply text and tool arguments stay buffered until the complete reply is validated. This keeps final answers white and text before tool calls grey. A provider that returns ordinary JSON instead of a stream is also accepted. Interrupted or invalid streams do not execute commands and are not replayed automatically.

These options are available:

```cmd
portal.exe --help
portal.exe --version
portal.exe --startup-check
portal.exe --check
portal.exe --tool-check
portal.exe --config "C:\settings\portal.json" --cwd "C:\Users\operator"
portal.exe --prompt "Diagnose this workstation"
```

`--startup-check` checks local config, identity, and the PowerShell path without inference. `--check` requires the model to reply with `PORTAL_OK`, with tools disabled. A refusal or different answer fails. It does not prove tool support. `--tool-check` permits only one exact command, `[Console]::WriteLine('PORTAL_TOOL_OK')`, verifies its output and status, and requires a final evidence report. It runs without an approval prompt because the option authorises that fixed check. An extra or different command fails. Use only one check option at a time. Help and version work without config.

Relative config overrides use the caller's folder. The default command folder is the caller's local folder. If it is a network folder or mapped drive, Portal selects the local profile folder. Use `--cwd` to override it. Each call starts in that folder, in a new PowerShell process.

## Config

The supplied config uses `http://ws-p3:8080/v1`, key `local`, and model `Strata-Qwen3.8-Flash-Next-Abliterated-IQ3_XXS-LowRAM-Dual-256K`. Keep the system prompt in this file. Portal sends it as configured and adds a separate labelled system message with workstation identity and command folder. Restart after config changes. Schema version 1 is required. Unknown fields, invalid limits, and missing values are rejected. Save the file as UTF-8; a leading UTF-8 byte order mark is accepted.

`base_url` is the API base; Portal appends `/chat/completions`. The server and model must support OpenAI-compatible function calls and tool result messages. The model name must match the server. There is no automatic model or provider switch.

`api_key_env` takes priority over `api_key`. A configured variable must exist and contain a value. Without either setting, authentication is omitted. Real keys require HTTPS and valid certificates. The literal `local` is the only nonempty token allowed over HTTP. It is a non-secret compatibility token. HTTP sends diagnostic data in plaintext; use it only on the intended LAN.

Do not commit real credentials. Use an environment variable, or put a private config in `portal.local.json` and start with `--config portal.local.json`. Git ignores this file, `.env` files, and private key files. The committed `portal.json` contains only the non-secret `local` token.

For an authenticated HTTPS endpoint, change these fields and keep the system prompt:

```json
"base_url": "https://your-gateway.example/v1",
"model": "your-tool-capable-model",
"api_key_env": "PORTAL_API_KEY",
"api_key": ""
```

Set `PORTAL_API_KEY` in the launching environment, or use a protected config key. A config key is plaintext. Do not commit real keys. PowerShell has your account rights and can read files those rights permit. Environment filtering is not a security sandbox.

Defaults: approval mode `ask`, request timeout 120 seconds, command timeout 60 seconds, and 20 tool calls per operator turn. `automatic` runs commands without approval and shows a startup warning. Use `ask` for fleet tests. The model cannot change host approval or limits.

The supplied system prompt requests a fast initial snapshot, few diagnostic calls, explicit units, and a distinction between facts, possible causes, and missing evidence. Routine reports should stay within 150 words and five results unless the operator requests detail. It prohibits unsupported conclusions from one memory snapshot. These instructions guide the model; they do not guarantee factual accuracy or define tool permissions.

ChatGPT subscription authentication is deferred. This build does not reuse Codex tokens or refresh OAuth credentials. A future subscription integration needs a tested access and recovery method.

## Command behaviour and limits

Portal runs OS Windows PowerShell 5.1 with no profile or interactive prompts. It sends an ASCII wrapper and a UTF-8 encoded command through stdin. It creates no temporary script and does not bypass PowerShell policy. Commands run serially.

The wrapper makes PowerShell errors terminating and returns exit code 1 for them. Otherwise it returns the final native `$LASTEXITCODE`, or zero if there was no native command. An explicit `exit` supplies its own status. Check earlier native failures explicitly if a script runs several native commands.

Each result includes exit code when known, stdout, stderr, error, timeout, cancellation, and truncation. Combined stdout and stderr retain 64 KiB. Discarded output is drained. Invalid UTF-8 bytes are replaced. A truncated result cannot prove omitted output was successful.

The console shows stdout and stderr as separate readable text blocks, with duration and status. The model receives structured JSON, including `duration_ms`. A running command reports its time limit and progress every five seconds. A forced timeout or cancellation has no command exit code; its outcome can be unknown. Completed changes are not undone.

At startup, Portal requests `/models` under the configured API base. It reads the selected model's `context_length`, `context_window`, `max_model_len`, or `meta.n_ctx`. It uses the lowest positive reported value when fields differ. Model IDs and the provider's listed aliases are supported. Your server reports 262,144 tokens for Strata. Portal shows the limit and, when supplied, token use from the last response's `usage` fields. Token use includes prompt and completion tokens. It is not an estimate based on text length.

Model token limits and application byte limits are separate. The provider counts and enforces model tokens. Portal permits a bounded request body of 64 bytes per reported context token, with a minimum of 2 MiB and maximum of 32 MiB. For Strata this is 16 MiB of JSON, not a 16 MiB model context. A metadata failure takes at most three seconds, or the configured request deadline if shorter. Without usable metadata, Portal reports an unknown token limit and keeps the 2 MiB application limit. It does not guess from the model name.

Other limits: config 256 KiB, system prompt 128 KiB, console line and command 64 KiB, HTTP response 2 MiB. Before a command group, Portal reserves only status space for each result and 64 KiB for the next reply. After each command, it fits retained output to the actual remaining byte budget. Further output shortening is marked `truncated`; exit codes and status remain available for every call ID. Use `/new` when the real application limit is reached. Call-ID history remains after `/new`; there is a 4096-ID process limit. Restart when that limit is reached.

Inference has at most three attempts within its request deadline. Only transport failures and HTTP 429, 500, 502, 503, and 504 are retried. Redirects and failed certificates are rejected. Transport retries never repeat PowerShell commands. A model can propose the same command with a new ID; inspect its purpose before approval.

PowerShell enters a Windows Job Object during process creation. Portal stops ordinary descendants on timeout, cancellation, and parent completion. Do not launch background applications with it. Service operations, scheduled work, and processes created through other services can outlive the tool. This is not a sandbox and does not undo completed changes.

History stays in memory. Portal writes no default logs, sessions, caches, or keys. Windows, security products, PowerShell, and approved repairs can write their own data.

## Recovery and maintenance

- A share failure before launch prevents startup. Restore SMB access or use a prepared alternate share. There is no offline client copy.
- A missing config or key variable stops startup. Correct it and restart.
- Network and model failures return to the prompt. Check DNS, inference service, and model. Restart with another config if needed.
- HTTP 401 or 403 needs a valid credential or server access rule. Portal does not open a browser.
- Use an elevated CMD for repairs that need administrator rights. Portal does not collect passwords or self-elevate.
- Cancellation can leave a completed change. Inspect the result before another repair.

No product can guarantee operation through every workstation, network, or server fault. Check `--check` through an external maintenance schedule. Check inference capacity before an incident. Test new builds and models in a separate share folder. Stop sessions before replacing deployed files. Keep the previous release for recovery.

## Test limits

Normal tests use a local fake server and temporary test folders. They do not call the LAN model. For the explicit live test, run from the repository:

```cmd
set PORTAL_LIVE_TEST=1
go test -run TestLiveEndpoint -v -timeout 270s
```

This test permits only `[Console]::WriteLine('PORTAL_TOOL_OK')`. It checks two tool exchanges, including a cleared conversation. Include [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) with release documentation. It is not a runtime dependency.

See [roadmap.md](roadmap.md) for evidence and open checks, and [agent.md](agent.md) for the product contract.
