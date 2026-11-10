use huddle_catalog::providers::provider_by_id;
use huddle_router::adapt::adapt;
use huddle_router::body::scan_with_strip;
use hyper::body::Bytes;

fn run(id: &str, body: &str) -> Bytes {
    adapt(
        &Bytes::copy_from_slice(body.as_bytes()),
        provider_by_id(id).expect("provider"),
    )
}

fn text(a: &Bytes) -> String {
    String::from_utf8(a.to_vec()).expect("utf8")
}

#[test]
fn a_body_that_is_no_object_goes_as_it_came() {
    assert_eq!(text(&run("zai", "[1,2]")), "[1,2]");
}

#[test]
fn a_strip_provider_loses_the_field_only_at_the_api_positions() {
    let out = run("qwen", QWEN_BODY);
    let v: serde_json::Value = serde_json::from_slice(&out).expect("valid JSON");
    assert!(v.get("context_management").is_none());
    assert!(v["system"][0].get("context_management").is_none());
    assert!(v["tools"][0].get("context_management").is_none());
    assert!(
        v["messages"][0]["content"][0]
            .get("context_management")
            .is_none()
    );
    assert!(
        v["messages"][0]["content"][0]["content"][0]
            .get("context_management")
            .is_none()
    );
    assert!(
        v["messages"][0]["content"][1]
            .get("context_management")
            .is_none()
    );
    assert_eq!(
        v["tools"][0]["input_schema"]["properties"]["context_management"],
        serde_json::json!({"type":"string"}),
        "a tool's schema keeps its own field"
    );
    assert_eq!(
        v["messages"][0]["content"][2]["input"]["context_management"], "keep",
        "a tool_use input is user data"
    );
}

const QWEN_BODY: &str = r#"{"model":"qwen3.8-max","context_management":{"type":"ephemeral"},"system":[{"type":"text","text":"t","context_management":{"type":"ephemeral"}}],"tools":[{"name":"t","input_schema":{"type":"object","properties":{"context_management":{"type":"string"}}},"context_management":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":[{"type":"text","text":"r","context_management":{"type":"ephemeral"}}],"context_management":{"type":"ephemeral"}},{"type":"text","text":"keep","context_management":{"type":"ephemeral"}},{"type":"tool_use","id":"a","name":"n","input":{"context_management":"keep"}}]}]}"#;

#[test]
fn a_name_only_inside_a_string_value_changes_nothing() {
    let body = r#"{"model":"qwen3.8-max","note":"context_management"}"#;
    assert_eq!(text(&run("qwen", body)), body);
}

#[test]
fn an_escaped_key_is_still_found() {
    let out = run(
        "qwen",
        r#"{"model":"qwen3.8-max","context_management":{"x":1}}"#,
    );
    let v: serde_json::Value = serde_json::from_slice(&out).expect("valid JSON");
    assert!(v.get("context_management").is_none());
}

#[test]
fn deleting_keeps_commas_right_in_every_position() {
    let cases = [
        (
            r#"{"context_management":1,"model":"qwen3.8-max"}"#,
            r#"{"model":"qwen3.8-max"}"#,
        ),
        (
            r#"{"model":"qwen3.8-max","context_management":1}"#,
            r#"{"model":"qwen3.8-max"}"#,
        ),
        (
            r#"{"a":1,"context_management":1,"model":"qwen3.8-max"}"#,
            r#"{"a":1,"model":"qwen3.8-max"}"#,
        ),
        (
            r#"{"context_management":1,"context":1,"model":"qwen3.8-max"}"#,
            r#"{"context":1,"model":"qwen3.8-max"}"#,
        ),
    ];
    for (input, want) in cases {
        assert_eq!(text(&run("qwen", input)), want, "{input}");
    }
    assert_eq!(text(&run("qwen", r#"{"context_management":1}"#)), "{}");
}

#[test]
fn context_management_is_stripped_for_qwen_alone() {
    assert_eq!(
        text(&run(
            "qwen",
            r#"{"model":"qwen3.8-max","context_management":{"x":1}}"#
        )),
        r#"{"model":"qwen3.8-max"}"#
    );
    let minimax = r#"{"model":"MiniMax-M3","context_management":{"x":1},"messages":[]}"#;
    assert_eq!(text(&run("minimax", minimax)), minimax);
}

#[test]
fn a_body_for_a_provider_with_no_strip_goes_out_byte_identical() {
    let body = r#"{ "model":"glm-5.3-flash","max_tokens":9999999,"tools":[{"type":"web_search_20250305"}],"cache_control":{"type":"ephemeral"},"context_management":{"x":1}}"#;
    for id in ["zai", "kimi", "deepseek", "minimax"] {
        let original = Bytes::copy_from_slice(body.as_bytes());
        let out = adapt(&original, provider_by_id(id).expect("provider"));
        assert_eq!(out, original, "{id}");
        assert_eq!(
            out.as_ptr(),
            original.as_ptr(),
            "{id} forwards the same bytes uncopied"
        );
    }
}

#[test]
fn scan_with_strip_and_no_names_is_the_plain_scan() {
    let scanned = scan_with_strip(br#"{"a":1}"#, &[]).expect("object");
    assert!(scanned.strip_edits.is_empty());
    assert_eq!(scanned.scan.members.len(), 1);
}
