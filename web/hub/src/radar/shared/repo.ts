/**
 * The repository helpers the dashboard reads: paths shown from the home directory, and the name a
 * repo path stands for. Pure string work; the fs-walking halves live with the plugin's server side.
 */

/** A path shown from the home directory: /Users/raouf/work → ~/work, /home/u/x → ~/x, any other path stands. */
export function homeShort(path: string): string {
  return path.replace(/^\/(?:Users|home)\/[^/]+/, "~");
}

/** The name a repo path stands for: its last non-empty segment ("claude-code-plugins", "app"). A name
 *  never shows as a path — the full path stays where the workspace is shown, in tooltips. */
export function repoName(path: string): string {
  const base = path
    .split("/")
    .filter((part) => part !== "")
    .pop();
  return base ?? path;
}
