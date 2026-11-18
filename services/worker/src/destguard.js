// destguard.js — the SMTP destination guard, CONNECT-TIME half (Phase 3 close-out C4). The one place in the
// worker that opens a connection to a host a user named, and the one place that decides whether the worker may.
//
// THE DEFECT. `smtp_host` accepted any IP and any name, and the worker connected to whatever it resolved to,
// from the operator's own network: a loopback, a private network, a link-local address, the cloud metadata
// address (or a name resolving to one) gave a user an SMTP session into the operator's side of the fence, with
// the session's reply text coming back to the user as `smtp_response`.
//
// THE MODEL is the Control's services/control/internal/hostguard (read it first; ranges.txt there is THE
// canonical table and a test on each side proves the two tables are the same set of rows):
//   - an address is refused if it is in a `block` row, or is IPv6 outside 2000::/3 (not global unicast);
//   - an address that CARRIES an IPv4 address (IPv4-mapped, NAT64, 6to4: the `embedded` rows) is judged by the
//     IPv4 address it carries;
//   - the allowlist (`smtp_host_allow_cidrs`, delivered per job as `destAllowCidrs`) is the one deliberate
//     hole: a CIDR list, default EMPTY, for a test harness that must reach its own sink on a private network.
//     It is consulted after the embedded-address judgement and overrides a refused row for the addresses it
//     names and for nothing else;
//   - a NAME is resolved ONCE, EVERY address it resolved to is judged, and the name is refused as a whole if
//     ANY address is refused (the passing addresses of a mixed answer are never dialled);
//   - anything that does not parse is refused.
//
// THE THREE PROPERTIES THAT MAKE IT CONNECT-TIME RATHER THAN CHECK-THEN-HOPE (guardedConnect):
//   1. It dials the IP LITERAL it judged, never the hostname, so there is no second resolution between the
//      check and the dial: DNS rebinding is impossible by construction, not by timing.
//   2. After the socket connects it judges sock.remoteAddress AGAIN and destroys the socket if that address is
//      refused: the equivalent of the Go dialer's Control hook, catching an OS / resolver-layer divergence the
//      first judgement could not see.
//   3. Certificate verification still runs against the ORIGINAL hostname (servername), exactly as before.
//
// Dependency-free: node:dns, node:net, node:tls only (the worker ships NO npm packages). A source gate
// (hostguard's TestNoUnguardedDialOfAUserHostExists, mirrored in test/destguard.test.mjs) allows ONLY this file
// and stream.js (the mTLS stream to the Control) to open a connection: smtp.js must go through here.
//
// A refusal carries the host, the address and the range for the worker's own LOG only (DestinationBlockedError
// .detail). Nothing from inside the network may reach the user: smtp.js reports `destination_blocked` with an
// empty smtp_response and never the detail.

import dns from "node:dns";
import net from "node:net";
import tls from "node:tls";

// ---------------------------------------------------------------------------------------------
// The table: row for row the rows of services/control/internal/hostguard/ranges.txt
// ---------------------------------------------------------------------------------------------

// Bounds the allowlist: it is a test affordance, not a routing table (mirrors hostguard.MaxAllowEntries).
export const MAX_ALLOW_ENTRIES = 64;

// kind "block": the whole range is refused. kind "embedded": the address carries an IPv4 address, which is
// judged against the IPv4 rows instead. `name` is for the log; it never reaches a response body.
export const RANGES = Object.freeze([
  // --- IPv4 ---
  { cidr: "0.0.0.0/8", kind: "block", name: "this network" },
  { cidr: "10.0.0.0/8", kind: "block", name: "private (RFC 1918)" },
  { cidr: "100.64.0.0/10", kind: "block", name: "shared address space (carrier-grade NAT)" },
  { cidr: "127.0.0.0/8", kind: "block", name: "loopback" },
  { cidr: "169.254.0.0/16", kind: "block", name: "link-local, including the cloud metadata address" },
  { cidr: "172.16.0.0/12", kind: "block", name: "private (RFC 1918)" },
  { cidr: "192.0.0.0/24", kind: "block", name: "IETF protocol assignments" },
  { cidr: "192.0.2.0/24", kind: "block", name: "documentation (TEST-NET-1)" },
  { cidr: "192.88.99.0/24", kind: "block", name: "6to4 relay anycast (deprecated)" },
  { cidr: "192.168.0.0/16", kind: "block", name: "private (RFC 1918)" },
  { cidr: "198.18.0.0/15", kind: "block", name: "benchmarking" },
  { cidr: "198.51.100.0/24", kind: "block", name: "documentation (TEST-NET-2)" },
  { cidr: "203.0.113.0/24", kind: "block", name: "documentation (TEST-NET-3)" },
  { cidr: "224.0.0.0/4", kind: "block", name: "multicast" },
  { cidr: "240.0.0.0/4", kind: "block", name: "reserved for future use" },
  { cidr: "255.255.255.255/32", kind: "block", name: "limited broadcast" },
  // --- IPv6 ---
  { cidr: "::/128", kind: "block", name: "unspecified" },
  { cidr: "::1/128", kind: "block", name: "loopback" },
  { cidr: "::ffff:0:0/96", kind: "embedded", name: "IPv4-mapped: the embedded IPv4 address is judged" },
  { cidr: "64:ff9b::/96", kind: "embedded", name: "NAT64 well-known prefix: the embedded IPv4 address is judged" },
  { cidr: "64:ff9b:1::/48", kind: "block", name: "local-use NAT64" },
  { cidr: "100::/64", kind: "block", name: "discard-only" },
  { cidr: "2001::/23", kind: "block", name: "IETF protocol assignments (Teredo and others)" },
  { cidr: "2001:db8::/32", kind: "block", name: "documentation" },
  { cidr: "2002::/16", kind: "embedded", name: "6to4: the embedded IPv4 address is judged" },
  { cidr: "3fff::/20", kind: "block", name: "documentation" },
  { cidr: "fc00::/7", kind: "block", name: "unique local" },
  { cidr: "fd00:ec2::/32", kind: "block", name: "cloud instance metadata (IPv6)" },
  { cidr: "fe80::/10", kind: "block", name: "link-local" },
  { cidr: "fec0::/10", kind: "block", name: "site-local (deprecated)" },
  { cidr: "ff00::/8", kind: "block", name: "multicast" },
].map((r) => Object.freeze(r)));

// ---------------------------------------------------------------------------------------------
// Address parsing. IPv4 is 32 bits and IPv6 is 128 bits, both as BigInt; an address is
// { family: 4 | 6, bits: BigInt, zone?: string } (zone is present only on a scoped IPv6 address).
// ---------------------------------------------------------------------------------------------

const U64_MAX = (1n << 64n) - 1n;

// parseCNumber reads one dotted part the way inet_aton does: decimal, leading-0 octal, or 0x hex. null when
// the part is none of those (an invalid octal such as `08` is NOT a number: the host is then a name).
function parseCNumber(p) {
  if (p === "") return null;
  let n;
  if (p.startsWith("0x") || p.startsWith("0X")) {
    const digits = p.slice(2);
    if (!/^[0-9a-fA-F]+$/.test(digits)) return null;
    n = BigInt("0x" + digits);
  } else if (p.length > 1 && p[0] === "0") {
    const digits = p.slice(1);
    if (!/^[0-7]+$/.test(digits)) return null;
    n = BigInt("0o" + digits);
  } else {
    if (!/^[0-9]+$/.test(p)) return null;
    n = BigInt(p);
  }
  return n > U64_MAX ? null : n;
}

// parseIPv4Loose is inet_aton: one to four parts, each decimal / octal / hex, every part but the last
// <= 255, the last part filling every remaining byte. Mirrors hostguard.go parseIPv4Loose exactly.
function parseIPv4Loose(s) {
  if (s === "") return null;
  const parts = s.split(".");
  if (parts.length > 4) return null;
  const nums = [];
  for (const p of parts) {
    const n = parseCNumber(p);
    if (n === null) return null;
    nums.push(n);
  }
  let v = 0n;
  for (let i = 0; i < nums.length - 1; i++) {
    if (nums[i] > 255n) return null;
    v |= nums[i] << BigInt(8 * (3 - i));
  }
  const last = nums[nums.length - 1];
  const remaining = BigInt(8 * (4 - (nums.length - 1)));
  if (last >= 1n << remaining) return null;
  v |= last;
  return { family: 4, bits: v };
}

// parseIPv4Strict is the dotted quad netip.ParseAddr accepts: four decimal parts, no leading zeros, each <= 255.
// Used for an allowlist / table CIDR and for the IPv4 tail of an IPv6 literal, where no loose spelling exists.
function parseIPv4Strict(s) {
  const parts = s.split(".");
  if (parts.length !== 4) return null;
  let v = 0n;
  for (const p of parts) {
    if (!/^(0|[1-9][0-9]{0,2})$/.test(p)) return null;
    const n = BigInt(p);
    if (n > 255n) return null;
    v = (v << 8n) | n;
  }
  return v;
}

const HEX_GROUP = /^[0-9a-fA-F]{1,4}$/;

// parseIPv6 reads an IPv6 address: eight groups, one "::" compression, an embedded dotted IPv4 tail
// (::ffff:a.b.c.d), and an optional "%zone" suffix (kept on the result; judgement refuses it as scoped).
function parseIPv6(text) {
  let s = text;
  let zone = "";
  const pct = s.indexOf("%");
  if (pct >= 0) {
    zone = s.slice(pct + 1);
    s = s.slice(0, pct);
    if (zone === "") return null;
  }
  if (s === "") return null;
  const dc = s.indexOf("::");
  if (dc >= 0 && s.indexOf("::", dc + 1) >= 0) return null; // two compressions, or ":::"
  const split = (x) => (x === "" ? [] : x.split(":"));
  const headList = split(dc >= 0 ? s.slice(0, dc) : s);
  const tailList = dc >= 0 ? split(s.slice(dc + 2)) : [];
  // A dotted IPv4 tail is the very last element of the text and nowhere else.
  const lastList = dc >= 0 ? tailList : headList;
  let v4groups = [];
  if (lastList.length > 0 && lastList[lastList.length - 1].includes(".")) {
    const v4 = parseIPv4Strict(lastList[lastList.length - 1]);
    if (v4 === null) return null;
    lastList.pop();
    v4groups = [v4 >> 16n, v4 & 0xffffn];
  }
  const groups = (list) => {
    const out = [];
    for (const g of list) {
      if (!HEX_GROUP.test(g)) return null;
      out.push(BigInt("0x" + g));
    }
    return out;
  };
  const head = groups(headList);
  const tail = groups(tailList);
  if (head === null || tail === null) return null;
  let seq;
  if (dc < 0) {
    seq = [...head, ...v4groups];
    if (seq.length !== 8) return null;
  } else {
    const used = head.length + tail.length + v4groups.length;
    if (used > 7) return null; // "::" must stand for at least one zero group
    seq = [...head, ...new Array(8 - used).fill(0n), ...tail, ...v4groups];
  }
  let bits = 0n;
  for (const g of seq) bits = (bits << 16n) | g;
  return zone ? { family: 6, bits, zone } : { family: 6, bits };
}

/**
 * parseHostLiteral reports whether host is an address and, if so, which. It reads every spelling a resolver
 * library (inet_aton, getaddrinfo) would read as an IPv4 address: decimal 2130706433, octal 0177.0.0.1, hex
 * 0x7f.1 and 0x7f000001, short forms 127.1; and IPv6, bracketed or not, with an embedded dotted IPv4 tail and
 * an optional %zone. Returns { family, bits } (plus `zone` when scoped) or null when host is a name.
 */
export function parseHostLiteral(host) {
  if (typeof host !== "string") return null;
  let h = host.trim();
  if (h.length >= 2 && h[0] === "[" && h[h.length - 1] === "]") h = h.slice(1, -1);
  if (h.includes(":")) return parseIPv6(h);
  return parseIPv4Loose(h);
}

// formatAddress renders the canonical text of an address: dotted quad, or RFC 5952 IPv6 (lowercase, the
// longest run of zero groups compressed). This is the string the guard DIALS, so what is judged is what is dialled.
function formatAddress(a) {
  if (a.family === 4) {
    return [24n, 16n, 8n, 0n].map((sh) => ((a.bits >> sh) & 255n).toString()).join(".");
  }
  const g = [];
  for (let i = 7; i >= 0; i--) g.push(Number((a.bits >> BigInt(i * 16)) & 0xffffn));
  let bestStart = -1;
  let bestLen = 0;
  for (let i = 0; i < 8;) {
    if (g[i] !== 0) { i++; continue; }
    let j = i;
    while (j < 8 && g[j] === 0) j++;
    if (j - i > bestLen) { bestStart = i; bestLen = j - i; }
    i = j;
  }
  const hex = (x) => x.toString(16);
  let out;
  if (bestLen < 2) out = g.map(hex).join(":");
  else out = g.slice(0, bestStart).map(hex).join(":") + "::" + g.slice(bestStart + bestLen).map(hex).join(":");
  return a.zone ? out + "%" + a.zone : out;
}

// ---------------------------------------------------------------------------------------------
// Prefixes: the table's rows and the allowlist's entries
// ---------------------------------------------------------------------------------------------

function widthOf(family) {
  return family === 4 ? 32n : 128n;
}

function maskTo(bits, len, family) {
  const shift = widthOf(family) - BigInt(len);
  return (bits >> shift) << shift;
}

function contains(prefix, a) {
  if (prefix.family !== a.family) return false; // an IPv4 address never matches an IPv6 prefix, or the reverse
  const shift = widthOf(a.family) - BigInt(prefix.len);
  return a.bits >> shift === prefix.bits >> shift;
}

// parseCidr reads "address/length" strictly (a bare address, a zone, a loose IPv4 spelling or a length with a
// leading zero is not a CIDR). Returns { family, bits (masked), len, cidr, exact } or null.
function parseCidr(text) {
  if (typeof text !== "string") return null;
  const s = text.trim();
  const slash = s.lastIndexOf("/");
  if (slash < 0) return null;
  const addrText = s.slice(0, slash);
  const lenText = s.slice(slash + 1);
  if (!/^(0|[1-9][0-9]*)$/.test(lenText)) return null;
  let a;
  if (addrText.includes(":")) {
    a = parseIPv6(addrText);
    if (a === null || a.zone) return null;
  } else {
    const v4 = parseIPv4Strict(addrText);
    if (v4 === null) return null;
    a = { family: 4, bits: v4 };
  }
  const len = Number(lenText);
  if (len > Number(widthOf(a.family))) return null;
  const masked = maskTo(a.bits, len, a.family);
  const p = { family: a.family, bits: masked, len, exact: masked === a.bits };
  p.cidr = `${formatAddress({ family: p.family, bits: p.bits })}/${len}`;
  return p;
}

const TABLE = RANGES.map((r) => {
  const p = parseCidr(r.cidr);
  if (p === null) throw new Error(`destguard: RANGES row ${JSON.stringify(r.cidr)} is not a CIDR`);
  if (!p.exact) throw new Error(`destguard: RANGES row ${r.cidr} is not a network address (host bits set)`);
  if (r.kind !== "block" && r.kind !== "embedded") throw new Error(`destguard: RANGES row ${r.cidr} has kind ${r.kind}`);
  return { ...p, kind: r.kind, name: r.name };
});

const GLOBAL_UNICAST = parseCidr("2000::/3");

/**
 * parseAllowList parses the destAllowCidrs values: CIDRs only (a bare address is an error, so a list cannot
 * name one host by accident and a typo cannot widen it), at most MAX_ALLOW_ENTRIES. undefined / null / [] is the
 * production default and yields []. Returns masked prefixes { family, bits, len, cidr }.
 */
export function parseAllowList(raw) {
  if (raw === undefined || raw === null) return [];
  if (!Array.isArray(raw)) throw new TypeError("the allowlist must be an array of CIDR strings");
  if (raw.length > MAX_ALLOW_ENTRIES) {
    throw new RangeError(`the allowlist has ${raw.length} entries; the limit is ${MAX_ALLOW_ENTRIES}`);
  }
  return raw.map((entry) => {
    const p = parseCidr(entry);
    if (p === null) throw new Error(`allowlist entry ${JSON.stringify(entry)} is not a CIDR`);
    return Object.freeze({ family: p.family, bits: p.bits, len: p.len, cidr: p.cidr });
  });
}

function isPrefix(x) {
  return x !== null && typeof x === "object" && (x.family === 4 || x.family === 6)
    && typeof x.bits === "bigint" && Number.isInteger(x.len);
}

// toAllow accepts an allowlist as CIDR strings (what the job frame carries) or as the prefixes parseAllowList
// returned, so a caller that parsed once need not parse again.
function toAllow(allow) {
  if (allow === undefined || allow === null) return [];
  if (!Array.isArray(allow)) throw new TypeError("the allowlist must be an array");
  if (allow.every((e) => typeof e === "string")) return parseAllowList(allow);
  if (allow.every(isPrefix)) return allow;
  throw new TypeError("the allowlist must be CIDR strings or prefixes returned by parseAllowList");
}

// ---------------------------------------------------------------------------------------------
// The judgement
// ---------------------------------------------------------------------------------------------

function refusedRow(a) {
  for (const r of TABLE) if (r.kind === "block" && contains(r, a)) return r.name;
  return null;
}

function refusedByTable(a) {
  return refusedRow(a) !== null || (a.family === 6 && !contains(GLOBAL_UNICAST, a));
}

// embeddedV4 extracts the IPv4 address a mapped / NAT64 (/96) or 6to4 (/16) address carries.
function embeddedV4(row, a) {
  if (row.len === 96) return { family: 4, bits: a.bits & 0xffffffffn };
  if (row.len === 16) return { family: 4, bits: (a.bits >> 80n) & 0xffffffffn }; // bits 16..48
  return null;
}

function judge(a, allow, depth) {
  if (a === null || typeof a !== "object" || (a.family !== 4 && a.family !== 6) || typeof a.bits !== "bigint") {
    return { blocked: true, range: "not an address", judged: "", allowlist: false };
  }
  const refused = (range) => ({ blocked: true, range, judged: formatAddress(a), allowlist: false });
  if (a.zone) return refused("scoped address");
  // An address that carries an IPv4 address is judged by the IPv4 it carries.
  if (a.family === 6 && depth < 2) {
    for (const r of TABLE) {
      if (r.kind === "embedded" && contains(r, a)) {
        const v4 = embeddedV4(r, a);
        if (v4 === null) return refused(r.name);
        return judge(v4, allow, depth + 1);
      }
    }
  }
  // The allowlist names addresses that are otherwise refused (and may name any other address too).
  for (const p of allow) {
    if (contains(p, a)) return { blocked: false, range: "", judged: formatAddress(a), allowlist: refusedByTable(a) };
  }
  const row = refusedRow(a);
  if (row !== null) return refused(row);
  if (a.family === 6 && !contains(GLOBAL_UNICAST, a)) return refused("not global unicast (outside 2000::/3)");
  return { blocked: false, range: "", judged: formatAddress(a), allowlist: false };
}

/**
 * judgeAddress decides whether addr may be connected to. addr is address text (any spelling parseHostLiteral
 * reads, which is also what a socket's remoteAddress looks like) or an address object; allow is the allowlist
 * (CIDR strings or parseAllowList output; none in production). Returns
 *   { blocked, range, judged, allowlist }
 * range is the row or rule that refused it ("" when allowed), judged is the text of the address actually judged
 * (the embedded IPv4 for a mapped / NAT64 / 6to4 address), allowlist is true when the address is allowed only
 * because the allowlist names it. A scoped address and anything that does not parse are refused.
 */
export function judgeAddress(addr, allow) {
  const a = typeof addr === "string" ? parseHostLiteral(addr) : addr;
  return judge(a, toAllow(allow), 0);
}

// ---------------------------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------------------------

/**
 * DestinationBlockedError is the guard's refusal. `.code` is "destination_blocked". `.detail` names the host, the
 * address and the range FOR THE WORKER'S OWN LOG: it is not enumerable (so it does not ride along in a JSON dump
 * or a console print of the error), it is not in the message, and it must never be put in anything reported to
 * the Control.
 */
export class DestinationBlockedError extends Error {
  constructor(detail) {
    super("destination_blocked");
    this.name = "DestinationBlockedError";
    this.code = "destination_blocked";
    Object.defineProperty(this, "detail", {
      value: Object.freeze({
        host: String(detail?.host ?? ""),
        address: String(detail?.address ?? ""),
        range: String(detail?.range ?? ""),
      }),
      enumerable: false,
    });
  }
}

function timeoutError() {
  const e = new Error("connect timeout");
  e.code = "ETIMEDOUT";
  return e;
}

function notFoundError(cause) {
  const e = new Error("the host did not resolve");
  e.code = "ENOTFOUND";
  if (cause) e.cause = cause;
  return e;
}

// ---------------------------------------------------------------------------------------------
// The check
// ---------------------------------------------------------------------------------------------

function systemLookup(host) {
  return dns.promises.lookup(host, { all: true, verbatim: true });
}

/**
 * checkDestination decides whether the worker may connect to host, and returns the addresses it may dial (text,
 * in resolver order; for a literal, the literal itself in canonical form). Throws DestinationBlockedError when it
 * may not. A literal address is judged and never looked up. A name is resolved ONCE with opts.lookup (default
 * dns.promises.lookup(host, { all: true, verbatim: true }); the option is a test seam returning
 * [{ address, family }]), EVERY address is judged, and the whole name is refused if ANY address is refused.
 * A resolver error or an empty answer throws an Error whose code is "ENOTFOUND": at connect time an
 * unresolvable name is a connect failure (the caller reports connect_refused as it always has), not a refusal.
 *
 * @param {string} host
 * @param {{ allow?: string[]|object[], lookup?: (host: string) => Promise<Array<{address: string, family?: number}>> }} [opts]
 * @returns {Promise<string[]>}
 */
export async function checkDestination(host, opts = {}) {
  const allow = toAllow(opts.allow);
  const lit = parseHostLiteral(host);
  if (lit !== null) {
    const d = judge(lit, allow, 0);
    const text = formatAddress(lit);
    if (d.blocked) throw new DestinationBlockedError({ host, address: text, range: d.range });
    return [text];
  }
  if (typeof host !== "string" || host === "") throw notFoundError(null);
  if (host.includes(":")) {
    // Not an address, and a name cannot carry a colon: refuse rather than hand a string the resolver might read
    // as an address in some spelling this parser does not know.
    throw new DestinationBlockedError({ host, address: "", range: "not an address and not a name" });
  }
  const lookup = opts.lookup ?? systemLookup;
  let answers;
  try {
    answers = await lookup(host);
  } catch (e) {
    throw notFoundError(e);
  }
  if (answers && !Array.isArray(answers)) answers = [answers];
  if (!answers || answers.length === 0) throw notFoundError(null);
  const addrs = [];
  for (const ans of answers) {
    const raw = typeof ans === "string" ? ans : ans?.address;
    const a = parseHostLiteral(raw);
    const d = judge(a, allow, 0);
    if (d.blocked) {
      throw new DestinationBlockedError({ host, address: typeof raw === "string" ? raw : "", range: d.range });
    }
    addrs.push(formatAddress(a));
  }
  return addrs;
}

// ---------------------------------------------------------------------------------------------
// The guarded dial
// ---------------------------------------------------------------------------------------------

/**
 * guardedConnect is the ONLY function in the worker that opens a connection to a user's host.
 *
 *   1. checkDestination: one resolution, every address judged, the whole host refused if any is.
 *   2. Dial the checked IP LITERAL (never the hostname: no second resolution, so no rebinding window), trying
 *      the addresses in resolver order; each attempt gets an equal share of the time that is left.
 *   3. Once connected, judge sock.remoteAddress again; a refused (or unparseable) address destroys the socket
 *      and throws DestinationBlockedError.
 *   4. timeoutMs bounds the whole call (resolution and every attempt); expiry rejects with code "ETIMEDOUT".
 *
 * Any other failure rejects with the socket's own error (or code "ENOTFOUND" for an unresolvable name).
 * Resolves with the connected socket ("connect" for plain; "secureConnect" for tls). For tls, certificate
 * verification is against `servername` (default: the original host), not the IP that was dialled.
 *
 * @param {object} o
 * @param {string} o.host       the user's host: a name or an address in any spelling
 * @param {number} o.port
 * @param {boolean} [o.tls]     implicit TLS (tls.connect) instead of a plain TCP connection
 * @param {string} [o.servername]  SNI / certificate-verification name (default: host)
 * @param {boolean} [o.rejectUnauthorized]  default true
 * @param {number} [o.timeoutMs]
 * @param {string[]|object[]} [o.allow]  allowlist (CIDR strings or parseAllowList output); none by default
 * @param {function} [o.lookup]   test seam, see checkDestination
 * @param {function} [o.connect]  test seam: (ip, port, tlsOptions|null) => socket, replacing net.connect /
 *        tls.connect. tlsOptions is the options object tls.connect would get ({host: ip, port, servername,
 *        rejectUnauthorized}), or null for a plain connection. The socket must emit "connect" (plain) or
 *        "secureConnect" (tls) and expose remoteAddress / destroy(), as a real socket does.
 * @returns {Promise<import("node:net").Socket>}
 */
export async function guardedConnect(o) {
  const { host, port, servername, rejectUnauthorized = true, timeoutMs, lookup, connect } = o;
  const wantTls = o.tls === true;
  const allow = toAllow(o.allow);
  const state = { timedOut: false, sock: null };
  const deadline = timeoutMs > 0 ? Date.now() + timeoutMs : null;

  let timer = null;
  const overall = new Promise((_, reject) => {
    if (deadline === null) return;
    timer = setTimeout(() => {
      state.timedOut = true;
      if (state.sock) { try { state.sock.destroy(); } catch { /* ignore */ } }
      reject(timeoutError());
    }, timeoutMs);
  });

  // dialOne opens ONE connection to an address that has already been judged and verifies the address it landed on.
  const dialOne = (ip, budgetMs) => new Promise((resolve, reject) => {
    if (net.isIP(ip) === 0) {
      // Cannot happen (ip is formatAddress output), and if it ever did a hostname here would be resolved a
      // second time: refuse rather than dial it.
      reject(new DestinationBlockedError({ host, address: ip, range: "not an address" }));
      return;
    }
    const tlsOptions = wantTls ? { host: ip, port, servername: servername ?? host, rejectUnauthorized } : null;
    let sock;
    try {
      if (connect) sock = connect(ip, port, tlsOptions);
      else if (wantTls) sock = tls.connect(tlsOptions);
      else sock = net.connect({ host: ip, port });
    } catch (e) {
      reject(e);
      return;
    }
    state.sock = sock;
    let settled = false;
    let attemptTimer = null;
    const cleanup = () => {
      if (attemptTimer) clearTimeout(attemptTimer);
      sock.removeListener("connect", onConnect);
      sock.removeListener("secureConnect", onSecure);
      sock.removeListener("error", onError);
      sock.removeListener("close", onClose);
    };
    const fail = (err) => {
      if (settled) return;
      settled = true;
      cleanup();
      sock.on("error", () => { /* the socket is being thrown away */ });
      try { sock.destroy(); } catch { /* ignore */ }
      reject(err);
    };
    // verify judges the address the socket actually connected to (the net.Dialer.Control equivalent).
    const verify = () => {
      const landed = sock.remoteAddress;
      const d = judgeAddress(landed, allow);
      if (d.blocked) {
        fail(new DestinationBlockedError({ host, address: typeof landed === "string" ? landed : "", range: d.range }));
        return false;
      }
      return true;
    };
    const succeed = () => {
      if (settled) return;
      settled = true;
      cleanup();
      // Keep an error sink on the socket: the caller attaches its own handlers a microtask later.
      sock.on("error", () => { /* the session's own handlers take over */ });
      resolve(sock);
    };
    function onConnect() {
      if (settled || !verify()) return;
      if (!wantTls) succeed();
    }
    function onSecure() {
      if (settled || !verify()) return;
      succeed();
    }
    function onError(e) { fail(e); }
    function onClose() { fail(Object.assign(new Error("connection closed before it was established"), { code: "ECONNRESET" })); }
    sock.on("connect", onConnect);
    if (wantTls) sock.on("secureConnect", onSecure);
    sock.on("error", onError);
    sock.on("close", onClose);
    if (budgetMs > 0) attemptTimer = setTimeout(() => fail(timeoutError()), budgetMs);
  });

  const dialAll = async () => {
    const addrs = await checkDestination(host, { allow, lookup });
    if (state.timedOut) throw timeoutError();
    const targets = [...new Set(addrs)];
    let lastErr = null;
    for (let i = 0; i < targets.length; i++) {
      const budget = deadline === null ? 0 : Math.max(1, Math.floor((deadline - Date.now()) / (targets.length - i)));
      try {
        const sock = await dialOne(targets[i], budget);
        if (state.timedOut) {
          try { sock.destroy(); } catch { /* ignore */ }
          throw timeoutError();
        }
        return sock;
      } catch (e) {
        if (e instanceof DestinationBlockedError) throw e;
        if (state.timedOut) throw timeoutError();
        lastErr = e;
      }
    }
    throw lastErr ?? notFoundError(null);
  };

  try {
    return await Promise.race([dialAll(), overall]);
  } finally {
    if (timer) clearTimeout(timer);
  }
}

/**
 * upgradeToTLS runs the STARTTLS handshake over an already-connected (and already-judged) plain socket,
 * verifying the certificate against `host`. Lives here, not in smtp.js, because wrapping a socket in
 * tls.connect is a dial site the source gate keeps in this file. Rejects with code "ETIMEDOUT" on timeout and
 * with the TLS error otherwise.
 *
 * @param {import("node:net").Socket} plainSock
 * @param {string} host  the name the certificate must match
 * @param {number} timeoutMs
 * @param {boolean} rejectUnauthorized
 * @returns {Promise<import("node:tls").TLSSocket>}
 */
export function upgradeToTLS(plainSock, host, timeoutMs, rejectUnauthorized) {
  return new Promise((resolve, reject) => {
    const sock = tls.connect({ socket: plainSock, servername: host, rejectUnauthorized });
    const to = setTimeout(() => { sock.destroy(); reject(timeoutError()); }, timeoutMs);
    sock.once("secureConnect", () => { clearTimeout(to); resolve(sock); });
    sock.once("error", (e) => { clearTimeout(to); reject(e); });
  });
}
