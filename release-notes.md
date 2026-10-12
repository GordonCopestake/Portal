# Portal 1.0.0-rc.6

Build date: 12 October 2026. This adds `--prompt` to send the first prompt at startup, then continue the session.

```cmd
portal.exe --prompt "What is my free RAM and what is using it?"
portal.exe --prompt this is the prompt I want to start with
```

Put other options before `--prompt`. Quote text that contains shell characters or words that start with `-`. Remaining unquoted positional words are joined with spaces. Empty text, text above 64 KiB, and combination with a check option or `--version` fail before startup. Slash text in this option goes to the model and cannot change local session permissions.

The first prompt uses the existing tool approval, cancellation, and statistics code. It does not consume console input before the model reply or tool approval. Further prompts and session commands remain available. EOF still permits the initial model request; tools still require approval in `ask` mode.

The prepared runtime files are in `build/1.0.0-rc.6`:

| File | Bytes | SHA256 |
| --- | ---: | --- |
| `portal.exe` | 10,536,960 | `3C9F6E7159BF52D4B31883EA20437C39879FB54BAED7BA71A1BA471C51237B03` |
| `portal.json` | 1,844 | `52C63249AD14373BEA2E9D44A590DFD371EC0F930A68E29555FD474CDA06E6EE` |

Tests and vet passed. New checks cover quoted and unquoted prompts, equals syntax, Unicode and line breaks, slash text, EOF, further prompts, token totals, approval, denial, exit at approval, and invalid options. The rebuilt executable passed a live unquoted prompt check against Strata at `http://ws-p3:8080/v1`. It returned `PORTAL_PROMPT_OK`, made one model request, ran no tools, and printed one summary with 711 input tokens and 26 output tokens. Help and version also passed.

The config is unchanged from rc.5. This candidate was tested locally; the UNC and fleet limits below still apply. Previous candidates are retained.

## Previous candidate: 1.0.0-rc.5

Build date: 12 October 2026. This adds session commands, statistics, and faster diagnostic feedback.

Session commands are `/help`, `/status`, `/permissions`, `/permissions ask`, `/permissions automatic`, `/new`, and `/exit`. Help, status, permissions, and exit also work at approval prompts. Permission changes affect only the current session. Unknown slash commands stay local. Clearing history retains session totals, permissions, and call IDs.

Normal exit, EOF, and idle Ctrl+C print one summary. Ctrl+C during a request or command stops the turn. Statistics include session duration, turns, model requests and HTTP attempts, errors, tool run attempts, denials, timeouts, cancellation, truncation, and cumulative provider token usage. Input counts include history sent again. Output counts include provider-counted thinking and tool calls. Missing or interrupted usage is not guessed. Reply rate includes network and prompt processing; generation rate uses matching provider timing and covers only those replies.

The console shows readable stdout, stderr, errors, duration, and status. Commands show their limit and report progress every five seconds. The model still receives structured JSON. Forced termination has no command exit code. Exact command groups can be approved once, approved individually, or denied. Group approval cannot apply to a later response.

Portal requests streamed replies with usage. Thinking appears as it arrives. Assistant text and tool arguments remain buffered until the complete response is validated. This preserves correct colours and prevents partial command execution. Ordinary JSON replies remain supported. Interrupted streams are not replayed automatically. Untrusted controls and credentials split across chunks remain protected.

`--check` requires the exact text marker and rejects refusals. `--tool-check` authorises only one fixed local marker command, verifies its result, and requires a final report. Extra or unsupported commands fail. The config prompt now requests few diagnostic calls, measured facts, explicit limits, and routine reports within 150 words and five results. Prompt guidance cannot guarantee model accuracy.

The prepared runtime files are in `build/1.0.0-rc.5`:

| File | Bytes | SHA256 |
| --- | ---: | --- |
| `portal.exe` | 10,534,400 | `A3D2EA12184DF9B17BF734ACAA80DE85E7F6ED9C8120185752F1A5045C844916` |
| `portal.json` | 1,844 | `52C63249AD14373BEA2E9D44A590DFD371EC0F930A68E29555FD474CDA06E6EE` |

Tests and vet passed. Tests cover session commands, local permission changes, statistics through cleared history, exact group scope, default denial, exit and EOF at approval, idle interrupt summaries, missing usage, retry counts, provider rate data, and strict health markers. Streaming tests cover early delivery, fragmented calls, usage-only events, Unicode, split credential redaction, cancellation, missing completion, malformed events, duplicate IDs, and the response cap. Real PowerShell tests confirm that forced exit codes are not reported as command status. Two live Strata exchanges passed, including cleared history.

The user-selected test copy is at `\\ws-gc\temp\Portal candidate 1.0.0-rc.5 dc67bbbd`. Windows reports the share path as `T:\temp`. Direct creation through SMB was denied, so staging used that local backing folder. No share permissions were changed. UNC startup, text inference, and the fixed tool check passed with `C:\Windows` as the working folder. The fixed tool check also passed after the isolated test folder was made read-only and a write probe was denied. Folder permissions were restored. Only executable and config remained.

A live terminal session through that UNC copy passed help, permission changes, status at approval, readable output, streamed grey thinking, orange commands and results, white final answers, retained totals after `/new`, and one summary after `/exit`. A six-second marker command showed the five-second progress update and completed in 6.33 seconds. RAM questions used one inspection command, completed in 1.04 and 1.10 seconds, and the model stated the limits of the snapshots. The final config requested a shorter report. This tests a diagnostic path, not factual accuracy or prompt compliance for all replies.

Idle Ctrl+C after the second RAM test printed one complete summary with 2,002 input tokens, 624 output tokens, 2,626 total tokens, a measured reply rate of 49.3 output tokens/s, and provider generation rate of 97.6 tokens/s. The development PowerShell terminal returned status 1 after the interrupt. Direct operator CMD behaviour remains a fleet check. The final rebuilt executable passed its fixed tool check again through the UNC copy. Protocol errors now specify that no commands from the current reply ran; earlier completed commands can still have changed the workstation.

The UNC tests use the share on the build computer. A separate clean workstation, direct operator CMD cancellation, application policy, signing, and disposable service/network fault tests remain open. Previous release candidates are retained.

## Previous candidate: 1.0.0-rc.4

Build date: 12 October 2026. This adds optional Windows console colour.

Thinking and text before tool calls are grey. Commands, approval requests, and results are orange. Final answers are white. Request and token-use messages are grey. Each coloured section resets the text style. Portal restores the original console mode on exit.

Files, pipes, unsupported consoles, `NO_COLOR=1`, and `TERM=dumb` use plain text. No terminal library or new dependency is required. Untrusted terminal controls are still escaped before display.

The provider's optional `reasoning_content` or `reasoning` field supplies thinking. It is display data only and is not retained or sent back in conversation history. Responses remain non-streaming. Thinking appears after the complete response arrives.

The prepared runtime files are in `build/1.0.0-rc.4`:

| File | Bytes | SHA256 |
| --- | ---: | --- |
| `portal.exe` | 10,477,568 | `29F1FDC859B5DBCFE943DDA637DCE64527F1796D3756B1CE1D90D787219BA6FA` |
| `portal.json` | 824 | `524C061CF19AC403074861091838AFF8E66B3A99F1797388F5BC26AAA6C2C678` |

`build.cmd` passed tests and vet. Tests cover both provider thinking fields, colour selection, style resets, plain output, file output, environment settings, control escaping, credential redaction, and exclusion of thinking from request history. A native Windows console check passed for enabling virtual terminal output and restoring the original mode.

Two live Strata terminal exchanges passed: one with plain output and one with colour. Only the exact harmless marker command was approved. The colour exchange showed grey thinking, an orange command and result, and a white final answer. The rebuilt executable also passed its live text check with redirected output and no colour-disable setting; the captured text contained no terminal controls. The operator's config is unchanged. Previous candidates are retained. Remote fleet release gates remain open.

## Previous candidate: 1.0.0-rc.3

Build date: 12 October 2026. This fixes premature rejection of normal command groups and adds provider context discovery.

Portal reads `/models` under the configured API base. The selected Strata model reports **262,144 context tokens**. Startup shows that value. The agent shows prompt-plus-completion token use when returned by the provider. Model tokens remain provider-counted; JSON byte budgets protect application memory.

For the reported Strata context, the request budget is 16 MiB. Request budgets range from 2 to 32 MiB; responses remain capped at 2 MiB. Command groups reserve result status space, not the theoretical maximum output for every command. Retained output uses its actual encoded size and is marked truncated when necessary. Every call keeps a result and exit status.

The prepared runtime files are in `build/1.0.0-rc.3`:

| File | Bytes | SHA256 |
| --- | ---: | --- |
| `portal.exe` | 10,469,888 | `99A77369EFE6AAFA352AFC33983366FE259DD36DCB8D24826CD89394EBBEBCFA` |
| `portal.json` | 824 | `524C061CF19AC403074861091838AFF8E66B3A99F1797388F5BC26AAA6C2C678` |

Tests and vet passed. New tests cover provider metadata, active versus training-sized limits, aliases, missing or invalid metadata, actual token usage, conversations above the old 2 MiB cap, 20- and 100-command groups, and Unicode/control output shortening. The rebuilt executable reported the correct live context and passed its text check. Two live Strata tool exchanges passed, including cleared history. Only the exact harmless marker command was permitted. The operator's config is unchanged. Remote fleet release gates remain open.

## Previous candidate: 1.0.0-rc.2

Build date: 12 October 2026. This fixes config loading after an editor saves JSON with a UTF-8 byte order mark. The loader accepts UTF-8 with or without the marker. It does not rewrite the file or change the model setting.

The prepared runtime files are in `build/1.0.0-rc.2`:

| File | Bytes | SHA256 |
| --- | ---: | --- |
| `portal.exe` | 10,458,624 | `4C55A3997881FDD4AD1217B55DC1A7D7FB140A94782A2BDB70FE1F5DB991A028` |
| `portal.json` | 824 | `524C061CF19AC403074861091838AFF8E66B3A99F1797388F5BC26AAA6C2C678` |

`build.cmd` passed tests and vet. A regression test covers UTF-8 with and without the marker, Unicode prompt text, the Strata model name, and preservation of the config file. The rebuilt executable passed startup with the actual edited config. The live model text check returned `PORTAL_OK` from the selected Strata model. Tool support for this model was not checked in this fix. Remote fleet release gates from rc.1 remain open.

## Previous candidate: 1.0.0-rc.1

Build date: 11 October 2026. This is a release candidate for fleet validation. It is not signed or approved for all workstations.

## Files

The prepared runtime files are in `build/1.0.0-rc.1`. Put them beside each other in the deployment share:

| File | Bytes | SHA256 |
| --- | ---: | --- |
| `portal.exe` | 10,457,600 | `1F3D71FDDFDFE6DEF3B19DF9B2BD9877B95E7D430771532F721328E81EBA863D` |
| `portal.json` | 785 | `21EDABE1D9A6C2FC3DC85E0044C3DB155A3F72F8274067F60028D840C2D4F704` |

Keep [README.md](README.md) and [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) with the release documentation. They are not runtime dependencies. A config change changes its checksum; record the deployment config separately.

Build inputs: Go 1.27.2, `golang.org/x/sys v0.49.0`, Windows amd64, GOAMD64 v1, CGO disabled, and `-trimpath`. Modules are pinned and `go mod verify` passed. Static PE imports contain only `kernel32.dll`. Runtime Windows APIs also use OS DLLs. No bundled library extraction is used.

Initial inference: `http://ws-p3:8080/v1`, non-secret token `local`, model `Qwen3.8-27B-UD-Q5_K_XL-MTP`. Approval mode is `ask`. Config contains the complete system prompt. There is no startup login or update path.

## Verified

Tests ran on `ws-gc`, Windows kernel 10.0 build 26300, x64, with an ordinary user token. Windows PowerShell 5.1 supplied by the OS runs the commands.

- `build.cmd` passed tests and `go vet ./...` before producing the executable.
- Config tests covered adjacent lookup, overrides, schema, unknown fields, prompt preservation, limits, missing key variables, and safe errors.
- Fake-server tests covered prompt and single-tool schema, correct call IDs, serial execution, invalid batches, denied approval, malformed arguments, unknown tools, empty and incomplete replies, refusals, redirects, untrusted TLS certificates, HTTP 401/403/404/429/500/503, oversized responses, deadlines, and cancellation.
- A deterministic exchange inspected a temporary file, repaired it, and verified it. An HTTP 503 after command completion caused an inference retry without repeated commands.
- Console tests covered cleared history, blank approval denial, EOF at approval, interrupt routing, and bounded history.
- Real PowerShell tests covered multiline and Unicode input, separate streams, exit codes, working folder, removed key variables, new-process state, the 64 KiB input boundary, large output, timeout, cancellation, and descendant cleanup after timeout and normal completion.
- The live endpoint passed two tool exchanges, including cleared history. Only `[Console]::WriteLine('PORTAL_TOOL_OK')` was permitted. The model used returned evidence in its final reply.
- The executable ran through `\\ws-gc\C$\SOURCE\portal\build\UNC agent candidate with spaces\portal.exe`, with `C:\Windows` as the command folder. A write probe was denied. Adjacent config, startup, live text inference, and an approved live PowerShell exchange passed. The folder contained only executable and config after the session. Test-folder inherited permissions were restored.

The UNC check used a loopback administrative share on the build machine. It does not prove a clean remote workstation or the final deployment share. The requested `\\ws-gc\portal` share was absent. No deployment share was created or changed.

## Open release gates

1. Run the candidate on a clean remote workstation from the intended read-only share. Confirm fleet OS versions and account permissions. Test two clients concurrently.
2. Check default file activity with Process Monitor or equivalent developer diagnostics. Source review shows no default application-state writes; Windows and approved tools can write their own data.
3. Verify direct CMD Ctrl+C behaviour on that workstation. Automated routing and real command cancellation pass. The development test terminal closes its parent session after Ctrl+C, after Portal emits a cancelled result and new prompt.
4. Check ordinary and elevated operation, signing requirements, application control, PowerShell language restrictions, and endpoint protection. This candidate is unsigned.
5. Test reversible service, event-log, and network faults on disposable machines. Test share interruption, DNS failure, connection refusal, and use after a period without launches.
6. Validate any cloud endpoint before using it. ChatGPT subscription authentication remains deferred; no subscription credentials were available or reused.

No claim of permanent availability is made. Share access, Windows execution policy, account rights, network access, and the inference server are external requirements. Job Objects stop ordinary descendants; they do not undo completed repairs or contain work started through other system services.

## Release procedure

Keep this candidate in a separate test folder. Use `ask` mode. Complete the open gates and record results before broad deployment. Stop active sessions before replacing deployed files. Keep the previous release. Run a scheduled endpoint check outside Portal so that service and credential problems are found before an incident.
