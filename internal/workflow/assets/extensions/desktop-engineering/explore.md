# desktop-engineering — explore

Verify a desktop application across windows, processes, files, permissions, and packaging.

## Decide whether this domain is relevant

Availability in configuration does not activate this domain. During `stip-explore`, select it only when its responsibility affects the change or a decisive unknown. Do not activate the whole catalog. Relevant example: fix a draft lost when a companion process restarts in a macOS application. Out-of-scope example: change a remote API without affecting desktop integration or runtime.

This extension turns native desktop functionality into an installable, isolated, maintainable application: windows, menus, shortcuts, files, processes, permissions, updates, packaging, crashes, and OS integration. Select it during `stip-explore` when touching macOS, Windows, or Linux beyond a web page: Electron/Tauri shells, WinUI, system menus, protocols, filesystems, notifications, or signed distribution. It also applies to embedded web frontends with native capabilities.

It does not apply to APIs without desktop clients, visual-only changes without native impact, or resized web pages. It does not choose Electron, Tauri, WinUI, AppKit/SwiftUI, or signing mechanisms for the project. Electron identifies trust boundaries between web content and the main process, recommending context isolation, sandboxing, CSP, IPC validation, no Node for remote content, and current versions ([Security](https://www.electronjs.org/docs/latest/tutorial/security)). Tauri describes capabilities granting or denying permissions per window/webview, while noting risks from broad scopes, unsafe Rust, or compromised dependencies ([Capabilities](https://v2.tauri.app/security/capabilities/)).

## Recognize and reuse existing work

For a new project, identify the desktop runtime, windows/webviews, IPC channels, permissions, storage, protocols, signing, optional auto-update, packaging scripts, and supported platforms. For an existing project, install the produced artifact, open a local or remote file, inspect effective permissions, disconnect networking, launch with a fresh profile, update, and close during an operation. Classify findings as **established** (reproducible artifact, configuration, or test), **inferred** (intent without evidence), **incomplete** (missing platform or path), **missing** (search found no trace), or **not-applicable** (unused capability). A permission manifest does not prove a window cannot call a dangerous channel; a local build does not establish identity with the signed package.

Microsoft recommends Windows App SDK/WinUI 3 for building, packaging, and deploying modern Windows apps ([Windows apps](https://learn.microsoft.com/en-us/windows/apps/)). Its inclusive-design guidance integrates accessibility into quality from the start ([Designing inclusive software](https://learn.microsoft.com/en-us/windows/apps/design/accessibility/designing-inclusive-software)). Apple describes AppKit's event-driven interface framework and integration with SwiftUI ([AppKit](https://developer.apple.com/documentation/appkit)). Apple also distinguishes Mac App Store and Developer ID distribution, including signing, Gatekeeper, and notarization ([Distributing software on macOS](https://developer.apple.com/macos/distribution/)). These platform-specific references do not replace testing the actual application.

Consult the bootstrap map, then seek only evidence relevant to the change. Classify each finding as **established, inferred, incomplete, missing, or not-applicable**, with evidence or justification. Missing documentation does not mean a practice is absent. Decide whether to reuse, complete, or replace existing work; an already satisfied step may require no further work.

## Scope the contribution to the shared contract

The MVP covers the target OS, launch/closure, an explicitly bounded native capability, an IPC or permission test, an installable package, and crash/recovery verification. Go deeper when opening remote content, executing files, accessing secrets, processing sensitive data, auto-updating, or supporting multiple OSs: review applicable Electron/Tauri rules, sign and compare artifacts, test fresh profiles, denied permissions, downgrade/rollback, and OS accessibility. Absence of an alert is not evidence of security.

Propose only necessary properties and how to verify them, using the candidates in [check.md](check.md). The agent adapts and remaps them to `AC-1`, `AC-2`, etc., unique within the **shared specification**; the engine does not remap domain IDs. Do not create a parallel specification or approval. Each criterion must state the outcome, context, and expected evidence. A new requirement after approval requires revising the contract and obtaining renewed approval.

## Boundaries

`frontend-engineering` covers webview states; `backend-engineering` and `api-integrations` own networking and authentication; `mobile-engineering` shares some installation constraints; `security-engineering` deepens threats. Avoid giving remote content Node or global capabilities, validating a different package from the distributed one, testing only developer profiles, or claiming cross-platform support after one OS. Electron/Tauri checklists are risk frameworks, not certification. Web apps without native processes or permissions do not trigger this extension.

Other domains mentioned here are possible collaborators, never automatically activated dependencies. Cloud-only work does not activate UX/design. Detailed sources are in [sources.md](sources.md); they inform decisions and do not create additional implicit acceptance criteria.
