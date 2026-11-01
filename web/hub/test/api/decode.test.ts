// decode.test.ts — decode checks a value from the wire against a valibot schema: it returns the
// schema's output, drops fields the schema does not name, and throws a DecodeError whose message names
// where the value came from and the path of the field that failed.
import * as v from "valibot";
import { describe, expect, it } from "vite-plus/test";
import { DecodeError, decode } from "../../src/api/decode.ts";

const Registry = v.object({
  sessions: v.array(v.object({ id: v.string(), state: v.string() })),
});

describe("decode", () => {
  it("returns the value the schema describes", () => {
    const wire = { sessions: [{ id: "s1", state: "working" }] };
    expect(decode(Registry, wire, "registry")).toEqual(wire);
  });

  it("a missing field names its path", () => {
    const wire = { sessions: [{ id: "s1", state: "idle" }, { id: "s2", state: "idle" }, { id: "s3" }] };
    expect(() => decode(Registry, wire, "registry")).toThrow("registry: sessions[2].state: missing");
  });

  it("an extra field is dropped", () => {
    const wire = { sessions: [{ id: "s1", state: "idle", secret: "x" }], stray: 1 };
    expect(decode(Registry, wire, "registry")).toEqual({ sessions: [{ id: "s1", state: "idle" }] });
  });

  it("a wrong type throws DecodeError", () => {
    const wire = { sessions: [{ id: 7, state: "idle" }] };
    let caught: unknown;
    try {
      decode(Registry, wire, "registry");
    } catch (err) {
      caught = err;
    }
    expect(caught).toBeInstanceOf(DecodeError);
    expect((caught as DecodeError).message).toBe("registry: sessions[0].id: expected string");
  });

  it("a value that is not the root type names no path", () => {
    expect(() => decode(Registry, null, "registry")).toThrow("registry: expected Object");
  });
});
