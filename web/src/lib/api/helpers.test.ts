import { describe, expect, it } from "vitest";
import { decodeImpactEvents, decodeLogEntry } from "./helpers";
import type { ImpactEvent } from "./types/graph";

describe("decodeLogEntry", () => {
  it("puts the envelope of an impact event back from the columns", () => {
    const e = decodeLogEntry<ImpactEvent>({
      seq: "7",
      id: "e1",
      changeId: "c1",
      type: "impact.proposed",
      subject: "i1",
      flow: "f1",
      by: "alice",
      at: "2026-01-01T00:00:00Z",
      payload: JSON.stringify({ op: "proposed", state: { key: "K" } }),
    });
    expect(e).toMatchObject({
      id: "e1",
      changeId: "c1",
      impactId: "i1",
      flow: "f1",
      by: "alice",
      op: "proposed",
      state: { id: "i1", key: "K" },
    });
  });
  it("leaves a fact as stored", () => {
    expect(
      decodeLogEntry<{ id: string }>({
        id: "x",
        type: "fact.artifact",
        payload: '{"id":"a"}',
      }),
    ).toEqual({ id: "a" });
  });
});

describe("decodeImpactEvents", () => {
  it("reads every event an entry carries, with what they share put back", () => {
    const payload = JSON.stringify({
      op: "reviewed",
      review: { status: "accepted", reviewId: "REV-1" },
      impacts: [
        { impactId: "i1", review: { comment: "ok 1" } },
        { impactId: "i2", review: { comment: "ok 2" } },
      ],
    });
    const events = decodeImpactEvents<{
      id: string;
      impactId: string;
      by: string;
      review: { status: string; reviewId: string; comment: string };
    }>({
      id: "e1",
      changeId: "c1",
      seq: 3,
      type: "impact.reviewed",
      by: "bob",
      payload,
    });
    expect(
      events.map((e) => [
        e.id,
        e.impactId,
        e.by,
        e.review.status,
        e.review.reviewId,
        e.review.comment,
      ]),
    ).toEqual([
      ["e1#0", "i1", "bob", "accepted", "REV-1", "ok 1"],
      ["e1#1", "i2", "bob", "accepted", "REV-1", "ok 2"],
    ]);
  });
  it("reads an entry of one event as before", () => {
    expect(
      decodeImpactEvents<{ impactId: string; op: string }>({
        id: "e2",
        type: "impact.withdrawn",
        subject: "i9",
        payload: '{"op":"withdrawn"}',
      }),
    ).toMatchObject([{ impactId: "i9", op: "withdrawn" }]);
  });
});
