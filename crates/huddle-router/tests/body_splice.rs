use huddle_router::body::{model_head, model_of, scan};

#[test]
fn scan_answers_every_member_with_decoded_keys() {
    let body = br#"{"model":"glm-5.3","cache_control":{"x":1}}"#;
    let s = scan(body).expect("object");
    assert_eq!(s.members.len(), 2);
    assert_eq!(s.members[0].key, "model");
    assert_eq!(
        &body[s.members[0].value_start..s.members[0].value_end],
        br#""glm-5.3""#
    );
    assert_eq!(s.members[1].key, "cache_control");
    assert_eq!(s.close, body.len() - 1);
}

#[test]
fn model_of_stops_at_the_model_key() {
    assert_eq!(
        model_of(br#"{"model":"glm-5.3","messages":not json"#),
        "glm-5.3"
    );
    assert_eq!(
        model_of(br#"{"max_tokens":1,"stream":true,"model":"claude-opus-5-5"}"#),
        "claude-opus-5-5"
    );
    for raw in [&br#"{"model":7}"#[..], br#"[1]"#, b"not json", b"{}", b""] {
        assert_eq!(model_of(raw), "", "{}", String::from_utf8_lossy(raw));
    }
}

#[test]
fn model_head_settles_like_the_whole_body() {
    let bodies: [&str; 11] = [
        r#"{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}"#,
        r#"{"max_tokens":1,"stream":true,"system":[{"type":"text","text":"a \"quoted\" } text"}],"model":"claude-opus-5-5","messages":[]}"#,
        r#"{"model":7,"messages":[]}"#,
        r#"{"model":true,"messages":[]}"#,
        r#"{"model":null}"#,
        r#"{"model":["a"],"messages":[]}"#,
        r#"{"messages":[{"a":[1,2,{"b":"c"}]}],"stream":true}"#,
        r#"{}"#,
        r#"[1,2]"#,
        r#""text""#,
        r#"{"model":"glm-5.3","messages":not json"#,
    ];
    for body in bodies {
        let want = model_of(body.as_bytes());
        let mut settled_at = None;
        for end in 0..=body.len() {
            let (model, settled) = model_head(&body.as_bytes()[..end]);
            if !settled {
                assert!(
                    settled_at.is_none(),
                    "{body}: settled then unsettled at {end}"
                );
                continue;
            }
            settled_at.get_or_insert(end);
            assert_eq!(model, want, "{body} at {end}");
        }
        assert!(settled_at.is_some(), "{body} settles by its end");
    }
}

#[test]
fn model_head_waits_for_the_model_value() {
    assert_eq!(model_head(br#"{"model":"glm-5.3"#), (String::new(), false));
    assert_eq!(model_head(br#"{"mod"#), (String::new(), false));
    assert_eq!(model_head(br#"{"model":"glm-5.3","x""#).0, "glm-5.3");
    assert!(model_head(br#"{"model":"glm-5.3""#).1);
}
