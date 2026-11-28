# Agents guide

Glitched is a Go desktop app built on MyGo. It needs no cgo. Only the race
detector does.

## Never run unsharded `go test ./...` locally

The root `package main` holds over 1,200 UI tests. Run whole, it passes Go's
default 10-minute timeout and loads every core of the host. CI splits it
across 8 runners with `./.github/root-shard`, and runs the other packages in
a separate job. Test what you touched, and let CI run the rest.

## Local verification

```sh
CGO_ENABLED=0 go vet ./...
go tool mygo vet .

# Root UI tests that cover the change
CGO_ENABLED=0 go test -count=1 -run '<Pattern>' .

# Each internal package you touched
CGO_ENABLED=0 go test -count=1 ./internal/<pkg>/...

# When you change shared or concurrent state
CGO_ENABLED=1 go test -race -count=1 ./internal/<pkg>/...

# When you change platform or runtime code
GOOS=windows GOARCH=amd64 go build ./cmd/...
```

You rarely need the full root suite locally. When you do, run one shard at a
time:

```sh
RUNNER_TEMP=$(mktemp -d) ./.github/root-shard run <i> 8 2   # i is 1 to 8
```

On a shared or cloud host, such as ferrum, keep parallelism low while other
threads run. Use `go test -p 2` and 2 shard processes. A starved CPU makes
the UI tests miss their frame budgets and fail for no reason in the code.

## Writing tests

- Run a test's `git` through `internal/gittest` (`gittest.Command` or
  `gittest.Env`). It sets `GIT_CONFIG_GLOBAL=/dev/null` and
  `GIT_CONFIG_NOSYSTEM=1`, and gives an identity to commit as. Host settings
  such as `tag.gpgsign`, `commit.gpgsign` or hooks then never change what
  the test's git does.
- A test that checks how real config loads can set a temporary `HOME` with
  its own `.gitconfig` instead. It still sets `GIT_CONFIG_NOSYSTEM=1`.

## Skills

- `.agents/skills/mygo-maintenance`: MyGo UI rules (lifetime, identity,
  input, state, rendering) and UI test practice.
- `.agents/skills/verify-glitched`: build Glitched and drive its real macOS
  window to prove a UI or engine change.
