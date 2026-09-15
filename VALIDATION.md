# Initial validation

## Executed locally

- Go 1.26.2 on macOS arm64; Swift 6.2.3 compiling in Swift 5 language mode.
- `go test -race ./...` passed.
- `go vet ./...` passed.
- Parser fuzzing passed over 1.2 million generated inputs; meter package statement coverage was 81.4%.
- Native `.app` compiled and ad-hoc signed successfully.
- `codesign --verify --deep --strict dist/Burn.app` passed.
- CLI JSON smoke checks passed for Codex models, daily and project reports, and Claude-only quota filtering.
- Read-only live scan across active/archived Codex and Claude source directories.
- At the validation snapshot: 19 discovered files; 13 Codex sessions reconciled; 2 inferred accounting resets; zero unresolved counter regressions; zero malformed records; zero missing timestamps. One quota-only record had no token totals, which is expected. Counts can grow while Codex is running.
- Newer quota snapshots correctly removed a secondary window left over from an older plan.

No original conversation text, raw usage logs, credentials, or real-data fixtures are included in the repository.

## Still requires manual testing

The native UI could not be launched through the desktop automation tool because the Mac was locked. Compilation and engine behavior are verified; visual appearance, menu-bar placement, refresh interaction, sleep/wake behavior, and quit lifecycle need a hands-on smoke test.

Intel is supported by the build script but was not run on Intel hardware. Cloud/remote completeness and invoice matching are explicitly outside the local-log tracker contract.
