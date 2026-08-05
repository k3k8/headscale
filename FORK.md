# Fork maintenance guide

Personal fork of headscale carrying device-naming and policy fixes.
`origin` = `juanfont/headscale` (upstream), `fork` = `k3k8/headscale` (ours).

## Branch structure

```
main                              ← deployable: upstream + all patches below
feat/apple-device-hostname-lookup ← device naming (iOS/macOS hostnames)
feat/k3k8-fork-build              ← CI workflow cleanup
fix/autogroup-internet            ← autogroup:internet exit node visibility
```

Feature branches are cumulative: each is rebased onto `main` and merged
back with `--no-ff`, so `main` is the only branch to deploy from.

## What this fork changes

| Area | Files | Why |
| --- | --- | --- |
| Device naming | `util/apple_devices.go`, `util/util.go`, `types/node.go` | iOS reports `localhost`; macOS reports names with spaces and `.local`. Upstream renames both to `invalid-<random>`. |
| Duplicate names | `db/node.go`, `state/state.go` | Sequential numeric suffixes instead of random hashes. |
| Policy | `policy/v2/filter.go`, `policy/v2/policy.go` | `autogroup:internet` produced no matchers, so exit nodes were invisible and the grant was a no-op. |
| CI | `.github/workflows/` | Upstream-only workflows removed; `k3k8-build.yml` added. |

## Upstream tests we intentionally changed

**Read this before resolving a rebase conflict in a `_test.go` file.**
These assertions encode upstream behaviour we deliberately diverge from.
If a rebase brings back the upstream version, do not "fix" our code to
match — re-apply our expectation.

### `hscontrol/policy/policy_test.go`

`TestReduceNodesFromPolicy/2788-exit-node-autogroup:internet`

- **Upstream expects**: only `server` visible; the exit node is hidden.
  Comment reads "autogroup:internet does not generate packet filters".
- **We expect**: `server` *and* `exit` visible, `wantMatchers: 1`.
- **Why**: the sibling cases `2788-exit-node-0000-route` and
  `2788-exit-node-::0-route` already expect the exit node to be visible
  for `0.0.0.0/0` and `::0/0`. Writing the same destination as
  `autogroup:internet` should not change which peers a client sees —
  otherwise the client cannot select the exit node at all.
- **Not a security relaxation**: the packet filter delivered to the
  client is unchanged (still empty). Only peer visibility widens, and
  only to nodes advertising routes overlapping public ranges. RFC1918
  subnet routers stay hidden.

### `hscontrol/util/util_test.go`

`TestEnsureHostname` — several cases upstream expects to become
`invalid-<random>` resolve to real names here (iOS `localhost` →
`iphone-15-pro`, macOS `Kota's MacBook Pro` → `kotas-macbook-pro`).

## Tracking upstream

```bash
# 1. Pull upstream into main
git fetch origin
git rebase origin/main

# 2. Replay each feature branch on top of the new main
for b in feat/apple-device-hostname-lookup feat/k3k8-fork-build fix/autogroup-internet; do
  git checkout "$b" && git rebase main || break
done

# 3. Merge them back
git checkout main
git merge --no-ff feat/apple-device-hostname-lookup
git merge --no-ff feat/k3k8-fork-build
git merge --no-ff fix/autogroup-internet

# 4. Verify before pushing
go test ./hscontrol/...
gofmt -l hscontrol/

# 5. Push
git push fork main feat/apple-device-hostname-lookup feat/k3k8-fork-build fix/autogroup-internet
```

Conflict likelihood:

- `feat/k3k8-fork-build` — low, touches only `.github/workflows/`.
- `feat/apple-device-hostname-lookup` — medium, `util.go` and `node.go`
  are actively developed upstream.
- `fix/autogroup-internet` — high, `policy/v2/` changes frequently and
  we alter a shared function signature (`compileFilterRules`,
  `destinationsToNetPortRange`, `compileFilterRulesForNode` all take a
  `forMatchers bool`). Upstream adding a call site will not compile
  until the flag is threaded through.

## Building and deploying

```bash
# A tag triggers the k3k8 Build workflow → ghcr.io/k3k8/headscale:<tag>
git tag v0.28.0-k3k8.7
git push fork refs/tags/v0.28.0-k3k8.7

# On the server
docker pull ghcr.io/k3k8/headscale:v0.28.0-k3k8.7
# (update compose/systemd to the new tag and restart)
```

Tag convention: `v{upstream_version}-k3k8.{patch}`. Bump the patch for
each build from the same upstream version.

> Push tags **individually** (`git push fork refs/tags/<tag>`), not with
> `--tags`. GitHub does not fire workflows when many tags arrive at once,
> which is why the first fork push produced no build.

## Adding more Apple device models

Edit `hscontrol/util/apple_devices.go` — one entry per model identifier.

```go
var appleModelNames = map[string]string{
    "iPhone16,1": "iphone-15-pro",
    "iPhone18,5": "iphone-18",  // ← add here
}
```

Sources: <https://theapplewiki.com/wiki/Models>, <https://appledb.dev>

Commit to `feat/apple-device-hostname-lookup`, then follow the tracking
steps above to bring it into `main`.
