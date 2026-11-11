# Running Wera on a home server

The public site is the dashboard on Vercel, at `wera-jobs.com`. Vercel sends
`/api` through a Cloudflare Tunnel to `wera-api-public` on this machine, so
nothing listens on the router. People on the LAN use `http://<server>:3000` and
do not touch the tunnel or the internet.

```
browser ──> Vercel ──> api.wera-jobs.com ──> wera-tunnel ──> wera-api-public
LAN     ──> :3000 wera-web ──> wera-api
```

## Use a cable

Put the server on Ethernet. On the home server, every long outage came from its
Wi-Fi link and not from the internet provider: the link stops passing traffic,
about 5 minutes later the router drops the machine for inactivity, and it then
takes up to 20 minutes to get back on (11 times in 17 days). All 6 outages that
killed the old quick tunnel began this way, and none of the 12 short drops did. The logs
cannot say whether the USB adapter or the router's radio is at fault; a cable
avoids both. If drops come back, look at the machine before the provider:

```bash
journalctl -k | grep -E 'disassociated|authentication with'
```

Until there is a cable, two things keep the Wi-Fi up. Install them once with
`sudo sh deploy/home/wifi-fix-install.sh`:

- **Wi-Fi power saving off** (`wifi-powersave-off.conf`). With it on, the
  rtl8xxxu adapter goes quiet until the router drops it "for inactivity"
  while every other device stays connected (2026-11-11).
- **A watchdog** (`wifi-watchdog.sh`, run every 2 minutes by
  `wifi-watchdog.timer`). It reconnects when the link is down or cannot
  reach the router, and reloads the adapter's driver when a reconnect did
  not help: a stuck adapter cannot even see the network until it is reset.
  NetworkManager alone can stop trying for hours: after a failed handshake
  it waits for someone to type the Wi-Fi password (2026-11-10, 13.5 hours
  down). Its log: `journalctl -u wifi-watchdog`.

The LAN address comes from DHCP and can change, so use the machine's name:
`http://<hostname>.local:3000`.

## When the network drops

Nothing needs doing. Cloudflare keeps a named tunnel while this machine is away,
so `wera-tunnel` reconnects on the same address when the network returns. Until
then the dashboard still loads from Vercel and says the server is not answering,
and an open tab catches up by itself.

## The tunnel

The tunnel and its address live in Cloudflare, under Networking, Tunnels. Its
public hostname sends `api.wera-jobs.com` to `http://wera-api-public:8080`: plain
HTTP, because Cloudflare handles HTTPS and that last hop stays inside Docker.

`TUNNEL_TOKEN` in `.env` connects `wera-tunnel` to it. To replace the token, use
Refresh token on the tunnel's Overview tab, put the new one in `.env`, then:

```bash
docker compose --profile public up -d --no-deps wera-tunnel
```

Do not run the `cloudflared service install` command Cloudflare shows: that
connector would run outside Docker and could not reach `wera-api-public`.

## Backups

Back up before every deploy, with the release as the label:

```bash
deploy/home/backup.sh v0.13.0
```

That dumps the database to `~/wera-backups/wera-pre-v0.13.0-<time>.sql.gz`,
checks the file, and rotates the backups. A daily backup runs from cron:

```bash
30 3 * * * /home/nick/Documents/jesse/wera/deploy/home/backup.sh --daily >> /home/nick/wera-backups/backup.log 2>&1
```

Rotation keeps every backup from the last 7 days, then the newest of each
week, and deletes everything older than 29 days: the privacy policy promises
that deleted accounts leave the backups within 30 days. If the limit in the
policy changes, change `BACKUP_DAYS` with it. That is about ten backups.

### Offsite copies

A backup on the same disk as the database is lost with it. To copy backups
to an S3-compatible bucket (Backblaze B2 costs about $0.006 per GB a month),
create a bucket and an application key, then write
`~/.config/wera/backup.env` (readable only by you, `chmod 600`):

```bash
BACKUP_S3_URI=s3://wera-backups/home
BACKUP_S3_ENDPOINT=https://s3.us-west-004.backblazeb2.com
AWS_ACCESS_KEY_ID=<key id>
AWS_SECRET_ACCESS_KEY=<application key>
```

Each backup run then mirrors the backup folder to the bucket, deletions
included, so the bucket keeps to the same 29 days.
