//! huddle-router: adapt.
//!
//! The one edit a provider-bound body can get: the provider's rejected fields stripped at the
//! positions the Anthropic Messages API defines. A provider with no strip list forwards every
//! body as it came.

use crate::body;
use huddle_catalog::providers::Provider;
use hyper::body::Bytes;

/// The body as the provider accepts it, taking the body so one copy is held. A provider with
/// no strip list, a body the scanner refuses and a body holding none of the names return the
/// same allocation; an edited body is the new bytes and the original is released.
pub fn adapt(body: Bytes, provider: &Provider) -> Bytes {
    if provider.strip.is_empty() {
        return body;
    }
    let Some(scanned) = body::scan_with_strip(&body, provider.strip) else {
        return body;
    };
    let mut edits = scanned.strip_edits;
    if edits.is_empty() {
        return body;
    }
    Bytes::from(body::apply(&body, &mut edits))
}
