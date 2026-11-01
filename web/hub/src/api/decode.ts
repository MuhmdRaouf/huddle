// decode.ts — the edge of the hub's web: a value that came off the wire is checked against a valibot
// schema once, here, and the code past this point holds a typed value. A mismatch throws a DecodeError
// that names where the value came from and the path of the first field that failed.
import * as v from "valibot";

/** A value that does not match its schema; the message reads "<where>: <path>: expected <type>" or "<where>: <path>: missing". */
export class DecodeError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "DecodeError";
  }
}

/** The issue's path as `sessions[2].state`; an empty string for the root. */
function pathOf(issue: v.BaseIssue<unknown>): string {
  let path = "";
  for (const item of issue.path ?? []) {
    const key = item.key;
    if (typeof key === "number") path += `[${key}]`;
    else path += path === "" ? String(key) : `.${String(key)}`;
  }
  return path;
}

/** What the issue says is wrong: a missing field, the type a schema expected, else valibot's own message. */
function reasonOf(issue: v.BaseIssue<unknown>): string {
  if (issue.type === "object" && issue.received === "undefined" && issue.path !== undefined) return "missing";
  return issue.kind === "schema" && issue.expected !== null ? `expected ${issue.expected}` : issue.message;
}

/**
 * Returns `value` as the schema's output, with every field the schema does not name dropped. Throws a
 * DecodeError naming `where` and the failing field's path.
 */
export function decode<const TSchema extends v.GenericSchema>(
  schema: TSchema,
  value: unknown,
  where: string,
): v.InferOutput<TSchema> {
  const result = v.safeParse(schema, value);
  if (result.success) return result.output;
  const issue = result.issues[0];
  const path = pathOf(issue);
  throw new DecodeError(
    path === "" ? `${where}: ${reasonOf(issue)}` : `${where}: ${path}: ${reasonOf(issue)}`,
  );
}
