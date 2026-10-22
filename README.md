# Agent Cleaner

Agent Cleaner is a Windows terminal app for reviewing and removing selected AI coding tools and their local data. The UI uses Go and Bubble Tea. It does not open a browser or run a web server.

> **Warning:** This app can permanently uninstall packages and delete files. Review every selected path and warning. Back up data you may need.

## Run

Building from source requires Go 1.26.0 or newer. A compiled `agent-cleaner.exe` runs without Go. No Python or Node.js runtime is required to launch the app.

From the project directory:

```powershell
go mod download
go run .
```

Build a standalone Windows executable:

```powershell
go build -o agent-cleaner.exe .
.\agent-cleaner.exe
```

`start.bat` runs `agent-cleaner.exe` when present; otherwise it runs `go run .`.

## Controls

| Key | Action |
| --- | --- |
| Up/Down or J/K | Move through targets or sessions |
| Space | Select or clear a target |
| Tab / Shift+Tab | Move focus across action controls |
| Enter | Activate the focused action, select a target, or restore the current session |
| PageUp / PageDown | Scroll target details, review warnings, session contents, or results |
| R | Review selected targets and their paths |
| Q in review | Choose quarantine, then type `DELETE` exactly |
| P in review | Choose permanent removal, then type `DELETE PERMANENTLY` exactly |
| T | Restore a quarantine session |
| S | Rescan known locations |
| Q or Ctrl+C / Ctrl+Q on the target screen | Quit |

The terminal needs at least 25 columns and 20 rows. Smaller windows show a resize message.

## What it detects

Detection uses a fixed catalog of paths. It does not search the whole computer.

- **Global npm packages:** Codex CLI, current and legacy Pi, OpenCode, Caveman Code, and 9Router. 9Router is a supporting model router, not an agent.
- **Global Bun package:** OMP Pi, `@oh-my-pi/pi-coding-agent`, under `BUN_INSTALL` or the default `~/.bun` global package root.
- **Executables:** Grok CLI, OMP Pi at `%LOCALAPPDATA%\omp\omp.exe`, and Tiny Agents at `%LOCALAPPDATA%\Programs\Python\Python313\Scripts\tiny-agents.exe`.
- **Configuration and data:** Codex, Claude Code and its local cache, Cursor, Continue, OpenCode, Pi, OMP Pi, selected Grok hook and lock files, and the local Codex runtime.

Custom Bun global directories configured through `bunfig` and OMP binary paths set through `PI_INSTALL_DIR` are not detected. Tiny Agents is detected only at the Python 3.13 path.

## Removal behavior

- **Quarantine** moves selected files and folders into `.agent-cleaner-quarantine` under your user profile. Restore is available if the original path is free. Quarantine does not free disk space, and targets on another volume cannot be moved there. A restore record is written before each move.
- **Permanent removal** deletes selected files and folders. The app cannot restore them, and deletion does not guarantee secure erasure from SSDs, backups, or synced storage.
- **Package removal** is always permanent, regardless of the selected file mode. npm and Bun are needed only when removing their global packages. npm may run package uninstall lifecycle scripts.

Configuration and data folders can contain credentials, sign-in data, custom skills, hooks, plugins, sessions, caches, or work history. Review each item's path and warning before selecting it. Close active agents first. The app reports failures and does not retry with broader paths.

## Safety and privacy

- Each removal rescans the catalog. Only currently detected item IDs can be acted on.
- The TUI accepts selections from the detected catalog, not arbitrary file paths or shell commands.
- The app does not send scan results to a remote service. Go downloads the pinned UI dependencies when building from source; npm and Bun run as separate processes for package removal.
- Agent Cleaner does not remove Orca or unrelated shared caches. The local Codex runtime may be used by other OpenAI apps, so review its warning before removing it.
- The charcoal surface keeps file-path panels readable; amber marks focus and review, red marks destructive actions or errors, and green marks successful operations.

## Checks

```powershell
go test ./...
go vet ./...
go build -o agent-cleaner.exe .
```

GitHub Actions runs these checks on Windows using the Go version from `go.mod`, then uploads `agent-cleaner.exe` as the `agent-cleaner-windows-amd64` artifact.
Download it from **Actions** > a successful run > **Artifacts**. This workflow does not publish a GitHub Release.

## License

Licensed under the Zero-Clause BSD License (0BSD). See [LICENSE](LICENSE).
