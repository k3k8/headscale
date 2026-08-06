# Fork maintenance guide

Personal fork of headscale. `origin` = `juanfont/headscale` (upstream),
`fork` = `k3k8/headscale` (ours). Currently based on **v0.29.3**.

## Branch structure

```
main                     ← deployable: upstream + the two branches below
feat/ios-device-hostname ← name iOS nodes after their device model
feat/k3k8-ci             ← workflow selection for the fork
```

Feature branches are rebased onto `main` and merged back with `--no-ff`,
so `main` is the only branch to deploy from.

## What this fork changes

Deliberately small. Everything else was upstreamed or fixed independently
by upstream — see "History" below before adding anything here.

| Area | Files | Why |
| --- | --- | --- |
| iOS naming | `util/apple_devices.go`, `state/state.go`, `db/node.go` | iOS reports `localhost` for every device, so several iPhones become localhost, localhost-1, localhost-2. `Hostinfo.DeviceModel` carries the real identity and upstream only uses it for log redaction. |
| CI | `.github/workflows/` | Upstream-only workflows removed; `k3k8-build.yml` added. |

### The iOS patch in one paragraph

`util.GivenNameFromHostinfo(hostname, hi)` is `dnsname.SanitizeHostname`
except that a hostname carrying no device information is replaced by the
device model. It is applied at all three GivenName derivation sites
(`state.go` registration, `db/node.go` registration, and the MapRequest
hostname update). `isAutoDerivedGivenName` takes the Hostinfo too and is
evaluated *before* Hostinfo is overwritten — an iOS node legitimately has
GivenName `iphone-15-pro` while its Hostname stays `localhost`, and
comparing against `SanitizeHostname` alone misreads that as an admin
rename.

`TestGivenNameFromHostinfoMatchesSanitize` pins the contract that
non-generic hostnames pass through untouched, so upstream changes to
`SanitizeHostname` keep flowing through.

## Tracking upstream

**Check upstream first.** Before writing any patch, look at how upstream
handles the area today. In April 2026 four local patches were written for
problems upstream fixed within days, and one (`autogroup:internet`) was
reinvented three months after upstream had already shipped the fix.

```bash
git fetch origin
git log --oneline main..origin/main            # how far behind?
git diff --stat main...origin/main -- hscontrol/   # did our files move?
```

If the area was restructured, prefer re-applying the patch onto the new
upstream code over rebasing — a rebase against a subsystem rewrite
produces conflicts against functions that no longer exist.

```bash
# 1. Pull upstream into main
git fetch origin
git rebase origin/main

# 2. Replay each feature branch
for b in feat/ios-device-hostname feat/k3k8-ci; do
  git checkout "$b" && git rebase main || break
done

# 3. Merge them back
git checkout main
git merge --no-ff feat/ios-device-hostname
git merge --no-ff feat/k3k8-ci

# 4. Verify (needs the nix shell: servertest wants tscli and tofu)
nix develop --command go test ./hscontrol/...
gofmt -l hscontrol/

# 5. Push
git push fork main feat/ios-device-hostname feat/k3k8-ci
```

For a large jump, resetting `main` to `origin/main` and re-applying is
often faster. Tag first so it stays reversible:

```bash
git tag backup/pre-<version>-follow main
git reset --hard origin/main
```

## Building and deploying

```bash
# A tag triggers the k3k8 Build workflow → ghcr.io/k3k8/headscale:<tag>
git tag v0.29.3-k3k8.1
git push fork refs/tags/v0.29.3-k3k8.1

# On the server
docker pull ghcr.io/k3k8/headscale:v0.29.3-k3k8.1
```

Tag convention: `v{upstream_version}-k3k8.{patch}`.

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

## History

Patches that used to live here and why they are gone. Kept so the same
work is not done twice.

| Patch | Fate |
| --- | --- |
| macOS hostname sanitisation | Upstream `d6dfdc10` (2026-04-17) routed hostname handling through `dnsname.SanitizeHostname`, which produces the same result. Fixes #3188, #2926, #2343, #2762, #2449. |
| `invalid-<random>` replacement avoidance | Same commit removed the `invalid-` fallback entirely. |
| Sequential duplicate naming | Upstream `a2c3ac09` (2026-04-17) added the same `base-1`, `base-2` collision bump in NodeStore. |
| `autogroup:internet` exit node visibility | Upstream `c7a0ca70` (#3212, 2026-04-28) fixed it via `DestsIsTheInternet()` + `IsExitNode()` — cleaner than expanding 48 internet prefixes into the matcher set, and without the side effect of surfacing nodes that advertise public subnet routes. |

Preserved at tags `backup/pre-v0.29-follow` and
`backup/pre-v0.29-autogroup` if the old implementations are ever needed.
