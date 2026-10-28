import { describe, expect, it } from "vitest";
import { bundleLevels, bundleTransitions, labelX } from "./lifecycleBundles";

const index = new Map(
  ["framing", "analysing", "designing", "delivered", "abandoned"].map(
    (s, i) => [s, i],
  ),
);

describe("bundleTransitions", () => {
  it("merges the transitions sharing a name, a target and a direction", () => {
    const b = bundleTransitions(
      [
        { name: "abandon", from: "framing", to: "abandoned" },
        { name: "analyse", from: "framing", to: "analysing" },
        { name: "abandon", from: "designing", to: "abandoned" },
        { name: "abandon", from: "analysing", to: "abandoned" },
        { name: "reframe", from: "analysing", to: "framing" },
      ],
      index,
    );
    expect(b.map((x) => [x.name, x.from, x.to, x.dir, x.span])).toEqual([
      ["abandon", [2, 1, 0], 4, 1, 4],
      ["analyse", [0], 1, 1, 1],
      ["reframe", [1], 0, -1, 1],
    ]);
  });

  it("keeps apart another name, another target or the other direction", () => {
    const b = bundleTransitions(
      [
        { name: "abandon", from: "framing", to: "abandoned" },
        { name: "drop", from: "analysing", to: "abandoned" },
        { name: "abandon", from: "analysing", to: "delivered" },
        { name: "move", from: "framing", to: "analysing" },
        { name: "move", from: "designing", to: "analysing" },
      ],
      index,
    );
    expect(b).toHaveLength(5);
  });

  it("leaves out self and unknown transitions and repeated sources", () => {
    const b = bundleTransitions(
      [
        { name: "stay", from: "framing", to: "framing" },
        { name: "go", from: "nowhere", to: "framing" },
        { name: "go", from: "framing", to: "analysing" },
        { name: "go", from: " framing ", to: "analysing" },
      ],
      index,
    );
    expect(b.map((x) => x.from)).toEqual([[0]]);
  });
});

describe("labelX", () => {
  it("is the middle of the rail for one source", () => {
    expect(labelX([10], 110)).toBe(60);
  });
  it("is the middle of the widest stretch between junctions", () => {
    expect(labelX([0, 100, 200], 400)).toBe(300);
    expect(labelX([400, 300], 0)).toBe(150);
  });
});

describe("bundleLevels", () => {
  const at = (ts: [string, string, string][]) => {
    const b = bundleTransitions(
      ts.map(([name, from, to]) => ({ name, from, to })),
      index,
    );
    const l = bundleLevels(b);
    return b.map((x) => l.get(x.key));
  };
  it("puts a trunk just over the rails it spans", () => {
    expect(
      at([
        ["analyse", "framing", "analysing"],
        ["design", "analysing", "designing"],
        ["abandon", "framing", "abandoned"],
        ["abandon", "designing", "abandoned"],
      ]),
    ).toEqual([1, 1, 2]);
  });
  it("stacks overlapping rails of one side, leaves the other side alone", () => {
    expect(
      at([
        ["a", "framing", "designing"],
        ["b", "analysing", "delivered"],
        ["c", "designing", "framing"],
      ]),
    ).toEqual([1, 2, 1]);
  });
});
