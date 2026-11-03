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

That dumps the database to `~/wera-backups`, checks the file, and deletes
backups older than 29 days. The privacy policy promises that deleted accounts
leave the backups within 30 days, so keep the daily cleanup in cron too:

```bash
30 3 * * * /home/nick/Documents/jesse/wera/deploy/home/backup.sh --prune >> /home/nick/wera-backups/prune.log 2>&1
```

If nothing has been deployed for 29 days, the cleanup takes a fresh backup
before deleting the old one, so there is always a backup and none is older than
the limit. If the limit in the policy changes, change `BACKUP_DAYS` with it.
