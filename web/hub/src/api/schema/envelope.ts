// envelope.ts — the hub stream's frames as the wire spells them: a message envelope with its topic, seq, op and
// id (data checked only as far as the op needs it, since each topic's own schema decodes the row), and the two
// control frames that carry no seq, hello and ping.
import * as v from "valibot";

const fields = { topic: v.string(), seq: v.number(), id: v.string() };

/** One message: the same shapes as keyed.ts's KeyedMessage. A snapshot's rows are checked down to their ids. */
export const envelopeSchema = v.variant("op", [
  v.object({
    ...fields,
    op: v.literal("snapshot"),
    data: v.object({ rows: v.array(v.object({ id: v.string(), data: v.unknown() })) }),
  }),
  v.object({ ...fields, op: v.literal("upsert"), data: v.unknown() }),
  v.object({ ...fields, op: v.literal("remove") }),
  v.object({ ...fields, op: v.literal("touch") }),
]);

/** The first frame of a connection: the newest seq the hub had issued, and the topics it answers with a snapshot instead of a replay. */
export const helloSchema = v.object({
  type: v.literal("hello"),
  head: v.number(),
  gone: v.optional(v.nullable(v.array(v.string()))),
});

/** The keep-alive the hub writes when it has nothing else to say. */
export const pingSchema = v.object({ type: v.literal("ping") });

/** Any frame the stream can carry. */
export const frameSchema = v.union([envelopeSchema, helloSchema, pingSchema]);

export type Frame = v.InferOutput<typeof frameSchema>;
export type Hello = v.InferOutput<typeof helloSchema>;
