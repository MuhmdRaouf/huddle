//! huddle-jobs: review.

use std::path::Path;

use crate::git::{self, ChangeSet, DiffMode, GitError};
use crate::model::{Job, JobState};
use crate::render::{self, ReviewView};

/// Changes and stat of a job's folder when it still exists and the job awaits review or
/// failed; with_diff adds the capped patch. Otherwise an empty ChangeSet and no diff.
pub fn review_view(job: &Job, with_diff: bool) -> Result<ReviewView, GitError> {
    let mut view = ReviewView {
        changes: ChangeSet::default(),
        diff_stat: None,
        diff: None,
    };
    let open = job.state == JobState::AwaitingReview || job.state == JobState::Failed;
    let dir = Path::new(&job.dir);
    if !open || !dir.exists() {
        return Ok(view);
    }
    view.changes = git::changes(dir, &job.base_sha)?;
    let stat = git::diff(dir, &job.base_sha, DiffMode::Stat)?;
    view.diff_stat = Some(stat);
    if with_diff {
        let patch = git::diff(dir, &job.base_sha, DiffMode::Patch)?;
        view.diff = Some(render::cap_diff(&patch, render::DIFF_CAP_CHARS));
    }
    Ok(view)
}
