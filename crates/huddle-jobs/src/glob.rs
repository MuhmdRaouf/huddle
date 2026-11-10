//! Path globs over repo-relative POSIX paths: `*` and `?` stop at `/`, `**` crosses it, dotfiles match.

/// True when `path` matches the list: any positive glob matches and no `!` glob does.
/// A list of only `!` globs starts from everything; an empty list matches nothing.
/// A leading `./` on a glob is stripped.
pub fn matches_any(globs: &[String], path: &str) -> bool {
    let (negative, positive): (Vec<&String>, Vec<&String>) =
        globs.iter().partition(|g| g.starts_with('!'));
    if globs.is_empty() {
        return false;
    }
    let included = positive.is_empty() || positive.iter().any(|g| matches_one(g, path));
    included && !negative.iter().any(|g| matches_one(&g[1..], path))
}

fn matches_one(glob: &str, path: &str) -> bool {
    let glob: Vec<char> = glob.strip_prefix("./").unwrap_or(glob).chars().collect();
    let path: Vec<char> = path.chars().collect();
    walk(&glob, &path)
}

fn walk(glob: &[char], path: &[char]) -> bool {
    match glob.first() {
        None => path.is_empty(),
        Some('*') if glob.get(1) == Some(&'*') => {
            let rest = if glob.get(2) == Some(&'/') {
                &glob[3..]
            } else {
                &glob[2..]
            };
            rest.is_empty()
                || (0..=path.len())
                    .any(|i| (i == 0 || path[i - 1] == '/') && walk(rest, &path[i..]))
        }
        Some('*') => {
            let stop = path.iter().position(|c| *c == '/').unwrap_or(path.len());
            (0..=stop).any(|i| walk(&glob[1..], &path[i..]))
        }
        Some('?') => path.first().is_some_and(|c| *c != '/') && walk(&glob[1..], &path[1..]),
        Some(c) => path.first() == Some(c) && walk(&glob[1..], &path[1..]),
    }
}
