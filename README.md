# diflett

A read-only, local web dashboard for working-tree changes across multiple Git repositories. The Go binary embeds its browser UI and uses the installed Git executable.

## Run

From this directory, with Go 1.22+ and Git on PATH:

```sh
go build -o diflett .
./diflett -port 8787 -interval 2s /path/to/project-a /path/to/project-b
```

Open http://127.0.0.1:8787 and select a repository. With no paths, diflett watches the current repository if launched inside one, or starts empty so you can add repositories from the dashboard. Paths may point to Git worktrees or subdirectories. Put flags before paths. Use **Add repository** in the sidebar (or from the empty dashboard) and enter an absolute local path to start another watcher immediately. Adding an existing repository selects it. Dashboard additions last for the current server process. To watch those repositories after a restart, include their paths in the launch command.

Choose **Single pane** for a unified patch or **Dual pane** for aligned before-and-after lines. The browser remembers your layout across repositories and reloads.

## Run in the background

On Linux or macOS, from this directory:

```sh
nohup ./diflett -port 8787 /path/to/project-a /path/to/project-b >diflett.log 2>&1 </dev/null &
echo $! >diflett.pid
```

To stop that process:

```sh
kill "$(cat diflett.pid)"
```

The service binds to IPv4 loopback. Each repository is polled independently. The selected repository streams snapshots to the browser using server-sent events and reconnects automatically. Unstaged and staged patches have separate tabs. The untracked tab displays full contents for non-ignored untracked text files with syntax highlighting (capped at 128 KiB per file and 512 KiB total, with a 256 KiB filename list cap). Each patch is capped at 256 KiB per repository per tab. Binary changes use Git’s standard summary.
