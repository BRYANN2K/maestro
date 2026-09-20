# Maestro terminal workspace

The default interface is built with OpenTUI 0.5.11, React 19.2.4, TypeScript
5.9.3 and Bun 1.3.14—the stack used by StackDeploy's cockpit. All interface
copy is English. It runs in a terminal, with no browser or web server.

Build and open it:

```sh
make build
./bin/maestro
```

Keep `maestro`, `maestro-ui`, `maestro-runtime`, and the platform's
`pi_natives.*.node` together when moving the bundle. Bun is needed to build,
not to run the compiled binaries. Stipulate and RLM still require Python 3.11+.

## Workspace

At 136 columns and above, changes sit on the left, the reviewed artifact in
the center, and conversation on the right. Smaller terminals keep the contract
and composer visible; **Ctrl+G** opens the conversation. Below 108 columns,
the changes rail moves out of view; use **Ctrl+K → Switch change** or other
actions in the palette. The minimum
supported working area is 80 columns by 24 rows.

The five phases are **Explore**, **Specify**, **Build**, **Verify**, and
**Deliver**. They show real proposal/specification files, the execution DAG,
recorded verification evidence, and delivery documentation. Changing the
visible phase does not advance the workflow. Approval, starting work, and
recording evidence are explicit operations enforced by the backend.

The specification's approval button opens a confirmation. Its digest binds
approval to the displayed contract; if a file changes in the meantime,
Maestro rejects the stale approval and asks for another review. A worker
returning does not mean its contribution has been accepted.

**Files** shows bounded, read-only source previews. **Changes** shows tracked
Git changes against HEAD. **History** opens saved sessions and explains why an
unavailable workspace cannot be resumed. The earlier editor remains available
through `maestro tui --classic`, using the same Maestro harness.

## Keyboard and mouse

| Control | Action |
| --- | --- |
| Ctrl+K | Open the command palette |
| Ctrl+P | Open Connections |
| Ctrl+L | Choose a model |
| Ctrl+G | Switch between contract and conversation on smaller terminals |
| Tab / Shift+Tab | Move focus |
| Enter | Activate the focused action or send the conversation |
| Shift+Enter / Ctrl+J | Add a newline in the conversation |
| Escape | Close a dialog; cancel an outstanding permission/input prompt |
| Ctrl+C | Cancel active work, close a dialog, or clear an idle composer |
| Ctrl+Q | Cancel work and exit |
| Mouse / wheel | Choose changes and phases, activate actions, scroll panels |
| Click an underlined link | Open its HTTP/HTTPS destination in your browser |

The composer supports pasted multiline text. Use `/help` for the palette,
`/model` for model selection, `/providers` for connections, `/resume` for
history, and `/workflow` to refresh the contract. Other slash commands use
Maestro's existing command parser. Credential commands use Connections so
keys do not enter the conversation history.

## Connections

Connect an account through the built-in provider adapter, add a provider API
key, or enter a custom OpenAI-compatible endpoint. Compatible endpoints use
the backend's model discovery. The model selector lists the backend catalog;
"Model selected" describes selection, not proof of a successful API request.

API keys are masked even inside the native render buffer. The Go host owns
the credential vault, discovery, OAuth, permissions, sessions, and workflow
operations. The UI child receives terminal configuration and a fresh private
connection token, not inherited provider credentials. OAuth instructions and
URLs appear in the connection dialog when the provider needs a browser.
Links in conversation messages, contract text, and login instructions are
clickable, including while authentication is waiting for input. Links also
carry native terminal hyperlink metadata. Dragging text does not open a link.

A newly saved endpoint becomes available in the current session. An open
model selector refreshes while discovery completes. Keyboard focus follows
dialog changes and scrolls connection fields into view on small terminals.

## Implementation and qualification

`internal/cockpit` launches the compiled UI over an authenticated loopback
JSONL channel. Only the UI writes to the interactive terminal. The host
serializes actions, allows cancellation and permission replies during a run,
and terminates the child when the session ends. Go restores terminal state
on exit, including failure paths.

Run `make ui-test` for native renderer interactions, TypeScript checks, host
boundary tests, and the compiled-UI Unix PTY tests. `go test ./... -race` also
covers the existing backend and classic interface. Local model fixtures test
routing without making live provider requests; native Windows terminal
behavior and live OAuth require separate qualification.
