//! huddle-router: adapt.
//!
//! The one edit a provider-bound body can get: the provider's rejected fields stripped at the
//! positions the Anthropic Messages API defines. A provider with no strip list forwards every
//! body as it came.

use crate::body;
use huddle_catalog::providers::Provider;
use hyper::body::Bytes;

/// The body as the provider accepts it. A provider with no strip list, a body the scanner
/// refuses and a body holding none of the names all go as they came, the same bytes uncopied.
pub fn adapt(body: &Bytes, provider: &Provider) -> Bytes {
    if provider.strip.is_empty() {
        return body.clone();
    }
    let Some(scanned) = body::scan_with_strip(body, provider.strip) else {
        return body.clone();
    };
    let mut edits = scanned.strip_edits;
    if edits.is_empty() {
        return body.clone();
    }
    Bytes::from(body::apply(body, &mut edits))
}
