// Cloudflare Email Worker: hands each message sent to a user's forwarding
// address (u-<token>@INBOUND_EMAIL_DOMAIN) to Wera's signed webhook, and
// bounces what Wera does not know. The message is not kept here.
//
// Settings (wrangler.toml [vars] and secrets):
//   WERA_URL               https://api.wera-jobs.com/api/inbound-email
//   INBOUND_EMAIL_SECRET   the same secret as the API's (wrangler secret put)

const MAX_BYTES = 1 << 20; // the API reads no more

async function sign(secret, text, body) {
  const key = await crypto.subtle.importKey('raw', new TextEncoder().encode(secret), { name: 'HMAC', hash: 'SHA-256' }, false, ['sign']);
  const head = new TextEncoder().encode(text);
  const all = new Uint8Array(head.length + body.length);
  all.set(head);
  all.set(body, head.length);
  const mac = new Uint8Array(await crypto.subtle.sign('HMAC', key, all));
  return [...mac].map((b) => b.toString(16).padStart(2, '0')).join('');
}

async function readRaw(stream) {
  const reader = stream.getReader();
  const parts = [];
  let size = 0;
  while (size < MAX_BYTES) {
    const { done, value } = await reader.read();
    if (done) break;
    parts.push(value);
    size += value.length;
  }
  reader.cancel().catch(() => {});
  const out = new Uint8Array(Math.min(size, MAX_BYTES));
  let at = 0;
  for (const p of parts) {
    const take = Math.min(p.length, out.length - at);
    out.set(p.subarray(0, take), at);
    at += take;
    if (at >= out.length) break;
  }
  return out;
}

export default {
  async email(message, env) {
    const rcpt = message.to.toLowerCase();
    const body = await readRaw(message.raw);
    const ts = Math.floor(Date.now() / 1000).toString();
    const sig = await sign(env.INBOUND_EMAIL_SECRET, `${ts}.${rcpt}.`, body);
    const res = await fetch(env.WERA_URL, {
      method: 'POST',
      headers: { 'Content-Type': 'message/rfc822', 'X-Wera-Rcpt': rcpt, 'X-Wera-Timestamp': ts, 'X-Wera-Signature': sig },
      body,
    });
    if (res.status === 404) {
      message.setReject('Unknown address');
      return;
    }
    if (!res.ok && res.status >= 500) {
      // Thrown: Cloudflare tells the sender delivery failed for now.
      throw new Error(`Wera answered ${res.status}`);
    }
  },
};
