use huddle_jobs::glob::matches_any;

fn list(globs: &[&str]) -> Vec<String> {
    globs.iter().map(|g| g.to_string()).collect()
}

#[test]
fn an_empty_list_matches_nothing() {
    assert!(!matches_any(&[], "a.rs"));
}

#[test]
fn a_star_stops_at_a_slash() {
    assert!(matches_any(&list(&["src/*.rs"]), "src/a.rs"));
    assert!(!matches_any(&list(&["src/*.rs"]), "src/b/a.rs"));
}

#[test]
fn two_stars_cross_directories() {
    assert!(matches_any(&list(&["src/**"]), "src/b/c/a.rs"));
    assert!(matches_any(&list(&["**/*.rs"]), "a.rs"));
    assert!(matches_any(&list(&["**/*.rs"]), "x/y/a.rs"));
    assert!(matches_any(&list(&["a/**/b.rs"]), "a/b.rs"));
    assert!(matches_any(&list(&["a/**/b.rs"]), "a/x/y/b.rs"));
    assert!(!matches_any(&list(&["a/**/b.rs"]), "ab.rs"));
}

#[test]
fn a_question_mark_is_one_character_but_not_a_slash() {
    assert!(matches_any(&list(&["a?.rs"]), "ab.rs"));
    assert!(!matches_any(&list(&["a?.rs"]), "a/.rs"));
}

#[test]
fn dotfiles_match() {
    assert!(matches_any(&list(&["**"]), ".github/ci.yml"));
    assert!(matches_any(&list(&["*"]), ".env"));
}

#[test]
fn a_negation_subtracts() {
    assert!(!matches_any(
        &list(&["src/**", "!src/gen/**"]),
        "src/gen/a.rs"
    ));
    assert!(matches_any(&list(&["src/**", "!src/gen/**"]), "src/a.rs"));
}

#[test]
fn only_negations_start_from_everything() {
    assert!(matches_any(&list(&["!docs/**"]), "src/a.rs"));
    assert!(!matches_any(&list(&["!docs/**"]), "docs/a.md"));
}

#[test]
fn a_leading_dot_slash_on_a_glob_is_stripped() {
    assert!(matches_any(&list(&["./src/*.rs"]), "src/a.rs"));
}
