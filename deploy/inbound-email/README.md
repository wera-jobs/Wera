# Forwarded recruiter replies (#201)

Users forward recruiter replies (by hand, or with a Gmail or Outlook rule)
to their own address, `u-<token>@in.wera-jobs.com`. Cloudflare Email
Routing hands each message to this worker, which posts it to Wera's signed
webhook (`POST /api/inbound-email`). Wera matches the reply to a tracked
job and saves a suggestion the user approves; it keeps only the match, the
sender's domain and the subject, never the body.

## Set up

1. Pick a secret and give it to the API and the worker:
   - `.env`: `INBOUND_EMAIL_DOMAIN=in.wera-jobs.com` and `INBOUND_EMAIL_SECRET=<secret>`,
     then restart `wera-api` and `wera-api-public`.
   - `npx wrangler secret put INBOUND_EMAIL_SECRET` (same value).
2. Deploy the worker: `npx wrangler deploy` in this folder.
3. In Cloudflare, wera-jobs.com, Email, Email Routing: enable it for the
   subdomain `in.wera-jobs.com` (Cloudflare adds the MX and SPF records), then
   under Routing rules set the catch-all for that subdomain to "Send to a
   Worker", `wera-inbound-email`.
4. Check: Settings in the dashboard, Email replies, Turn on, then send a mail to
   the address shown. "Last email" updates within a minute.

## Notes

- An unknown or retired address is bounced (the API answers 404).
- The API refuses a request more than 5 minutes old or with a bad signature.
- Gmail sends a confirmation to the address before it forwards; the
  dashboard shows its code and link so the user can finish setup.
