"""Draws the charts in docs/run-schedule.md (light and dark SVG).

usage: python3 scripts/run-schedule/charts.py posted.txt docs/run-schedule days.json

posted.txt holds one posting time per line, "YYYY-MM-DD HH:MM" in
America/Chicago, for postings whose board gives a time of day (those at
exactly midnight UTC give only a date and are left out):

  SELECT to_char(posted_at AT TIME ZONE 'America/Chicago', 'YYYY-MM-DD HH24:MI')
  FROM jobs WHERE source <> 'manual' AND posted_at >= :from AND posted_at < :to
    AND posted_at <> date_trunc('day', posted_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC';

days.json is every posting in the window counted by weekday, Monday first,
as a JSON array of seven numbers. Run the queries on a restored backup,
never on the live database.
"""
import datetime as dt
import json
import os
import sys

ts = [dt.datetime.strptime(l.strip(), '%Y-%m-%d %H:%M') for l in open(sys.argv[1]) if l.strip()]
out = sys.argv[2]
os.makedirs(out, exist_ok=True)

THEMES = {
    'light': dict(surface='#fcfcfb', ink='#0b0b0b', ink2='#52514e', muted='#898781', grid='#e1e0d9', axis='#c3c2b7',
                  s=['#2a78d6', '#eb6834', '#1baf7a'], mark='#52514e'),
    'dark': dict(surface='#1a1a19', ink='#f0efec', ink2='#c3c2b7', muted='#898781', grid='#2c2c2a', axis='#383835',
                 s=['#3987e5', '#d95926', '#199e70'], mark='#c3c2b7'),
}
FONT = 'system-ui, -apple-system, Segoe UI, sans-serif'
W, H = 720, 340
L, R, T, B = 56, 24, 64, 48


def svg(theme, title, sub, body, h=H):
    c = THEMES[theme]
    return (f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{h}" viewBox="0 0 {W} {h}" '
            f'font-family="{FONT}" role="img" aria-label="{title}">'
            f'<rect width="{W}" height="{h}" rx="8" fill="{c["surface"]}"/>'
            f'<text x="{L}" y="26" font-size="15" font-weight="600" fill="{c["ink"]}">{title}</text>'
            f'<text x="{L}" y="45" font-size="12" fill="{c["ink2"]}">{sub}</text>{body}</svg>')


def col(x, y0, y1, w, fill):
    """A column with a 4px rounded top, square at the baseline."""
    r = min(4, (y0 - y1) / 2, w / 2)
    if y0 - y1 < 0.5:
        return ''
    return (f'<path d="M{x:.1f},{y0:.1f} V{y1 + r:.1f} Q{x:.1f},{y1:.1f} {x + r:.1f},{y1:.1f} '
            f'H{x + w - r:.1f} Q{x + w:.1f},{y1:.1f} {x + w:.1f},{y1 + r:.1f} V{y0:.1f} Z" fill="{fill}"/>')


def yaxis(c, ymax, step, fmt, pw):
    s = ''
    v = 0
    while v <= ymax + 1e-9:
        y = T + (H - T - B) * (1 - v / ymax)
        s += f'<line x1="{L}" x2="{L + pw}" y1="{y:.1f}" y2="{y:.1f}" stroke="{c["grid"] if v else c["axis"]}" stroke-width="1"/>'
        s += f'<text x="{L - 8}" y="{y + 4:.1f}" font-size="11" fill="{c["muted"]}" text-anchor="end">{fmt(v)}</text>'
        v += step
    return s


# 1. Weekday postings by hour (share of all weekday postings).
weekday = [t for t in ts if t.weekday() < 5]
hours = [0] * 24
for t in weekday:
    hours[t.hour] += 1
share = [h / len(weekday) * 100 for h in hours]
data = {'hour_share': [round(x, 1) for x in share]}


def hour_chart(theme):
    c = THEMES[theme]
    pw = W - L - R
    ymax = 12
    band = pw / 24
    w = min(24, band - 2)
    body = yaxis(c, ymax, 3, lambda v: f'{v:.0f}%', pw)
    for i, v in enumerate(share):
        x = L + i * band + (band - w) / 2
        body += col(x, H - B, T + (H - T - B) * (1 - v / ymax), w, c['s'][0])
        if i % 3 == 0:
            label = '12a' if i == 0 else f'{i}a' if i < 12 else '12p' if i == 12 else f'{i - 12}p'
            body += f'<text x="{L + i * band + band / 2:.1f}" y="{H - B + 16}" font-size="11" fill="{c["muted"]}" text-anchor="middle">{label}</text>'
    for h, m, name in [(8, 0, '8:00'), (12, 0, '12:00'), (17, 0, '17:00')]:
        x = L + (h + m / 60) * band
        body += f'<line x1="{x:.1f}" x2="{x:.1f}" y1="{T - 4}" y2="{H - B}" stroke="{c["mark"]}" stroke-width="1.5"/>'
        body += f'<text x="{x + 4:.1f}" y="{T + 6}" font-size="11" fill="{c["ink2"]}">run {name}</text>'
    return svg(theme, 'When weekday jobs are posted',
               f'Share of {len(weekday):,} weekday postings by hour, Chicago time. Lines mark the runs.', body)


# 2. Postings by day of week.
days = json.load(open(sys.argv[3]))  # Mon..Sun, every posting incl. date-only ones
data['day_counts'] = days
NAMES = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun']


def day_chart(theme):
    c = THEMES[theme]
    pw = W - L - R
    ymax = 150000
    band = pw / 7
    w = 24
    body = yaxis(c, ymax, 50000, lambda v: f'{v / 1000:.0f}K' if v else '0', pw)
    for i, v in enumerate(days):
        x = L + i * band + (band - w) / 2
        y = T + (H - T - B) * (1 - v / ymax)
        body += col(x, H - B, y, w, c['s'][0])
        body += f'<text x="{x + w / 2:.1f}" y="{y - 6:.1f}" font-size="11" fill="{c["ink2"]}" text-anchor="middle">{v / 1000:.0f}K</text>'
        body += f'<text x="{x + w / 2:.1f}" y="{H - B + 16}" font-size="11" fill="{c["muted"]}" text-anchor="middle">{NAMES[i]}</text>'
    wk, n = days[5] + days[6], sum(days)
    return svg(theme, 'Jobs posted by day of the week',
               f'{n:,} postings, 7 Sep to 1 Nov 2026. Saturday and Sunday together: {wk / n:.1%}.', body)


# 3. How fast a new job reaches Wera under each schedule.
SCHED = [
    ('8:00, 12:00, 17:00 weekdays (now)', [(8, 0), (12, 0), (17, 0)], range(5)),
    ('11:00, 16:30 weekdays (Nov 3)', [(11, 0), (16, 30)], range(5)),
    ('Every 2 h, 9:00 to 19:00, daily (before)', [(h, 0) for h in range(9, 20, 2)], range(7)),
]


def waits(runs, days_ok):
    runs = sorted(runs)
    res = []
    for t in ts:
        d = t
        for k in range(8):
            day = (t + dt.timedelta(days=k)).date()
            if day.weekday() not in days_ok:
                continue
            cand = [dt.datetime.combine(day, dt.time(h, m)) for h, m in runs]
            cand = [x for x in cand if x >= t]
            if cand:
                res.append((cand[0] - t).total_seconds() / 3600)
                break
    return sorted(res)


curves = []
table = []
for name, runs, dd in SCHED:
    w_ = waits(runs, set(dd))
    n = len(w_)
    pct = lambda h: sum(x <= h for x in w_) / n * 100
    curves.append([pct(h / 2) for h in range(0, 49)])
    table.append(dict(schedule=name, median=round(w_[n // 2], 1), mean=round(sum(w_) / n, 1),
                      within4=round(pct(4)), within12=round(pct(12)), within24=round(pct(24))))
data['schedules'] = table


LEGEND = [('8:00, 12:00, 17:00', 'weekdays (now)'), ('11:00, 16:30', 'weekdays (Nov 3)'), ('Every 2 h, 9 to 19', 'every day (before)')]


def wait_chart(theme):
    c = THEMES[theme]
    pw = W - L - R - 120  # room for end labels
    body = yaxis(c, 100, 25, lambda v: f'{v:.0f}%', pw)
    for hh in range(0, 25, 4):
        x = L + pw * hh / 24
        body += f'<text x="{x:.1f}" y="{H - B + 16}" font-size="11" fill="{c["muted"]}" text-anchor="middle">{hh} h</text>'
    body += f'<text x="{L + pw / 2:.1f}" y="{H - 10}" font-size="11" fill="{c["muted"]}" text-anchor="middle">Hours after the job is posted</text>'
    ends = []
    for i, cv in enumerate(curves):
        pts = ' '.join(f'{L + pw * k / 48:.1f},{T + (H - T - B) * (1 - v / 100):.1f}' for k, v in enumerate(cv))
        body += f'<polyline points="{pts}" fill="none" stroke="{c["s"][i]}" stroke-width="2" stroke-linejoin="round" stroke-linecap="round"/>'
        y4 = T + (H - T - B) * (1 - cv[8] / 100)
        x4 = L + pw * 8 / 48
        body += f'<circle cx="{x4:.1f}" cy="{y4:.1f}" r="4" fill="{c["s"][i]}" stroke="{c["surface"]}" stroke-width="2"/>'
        ends.append((y4, x4, f'{cv[8]:.0f}%', i))
    # Value labels at the 4-hour mark, right of each dot.
    for y4, x4, txt, i in ends:
        body += f'<text x="{x4 + 8:.1f}" y="{y4 + 4:.1f}" font-size="11" fill="{c["ink2"]}">{txt}</text>'
    x4 = L + pw * 8 / 48
    body += f'<line x1="{x4:.1f}" x2="{x4:.1f}" y1="{T}" y2="{H - B}" stroke="{c["grid"]}" stroke-width="1"/>'
    # Legend, right column.
    lx = L + pw + 16
    for i, (name, _, _) in enumerate(SCHED):
        y = T + 10 + i * 46
        body += f'<line x1="{lx}" x2="{lx + 14}" y1="{y}" y2="{y}" stroke="{c["s"][i]}" stroke-width="2" stroke-linecap="round"/>'
        head, rest = LEGEND[i]
        body += f'<text x="{lx + 20}" y="{y + 4}" font-size="11" fill="{c["ink"]}">{head}</text>'
        body += f'<text x="{lx + 20}" y="{y + 18}" font-size="11" fill="{c["ink2"]}">{rest}</text>'
    return svg(theme, 'How soon a new job reaches Wera',
               'Share of postings Wera has picked up, by hours since posting. Dots mark 4 hours.', body)


def wrap(s, n):
    out, cur = [], ''
    for word in s.split():
        if cur and len(cur) + 1 + len(word) > n:
            out.append(cur)
            cur = word
        else:
            cur = (cur + ' ' + word).strip()
    if cur:
        out.append(cur)
    return out


for theme in THEMES:
    for name, fn in [('posting-hours', hour_chart), ('posting-days', day_chart), ('time-to-wera', wait_chart)]:
        with open(os.path.join(out, f'{name}-{theme}.svg'), 'w') as f:
            f.write(fn(theme))
print(json.dumps(data, indent=1))
