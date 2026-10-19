// Numbers, money and times, written the way the screens say them.
//
// Times are epoch milliseconds, read in one time zone. The demo reads them in
// UTC, so its Sep 25 is the same everywhere; real data reads them in the
// browser’s own zone. The server builds real views in the zone the browser
// names, with makeZone, so both sides agree on where a day starts.

export const MIN = 60_000;
export const HOUR = 60 * MIN;
export const DAY = 24 * HOUR;

/** Midnight at the start of the demo’s today, Sep 25 2026. */
export const DAY0 = Date.UTC(2026, 8, 25);

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

// ─── time zones ────────────────────────────────────────

export interface Zone {
  name: string;
  /** What to add to an instant to read its wall clock with UTC getters. */
  offset(at: number): number;
  /** The instant the day holding `at` starts. */
  dayStart(at: number): number;
  /** Wall-clock hours since that midnight, as a float. */
  hourOf(at: number): number;
}

const QUARTER = 15 * MIN;

/** A zone by its IANA name. Unknown names fall back to UTC. */
export function makeZone(name: string | null | undefined): Zone {
  let offset: (at: number) => number = () => 0;
  let label = 'UTC';
  if (name && name !== 'UTC') {
    try {
      const f = new Intl.DateTimeFormat('en-US', {
        timeZone: name, hourCycle: 'h23', year: 'numeric', month: 'numeric', day: 'numeric', hour: 'numeric', minute: 'numeric',
      });
      // Offsets only change on quarter hours, so one lookup covers fifteen minutes.
      const cache = new Map<number, number>();
      offset = (at) => {
        const k = Math.floor(at / QUARTER);
        let v = cache.get(k);
        if (v === undefined) {
          const p: Record<string, number> = {};
          for (const x of f.formatToParts(k * QUARTER)) if (x.type !== 'literal') p[x.type] = Number(x.value);
          v = Date.UTC(p.year!, p.month! - 1, p.day!, p.hour! % 24, p.minute!) - k * QUARTER;
          if (cache.size > 4000) cache.clear();
          cache.set(k, v);
        }
        return v;
      };
      label = name;
    } catch {
      // Not a zone this runtime knows: read it as UTC.
    }
  }
  return {
    name: label,
    offset,
    dayStart(at) {
      const wall = Math.floor((at + offset(at)) / DAY) * DAY;
      // The offset at midnight can differ from the one now, on the day clocks change.
      let t = wall - offset(at);
      t = wall - offset(t);
      return t;
    },
    hourOf(at) {
      const w = at + offset(at);
      return (w - Math.floor(w / DAY) * DAY) / HOUR;
    },
  };
}

let zone = makeZone('UTC');

/** Read times in this zone from now on. The demo leaves it at UTC. */
export function setZone(name: string | null): void {
  zone = makeZone(name);
}

export function currentZone(): Zone {
  return zone;
}

/** The browser’s own zone, by name. */
export function localZoneName(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  } catch {
    return 'UTC';
  }
}

/** The wall clock at an instant, in the current zone. */
export function wall(at: number, z: Zone = zone): { y: number; mo: number; d: number; dow: number; h: number; m: number; s: number; ms: number } {
  const d = new Date(at + z.offset(at));
  return {
    y: d.getUTCFullYear(), mo: d.getUTCMonth(), d: d.getUTCDate(), dow: d.getUTCDay(),
    h: d.getUTCHours(), m: d.getUTCMinutes(), s: d.getUTCSeconds(), ms: d.getUTCMilliseconds(),
  };
}

export function fmt(n: number): string {
  return Math.round(n).toLocaleString('en-US');
}

/** $118, or $18.60 when there are cents to show. */
export function money(dollars: number, cents: 'auto' | 'always' | 'never' = 'auto'): string {
  const showCents = cents === 'always' || (cents === 'auto' && Math.round(dollars * 100) % 100 !== 0);
  const s = Math.abs(dollars).toLocaleString('en-US', {
    minimumFractionDigits: showCents ? 2 : 0,
    maximumFractionDigits: showCents ? 2 : 0,
  });
  return `${dollars < 0 ? '−' : ''}$${s}`;
}

function hm(at: number): { h: number; m: number; s: number; ms: number } {
  return wall(at);
}

/** 2:14 PM */
export function time(at: number): string {
  const { h, m } = hm(at);
  return `${h % 12 || 12}:${String(m).padStart(2, '0')} ${h < 12 ? 'AM' : 'PM'}`;
}

/** 2:02:07.412 PM, for the Story timeline. */
export function timePrecise(at: number, withMs = true): string {
  const { h, m, s, ms } = hm(at);
  // Seeded times land on the second; ".000" would read as made up.
  const tail = withMs && ms ? `.${String(ms).padStart(3, '0')}` : '';
  return `${h % 12 || 12}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}${tail} ${h < 12 ? 'AM' : 'PM'}`;
}

/** 12 PM, 6 AM: chart axes. */
export function hourLabel(h: number): string {
  const hh = ((h % 24) + 24) % 24;
  return `${hh % 12 || 12} ${hh < 12 ? 'AM' : 'PM'}`;
}

/** Sep 25 */
export function date(at: number): string {
  const d = wall(at);
  return `${MONTHS[d.mo]} ${d.d}`;
}

/** Sep 25 to 30, or Sep 28 to Oct 4 across a month. */
export function dateRange(from: number, to: number): string {
  return `${date(from)} to ${wall(from).mo === wall(to).mo ? wall(to).d : date(to)}`;
}

/** just now, 2 min ago, 3 hours ago, 2 days ago */
export function ago(at: number, now: number): string {
  const d = Math.max(0, now - at);
  if (d < 45_000) return 'just now';
  if (d < HOUR) {
    const m = Math.max(1, Math.round(d / MIN));
    return `${m} min ago`;
  }
  if (d < 36 * HOUR) {
    const h = Math.round(d / HOUR);
    return `${h} hour${h === 1 ? '' : 's'} ago`;
  }
  const days = Math.round(d / DAY);
  return `${days} day${days === 1 ? '' : 's'} ago`;
}

/** just now, 2 minutes ago, 3 hours ago: the long form, for sentences. */
export function agoWords(at: number, now: number): string {
  const d = Math.max(0, now - at);
  if (d < 45_000) return 'just now';
  if (d < HOUR) return `${minutes(d)} ago`;
  return ago(at, now);
}

/** "2 minutes", "1 minute", for sentences. */
export function minutes(ms: number): string {
  const m = Math.max(1, Math.round(ms / MIN));
  return `${m} minute${m === 1 ? '' : 's'}`;
}

/** Hours since midnight, as a float: 14.23 at 2:14 PM. */
export function hourOf(at: number): number {
  return zone.hourOf(at);
}

export function dayStart(at: number): number {
  return zone.dayStart(at);
}

/** "one", "two" … for small counts in sentences; digits after nine. */
export function words(n: number, capital = false): string {
  const w = ['no', 'one', 'two', 'three', 'four', 'five', 'six', 'seven', 'eight', 'nine'][n] ?? fmt(n);
  return capital ? w[0]!.toUpperCase() + w.slice(1) : w;
}

export function plural(n: number, one: string, many = `${one}s`): string {
  return n === 1 ? one : many;
}

/** Possessive: Sam’s, James’. */
export function poss(name: string): string {
  return `${name}’s`;
}
