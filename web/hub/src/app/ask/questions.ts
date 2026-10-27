// questions.ts — the AskUserQuestion tool's input at the edge: the hook sends it as one JSON
// string, and the answer sheet needs the questions decoded. An input that does not decode (a cut
// string, a plain command, a shape without a text on a question) reads null, and the ask keeps
// today's preview. The answers a pick set builds go to the server keyed by each question's text.

/** One option Claude offered: the label the owner picks and the line under it. */
export type AskOption = {
  label: string;
  description: string;
};

/** One question: its text (the answer's key), its short header, its options and whether more
 *  than one option may be picked. */
export type AskQuestion = {
  question: string;
  header: string;
  options: AskOption[];
  multiSelect: boolean;
};

/** What the owner chose for one question: the labels picked, or the words typed over them. */
export type AskPick = {
  picked: readonly string[];
  typed?: string | undefined;
};

const str = (v: unknown): string | null => (typeof v === "string" ? v : null);

/** Reads an ask's input into its questions, or null when the input is not an ask we can draw:
 *  the JSON must carry a `questions` array, and every question a text. */
export function questionsOf(input: string): AskQuestion[] | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(input);
  } catch {
    return null;
  }
  const raw = (parsed as { questions?: unknown } | null)?.questions;
  if (!Array.isArray(raw)) return null;
  const questions: AskQuestion[] = [];
  for (const entry of raw) {
    const q = (entry ?? {}) as {
      question?: unknown;
      header?: unknown;
      options?: unknown;
      multiSelect?: unknown;
    };
    const question = str(q.question);
    if (question === null) return null;
    const options: AskOption[] = [];
    if (q.options !== undefined) {
      if (!Array.isArray(q.options)) return null;
      for (const option of q.options) {
        const o = (option ?? {}) as { label?: unknown; description?: unknown };
        const label = str(o.label);
        if (label === null) return null;
        options.push({ label, description: str(o.description) ?? "" });
      }
    }
    questions.push({
      question,
      header: str(q.header) ?? "",
      options,
      multiSelect: q.multiSelect === true,
    });
  }
  return questions;
}

/** The answers the picks build, or null while any question has no answer: each answer sits under
 *  its question's exact text — the picked labels in option order joined by ",", a typed answer
 *  with words as typed, a typed answer without words as the picks. */
export function answersOf(
  questions: readonly AskQuestion[],
  picks: readonly AskPick[],
): Record<string, string> | null {
  const answers: Record<string, string> = {};
  for (const [i, q] of questions.entries()) {
    const pick = picks[i];
    const typed = pick?.typed?.trim() ?? "";
    let answer: string | null = null;
    if (typed !== "") {
      answer = typed;
    } else if (pick !== undefined && pick.picked.length > 0) {
      const labels = q.options.filter((o) => pick.picked.includes(o.label)).map((o) => o.label);
      if (labels.length > 0) answer = labels.join(",");
    }
    if (answer === null) return null;
    answers[q.question] = answer;
  }
  return answers;
}
