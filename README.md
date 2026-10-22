# Agent Cleaner

Agent Cleaner is a Windows terminal app for reviewing and removing selected AI coding tools and their local data. It is built with Go and Bubble Tea, scans a fixed catalog rather than the whole computer, and does not open a browser or run a web server.

> [!WARNING]
> Agent Cleaner can permanently uninstall packages and delete files. Review each selected target, its path, and its warning before continuing. Back up anything you may need.

## Run

Building from source requires Go 1.26.0 or newer. Run from the project directory:

```powershell
go run .
```

Build a standalone executable:

```powershell
go build -o agent-cleaner.exe .
.\agent-cleaner.exe
```

The compiled executable does not require Go. `start.bat` runs `agent-cleaner.exe` if present, otherwise it runs `go run .` and requires Go. Node.js and Python are not needed to launch the app. npm or Bun is needed to remove its corresponding global packages.

## Controls

| Key | Action |
| --- | --- |
| Up/Down or J/K | Move through targets, sessions, or results |
| Space | Select or clear a target |
| Tab / Shift+Tab | Move focus across the main-screen actions |
| Enter | Activate the focused action, select a target, or restore the selected session |
| PageUp / PageDown | Scroll details, review warnings, session contents, or results |
| R | Review selected targets and their paths |
| Q in review | Choose quarantine, then type `DELETE` exactly |
| P in review | Choose permanent removal, then type `DELETE PERMANENTLY` exactly |
| T | Choose a quarantine session to restore |
| S | Rescan known locations |
| Q on the target screen | Quit |
| Ctrl+C / Ctrl+Q | Quit when no operation is running |

The terminal needs at least 25 columns and 20 rows. Smaller windows show a resize message.

## Detection scope

Detection is limited to the catalog below. It does not search the whole computer.

| Type | Targets |
| --- | --- |
| Global npm packages | `@openai/codex`, `@opencode/cli`, `@juliusbrussee/caveman-code`, `@earendil-works/pi-coding-agent`, legacy Pi package `@mariozechner/pi-coding-agent`, and `9router` |
| Global Bun package | OMP Pi, `@oh-my-pi/pi-coding-agent` |
| Executables | Grok at `~\.grok\bin\grok.exe`; OMP Pi at `%LOCALAPPDATA%\omp\omp.exe`; Tiny Agents at `%LOCALAPPDATA%\Programs\Python\Python313\Scripts\tiny-agents.exe` |
| Configuration and data | Codex, Claude Code and its cache, Cursor, Continue, OpenCode, Pi, OMP Pi, selected Grok hooks and lock files, and the local Codex runtime |

For npm packages, `NPM_CONFIG_PREFIX` supplies the global prefix; otherwise Agent Cleaner queries `npm root --global` if npm is on `PATH`. Bun uses `BUN_INSTALL` or the default `~\.bun` root.

The app does not detect custom Bun global directories configured through `bunfig`, OMP binaries installed through `PI_INSTALL_DIR`, or Tiny Agents outside the Python 3.13 Scripts path.

## Removal and recovery

- **Quarantine** moves selected files and folders to `~\.agent-cleaner-quarantine`. Restore is available only while the item is present and its original path is free. Quarantine does not free disk space, and targets on another volume cannot be moved there.
- **Permanent removal** deletes selected files and folders. It cannot be undone by the app and does not guarantee secure erasure from SSDs, backups, or synced storage.
- **Package removal is always permanent**, including when selected alongside files being quarantined. npm or Bun runs as a separate process; npm may run package uninstall lifecycle scripts.

Configuration and data folders may contain credentials, sign-in data, custom skills, hooks, plugins, sessions, caches, or work history. Close active agents first. Review each target's path and warning. The app reports failures and does not retry with broader paths.

## Safety and privacy

- Each review and removal rescans the catalog. Only currently detected item IDs can be acted on; selections cannot supply arbitrary paths or shell commands.
- Scan results are not sent to a remote service. npm may be queried locally to find its global root; npm or Bun is launched separately for package removal.
- The Grok hooks target may include Orca integration state. Agent Cleaner does not target Orca itself, but review that target's warning before selecting it.
- The local Codex runtime may be used by other OpenAI apps. Review its warning before removing it.

## Development

Run the checks and build:

```powershell
go test ./...
go vet ./...
go build -o agent-cleaner.exe .
```

GitHub Actions runs these checks on Windows using the Go version in `go.mod`, then uploads `agent-cleaner.exe` as the `agent-cleaner-windows-amd64` artifact. Download it from **Actions** > a successful run > **Artifacts**. The workflow does not publish a GitHub Release.
