# Phone releases

The public API is **https://weather-api.shtegtimi.com**. Cloudflare's named tunnel
`weather-api-phone` connects to the stable Nginx listener on **127.0.0.1:8083**.
Application releases listen on separate loopback ports starting at 18082. The AI
server on 8080 and Shtegtimi test site on 8081 are independent.

## Automatic deployment

1. A push to `main` runs `.github/workflows/phone-release.yml`: Go race tests,
   vet, deployment-manager tests, and an ARM64 build with the exact commit SHA.
2. Only successful main builds publish a GitHub release tagged `phone-<full SHA>`
   with `server-linux-arm64` and its size/checksum/revision manifest. Pull requests
   test and build, but never publish. GitHub Actions serializes runs for main.
3. `weather-update.timer` checks main every 60 seconds after the previous check
   finishes. The phone uses outbound HTTPS only; no GitHub token, public SSH,
   inbound webhook, or self-hosted runner is required. This delivery mechanism
   assumes the repository and build assets remain public.
4. The root-owned deployment manager downloads only the build matching current
   main, verifies its manifest and ARM64 format, and checks main again before
   promotion. Each immutable release lives in `/opt/weather-risk-api/releases/SHA`.
5. A systemd instance starts the candidate **as ayu**. It must pass `/health`,
   `/version`, matching `X-Release` headers, and a live forecast schema check.
6. An atomic `current` symlink selects Nginx's upstream configuration. Nginx
   validates before a graceful reload; existing workers can finish requests.
   The manager verifies the local proxy and public hostname, then checks release
   identity and health for a further 20 seconds before accepting the release.
7. Failed promotion restores the previous route and stops the candidate. The
   failed SHA is not retried automatically until a new commit or explicit resume.
   Failed downloads are retried; unavailable builds leave the live release alone.
8. The last **five successful releases** remain on disk, including protected
   current/previous targets. Current and previous processes stay running. Other
   processes are stopped gracefully before their directories are pruned.

The release manager holds a phone-side file lock. An interrupted switch leaves a
journal; before proxy startup, recovery restores the last committed route and
starts current/previous processes. Automatic deployment does not update the
root-owned manager or systemd/proxy configuration; apply infrastructure changes
explicitly with `install.sh`.

## Operations

SSH target: `ayu@172.16.42.1` over USB (or the phone's Tailscale address).

```sh
sudo weather-deploy status
sudo weather-deploy rollback                 # previous working release; pauses auto deployment
sudo weather-deploy rollback FULL_COMMIT_SHA # one of the retained successful releases
sudo weather-deploy pause
sudo weather-deploy resume                   # clear pause/failed SHA; next timer may deploy main
sudo systemctl start weather-update          # check main now
sudo journalctl -u weather-update -n 80 --no-pager
sudo journalctl -u weather-proxy -n 50 --no-pager
curl -fsS https://weather-api.shtegtimi.com/version
```

Manual rollback pauses automatic promotion so the next timer cannot immediately
undo the rollback. To retry a rejected main commit, run `resume` and start
`weather-update`. To inspect an individual worker:

```sh
sudo systemctl status weather-release@FULL_COMMIT_SHA
sudo journalctl -u weather-release@FULL_COMMIT_SHA -n 50 --no-pager
```

Release identity is compiled into the binary, exposed by `/version` and the
`X-Release` response header. Forecast JSON remains compatible. Each release keeps
its own in-memory cache; a real forecast warms the candidate before promotion.
Forecast verification logs persist under `/var/lib/weather-risk-api/SHA.jsonl`,
outside pruned release directories. Older pre-migration logs remain under
`/home/ayu/Projects/weather-risk-api/data`.

## Provision or restore the phone

Requirements: postmarketOS ARM64, systemd, user `ayu`, Python 3, Git, Nginx, curl,
Cloudflared, CA certificates, and Internet access. Transfer this directory and run:

```sh
sudo sh deploy/phone/install.sh
```

For the first release, produce a binary with the exact source commit SHA:

```sh
make test
python3 -m unittest discover -s deploy/phone -p 'test_*.py' -v
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath \
  -ldflags="-s -w -X main.buildRevision=$(git rev-parse HEAD)" \
  -o /tmp/server-linux-arm64 .
```

Generate the same manifest as the workflow, transfer the binary and manifest, then
bootstrap **before changing the live tunnel route**:

```sh
sudo weather-deploy install FULL_COMMIT_SHA /path/to/server-linux-arm64 /path/to/manifest.json
curl -fsS http://127.0.0.1:8083/version
sudo systemctl enable weather-proxy
```

Point the Cloudflare published application at `http://localhost:8083`, verify
HTTPS `/health`, `/version`, and a forecast, then set `/etc/weather-deploy.json`:

```json
{"public_url":"https://weather-api.shtegtimi.com","soak_seconds":20}
```

Enable the updater with `sudo systemctl enable --now weather-update.timer`.
The tunnel token remains at `/home/ayu/.config/weather-api/tunnel.token` (mode
600); it does not belong in this repository. The legacy `weather-risk-api.service`
on port 8082 can be disabled after the public proxy route is verified.

## Design limits

This prevents the normal stop/start deployment gap; it does not make a single
phone immune to power, network, kernel, proxy, or hardware failure. Post-promotion
checks detect immediate failures; a later logic bug still requires rollback.
GitHub main and its workflow/release permissions are the deployment trust boundary.
The checksum detects incomplete/corrupt transfers; it is not independent artifact
attestation. Only protected main should publish phone releases. No extra SSH or
GitHub credential is installed on the phone.
