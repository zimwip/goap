import { describe, expect, it } from "vitest";
import {
  ownSources,
  projectParents,
  sourceOf,
  sourceProblem,
  sourceProps,
  sourcesInForce,
} from "./sources";

describe("sources", () => {
  it("reads a declared source from its node", () => {
    expect(
      sourceOf("SRC:acme", {
        kind: "graph",
        location: "https://acme",
        params: { a: 1 },
      }),
    ).toMatchObject({
      name: "acme",
      kind: "graph",
      location: "https://acme",
      params: '{"a":1}',
    });
    expect(sourceOf("LLB:x", {})).toBeUndefined();
    expect(sourceOf("SRC:e", { params: {} })?.params).toBe("");
  });

  it("checks a source like lnk.Source.Validate", () => {
    const s = {
      name: "docs",
      description: "",
      kind: "files",
      namespace: "",
      location: "",
      branch: "",
      params: "",
    };
    expect(sourceProblem(s)).toMatch(/root directory/);
    expect(sourceProblem({ ...s, location: "/srv" })).toBe("");
    expect(sourceProblem({ ...s, name: "Docs", location: "/srv" })).toMatch(
      /name/,
    );
    expect(sourceProblem({ ...s, location: "/srv", params: "[1]" })).toMatch(
      /object/,
    );
    expect(sourceProps({ ...s, location: "/srv" })).toMatchObject({
      name: "docs",
      kind: "files",
      location: "/srv",
      params: {},
    });
  });

  it("resolves the mapping in force along the project chain, the nearest winning", () => {
    const props: Record<string, Record<string, unknown>> = {
      ROOT: { sources: { alm: "a", code: "c" } },
      P: { sources: { alm: "b" } },
      Q: {},
    };
    const parent: Record<string, string> = { P: "ROOT", Q: "P" };
    expect(
      sourcesInForce(
        "Q",
        (k) => props[k],
        (k) => parent[k] ?? "",
      ),
    ).toEqual([
      { name: "alm", source: "b", from: "P" },
      { name: "code", source: "c", from: "ROOT" },
    ]);
    expect(ownSources({ sources: { x: "y", bad: 3 } })).toEqual({ x: "y" });
  });

  it("reads the parents of the projects from their links, a root linking itself", () => {
    const nodes = [
      { id: "1", key: "ROOT" },
      { id: "2", key: "P" },
    ];
    const links = [
      { type: "pp", from: { id: "1" }, to: { id: "1" } },
      { type: "pp", from: { id: "2" }, to: { id: "1" } },
    ];
    expect([...projectParents(nodes as never, links as never, "pp")]).toEqual([
      ["P", "ROOT"],
    ]);
  });
});
