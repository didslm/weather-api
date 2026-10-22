# Deployments run entirely on the phone

Public API: **https://weather-api.shtegtimi.com**.

A push to `main` from **any machine** is enough. The phone checks GitHub every
minute, pulls main into a fresh local workspace, runs the tests, builds a native
ARM64 binary, and promotes it after verification. No GitHub Actions, release
assets, build server, GitHub token, public SSH, or developer laptop is involved.
The repository must remain public for credential-free fetches.

## Deployment sequence

1. `weather-update.timer` checks main every 60 seconds after the previous check
   finishes. It holds a phone-side file lock so two deployments cannot overlap.
2. Clone main into a new workspace under `/var/cache/weather-risk-api`, then
   verify its full commit SHA. Source commands run **as ayu**, never as root.
3. On the phone, run `go test ./...`, `go vet ./...`, deployment-manager Python
   tests, and `go build` with the commit SHA embedded. Go build/module caches
   persist between builds. Compilation is limited to two Go threads, a 150% CPU
   quota and 2 GB service memory limit to leave room for the live API.
4. Store the binary, checksum/size/revision manifest, source archive, port, and
   service environment in `/opt/weather-risk-api/releases/FULL_SHA`. The manager
   rechecks main after building so an obsolete commit cannot overtake a new push.
5. Start the candidate as an unprivileged systemd instance on its own loopback
   port. Check `/health`, `/version`, `X-Release`, and a real forecast response.
6. Atomically change the `current` symlink and gracefully reload Nginx. Nginx
   validates the configuration before reloading; old workers finish requests.
7. Verify the local proxy and public hostname, then observe health/revision for
   another 20 seconds. If checks fail, restore the previous route automatically.
8. Keep the last **five successful releases** on disk. Keep current and previous
   processes running for quick rollback; stop older processes gracefully before
   pruning their directories. Live data is stored separately and never pruned.

A failed test/build or candidate is not retried for the same SHA until an explicit
resume or a new commit. Network failures are retried on the next timer. A manual
rollback pauses automatic promotion so the next timer cannot undo the rollback.
An interrupted switch is journaled; proxy startup restores the previous committed
route and starts the required release processes.

Cloudflare's named tunnel `weather-api-phone` points to stable Nginx on
**127.0.0.1:8083**. Workers use separate loopback ports starting at 18082. The AI
server on 8080 and Shtegtimi test site on 8081 remain independent.

## Commands on the phone

SSH: `ayu@172.16.42.1` over USB, or the phone's Tailscale address.

```sh
sudo /usr/local/sbin/weather-deploy status
sudo /usr/local/sbin/weather-deploy rollback                  # previous successful release
sudo /usr/local/sbin/weather-deploy rollback FULL_COMMIT_SHA  # retained successful release
sudo /usr/local/sbin/weather-deploy pause
sudo /usr/local/sbin/weather-deploy resume                   # allow main/retry failed SHA
sudo systemctl start weather-update                          # check main immediately
sudo journalctl -u weather-update -n 80 --no-pager
sudo journalctl -u weather-proxy -n 50 --no-pager
curl -fsS https://weather-api.shtegtimi.com/version
```

Worker logs:

```sh
sudo journalctl -u weather-release@FULL_COMMIT_SHA -n 50 --no-pager
```

`current` and `previous` are symlinks under `/opt/weather-risk-api`. Release identity
is compiled into `/version` and the `X-Release` response header. Forecast response
JSON remains compatible. Candidates warm their own in-memory cache before traffic
switching. Forecast verification logs persist in `/var/lib/weather-risk-api/SHA.jsonl`.
Pre-migration logs remain under `/home/ayu/Projects/weather-risk-api/data`.

## Provisioning or infrastructure changes

Requirements: postmarketOS ARM64, systemd, user `ayu`, Python 3, Go, Git, Nginx,
curl, runuser, Cloudflared, CA certificates, and Internet access. Install Go with
`sudo apk add go` if needed. Transfer this directory and provision:

```sh
sudo sh deploy/phone/install.sh
sudo /usr/local/sbin/weather-deploy update
sudo systemctl enable weather-proxy
```

Before the first bootstrap, `/etc/weather-deploy.json` has an empty public URL so
the candidate can be checked alongside the existing API. Point Cloudflare at
`http://localhost:8083`, verify the public route, then configure:

```json
{"public_url":"https://weather-api.shtegtimi.com","soak_seconds":20}
```

```sh
sudo systemctl enable --now weather-update.timer
```

The root-owned manager, unit templates, and Nginx configuration are infrastructure.
Apply their changes explicitly with `install.sh`; application releases never
execute repository deployment scripts as root. Cloned source, tests, and build
commands run as ayu inside an isolated build workspace. Main is the trust boundary:
anyone able to change it can deploy code with that user's privileges.

Cloudflare's token remains only at `/home/ayu/.config/weather-api/tunnel.token`
(mode 600). The legacy `weather-risk-api.service` on 8082 is disabled after proxy
migration. Keep shared data outside release directories when restoring backups.

## Limits

This avoids the normal deployment stop/start gap. A single phone can still lose
availability through power, network, kernel, proxy, or hardware failure. Checks
catch immediate failures; a later logic bug still requires rollback. The five
release limit applies to successful on-phone deployments, not to log history or
Go's shared build cache. First compilation can take several minutes; later builds
reuse the cache. If a build exceeds its resource/time limits, the live release
continues serving.
