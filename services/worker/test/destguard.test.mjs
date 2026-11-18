// destguard.test.mjs — the SMTP destination guard, CONNECT-TIME half (Phase 3 close-out C4), proven with
// real sockets where a socket is the point and with seams (a fake resolver, a fake socket factory) where the
// point is what the guard does with an answer. Mirror of services/control/internal/hostguard/hostguard_test.go.
//
// WHAT IS PROVED
//   a. the worker's table is, row for row, the Control's ranges.txt (and the comparison can fail);
//   b. every block row is refused at its first, last and middle address; every embedded row refuses an embedded
//      loopback and admits an embedded public address;
//   c. the owner's list, by name, with the REASON for each refusal; the public addresses on the other side of every
//      edge are allowed (the paired control: a guard that refuses everything passes the refusal half);
//   d. every spelling a resolver would read as an address is judged, never looked up, never dialled;
//   e. a name is resolved once, every address judged, the whole name refused if any is refused;
//   f. DNS rebinding cannot happen (the judged IP is the dialled IP, one resolution) and an OS-layer divergence is
//      caught after the connect;
//   g. the allowlist admits its own range and nothing next to it;
//   h. a REAL listener sees one connection when allowed and ZERO when refused;
//   i. runSmtpSession reports destination_blocked with an empty smtp_response, and still maps the other failures
//      as it always did;
//   k. no file in src/ other than destguard.js and stream.js can open a socket (source gate, with its control).
//
// No dotted-quad IPv4 literal of a refused class and no public address is written in this file: addresses are
// built from octets at run time (v4 below). Loopback literals and CIDRs of the private ranges are the only
// exceptions, and only where the test is about them.

import { test } from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import net from "node:net";
import tls from "node:tls";
import { EventEmitter } from "node:events";
import { fileURLToPath } from "node:url";
import * as dg from "../src/destguard.js";
import { runSmtpSession, SMTPError } from "../src/smtp.js";
import { MemoryCredential } from "../src/creds.js";
import { startSink } from "./sink.mjs";

const { DestinationBlockedError } = dg;

// --- helpers ---------------------------------------------------------------------------------------

const v4 = (a, b, c, d) => [a, b, c, d].join(".");
const bits4 = (a, b, c, d) => (BigInt(a) << 24n) | (BigInt(b) << 16n) | (BigInt(c) << 8n) | BigInt(d);
const PUBLIC = v4(8, 8, 4, 4);
const PUBLIC2 = v4(1, 0, 0, 1);
const LOOPBACK = v4(127, 0, 0, 1);
const METADATA = v4(169, 254, 169, 254);
const PRIVATE = v4(10, 0, 0, 5);
const LOOPBACK_ALLOW = ["127.0.0.0/8"];

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function until(pred, ms = 3000) {
  const start = Date.now();
  while (Date.now() - start < ms) {
    if (pred()) return true;
    await sleep(10);
  }
  return pred();
}

// caught runs fn and returns the error it rejected with; a resolution is a test failure (a reject test must
// reject, and every caller then asserts the REASON).
async function caught(fn) {
  try {
    await fn();
  } catch (e) {
    return e;
  }
  assert.fail("expected a rejection, but the call resolved");
}

// quietly runs fn with console.warn captured (the guard logs one line per refusal) and returns what was logged.
async function quietly(fn) {
  const orig = console.warn;
  const lines = [];
  console.warn = (...a) => lines.push(a.join(" "));
  try {
    const result = await fn();
    return { result, lines };
  } finally {
    console.warn = orig;
  }
}

function isBlocked(rangeRe) {
  return (e) => {
    assert.ok(e instanceof DestinationBlockedError, `want a DestinationBlockedError, got ${e && e.name}: ${e && e.message}`);
    assert.equal(e.code, "destination_blocked");
    assert.match(e.detail.range, rangeRe, `the refusal named the wrong range: ${e.detail.range}`);
    return true;
  };
}

// A resolver seam: `answers` maps a name to its addresses (strings); an unknown name rejects as a resolver would.
function fakeLookup(answers) {
  const calls = [];
  const fn = async (host) => {
    calls.push(host);
    if (typeof answers === "function") return answers(host, calls.length);
    if (!Object.hasOwn(answers, host)) throw Object.assign(new Error("no such host"), { code: "ENOTFOUND" });
    return answers[host].map((address) => ({ address, family: address.includes(":") ? 6 : 4 }));
  };
  fn.calls = calls;
  return fn;
}

// A socket seam. `remote` is what sock.remoteAddress reports (default: the address that was dialled); `event` is the
// event a real socket would emit when established; `never` makes it hang; `fail` makes it error.
class FakeSocket extends EventEmitter {
  constructor(remoteAddress, { event = "connect", never = false, fail = null } = {}) {
    super();
    this.remoteAddress = remoteAddress;
    this.destroyed = false;
    if (never) return;
    setImmediate(() => {
      if (this.destroyed) return;
      if (fail) this.emit("error", fail);
      else this.emit(event);
    });
  }
  destroy() {
    this.destroyed = true;
    return this;
  }
}

// A connect seam that records every dial. `make(ip, port, tlsOptions, n)` returns the socket (default: connects, and
// reports the dialled address as the remote one).
function fakeConnect(make) {
  const dials = [];
  const sockets = [];
  const fn = (ip, port, tlsOptions) => {
    dials.push({ ip, port, tlsOptions });
    const sock = make ? make(ip, port, tlsOptions, dials.length) : new FakeSocket(ip, { event: tlsOptions ? "secureConnect" : "connect" });
    sockets.push(sock);
    return sock;
  };
  fn.dials = dials;
  fn.sockets = sockets;
  return fn;
}

// A real listener that counts the connections it is offered.
async function listen() {
  const sockets = [];
  const server = net.createServer((s) => {
    sockets.push(s);
    s.on("error", () => {});
  });
  await new Promise((r) => server.listen(0, "127.0.0.1", r));
  return {
    port: server.address().port,
    get connections() { return sockets.length; },
    close: async () => {
      for (const s of sockets) s.destroy();
      await new Promise((r) => server.close(r));
    },
  };
}

// --- ranges.txt, parsed independently of the module under test --------------------------------------

const RANGES_TXT = fs.readFileSync(new URL("../../control/internal/hostguard/ranges.txt", import.meta.url), "utf8");

function parseRangesFile(text) {
  return text
    .split("\n")
    .map((l) => l.trim())
    .filter((l) => l !== "" && !l.startsWith("#"))
    .map((l) => {
      const f = l.split("|");
      assert.ok(f.length >= 3, `ranges.txt: want CIDR|kind|name, got ${l}`);
      return { cidr: f[0].trim(), kind: f[1].trim(), name: f.slice(2).join("|").trim() };
    });
}

// rowKey is the row in a spelling-independent form (family, network bits, length) plus its kind.
function rowKey(row) {
  const [p] = dg.parseAllowList([row.cidr]);
  return `${p.family}/${p.bits.toString(16)}/${p.len}|${row.kind}`;
}

// compareTables returns a description of every difference between two tables (empty: they are the same set).
function compareTables(a, b) {
  const diffs = [];
  const ka = a.map(rowKey);
  const kb = b.map(rowKey);
  const sa = new Set(ka);
  const sb = new Set(kb);
  for (const r of a) if (!sb.has(rowKey(r))) diffs.push(`only in the first table: ${r.cidr}|${r.kind}`);
  for (const r of b) if (!sa.has(rowKey(r))) diffs.push(`only in the second table: ${r.cidr}|${r.kind}`);
  if (ka.length !== kb.length) diffs.push(`row counts differ: ${ka.length} vs ${kb.length}`);
  if (sa.size !== ka.length) diffs.push("the first table lists a row twice");
  if (sb.size !== kb.length) diffs.push("the second table lists a row twice");
  return diffs;
}

// probes returns an address's first, last and a middle address as address objects.
function probes(cidr) {
  const [p] = dg.parseAllowList([cidr]);
  const width = p.family === 4 ? 32n : 128n;
  const host = width - BigInt(p.len);
  const first = p.bits;
  const last = p.bits | ((1n << host) - 1n);
  const mid = host > 0n ? p.bits | (1n << (host - 1n)) : p.bits;
  return [first, last, mid].map((bits) => ({ family: p.family, bits }));
}

// embed writes an IPv4 address (as a number) into a mapped / NAT64 (/96) or 6to4 (/16) prefix.
function embed(cidr, v4bits) {
  const [p] = dg.parseAllowList([cidr]);
  return { family: 6, bits: p.len === 96 ? p.bits | v4bits : p.bits | (v4bits << 80n) };
}

// --- a. the table ------------------------------------------------------------------------------------

test("a. TABLE PARITY: RANGES is, row for row, services/control/internal/hostguard/ranges.txt", () => {
  const file = parseRangesFile(RANGES_TXT);
  assert.ok(file.length >= 30, `ranges.txt has ${file.length} rows; the floor is 30`);
  assert.ok(dg.RANGES.length >= 30, `RANGES has ${dg.RANGES.length} rows; the floor is 30`);
  assert.deepEqual(compareTables(dg.RANGES, file), [], "the worker's table and the Control's ranges.txt differ");
  assert.equal(dg.RANGES.length, file.length, "the row counts differ");
  assert.equal(file.filter((r) => r.kind === "embedded").length, 3, "ranges.txt must carry exactly the three embedded families");
  for (const r of dg.RANGES) assert.ok(r.kind === "block" || r.kind === "embedded", `${r.cidr}: unknown kind ${r.kind}`);
});

test("a. TABLE PARITY control: a doctored table is reported as different, in both directions and for a changed kind", () => {
  const file = parseRangesFile(RANGES_TXT);
  // one row removed from the Control's copy
  const without = file.filter((r) => r.cidr !== "169.254.0.0/16");
  assert.equal(without.length, file.length - 1);
  const missing = compareTables(dg.RANGES, without);
  assert.ok(missing.some((d) => d.includes("169.254.0.0/16")), `the removed row was not reported: ${missing}`);
  // one row removed from the worker's copy
  const workerWithout = dg.RANGES.filter((r) => r.cidr !== "fe80::/10");
  const extra = compareTables(workerWithout, file);
  assert.ok(extra.some((d) => d.includes("fe80::/10")), `the row the worker lacks was not reported: ${extra}`);
  // a row whose kind differs (block -> embedded) is a difference too
  const flipped = file.map((r) => (r.cidr === "100::/64" ? { ...r, kind: "embedded" } : r));
  assert.ok(compareTables(dg.RANGES, flipped).length >= 2, "a changed kind was not reported");
  // an added row
  assert.ok(compareTables(dg.RANGES, [...file, { cidr: v4(11, 0, 0, 0) + "/8", kind: "block", name: "x" }]).length >= 1, "an added row was not reported");
  // spelling-independence: the same table written differently is NOT a difference (the control that the comparison is not just "always red")
  const respelled = file.map((r) => (r.cidr === "::1/128" ? { ...r, cidr: "0:0:0:0:0:0:0:1/128" } : r));
  assert.deepEqual(compareTables(dg.RANGES, respelled), []);
});

// --- b. every row --------------------------------------------------------------------------------------

test("b. EVERY ROW of ranges.txt: block rows are refused at first/last/middle; embedded rows judge what they carry", () => {
  const file = parseRangesFile(RANGES_TXT);
  let checked = 0;
  for (const row of file) {
    if (row.kind === "block") {
      for (const a of probes(row.cidr)) {
        const d = dg.judgeAddress(a);
        assert.equal(d.blocked, true, `${row.cidr} (${row.name}): ${d.judged} was ALLOWED`);
        assert.notEqual(d.range, "", `${row.cidr}: a refusal must say which range`);
        checked++;
      }
    } else {
      const loop = dg.judgeAddress(embed(row.cidr, bits4(127, 0, 0, 1)));
      assert.equal(loop.blocked, true, `${row.cidr} (${row.name}): an embedded loopback was ALLOWED`);
      assert.match(loop.range, /loopback/, `${row.cidr}: an embedded loopback must be refused AS a loopback`);
      assert.equal(loop.judged, LOOPBACK, "an embedded address is judged by the IPv4 it carries");
      // the paired control inside the same loop
      const pub = dg.judgeAddress(embed(row.cidr, bits4(8, 8, 4, 4)));
      assert.equal(pub.blocked, false, `CONTROL FAILED: ${row.cidr}: an embedded public address was refused (${pub.range})`);
      assert.equal(pub.judged, PUBLIC);
      checked += 2;
    }
  }
  assert.ok(checked >= 90, `only ${checked} probes ran; the floor is 90`);
});

// --- c. the owner's list -------------------------------------------------------------------------------

const OWNERS_BLOCKED = {
  "0/8": [v4(0, 0, 0, 1), /this network/],
  "10/8": [v4(10, 1, 2, 3), /private/],
  "100.64/10": [v4(100, 64, 0, 1), /shared address space/],
  "127/8": [v4(127, 0, 0, 1), /loopback/],
  "cloud metadata": [v4(169, 254, 169, 254), /metadata/],
  "169.254/16": [v4(169, 254, 0, 1), /link-local/],
  "172.16/12": [v4(172, 16, 0, 1), /private/],
  "172.31 end": [v4(172, 31, 255, 255), /private/],
  "192.0.0/24": [v4(192, 0, 0, 1), /IETF protocol/],
  "192.0.2/24": [v4(192, 0, 2, 1), /TEST-NET-1/],
  "192.168/16": [v4(192, 168, 1, 1), /private/],
  "198.18/15": [v4(198, 18, 0, 1), /benchmarking/],
  "198.19 end": [v4(198, 19, 255, 255), /benchmarking/],
  "198.51.100/24": [v4(198, 51, 100, 1), /TEST-NET-2/],
  "203.0.113/24": [v4(203, 0, 113, 1), /TEST-NET-3/],
  "multicast": [v4(224, 0, 0, 1), /multicast/],
  "reserved": [v4(240, 0, 0, 1), /reserved for future use/],
  "broadcast": [v4(255, 255, 255, 255), /reserved for future use|limited broadcast/],
  "::": ["::", /unspecified/],
  "::1": ["::1", /loopback/],
  "mapped loopback": ["::ffff:7f00:1", /loopback/],
  "mapped metadata": ["::ffff:a9fe:a9fe", /link-local/],
  "NAT64 of loopback": ["64:ff9b::7f00:1", /loopback/],
  "NAT64 of private": ["64:ff9b::a00:1", /private/],
  "100::/64": ["100::1", /discard-only/],
  "2001:db8::/32": ["2001:db8::1", /documentation/],
  "6to4 of loopback": ["2002:7f00:1::1", /loopback/],
  "6to4 of metadata": ["2002:a9fe:a9fe::1", /link-local/],
  "fc00::/7": ["fc00::1", /unique local/],
  "fd00::/8": ["fd12:3456::1", /unique local/],
  "AWS IPv6 metadata": ["fd00:ec2::254", /unique local|metadata/],
  "fe80::/10": ["fe80::1", /link-local/],
  "ff00::/8": ["ff02::1", /multicast/],
  "outside 2000::/3": ["4000::1", /not global unicast/],
  "a scoped address": ["fe80::1%eth0", /scoped address/],
  "2001::/23 (Teredo)": ["2001::1", /IETF protocol/],
};

test("c. the owner's blocked addresses are refused, each for its own reason (judge and connect-time check agree)", async () => {
  const names = Object.keys(OWNERS_BLOCKED);
  assert.ok(names.length >= 30, "the owner's list shrank");
  for (const name of names) {
    const [addr, rangeRe] = OWNERS_BLOCKED[name];
    const d = dg.judgeAddress(addr);
    assert.equal(d.blocked, true, `${name}: ${addr} was ALLOWED`);
    assert.match(d.range, rangeRe, `${name}: refused for the wrong reason: ${d.range}`);
    // the same address as a HOST, through the connect-time check: refused, same reason, nothing resolved
    const lookup = fakeLookup({});
    const e = await caught(() => dg.checkDestination(addr, { lookup }));
    isBlocked(rangeRe)(e);
    assert.equal(lookup.calls.length, 0, `${name}: a literal address was looked up`);
  }
  // an embedded address is judged by what it carries, and says so
  assert.equal(dg.judgeAddress("::ffff:7f00:1").judged, LOOPBACK);
  assert.equal(dg.judgeAddress("2002:a9fe:a9fe::1").judged, METADATA);
});

test("c. CONTROL: public addresses on the other side of every IPv4 edge, public IPv6 and embedded public addresses are allowed", async () => {
  const allowed = [
    PUBLIC, PUBLIC2,
    v4(9, 255, 255, 255), v4(11, 0, 0, 0), // around 10/8
    v4(100, 63, 255, 255), v4(100, 128, 0, 0), // around 100.64/10
    v4(126, 255, 255, 255), v4(128, 0, 0, 0), // around 127/8
    v4(169, 253, 255, 255), v4(169, 255, 0, 0), // around 169.254/16
    v4(172, 15, 255, 255), v4(172, 32, 0, 0), // around 172.16/12
    v4(192, 0, 1, 0), v4(192, 0, 3, 0), // around 192.0.0/24 and 192.0.2/24
    v4(192, 167, 255, 255), v4(192, 169, 0, 0), // around 192.168/16
    v4(198, 17, 255, 255), v4(198, 20, 0, 0), // around 198.18/15
    v4(198, 51, 99, 255), v4(198, 51, 101, 0), // around 198.51.100/24
    v4(203, 0, 112, 255), v4(203, 0, 114, 0), // around 203.0.113/24
    v4(223, 255, 255, 255), // just below multicast
    "2001:4860:4860::8888",
    "2606:4700:4700::1111",
    "::ffff:808:404", // mapped, carrying the public test address
    "64:ff9b::808:404", // NAT64, carrying it
    "2002:808:404::1", // 6to4, carrying it
  ];
  assert.ok(allowed.length >= 25, "the public-control list shrank");
  for (const a of allowed) {
    const d = dg.judgeAddress(a);
    assert.equal(d.blocked, false, `CONTROL FAILED: the public address ${a} was refused (${d.range})`);
    assert.equal(d.range, "");
    assert.equal(d.allowlist, false, `${a} was allowed only by an allowlist that was never given`);
    // ... and as a host: allowed, and the address it would dial is returned
    const got = await dg.checkDestination(a, { lookup: fakeLookup({}) });
    assert.equal(got.length, 1);
  }
  // the dotted spelling of the mapped / NAT64 forms reads the same
  assert.equal(dg.judgeAddress(`::ffff:${PUBLIC}`).blocked, false);
  assert.equal(dg.judgeAddress(`64:ff9b::${PUBLIC}`).blocked, false);
  assert.equal(dg.judgeAddress(`::ffff:${LOOPBACK}`).blocked, true);
});

test("c. an unparseable address, an address object that is not one, and a name given to judgeAddress are refused", () => {
  for (const bad of ["", "not an address", "example.com", v4(999, 1, 1, 1), "::g", "1::2::3", ":::", "12345::1", "fe80::1%", `::ffff:${v4(1, 2, 3, "04")}`, undefined, null, 42, {}, { family: 4 }]) {
    const d = dg.judgeAddress(bad);
    assert.equal(d.blocked, true, `${JSON.stringify(bad)} was ALLOWED`);
    assert.match(d.range, /not an address/, `${JSON.stringify(bad)}: wrong reason ${d.range}`);
  }
});

// --- d. spellings --------------------------------------------------------------------------------------

const SPELLINGS = [
  ["2130706433", 4, bits4(127, 0, 0, 1)], // decimal
  ["0177.0.0.1", 4, bits4(127, 0, 0, 1)], // octal
  ["0x7f.1", 4, bits4(127, 0, 0, 1)], // hex, short form
  ["0x7f000001", 4, bits4(127, 0, 0, 1)], // hex, whole word
  ["127.1", 4, bits4(127, 0, 0, 1)], // short form
  ["017700000001", 4, bits4(127, 0, 0, 1)], // octal, whole word
  ["2852039166", 4, bits4(169, 254, 169, 254)], // decimal, the metadata address
  ["0xa9fea9fe", 4, bits4(169, 254, 169, 254)],
  ["0251.0376.0251.0376", 4, bits4(169, 254, 169, 254)],
  ["167772161", 4, bits4(10, 0, 0, 1)],
  ["0x0a.0.0.1", 4, bits4(10, 0, 0, 1)],
  ["[::1]", 6, 1n],
  ["::ffff:127.0.0.1", 6, (0xffffn << 32n) | bits4(127, 0, 0, 1)],
  ["134743044", 4, bits4(8, 8, 4, 4)], // a PUBLIC address in decimal: must be ALLOWED
];

test("d. SPELLINGS a resolver would read as an address parse to the same address", () => {
  for (const [text, family, bits] of SPELLINGS) {
    const got = dg.parseHostLiteral(text);
    assert.ok(got, `parseHostLiteral(${text}) read a literal as a name`);
    assert.equal(got.family, family, `${text}: family`);
    assert.equal(got.bits, bits, `${text}: read as ${got.bits.toString(16)}, want ${bits.toString(16)}`);
    assert.equal(got.zone, undefined);
  }
  // a scoped address keeps its zone (and is refused as scoped)
  assert.equal(dg.parseHostLiteral("fe80::1%eth0").zone, "eth0");
  // surrounding whitespace is ignored, as the Control ignores it
  assert.equal(dg.parseHostLiteral("  2130706433 ").bits, bits4(127, 0, 0, 1));
});

test("d. names, and strings that only look like addresses, are NOT literals", () => {
  for (const name of ["example.com", "mail.example.net", "0x7f.example.com", v4("08", 0, 0, 1), v4(1, 2, 3, 4) + ".5", v4(999, 1, 1, 1), "localhost", "a-b.c", "", v4(1, 2, 3, ""), ".1", "0x", "0xg", v4(256, 1), v4(1, 256), "4294967296", v4(0, 0, 0, 0) + ".0", "1..2", "08"]) {
    assert.equal(dg.parseHostLiteral(name), null, `${JSON.stringify(name)} was read as an address`);
  }
  // the IPv4 edges of the loose reader: the last part fills the remaining bytes, and no more
  assert.equal(dg.parseHostLiteral("4294967295").bits, bits4(255, 255, 255, 255));
  assert.equal(dg.parseHostLiteral("1.16777215").bits, bits4(1, 255, 255, 255));
  assert.equal(dg.parseHostLiteral("1.16777216"), null);
  assert.equal(dg.parseHostLiteral("1.2.65535").bits, bits4(1, 2, 255, 255));
  assert.equal(dg.parseHostLiteral("1.2.65536"), null);
  assert.equal(dg.parseHostLiteral(v4(0, 0, 0, 0)).bits, 0n);
  assert.equal(dg.parseHostLiteral("00").bits, 0n);
  assert.equal(dg.parseHostLiteral("0X7F.0.0.1").bits, bits4(127, 0, 0, 1));
});

test("d. IPv6 spellings: compression, an embedded dotted tail, brackets, and the shapes that are not addresses", () => {
  const g = (s) => dg.parseHostLiteral(s)?.bits;
  assert.equal(g("::"), 0n);
  assert.equal(g("::1"), 1n);
  assert.equal(g("1::"), 1n << 112n);
  assert.equal(g("1:2:3:4:5:6:7:8"), 0x00010002000300040005000600070008n);
  assert.equal(g("1:2:3:4:5:6:7::"), 0x00010002000300040005000600070000n);
  assert.equal(g("::2:3:4:5:6:7:8"), 0x0000000200030004000500060007_0008n);
  assert.equal(g("[2001:4860:4860::8888]"), g("2001:4860:4860:0:0:0:0:8888"));
  assert.equal(g(`::ffff:${PUBLIC}`), (0xffffn << 32n) | bits4(8, 8, 4, 4));
  assert.equal(g(`1:2:3:4:5:6:${v4(1, 2, 3, 4)}`), 0x0001000200030004000500060102_0304n);
  for (const bad of ["1:2:3:4:5:6:7", "1:2:3:4:5:6:7:8:9", "1::2:3:4:5:6:7:8", "1::2::3", ":1:2:3:4:5:6:7", "1:2:3:4:5:6:7:", `${v4(1, 2, 3, 4)}::`, "::1.2.3", `::${v4(1, 2, 3, 4)}.5`, `::ffff:${v4(1, 2, 3, 256)}`, `::ffff:${v4("01", 2, 3, 4)}`, "g::1", "[::1", "::1]x"]) {
    assert.equal(dg.parseHostLiteral(bad), null, `${bad} was read as an address`);
  }
});

test("d. every refused spelling is refused by guardedConnect WITHOUT a lookup and WITHOUT a dial; the public one is dialled as the canonical address", async () => {
  let refused = 0;
  for (const [text, , bits] of SPELLINGS) {
    const lookup = fakeLookup({ [text]: [PUBLIC] }); // if the spelling were treated as a name this answer would let it through
    const connect = fakeConnect();
    const verdict = dg.judgeAddress(text);
    if (verdict.blocked) {
      refused++;
      const e = await caught(() => dg.guardedConnect({ host: text, port: 25, lookup, connect, timeoutMs: 1000 }));
      isBlocked(verdict.range === "" ? /./ : new RegExp(verdict.range.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")))(e);
      assert.equal(e.detail.host, text, "the refusal names the host the user typed");
      assert.equal(lookup.calls.length, 0, `${text}: a literal address was resolved`);
      assert.equal(connect.dials.length, 0, `${text}: a refused literal was dialled`);
    } else {
      // CONTROL: the public decimal spelling is allowed, and what is dialled is the CANONICAL address, never the spelling
      assert.equal(bits, bits4(8, 8, 4, 4));
      const sock = await dg.guardedConnect({ host: text, port: 25, lookup, connect, timeoutMs: 1000 });
      assert.ok(sock);
      assert.deepEqual(connect.dials.map((d) => d.ip), [PUBLIC], "the dialled address must be the canonical literal, not the spelling the user typed");
      assert.equal(lookup.calls.length, 0, "a literal address was resolved");
    }
  }
  assert.ok(refused >= 10, `only ${refused} spellings were refused; the floor is 10`);
});

// --- e. names -----------------------------------------------------------------------------------------

test("e. NAMES: every address a name resolves to is judged; ANY refused address refuses the whole name", async () => {
  const answers = {
    "good.example.test": [PUBLIC],
    "dual.example.test": [PUBLIC, PUBLIC2, "2001:4860:4860::8888"],
    "mixed.example.test": [PUBLIC, PRIVATE], // the passing address must not be dialled
    "mixed-rev.example.test": [PRIVATE, PUBLIC],
    "private.example.test": [PRIVATE],
    "meta.example.test": [METADATA],
    "v6local.example.test": ["fd00:ec2::254"],
    "mapped.example.test": ["::ffff:7f00:1"],
    "scoped.example.test": ["fe80::1%eth0"],
  };
  const expectRefused = {
    "mixed.example.test": [PRIVATE, /private/],
    "mixed-rev.example.test": [PRIVATE, /private/],
    "private.example.test": [PRIVATE, /private/],
    "meta.example.test": [METADATA, /metadata/],
    "v6local.example.test": ["fd00:ec2::254", /unique local|metadata/],
    "mapped.example.test": ["::ffff:7f00:1", /loopback/],
    "scoped.example.test": ["fe80::1%eth0", /scoped/],
  };
  const lookup = fakeLookup(answers);
  assert.deepEqual(await dg.checkDestination("good.example.test", { lookup }), [PUBLIC]);
  assert.deepEqual(await dg.checkDestination("dual.example.test", { lookup }), [PUBLIC, PUBLIC2, "2001:4860:4860::8888"], "an allowed answer comes back in resolver order");
  let n = 2;
  for (const [host, [address, range]] of Object.entries(expectRefused)) {
    const e = await caught(() => dg.checkDestination(host, { lookup }));
    isBlocked(range)(e);
    assert.equal(e.detail.host, host);
    assert.equal(e.detail.address, address, `${host}: the refusal must name the refused address`);
    n++;
  }
  assert.equal(lookup.calls.length, n, "exactly one lookup per call");
});

test("e. NAMES: a refused name is never dialled, whatever its passing addresses; the lookup runs exactly once", async () => {
  for (const answer of [[PUBLIC, PRIVATE], [PRIVATE, PUBLIC], [PUBLIC, PUBLIC2, METADATA]]) {
    const lookup = fakeLookup({ "mixed.example.test": answer });
    const connect = fakeConnect();
    const e = await caught(() => dg.guardedConnect({ host: "mixed.example.test", port: 587, lookup, connect, timeoutMs: 1000 }));
    isBlocked(/private|metadata/)(e);
    assert.equal(connect.dials.length, 0, `the passing addresses of a mixed answer were dialled: ${JSON.stringify(connect.dials)}`);
    assert.equal(lookup.calls.length, 1);
  }
});

test("e. an UNRESOLVABLE name is a connect failure (ENOTFOUND), not a guard refusal; empty answers, resolver errors and odd shapes included", async () => {
  const cases = {
    "no such name": fakeLookup({}),
    "empty answer": fakeLookup({ "x.example.test": [] }),
    "resolver rejects": async () => { throw new Error("SERVFAIL"); },
    "resolver returns nothing": async () => undefined,
  };
  for (const [what, lookup] of Object.entries(cases)) {
    const e = await caught(() => dg.checkDestination("x.example.test", { lookup }));
    assert.ok(!(e instanceof DestinationBlockedError), `${what}: an unresolvable name is not a refusal`);
    assert.equal(e.code, "ENOTFOUND", `${what}: code`);
    const connect = fakeConnect();
    const e2 = await caught(() => dg.guardedConnect({ host: "x.example.test", port: 25, lookup, connect, timeoutMs: 1000 }));
    assert.equal(e2.code, "ENOTFOUND", `${what}: guardedConnect code`);
    assert.equal(connect.dials.length, 0);
  }
  // CONTROL: the same names resolving to a refused address are a REFUSAL, so "not found" is not "nothing is checked"
  const e = await caught(() => dg.checkDestination("x.example.test", { lookup: fakeLookup({ "x.example.test": [LOOPBACK] }) }));
  isBlocked(/loopback/)(e);
});

test("e. a host that carries a colon but is not an address is refused (a name cannot carry one); a name resolver answer that is not an address is refused", async () => {
  const lookup = fakeLookup({});
  const e = await caught(() => dg.checkDestination("not:an:address:::", { lookup }));
  isBlocked(/not an address and not a name/)(e);
  assert.equal(lookup.calls.length, 0);
  const e2 = await caught(() => dg.checkDestination("odd.example.test", { lookup: fakeLookup({ "odd.example.test": ["not-an-address"] }) }));
  isBlocked(/not an address/)(e2);
  // the default resolver is the system one: a *.invalid name never resolves (RFC 6761), and the failure is ENOTFOUND-shaped
  const e3 = await caught(() => dg.checkDestination("nothing.example.invalid"));
  assert.equal(e3.code, "ENOTFOUND");
});

// --- f. rebinding and the post-connect check --------------------------------------------------------------

test("f. REBINDING FIXTURE: the judged IP is the dialled IP, and the second answer is never consulted", async () => {
  // public the first time, the metadata address every time after
  const lookup = fakeLookup((host, n) => [{ address: n === 1 ? PUBLIC : METADATA, family: 4 }]);
  const connect = fakeConnect();
  const sock = await dg.guardedConnect({ host: "rebind.example.test", port: 587, lookup, connect, timeoutMs: 1000 });
  assert.equal(sock, connect.sockets[0], "guardedConnect did not return the socket it dialled");
  assert.deepEqual(connect.dials, [{ ip: PUBLIC, port: 587, tlsOptions: null }], "it must dial exactly the address it judged — never the hostname");
  assert.equal(lookup.calls.length, 1, "the name was resolved more than once: a second resolution is a rebinding window");
  assert.equal(sock.destroyed, false);
  // CONTROL: the fixture's second answer IS poisonous — a second call (which resolves again) is refused, so the
  // single resolution above is what protected the first call, not a fixture that never rebinds.
  const e = await caught(() => dg.guardedConnect({ host: "rebind.example.test", port: 587, lookup, connect, timeoutMs: 1000 }));
  isBlocked(/metadata/)(e);
  assert.equal(connect.dials.length, 1, "the poisoned second answer was dialled");
  assert.equal(lookup.calls.length, 2);
});

test("f. the dial target is always an IP literal: the hostname never reaches the socket layer", async () => {
  const lookup = fakeLookup({ "mail.example.test": [PUBLIC, "2001:4860:4860::8888"] });
  const connect = fakeConnect((ip) => new FakeSocket(ip, ip === PUBLIC ? { fail: Object.assign(new Error("refused"), { code: "ECONNREFUSED" }) } : {}));
  const sock = await dg.guardedConnect({ host: "mail.example.test", port: 25, lookup, connect, timeoutMs: 1000 });
  assert.ok(sock);
  for (const d of connect.dials) assert.notEqual(net.isIP(d.ip), 0, `${d.ip} is not an IP literal`);
  assert.deepEqual(connect.dials.map((d) => d.ip), [PUBLIC, "2001:4860:4860::8888"], "addresses are tried in resolver order, each judged first");
  assert.equal(lookup.calls.length, 1);
  assert.equal(connect.sockets[0].destroyed, true, "the failed attempt's socket was not released");
  assert.equal(connect.sockets[1].destroyed, false);
});

test("f. POST-CONNECT CHECK: a socket whose remote address is private although the dialled IP was public is destroyed and refused", async () => {
  for (const [what, remote, range] of [
    ["a private address", PRIVATE, /private/],
    ["the metadata address", METADATA, /metadata/],
    ["a mapped loopback", "::ffff:7f00:1", /loopback/],
    ["no address at all", undefined, /not an address/],
    ["something that is not an address", "garbage", /not an address/],
  ]) {
    const lookup = fakeLookup({ "diverge.example.test": [PUBLIC] });
    const connect = fakeConnect((ip) => new FakeSocket(remote));
    const e = await caught(() => dg.guardedConnect({ host: "diverge.example.test", port: 25, lookup, connect, timeoutMs: 1000 }));
    isBlocked(range)(e);
    assert.equal(e.detail.host, "diverge.example.test");
    assert.equal(connect.dials.length, 1);
    assert.equal(connect.sockets[0].destroyed, true, `${what}: the socket that landed on a refused address was not destroyed`);
  }
  // CONTROL: the same seam with a PUBLIC remote address succeeds and returns the socket, undestroyed
  const lookup = fakeLookup({ "diverge.example.test": [PUBLIC] });
  const connect = fakeConnect((ip) => new FakeSocket(ip));
  const sock = await dg.guardedConnect({ host: "diverge.example.test", port: 25, lookup, connect, timeoutMs: 1000 });
  assert.equal(sock, connect.sockets[0]);
  assert.equal(sock.destroyed, false);
  assert.equal(sock.remoteAddress, PUBLIC);
});

test("f. the post-connect check also covers an implicit-TLS socket, at connect and again at secureConnect", async () => {
  const lookup = fakeLookup({ "tls.example.test": [PUBLIC] });
  // lands on a private address; announced only by secureConnect
  const bad = fakeConnect((ip) => new FakeSocket(PRIVATE, { event: "secureConnect" }));
  const e = await caught(() => dg.guardedConnect({ host: "tls.example.test", port: 465, tls: true, lookup, connect: bad, timeoutMs: 1000 }));
  isBlocked(/private/)(e);
  assert.equal(bad.sockets[0].destroyed, true);
  // CONTROL: a public remote address, secureConnect only: resolves
  const good = fakeConnect((ip) => new FakeSocket(ip, { event: "secureConnect" }));
  assert.equal(await dg.guardedConnect({ host: "tls.example.test", port: 465, tls: true, lookup, connect: good, timeoutMs: 1000 }), good.sockets[0]);
  // a plain "connect" alone does NOT complete a TLS dial (the handshake has not happened yet)
  const half = fakeConnect((ip) => new FakeSocket(ip, { event: "connect" }));
  const e2 = await caught(() => dg.guardedConnect({ host: "tls.example.test", port: 465, tls: true, lookup, connect: half, timeoutMs: 80 }));
  assert.equal(e2.code, "ETIMEDOUT");
  assert.equal(half.sockets[0].destroyed, true);
});

test("f. certificate verification stays against the ORIGINAL hostname: the TLS dial carries servername=host and the caller's rejectUnauthorized", async () => {
  const lookup = fakeLookup({ "mail.example.test": [PUBLIC] });
  for (const reject of [true, false]) {
    const connect = fakeConnect((ip, port, tlsOptions) => new FakeSocket(ip, { event: "secureConnect" }));
    await dg.guardedConnect({ host: "mail.example.test", port: 465, tls: true, servername: "mail.example.test", rejectUnauthorized: reject, lookup, connect, timeoutMs: 1000 });
    assert.deepEqual(connect.dials, [{ ip: PUBLIC, port: 465, tlsOptions: { host: PUBLIC, port: 465, servername: "mail.example.test", rejectUnauthorized: reject } }]);
  }
  // servername defaults to the host; rejectUnauthorized defaults to true (never silently off)
  const connect = fakeConnect((ip) => new FakeSocket(ip, { event: "secureConnect" }));
  await dg.guardedConnect({ host: "mail.example.test", port: 465, tls: true, lookup, connect, timeoutMs: 1000 });
  assert.equal(connect.dials[0].tlsOptions.servername, "mail.example.test");
  assert.equal(connect.dials[0].tlsOptions.rejectUnauthorized, true);
  // a plain dial carries no TLS options
  const plain = fakeConnect();
  await dg.guardedConnect({ host: "mail.example.test", port: 25, lookup, connect: plain, timeoutMs: 1000 });
  assert.equal(plain.dials[0].tlsOptions, null);
});

test("f. TIMEOUT: timeoutMs bounds the whole call (the resolver and the connect) and rejects with ETIMEDOUT; a prompt call is unaffected", async () => {
  // a connect that never completes
  const hang = fakeConnect(() => new FakeSocket(PUBLIC, { never: true }));
  const e = await caught(() => dg.guardedConnect({ host: "slow.example.test", port: 25, lookup: fakeLookup({ "slow.example.test": [PUBLIC] }), connect: hang, timeoutMs: 80 }));
  assert.equal(e.code, "ETIMEDOUT");
  assert.ok(!(e instanceof DestinationBlockedError));
  assert.equal(hang.sockets[0].destroyed, true, "the socket that timed out was not destroyed");
  // a resolver that never answers
  const lookup = async () => new Promise(() => {});
  const connect = fakeConnect();
  const t0 = Date.now();
  const e2 = await caught(() => dg.guardedConnect({ host: "slow-dns.example.test", port: 25, lookup, connect, timeoutMs: 80 }));
  assert.equal(e2.code, "ETIMEDOUT");
  assert.ok(Date.now() - t0 < 2000, "the call outlived its timeout");
  assert.equal(connect.dials.length, 0);
  // a resolver that answers AFTER the timeout must not leave a connection behind
  let release;
  const late = async () => new Promise((r) => { release = () => r([{ address: PUBLIC, family: 4 }]); });
  const lateConnect = fakeConnect();
  const e3 = await caught(() => dg.guardedConnect({ host: "late.example.test", port: 25, lookup: late, connect: lateConnect, timeoutMs: 60 }));
  assert.equal(e3.code, "ETIMEDOUT");
  release();
  await sleep(50);
  assert.equal(lateConnect.dials.length, 0, "a connection was opened after the call had already timed out");
  // CONTROL: the same timeout does not fire on a prompt connection
  const ok = fakeConnect();
  assert.ok(await dg.guardedConnect({ host: "fast.example.test", port: 25, lookup: fakeLookup({ "fast.example.test": [PUBLIC] }), connect: ok, timeoutMs: 80 }));
  await sleep(120);
  assert.equal(ok.sockets[0].destroyed, false, "the timeout timer outlived a successful connection and destroyed it");
});

test("f. when every address fails to connect the last socket error is the one reported (it maps to connect_refused upstream)", async () => {
  const lookup = fakeLookup({ "down.example.test": [PUBLIC, PUBLIC2] });
  const connect = fakeConnect((ip) => new FakeSocket(ip, { fail: Object.assign(new Error("refused"), { code: "ECONNREFUSED" }) }));
  const e = await caught(() => dg.guardedConnect({ host: "down.example.test", port: 25, lookup, connect, timeoutMs: 1000 }));
  assert.equal(e.code, "ECONNREFUSED");
  assert.deepEqual(connect.dials.map((d) => d.ip), [PUBLIC, PUBLIC2]);
  assert.ok(connect.sockets.every((s) => s.destroyed));
  assert.equal(lookup.calls.length, 1);
});

// --- g. the allowlist ------------------------------------------------------------------------------------

test("g. ALLOWLIST: a named range is admitted (a name resolving into it connects); its neighbours, the metadata address and loopback are still refused", async () => {
  const allow = [`${v4(10, 1, 0, 0)}/16`, "fd00:1::/32"];
  const sink = v4(10, 1, 0, 9);
  // admitted
  const lookup = fakeLookup({ "sink.example.test": [sink], "sink6.example.test": ["fd00:1::9"] });
  const connect = fakeConnect();
  const sock = await dg.guardedConnect({ host: "sink.example.test", port: 25, allow, lookup, connect, timeoutMs: 1000 });
  assert.equal(sock, connect.sockets[0]);
  assert.deepEqual(connect.dials.map((d) => d.ip), [sink], "the allowlisted address was not dialled");
  assert.ok(await dg.guardedConnect({ host: "sink6.example.test", port: 25, allow, lookup, connect, timeoutMs: 1000 }));
  // judged as allowed BY the allowlist, and for nothing else
  assert.deepEqual(dg.judgeAddress(sink, allow), { blocked: false, range: "", judged: sink, allowlist: true });
  assert.equal(dg.judgeAddress(PUBLIC, allow).allowlist, false, "a public address is not allowed BY the allowlist");
  // still refused: the neighbours, the metadata address, loopback, another private range, another ULA
  for (const [what, addr, range] of [
    ["below the range", v4(10, 0, 255, 255), /private/],
    ["above the range", v4(10, 2, 0, 0), /private/],
    ["the metadata address", METADATA, /metadata/],
    ["loopback", LOOPBACK, /loopback/],
    ["another private range", v4(192, 168, 0, 1), /private/],
    ["another ULA", "fd00:2::1", /unique local/],
    ["the IPv6 metadata address", "fd00:ec2::254", /unique local|metadata/],
  ]) {
    const c = fakeConnect();
    const e = await caught(() => dg.guardedConnect({ host: addr, port: 25, allow, lookup: fakeLookup({}), connect: c, timeoutMs: 1000 }));
    isBlocked(range)(e);
    assert.equal(c.dials.length, 0, `${what}: dialled despite not being allowlisted`);
  }
  // a mixed answer still needs EVERY address named
  const mixed = fakeLookup({ "mixed.example.test": [sink, v4(10, 2, 0, 9)] });
  const c2 = fakeConnect();
  isBlocked(/private/)(await caught(() => dg.guardedConnect({ host: "mixed.example.test", port: 25, allow, lookup: mixed, connect: c2, timeoutMs: 1000 })));
  assert.equal(c2.dials.length, 0);
  // an embedded address is judged by what it carries, against the allowlist
  assert.equal(dg.judgeAddress("::ffff:a01:9", allow).blocked, false);
  assert.equal(dg.judgeAddress("::ffff:a02:9", allow).blocked, true);
});

test("g. CONTROL: with the empty allowlist (production) the very same sink address is refused, at the check and at the post-connect judgement", async () => {
  const sink = v4(10, 1, 0, 9);
  for (const allow of [undefined, null, [], ["fd00:1::/32"]]) {
    const connect = fakeConnect();
    const e = await caught(() => dg.guardedConnect({ host: "sink.example.test", port: 25, allow, lookup: fakeLookup({ "sink.example.test": [sink] }), connect, timeoutMs: 1000 }));
    isBlocked(/private/)(e);
    assert.equal(e.detail.address, sink);
    assert.equal(connect.dials.length, 0);
  }
  // the post-connect judgement honours the allowlist too: a socket landing in the allowlisted range is fine ...
  const allow = [`${v4(10, 1, 0, 0)}/16`];
  const ok = fakeConnect((ip) => new FakeSocket(sink));
  assert.ok(await dg.guardedConnect({ host: "sink.example.test", port: 25, allow, lookup: fakeLookup({ "sink.example.test": [sink] }), connect: ok, timeoutMs: 1000 }));
  // ... and one landing anywhere else is destroyed
  const off = fakeConnect((ip) => new FakeSocket(v4(10, 2, 0, 9)));
  isBlocked(/private/)(await caught(() => dg.guardedConnect({ host: "sink.example.test", port: 25, allow, lookup: fakeLookup({ "sink.example.test": [sink] }), connect: off, timeoutMs: 1000 })));
  assert.equal(off.sockets[0].destroyed, true);
  // parsed prefixes are accepted wherever strings are
  const parsed = dg.parseAllowList(allow);
  assert.ok(await dg.guardedConnect({ host: "sink.example.test", port: 25, allow: parsed, lookup: fakeLookup({ "sink.example.test": [sink] }), connect: fakeConnect(), timeoutMs: 1000 }));
});

test("g. parseAllowList: CIDRs only, masked, bounded; undefined / null / [] is the production default", () => {
  for (const [what, bad] of [
    ["a bare IPv4 address", [v4(10, 0, 0, 1)]],
    ["a bare IPv6 address", ["::1"]],
    ["not a CIDR", ["not a cidr"]],
    ["a prefix length too long", [`${v4(10, 0, 0, 0)}/33`]],
    ["an IPv6 prefix length too long", ["::/129"]],
    ["a zone", ["fe80::1%eth0/64"]],
    ["a loose IPv4 spelling", ["0x7f.1/8"]],
    ["a leading zero in the length", [`${v4(10, 0, 0, 0)}/08`]],
    ["an empty entry", [""]],
    ["a non-string entry", [42]],
    ["a name", ["example.com/24"]],
    ["a good entry then a bad one", [`${v4(10, 0, 0, 0)}/8`, "x"]],
  ]) {
    assert.throws(() => dg.parseAllowList(bad), Error, `${what}: the allowlist accepted ${JSON.stringify(bad)}`);
  }
  assert.throws(() => dg.parseAllowList("10.0.0.0/8"), TypeError, "a string is not a list");
  // the bound: 64 is fine, 65 is not
  const entry = `${v4(10, 0, 0, 0)}/8`;
  assert.equal(dg.parseAllowList(new Array(dg.MAX_ALLOW_ENTRIES).fill(entry)).length, 64);
  assert.throws(() => dg.parseAllowList(new Array(dg.MAX_ALLOW_ENTRIES + 1).fill(entry)), RangeError);
  // the production default
  for (const empty of [undefined, null, []]) assert.deepEqual(dg.parseAllowList(empty), []);
  // masked, canonical, both families
  const [a, b] = dg.parseAllowList([`${v4(10, 1, 2, 3)}/16`, "fd00:1:2::5/32"]);
  assert.equal(a.cidr, `${v4(10, 1, 0, 0)}/16`);
  assert.equal(a.bits, bits4(10, 1, 0, 0));
  assert.equal(a.len, 16);
  assert.equal(b.cidr, "fd00:1::/32");
  assert.equal(b.family, 6);
  // surrounding whitespace is tolerated, as the Control tolerates it
  assert.equal(dg.parseAllowList([` ${v4(10, 1, 0, 0)}/16 `])[0].cidr, `${v4(10, 1, 0, 0)}/16`);
});

// --- h. a real socket ------------------------------------------------------------------------------------

test("h. REAL SOCKET: allowlisted, a loopback listener sees exactly one connection; with NO allowlist it sees ZERO", async () => {
  const allowed = await listen();
  const blocked = await listen();
  try {
    // allowed: one real connection, landing on the loopback address
    const sock = await dg.guardedConnect({ host: LOOPBACK, port: allowed.port, allow: LOOPBACK_ALLOW, timeoutMs: 3000 });
    assert.equal(sock.remoteAddress, LOOPBACK);
    assert.ok(await until(() => allowed.connections === 1), "the listener never saw the allowed connection");
    sock.destroy();
    await sleep(100);
    assert.equal(allowed.connections, 1, "exactly one connection was expected");

    // refused: the same call, no allowlist
    const e = await caught(() => dg.guardedConnect({ host: LOOPBACK, port: blocked.port, timeoutMs: 3000 }));
    isBlocked(/loopback/)(e);
    await sleep(200);
    assert.equal(blocked.connections, 0, "the refused destination was connected to anyway");
    // CONTROL: this listener does count connections (so zero above is an observation, not a broken counter)
    const raw = net.connect({ host: LOOPBACK, port: blocked.port });
    raw.on("error", () => {});
    assert.ok(await until(() => blocked.connections === 1), "the control connection was not counted: the zero above proves nothing");
    raw.destroy();
  } finally {
    await allowed.close();
    await blocked.close();
  }
});

test("h. REAL SOCKET through a name: the resolver seam says loopback; allowlisted it connects (one connection), not allowlisted it is refused before any dial", async () => {
  const srv = await listen();
  try {
    const lookup = fakeLookup({ "sink.example.test": [LOOPBACK] });
    const e = await caught(() => dg.guardedConnect({ host: "sink.example.test", port: srv.port, lookup, timeoutMs: 3000 }));
    isBlocked(/loopback/)(e);
    await sleep(150);
    assert.equal(srv.connections, 0);
    const sock = await dg.guardedConnect({ host: "sink.example.test", port: srv.port, allow: LOOPBACK_ALLOW, lookup, timeoutMs: 3000 });
    assert.ok(await until(() => srv.connections === 1));
    sock.destroy();
    assert.equal(lookup.calls.length, 2, "one resolution per call");
  } finally {
    await srv.close();
  }
});

test("h. REAL SOCKET: a closed port is a connect failure (ECONNREFUSED), not a refusal; a real implicit-TLS dial verifies and a refused one never reaches the TLS server", async () => {
  // a closed port on an allowlisted loopback: an ordinary connect error
  const probe = await listen();
  const closedPort = probe.port;
  await probe.close();
  const e = await caught(() => dg.guardedConnect({ host: LOOPBACK, port: closedPort, allow: LOOPBACK_ALLOW, timeoutMs: 3000 }));
  assert.ok(!(e instanceof DestinationBlockedError));
  assert.equal(e.code, "ECONNREFUSED");

  // implicit TLS against a real TLS server with the dev sink certificate
  const certDir = new URL("../../../secrets/dev/certs/", import.meta.url);
  const server = tls.createServer({ cert: fs.readFileSync(new URL("sink.crt", certDir)), key: fs.readFileSync(new URL("sink.key", certDir)) }, (s) => s.on("error", () => {}));
  let tcp = 0;
  let secure = 0;
  server.on("connection", (s) => { tcp++; s.on("error", () => {}); });
  server.on("secureConnection", () => { secure++; });
  server.on("tlsClientError", () => {});
  await new Promise((r) => server.listen(0, "127.0.0.1", r));
  const port = server.address().port;
  try {
    const refused = await caught(() => dg.guardedConnect({ host: LOOPBACK, port, tls: true, rejectUnauthorized: false, timeoutMs: 3000 }));
    isBlocked(/loopback/)(refused);
    await sleep(150);
    assert.equal(tcp, 0, "a refused implicit-TLS destination was connected to");
    const sock = await dg.guardedConnect({ host: LOOPBACK, port, tls: true, servername: "sink.example.test", rejectUnauthorized: false, allow: LOOPBACK_ALLOW, timeoutMs: 3000 });
    assert.equal(sock.encrypted, true);
    assert.equal(sock.remoteAddress, LOOPBACK);
    assert.ok(await until(() => secure === 1));
    sock.destroy();
    // CONTROL: with certificate verification ON the dev certificate for another name is rejected, as before the guard
    const strict = await caught(() => dg.guardedConnect({ host: LOOPBACK, port, tls: true, servername: "mail.example.test", rejectUnauthorized: true, allow: LOOPBACK_ALLOW, timeoutMs: 3000 }));
    assert.ok(!(strict instanceof DestinationBlockedError), `certificate verification failed for the wrong reason: ${strict.message}`);
  } finally {
    await new Promise((r) => server.close(r));
  }
});

// --- i. runSmtpSession -----------------------------------------------------------------------------------

const CANARY = "canary-destguard-secret-9Z";

function jobFor(host, port, extra = {}) {
  return {
    smtp: { host, port, username: "user@ourdomain.com", security: "starttls", ...extra },
    envelopeFrom: "user@ourdomain.com",
    recipient: "alice@example.com",
    dataBytes: "From: <user@ourdomain.com>\r\nTo: alice@example.com\r\nSubject: t\r\n\r\nhi",
  };
}

async function runWith(job, opts = {}) {
  const cred = new MemoryCredential(CANARY);
  try {
    return { result: await runSmtpSession(job, cred, { timeoutMs: 5000, ...opts }), error: null };
  } catch (e) {
    return { result: null, error: e };
  } finally {
    cred.zero();
  }
}

test("i. runSmtpSession: a loopback host with NO allowlist is destination_blocked, with an EMPTY smtp_response, and the listener sees ZERO connections", async () => {
  const srv = await listen();
  const sink = await startSink({ authResult: "ok" });
  try {
    const { result, lines } = await quietly(() => runWith(jobFor(LOOPBACK, srv.port)));
    const { error } = result;
    assert.ok(error instanceof SMTPError, `want an SMTPError, got ${error}`);
    assert.equal(error.reason, "destination_blocked");
    assert.equal(error.smtpResponse, "", "nothing from inside the network may reach the user");
    assert.equal(error.message, "destination_blocked");
    await sleep(150);
    assert.equal(srv.connections, 0, "the refused host was connected to");
    // the detail went to the worker's own log, one line, and nowhere else
    assert.equal(lines.length, 1);
    assert.match(lines[0], /destination refused/);
    assert.match(lines[0], /loopback/);
    assert.ok(!lines.join("\n").includes(CANARY));
    const blob = `${error.message}\n${error.reason}\n${error.smtpResponse}\n${JSON.stringify(error, Object.getOwnPropertyNames(error))}`;
    assert.ok(!blob.includes("loopback"), "the range leaked into the error");
    // the same session against a real sink, no allowlist: nothing sent at all
    const r2 = await quietly(() => runWith(jobFor(LOOPBACK, sink.port)));
    assert.equal(r2.result.error?.reason, "destination_blocked");
    assert.equal(sink.received.length, 0, "the sink saw traffic from a refused session");

    // CONTROL: with the loopback range allowlisted the SAME session proceeds exactly as before
    const ok = await runWith(jobFor(LOOPBACK, sink.port, { destAllowCidrs: LOOPBACK_ALLOW }));
    assert.equal(ok.error, null, `the allowlisted session failed: ${ok.error?.reason}`);
    assert.match(ok.result.smtpResponse, /^250/);
    for (const verb of [/EHLO/i, /STARTTLS/i, /AUTH/i, /MAIL FROM/i, /RCPT TO/i, /DATA/i]) {
      assert.ok(sink.received.some((l) => verb.test(l)), `the allowlisted session never sent ${verb}`);
    }
  } finally {
    await srv.close();
    await sink.close();
  }
});

test("i. runSmtpSession: a NAME that resolves into the operator's network is destination_blocked and the error carries none of it", async () => {
  const lookup = fakeLookup({ "internal.example.test": [PUBLIC, PRIVATE] });
  const connect = fakeConnect();
  const { result, lines } = await quietly(() => runWith(jobFor("internal.example.test", 587), { destGuard: { lookup, connect } }));
  const { error } = result;
  assert.ok(error instanceof SMTPError);
  assert.equal(error.reason, "destination_blocked");
  assert.equal(error.smtpResponse, "");
  assert.equal(connect.dials.length, 0);
  assert.equal(lookup.calls.length, 1);
  const blob = `${error.message}\n${error.reason}\n${error.smtpResponse}\n${JSON.stringify(error, Object.getOwnPropertyNames(error))}`;
  assert.ok(!blob.includes(PRIVATE), "the internal address leaked into the error");
  assert.ok(lines[0].includes(PRIVATE) && lines[0].includes("internal.example.test"), "the log line must name the host and the address for the operator");
  // an implicit-TLS session is refused the same way
  const tlsJob = jobFor("internal.example.test", 465);
  tlsJob.smtp.security = "implicit_tls";
  const tlsRun = await quietly(() => runWith(tlsJob, { destGuard: { lookup, connect } }));
  assert.equal(tlsRun.result.error?.reason, "destination_blocked");
  assert.equal(connect.dials.length, 0);
});

test("i. runSmtpSession: an OS-layer divergence (a public name whose socket lands on loopback) is destination_blocked, and no credential-bearing byte is sent", async () => {
  const sink = await startSink({ authResult: "ok" });
  try {
    const lookup = fakeLookup({ "diverge.example.test": [PUBLIC] });
    // the seam dials the REAL sink, as an OS that disagreed with the resolver would
    const connect = fakeConnect((ip, port) => net.connect({ host: LOOPBACK, port: sink.port }));
    const { result } = await quietly(() => runWith(jobFor("diverge.example.test", 587), { destGuard: { lookup, connect } }));
    assert.equal(result.error?.reason, "destination_blocked");
    assert.equal(result.error?.smtpResponse, "");
    assert.equal(connect.sockets[0].destroyed, true, "the socket on the refused address was not destroyed");
    await sleep(100);
    assert.ok(!sink.received.some((l) => /EHLO|HELO|AUTH|MAIL|RCPT|DATA/i.test(l)), "an SMTP command reached a refused destination");
  } finally {
    await sink.close();
  }
});

test("i. runSmtpSession: a malformed allowlist refuses the session (fail closed) before any connection", async () => {
  for (const bad of [["not a cidr"], [v4(10, 0, 0, 1)], "127.0.0.0/8", [`${v4(127, 0, 0, 0)}/33`], new Array(65).fill("127.0.0.0/8")]) {
    const srv = await listen();
    try {
      const { result, lines } = await quietly(() => runWith(jobFor(LOOPBACK, srv.port, { destAllowCidrs: bad })));
      assert.equal(result.error?.reason, "destination_blocked", `${JSON.stringify(bad).slice(0, 40)}: wrong reason`);
      assert.equal(result.error?.smtpResponse, "");
      assert.match(lines.join("\n"), /allowlist is malformed/);
      await sleep(100);
      assert.equal(srv.connections, 0);
    } finally {
      await srv.close();
    }
  }
});

test("i. the other failures keep the reasons they always had: connect_refused, timeout, unresolvable -> connect_refused, starttls_unavailable", async () => {
  // connection refused on an allowlisted loopback port nobody listens on
  const probe = await listen();
  const closedPort = probe.port;
  await probe.close();
  const refused = await runWith(jobFor(LOOPBACK, closedPort, { destAllowCidrs: LOOPBACK_ALLOW }));
  assert.equal(refused.error?.reason, "connect_refused");
  assert.equal(refused.error?.smtpResponse, "");

  // an unresolvable name (the resolver seam rejects) is a connect failure, not a refusal
  const gone = await runWith(jobFor("gone.example.test", 25), { destGuard: { lookup: fakeLookup({}), connect: fakeConnect() } });
  assert.equal(gone.error?.reason, "connect_refused");

  // a connect that never completes within timeoutMs is "timeout"
  const lookup = fakeLookup({ "slow.example.test": [PUBLIC] });
  const hang = fakeConnect(() => new FakeSocket(PUBLIC, { never: true }));
  const slow = await runWith(jobFor("slow.example.test", 25), { timeoutMs: 80, destGuard: { lookup, connect: hang } });
  assert.equal(slow.error?.reason, "timeout");
  assert.equal(hang.sockets[0].destroyed, true);

  // an implicit-TLS dial carries the ORIGINAL hostname for certificate verification and verifies by default
  const spy = fakeConnect(() => new FakeSocket(PUBLIC, { never: true }));
  const job = jobFor("mail.example.test", 465);
  job.smtp.security = "implicit_tls";
  await runWith(job, { timeoutMs: 60, destGuard: { lookup: fakeLookup({ "mail.example.test": [PUBLIC] }), connect: spy } });
  assert.deepEqual(spy.dials[0].tlsOptions, { host: PUBLIC, port: 465, servername: "mail.example.test", rejectUnauthorized: true });

  // the existing no-STARTTLS abort-before-AUTH behaviour, through the guard
  const sink = await startSink({ advertiseStartTLS: false });
  try {
    const noTls = await runWith(jobFor(LOOPBACK, sink.port, { destAllowCidrs: LOOPBACK_ALLOW }));
    assert.equal(noTls.error?.reason, "starttls_unavailable");
    assert.ok(!sink.received.some((l) => /AUTH/i.test(l)));
  } finally {
    await sink.close();
  }
});

test("i. the STARTTLS upgrade still works through the moved helper, and its failure is starttls_failed", async () => {
  // a real session: the sink upgrades to TLS and the session completes (also covered by smtp.test.mjs; asserted here
  // because upgradeToTLS now lives in destguard.js)
  const sink = await startSink({ authResult: "ok" });
  try {
    const ok = await runWith(jobFor(LOOPBACK, sink.port, { destAllowCidrs: LOOPBACK_ALLOW }));
    assert.equal(ok.error, null);
    assert.ok(sink.tlsWasActive(), "the session never upgraded to TLS");
  } finally {
    await sink.close();
  }
  // a server that says 220 to STARTTLS and then speaks garbage: the handshake fails, and that is starttls_failed
  const server = net.createServer((s) => {
    let upgraded = false;
    s.on("error", () => {});
    s.write("220 hello\r\n");
    s.on("data", (d) => {
      if (upgraded) {
        // the client's TLS ClientHello arrives here: answer with something that is not TLS and hang up
        s.end("this is not a TLS handshake\r\n");
        return;
      }
      const line = d.toString();
      if (/^EHLO/i.test(line)) s.write("250-hello\r\n250-STARTTLS\r\n250 AUTH LOGIN\r\n");
      else if (/^STARTTLS/i.test(line)) {
        upgraded = true;
        s.write("220 go ahead\r\n");
      }
    });
  });
  await new Promise((r) => server.listen(0, "127.0.0.1", r));
  try {
    const bad = await runWith(jobFor(LOOPBACK, server.address().port, { destAllowCidrs: LOOPBACK_ALLOW }));
    assert.equal(bad.error?.reason, "starttls_failed");
    assert.equal(bad.error?.smtpResponse, "");
  } finally {
    server.closeAllConnections?.();
    await new Promise((r) => server.close(r));
  }
});

// --- k. the source gate ---------------------------------------------------------------------------------------

const DIAL_RE = /\b(net|tls)\.connect\s*\(|createConnection\s*\(|\bnew\s+(net\.)?Socket\s*\(|\.connect\s*\(\s*\{/;

// scanDials returns "name:line" for every non-comment line that opens a connection.
function scanDials(name, text) {
  const out = [];
  text.split("\n").forEach((line, i) => {
    const t = line.trim();
    if (t.startsWith("//") || t.startsWith("*") || t.startsWith("/*")) return;
    if (DIAL_RE.test(line)) out.push(`${name}:${i + 1}`);
  });
  return out;
}

const SRC_DIR = fileURLToPath(new URL("../src/", import.meta.url));
// destguard.js is THE guarded dialer; stream.js is the worker's mutual-TLS stream to its own Control.
const DIAL_ALLOWED = ["destguard.js", "stream.js"];

test("k. SOURCE GATE: no file in src/ other than destguard.js and stream.js opens a connection", () => {
  const files = fs.readdirSync(SRC_DIR).filter((f) => f.endsWith(".js"));
  assert.ok(files.length >= 8, `the gate read only ${files.length} source files; the floor is 8 — it is not seeing the tree`);
  assert.ok(files.includes("smtp.js") && files.includes("destguard.js") && files.includes("stream.js"));
  const offending = [];
  const hits = new Set();
  for (const f of files) {
    const sites = scanDials(f, fs.readFileSync(SRC_DIR + f, "utf8"));
    if (sites.length > 0 && DIAL_ALLOWED.includes(f)) hits.add(f);
    else offending.push(...sites);
  }
  assert.deepEqual(offending, [], `UNGUARDED DIAL: a user-chosen host must only ever be dialled through destguard.js: ${offending}`);
  for (const f of DIAL_ALLOWED) assert.ok(hits.has(f), `${f} is on the dial allowlist but has no dial site: a stale entry hides the next real one`);
});

test("k. SOURCE GATE control: the scanner flags every form of dial, in a clean file's lookalike and in the real smtp.js with one line added; it skips comments", () => {
  const bad = {
    "net.connect": "const s = net.connect({ host, port });\n",
    "tls.connect": "const s = tls.connect({ host, port });\n",
    "tls.connect over a socket": "const s = tls.connect({ socket: plain });\n",
    "createConnection": "const s = createConnection(port, host);\n",
    "new Socket": "const s = new Socket();\n",
    "new net.Socket": "const s = new net.Socket();\n",
    "an object connect": "s.connect({ port });\n",
    "spaced": "const s = net.connect ({ host, port });\n",
  };
  for (const [what, text] of Object.entries(bad)) {
    assert.equal(scanDials("fixture.js", text).length, 1, `CONTROL FAILED: the scanner missed ${what}`);
  }
  const clean = "// net.connect({ host, port }) is mentioned in a comment\n/* tls.connect({}) */\n * createConnection(port)\nconst x = guardedConnect({ host, port });\nawait connectPlain(host, port);\n";
  assert.deepEqual(scanDials("fixture.js", clean), [], "a comment or a guarded call was flagged");
  // the real smtp.js is clean, and the same text with one dial added is caught at the line it was added on
  const smtp = fs.readFileSync(SRC_DIR + "smtp.js", "utf8");
  assert.deepEqual(scanDials("smtp.js", smtp), []);
  const injected = smtp + "\nconst leak = net.connect({ host, port });\n";
  assert.equal(scanDials("smtp.js", injected).length, 1);
});
