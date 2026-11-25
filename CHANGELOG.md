# Changelog

All notable changes to this project will be documented in this file.

## [v0.2.0] - 2026-11-25

### Added
- Support launching `diflett` outside a Git repository with no arguments, opening an empty dashboard with an inline **+ Add repository** action.
- Show full code with line numbers and syntax highlighting in the **Untracked** tab for non-ignored text files (capped at 128 KiB per file and 512 KiB total), with safe handling for binary files, symlinks, and empty files.
- Extract version notes from `CHANGELOG.md` into GitHub Releases alongside generated commit notes.

## [v0.1.0] - 2026-11-24

### Added
- Initial `diflett` local Git working-tree diff dashboard (Go backend with embedded browser UI, SSE live updates, single-pane and dual-pane diffs, expandable context lines, and light/dark themes).
- Multi-architecture GitHub Actions release workflow (`linux`, `darwin`, and `windows` on `amd64` and `arm64`) with SHA-256 checksums.
