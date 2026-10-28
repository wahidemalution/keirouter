import { test } from "node:test";
import assert from "node:assert/strict";
import { fmtTime, serverDateISO, serverIana, setServerZone, startOfServerDay, zonedParts } from "./serverTime.ts";

// 2026-10-28T20:30:00Z is 03:30 on Oct 29 in UTC+7.
const at = Date.UTC(2026, 9, 28, 20, 30, 0);

test("a whole-hour offset formats in that zone", () => {
  setServerZone("", 7 * 3600);
  assert.equal(fmtTime(at, { hour: "2-digit", minute: "2-digit", hour12: false }, "en-GB"), "03:30");
  assert.equal(serverDateISO(at), "2026-10-29");
  assert.deepEqual(zonedParts(at), { year: 2026, month: 10, day: 29, hour: 3, minute: 30, second: 0 });
  assert.equal(serverIana(), "", "no IANA name means the server picks its own zone");
});

test("startOfServerDay is midnight in the server zone", () => {
  setServerZone("", 7 * 3600);
  assert.equal(new Date(startOfServerDay(at)).toISOString(), "2026-10-28T17:00:00.000Z");
});

test("an IANA name wins over the offset", () => {
  setServerZone("Asia/Kolkata", 0);
  assert.equal(serverIana(), "Asia/Kolkata");
  assert.equal(fmtTime(at, { hour: "2-digit", minute: "2-digit", hour12: false }, "en-GB"), "02:00");
});

test("an invalid IANA name falls back to the offset, and UTC works", () => {
  setServerZone("Not/AZone", 0);
  assert.equal(serverIana(), "");
  assert.equal(serverDateISO(at), "2026-10-28");
});
