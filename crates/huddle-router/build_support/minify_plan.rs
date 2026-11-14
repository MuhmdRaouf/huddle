//! The build script's decision about minifying the dashboard page.
//!
//! `build.rs` includes this file with `mod`; the crate's test build includes it too, so the
//! decision is unit-tested even though build scripts have no test target of their own.

/// What the build does with the joined CSS and JS before it gzips the page.
#[derive(Debug, PartialEq, Eq)]
pub enum MinifyPlan {
    /// esbuild is present: minify both.
    Minify,
    /// esbuild is absent and the build does not insist on it: embed the plain page and warn.
    PlainWithWarning,
    /// esbuild is absent and the build insists on it (a release build): stop the build.
    Fail,
}

/// `esbuild_found`: an esbuild binary answered `--version`. `required`: `HUDDLE_REQUIRE_MINIFY`
/// is set, as it is in CI's release builds and the docker release build.
pub fn plan(esbuild_found: bool, required: bool) -> MinifyPlan {
    match (esbuild_found, required) {
        (true, _) => MinifyPlan::Minify,
        (false, false) => MinifyPlan::PlainWithWarning,
        (false, true) => MinifyPlan::Fail,
    }
}

#[cfg(test)]
mod tests {
    use super::{MinifyPlan, plan};

    #[test]
    fn e8_build_without_esbuild_embeds_the_plain_page() {
        assert_eq!(plan(false, false), MinifyPlan::PlainWithWarning);
    }

    #[test]
    fn e8_a_required_minify_without_esbuild_fails_the_build() {
        assert_eq!(plan(false, true), MinifyPlan::Fail);
    }

    #[test]
    fn e8_esbuild_present_minifies_whether_required_or_not() {
        assert_eq!(plan(true, false), MinifyPlan::Minify);
        assert_eq!(plan(true, true), MinifyPlan::Minify);
    }
}
