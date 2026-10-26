// diff.ts — the line diff the Edit preview draws: one longest-common-subsequence walk over lines,
// in walk order, no dependency.

/** One line of the diff: context (a space), removed or added, in walk order. */
export type DiffLine = { sign: "-" | "+" | " "; text: string };

/** The lines of one side of the diff; an empty text has no lines, not one empty line. */
const lines = (text: string): string[] => (text === "" ? [] : text.split("\n"));

/** One row's cell; the border row and the cell past a row's end read as 0. */
const cell = (row: number[] | undefined, j: number): number => row?.[j] ?? 0;

/** The line diff between the two texts: shared lines as context, the rest as removed or added. */
export const lineDiff = (before: string, after: string): DiffLine[] => {
  const oldLines = lines(before);
  const newLines = lines(after);
  // lcs[i][j] is the longest common subsequence of oldLines[i..] and newLines[j..]
  const lcs: number[][] = Array.from({ length: oldLines.length + 1 }, () =>
    Array.from({ length: newLines.length + 1 }, () => 0),
  );
  for (let i = oldLines.length - 1; i >= 0; i--) {
    const row = lcs[i];
    if (row === undefined) continue;
    for (let j = newLines.length - 1; j >= 0; j--) {
      row[j] =
        oldLines[i] === newLines[j]
          ? cell(lcs[i + 1], j + 1) + 1
          : Math.max(cell(lcs[i + 1], j), cell(row, j + 1));
    }
  }
  const out: DiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < oldLines.length || j < newLines.length) {
    const oldLine = oldLines[i];
    const newLine = newLines[j];
    if (oldLine !== undefined && oldLine === newLine) {
      out.push({ sign: " ", text: oldLine });
      i++;
      j++;
    } else if (
      newLine === undefined ||
      (oldLine !== undefined && cell(lcs[i + 1], j) >= cell(lcs[i], j + 1))
    ) {
      out.push({ sign: "-", text: oldLine ?? "" });
      i++;
    } else {
      out.push({ sign: "+", text: newLine });
      j++;
    }
  }
  return out;
};
