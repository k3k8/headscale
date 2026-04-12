# Fork maintenance guide

This fork adds iOS device hostname resolution on top of upstream headscale.
`origin` = `juanfont/headscale` (upstream), `fork` = `k3k8/headscale` (ours).

## Branch structure

```
main                              ← our deployable branch (upstream + patches)
feat/apple-device-hostname-lookup ← iOS hostname fix
feat/k3k8-fork-build              ← CI workflow cleanup
```

## Tracking upstream

```bash
# 1. Pull upstream into main
git fetch origin
git rebase origin/main

# 2. Replay each feature branch on top of new main
git checkout feat/apple-device-hostname-lookup
git rebase main

git checkout feat/k3k8-fork-build
git rebase main

# 3. Merge feature branches back into main
git checkout main
git merge --no-ff feat/apple-device-hostname-lookup
git merge --no-ff feat/k3k8-fork-build

# 4. Push
git push fork main feat/apple-device-hostname-lookup feat/k3k8-fork-build
```

> `feat/k3k8-fork-build` only touches `.github/workflows/` so it rarely
> conflicts. `feat/apple-device-hostname-lookup` touches Go sources and may
> need manual conflict resolution if upstream edits the same files.

## Building and deploying

```bash
# Tag triggers the k3k8 Build workflow → ghcr.io/k3k8/headscale:<tag>
git tag v0.28.0-k3k8.3
git push fork refs/tags/v0.28.0-k3k8.3

# Pull and restart on the server
docker pull ghcr.io/k3k8/headscale:v0.28.0-k3k8.3
# (then update compose/systemd to point at new tag and restart)
```

Tag convention: `v{upstream_version}-k3k8.{patch}` e.g. `v0.28.0-k3k8.1`.
Bump the patch number for each new build from the same upstream version.

## Adding more Apple device models

Edit `hscontrol/util/apple_devices.go` — one entry per model identifier.
Sources: <https://www.theiphonewiki.com/wiki/Models>

```go
var appleModelNames = map[string]string{
    "iPhone16,1": "iphone-15-pro",
    "iPhone16,2": "iphone-15-pro-max",  // ← add here
}
```

Commit directly to `feat/apple-device-hostname-lookup`, then follow the
tracking steps above to bring it into main.
