# Command Code system-prompt delivery

[Issue #6657](https://github.com/OrchestratorInc/agent-orchestrator/issues/6657)
proposes one generated Command Code mod to replace AO's Terminal UI
`SessionStart` context and Chat first-prompt preamble. The required ACP gate
fails on the tested upstream versions. Keep both existing delivery paths.

## Verification

Checked on 14 October 2026 with Node.js 22.23.2 on macOS arm64, against
Command Code's published npm packages 1.79.2 and 1.82.1. AO's implementation was
inspected at `ad65dcf04` on `main`.

The probe supplied a session mod that reads `system.md` once and returns the
sentinel `AO-TEST-9F3A2B` from `appendSystemPrompt`. Separate markers recorded
module import, factory registration, and hook invocation. A Node preload
intercepted HTTP requests before transmission, recording request field names,
message roles, and sentinel locations. It used a placeholder Command Code API
key and returned an intentional HTTP 400 after capturing the model request.
No model request was sent to a provider.

| Version | Invocation | Module imported | Hook called | Sentinel in system message |
| --- | --- | --- | --- | --- |
| 1.79.2 | `cmd --mod <file> -p "Reply OK."` | yes | yes | yes |
| 1.79.2 | `cmd acp --mod <file>` | no | no | no |
| 1.79.2 | `cmd --mod <file> acp` | no | no | no |
| 1.82.1 | `cmd --mod <file> -p "Reply OK."` | yes | yes | yes |
| 1.82.1 | `cmd acp --mod <file>` | no | no | no |
| 1.82.1 | `cmd --mod <file> acp` | no | no | no |

The ACP probes completed `initialize` and `session/new`, then sent one
`session/prompt`. Their captured OpenAI-compatible model requests contained
`system` and `user` messages without the sentinel. The print control captured
the sentinel at `messages[0].content`, with `messages[0].role` equal to
`system`. The intentional capture error ended the turn; it is not evidence of
a successful model response or compaction test.

All invocations also included `--skip-onboarding --no-auto-update --trust`.
Accepting a flag and starting ACP therefore does not establish that ACP applies
the flag. Moving `--mod` before the subcommand did not change its behavior.

## Upstream cause

The published `command-code/dist/cli.mjs` bundles for both tested versions have
the same relevant call path:

1. The root command registers `interactiveModeAction`. That action copies the
   parsed `mod` and `modOption` options into `setModPathsOverride` and
   `setModOptionsOverride`.
2. `createAcpCommand` registers a separate action that calls `runAcp` without
   passing or applying the inherited options. The root action does not run for
   the ACP subcommand. The shared `preAction` hook only logs and starts telemetry.
3. `runAcp` loads the mod host before starting the ACP server. `buildHost` passes
   the still-empty session mod paths to `loadMods` as `extraPaths`.
4. The host is cached. `bindAgentSession` reuses it when creating a session.

ACP does load built-in and user-level mods. The failed channel is specifically
the session `--mod` path. Disabling project discovery is a separate decision:
the loader processes `extraPaths` independently of project discovery.

`session/new` forwards `cwd` and `mcpServers`, without system-prompt or mod-path
metadata. ACP's live config options accept `model` and `effort`. Installing a
global mod or rewriting the user's settings would change the proposed scope.

The upstream fix should apply inherited `--mod` and `--mod-option` values before
ACP's first mod-host load. Do not patch the user's installed CLI from AO.

## Repeating the gate

Use an AO-owned scratch directory and the same generated mod for each surface:

```ts
import { appendFileSync, readFileSync } from "node:fs";

const standing = readFileSync("<absolute scratch path>/system.md", "utf8");
const marker = "<absolute scratch path>/markers.txt";
appendFileSync(marker, "module imported\n");

export default function (cmd) {
  appendFileSync(marker, "factory registered\n");
  cmd.hooks({
    appendSystemPrompt: () => {
      appendFileSync(marker, "hook called\n");
      return standing;
    },
  });
}
```

Put the sentinel in `system.md`. Use a request interceptor that preserves local
`data:` fetches, supplies any startup HTTP responses, and captures the model
request without forwarding it. Record sentinel locations and message roles
without logging credentials or full prompts.

Run the print control, then both ACP flag orders. For ACP, send `initialize`
with protocol version 1, `session/new` with the scratch `cwd` and an empty
`mcpServers` array, and `session/prompt` with the returned session ID and
`[{"type":"text","text":"Reply OK."}]`. Clear markers between processes.
Check module import separately from hook invocation and request placement.

Before replacing AO's delivery paths, a supported Command Code version must
load the session mod on both surfaces and append its instructions to the system
prompt after compaction and native resume. Then cover AO launch, restore,
restart, and agent-switch propagation, including prompt-file write failures and
mod-load diagnostics. Do not remove the ACP preamble based on argument parsing
alone.

Upstream references: [mods and hook contract](https://commandcode.ai/docs/mods),
[ACP transport](https://commandcode.ai/docs/acp), and
[CLI flags](https://commandcode.ai/docs/reference/cli). The docs establish the
intended API; the package inspection and probes establish the behavior above.
