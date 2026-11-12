//! huddle-router: refusal. What a provider route answers when the key is missing, refused or out
//! of balance, what it says for a beta it cannot serve, and the one-line reason an upstream error
//! gives. Every refusal is a non-retryable 400 that quotes nothing the provider sent.

use serde_json::Value;

/// The status every refusal is answered with: a 400 is neither a login failure nor retried.
pub const REFUSAL_STATUS: u16 = 400;

/// The most of an upstream answer an inspection holds back before deciding.
pub const INSPECT_LIMIT: usize = 64 * 1024;

/// The router's own name.
pub const NAME: &str = "huddle-router";

/// The header pairs every refusal carries, exactly.
pub const REFUSAL_HEADERS: [(&str, &str); 2] = [
    ("content-type", "application/json"),
    ("x-should-retry", "false"),
];

/// The most of an error answer read to say why it failed.
const REASON_BYTES: usize = 2048;

/// The longest reason a log line or route event carries.
const REASON_CHARS: usize = 300;

/// The run length past which key-shaped characters are a credential.
const SECRET_RUN: usize = 24;

/// Each provider's own error codes for an account out of balance or quota, as each documents
/// them: Z.ai 1113/1316/1317, Moonshot `exceeded_current_quota_error`, MiniMax 1008, Alibaba
/// Model Studio `Arrearage` and `isv.OUT_OF_SERVICE`. DeepSeek's 402 is matched by status.
const BALANCE_CODES: [&str; 7] = [
    "1113",
    "1316",
    "1317",
    "exceeded_current_quota_error",
    "1008",
    "Arrearage",
    "isv.OUT_OF_SERVICE",
];

/// Why a provider answer is replaced.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Refusal {
    /// The key was refused, or there is none.
    Key,
    /// The account has no balance or quota left.
    Balance,
    /// The request was forbidden; the key itself is not accused.
    Forbidden,
}

/// The provider names a refusal's wording needs.
#[derive(Debug, Clone)]
pub struct RefusalNames {
    pub display: String,
    pub slash: String,
    pub billing_url: String,
}

/// An answer the lane sends in place of the upstream bytes: status, header pairs, body.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Replacement {
    pub status: u16,
    pub headers: Vec<(&'static str, &'static str)>,
    pub body: Vec<u8>,
}

/// True of the upstream answers worth reading before they are sent: every error. A success
/// streams through untouched.
pub fn may_refuse(status: u16) -> bool {
    status >= 400
}

/// Why the provider's answer is a refusal, or none for any other answer (sent as it came).
/// Balance wins over key.
pub fn refusal_of(status: u16, body: &[u8]) -> Option<Refusal> {
    if status < 400 {
        return None;
    }
    let parsed: Option<Value> = serde_json::from_slice(body).ok();
    if out_of_balance(status, parsed.as_ref()) {
        return Some(Refusal::Balance);
    }
    match status {
        401 => Some(Refusal::Key),
        403 => Some(Refusal::Forbidden),
        _ => None,
    }
}

/// Whether an error answer says the account is out of balance or quota: a 402, or one of the
/// balance codes or wordings in the places each provider puts them.
pub fn out_of_balance(status: u16, body: Option<&Value>) -> bool {
    if status == 402 {
        return true;
    }
    let Some(top) = body.and_then(Value::as_object) else {
        return false;
    };
    let error = top.get("error").and_then(Value::as_object);
    let base = top.get("base_resp").and_then(Value::as_object);
    let codes = [
        error.and_then(|record| record.get("code")),
        error.and_then(|record| record.get("type")),
        top.get("code"),
        top.get("type"),
        base.and_then(|record| record.get("status_code")),
    ];
    for code in codes.into_iter().flatten() {
        if let Some(text) = code_text(code)
            && BALANCE_CODES.contains(&text.as_str())
        {
            return true;
        }
    }
    let messages = [
        error.and_then(|record| record.get("message")),
        top.get("message"),
        base.and_then(|record| record.get("status_msg")),
    ];
    for message in messages.into_iter().flatten() {
        if let Some(text) = message.as_str()
            && is_balance_wording(text)
        {
            return true;
        }
    }
    false
}

/// The Anthropic-shaped, non-retryable answer for a refusal.
pub fn refusal_answer(names: &RefusalNames, refusal: Refusal, why: &str) -> Replacement {
    Replacement {
        status: REFUSAL_STATUS,
        headers: REFUSAL_HEADERS.to_vec(),
        body: error_body(
            "invalid_request_error",
            &refusal_message(names, refusal, why),
        ),
    }
}

/// The answer for a provider request without a key.
pub fn no_key_answer(names: &RefusalNames) -> Replacement {
    refusal_answer(names, Refusal::Key, "no key is set")
}

/// The answer for a key that is set but cannot be used; why says what is wrong with it.
pub fn unusable_key_answer(names: &RefusalNames, why: &str) -> Replacement {
    refusal_answer(names, Refusal::Key, why)
}

/// The answer when reading the provider's key times out: a 503 naming the provider.
pub fn key_timeout_answer(provider: &str, display: &str) -> Replacement {
    Replacement {
        status: 503,
        headers: vec![("content-type", "application/json")],
        body: error_body(
            "api_error",
            &format!("{provider} router: reading the {display} key timed out"),
        ),
    }
}

/// The 400 Claude Code reads as a rejected beta: it drops threading and resends the whole
/// conversation.
pub fn threads_beta_answer(provider: &str, tokens: &[String]) -> Replacement {
    let joined = tokens.join("`, `");
    let message = format!(
        "Unexpected value(s) `{joined}` for the `anthropic-beta` header: \
         {provider} keeps no server-side message thread, so Claude Code must resend the whole conversation."
    );
    Replacement {
        status: REFUSAL_STATUS,
        headers: vec![("content-type", "application/json")],
        body: error_body("invalid_request_error", &message),
    }
}

/// The message-threads tokens of the `anthropic-beta` header: every comma-separated token,
/// trimmed, across all header lines, named in the spelling the request carried.
pub fn threads_beta_tokens<'a>(header_values: impl Iterator<Item = &'a str>) -> Vec<String> {
    header_values
        .flat_map(|value| value.split(','))
        .map(str::trim)
        .filter(|token| token.to_ascii_lowercase().starts_with("message-threads-"))
        .map(str::to_string)
        .collect()
}

/// The body with every occurrence of the route's key replaced by `[key redacted]`, or none when
/// the key is empty or absent.
pub fn redact_key(body: &[u8], key: &str) -> Option<Vec<u8>> {
    let needle = key.as_bytes();
    if needle.is_empty() || find(body, needle).is_none() {
        return None;
    }
    let mut out = Vec::with_capacity(body.len());
    let mut rest = body;
    while let Some(at) = find(rest, needle) {
        out.extend_from_slice(&rest[..at]);
        out.extend_from_slice(b"[key redacted]");
        rest = &rest[at + needle.len()..];
    }
    out.extend_from_slice(rest);
    Some(out)
}

/// The one-line reason an upstream error answer gives, or none when it gives none: its
/// `type: message`, else the first 300 characters of its text — whitespace collapsed, the
/// route's key and every credential-shaped run redacted, so no secret reaches a log or the spool.
pub fn error_reason(body: &[u8], key: &str) -> Option<String> {
    let end = body.len().min(REASON_BYTES);
    let text = String::from_utf8_lossy(&body[..end]);
    let said = json_error_message(&text).unwrap_or_else(|| text.into_owned());
    let said = if key.is_empty() {
        said
    } else {
        said.replace(key, "…")
    };
    let line = collapse_whitespace(&said);
    let line = redact_secret_runs(&line);
    let cut: String = line.chars().take(REASON_CHARS).collect();
    let line = cut.trim_end();
    if line.is_empty() {
        None
    } else {
        Some(line.to_string())
    }
}

/// The message a refusal carries; why says what the router saw.
fn refusal_message(names: &RefusalNames, refusal: Refusal, why: &str) -> String {
    let RefusalNames {
        display,
        slash,
        billing_url,
    } = names;
    match refusal {
        Refusal::Key => {
            format!(
                "{display} key refused or missing: run {slash}setup ({why}). Claude models are not affected."
            )
        }
        Refusal::Balance => {
            format!(
                "{display} says this key has no balance or quota left ({why}): top up at {billing_url}, then retry. Claude models are not affected."
            )
        }
        Refusal::Forbidden => {
            format!("{display} refused this request ({why}). Claude models are not affected.")
        }
    }
}

/// The Anthropic-shaped error body: compact JSON, no trailing newline.
fn error_body(kind: &str, message: &str) -> Vec<u8> {
    let value = serde_json::json!({
        "type": "error",
        "error": { "type": kind, "message": message },
    });
    // Serializing this fixed shape cannot fail; an empty body keeps the answer well-formed.
    serde_json::to_vec(&value).unwrap_or_default()
}

/// The message an upstream error body carries, in the shapes providers actually send:
/// `{error:{type|code,message}}` and `{code,msg|message}`; none for anything else, leaving the
/// raw text as the reason.
fn json_error_message(text: &str) -> Option<String> {
    let parsed: Value = serde_json::from_str(text).ok()?;
    let top = parsed.as_object()?;
    if let Some(error) = top.get("error").and_then(Value::as_object) {
        let what = error_field(error, "type").or_else(|| error_field(error, "code"));
        return said(what, error_field(error, "message"));
    }
    let message = error_field(top, "msg").or_else(|| error_field(top, "message"));
    said(error_field(top, "code"), message)
}

/// A field of a parsed error record the reason reads: a non-empty string, or a number rendered.
fn error_field(record: &serde_json::Map<String, Value>, name: &str) -> Option<String> {
    match record.get(name) {
        Some(Value::String(text)) if !text.is_empty() => Some(text.clone()),
        Some(Value::Number(number)) => Some(render_number(number)),
        _ => None,
    }
}

/// An error's `what: message`, the message alone without a what, none without both.
fn said(what: Option<String>, message: Option<String>) -> Option<String> {
    match (what, message) {
        (Some(what), Some(message)) => Some(format!("{what}: {message}")),
        (what, message) => what.or(message),
    }
}

/// A code a provider reports: a string stands, a number renders as an integer when it has no
/// fractional part, else as serde_json prints it.
fn code_text(code: &Value) -> Option<String> {
    match code {
        Value::String(text) => Some(text.clone()),
        Value::Number(number) => Some(render_number(number)),
        _ => None,
    }
}

fn render_number(number: &serde_json::Number) -> String {
    if let Some(int) = number.as_i64() {
        return int.to_string();
    }
    if let Some(uint) = number.as_u64() {
        return uint.to_string();
    }
    match number.as_f64() {
        Some(value) if value.fract() == 0.0 => {
            let whole = value as i64;
            format!("{whole}")
        }
        Some(value) => value.to_string(),
        None => number.to_string(),
    }
}

/// The documented wording of an out-of-balance error, ASCII-case-insensitive or verbatim.
fn is_balance_wording(message: &str) -> bool {
    let lowered = message.to_ascii_lowercase();
    lowered.contains("insufficient balance")
        || lowered.contains("account is in good standing")
        || message.contains("余额不足")
        || message.contains("欠费")
}

/// The first offset of needle in haystack, none when the needle is empty or absent.
fn find(haystack: &[u8], needle: &[u8]) -> Option<usize> {
    if needle.is_empty() {
        return None;
    }
    haystack
        .windows(needle.len())
        .position(|window| window == needle)
}

/// The text with every whitespace run collapsed into one space of.
fn collapse_whitespace(text: &str) -> String {
    let mut line = String::with_capacity(text.len());
    let mut spaced = false;
    for ch in text.chars() {
        if ch.is_whitespace() {
            spaced = !line.is_empty();
            continue;
        }
        if spaced {
            line.push(' ');
        }
        spaced = false;
        line.push(ch);
    }
    line
}

/// The line with every run of key- and token-shaped characters long enough to be a credential
/// replaced by `…`.
fn redact_secret_runs(line: &str) -> String {
    let mut out = String::with_capacity(line.len());
    let mut run = String::new();
    for ch in line.chars() {
        if ch.is_ascii_alphanumeric() || ch == '.' || ch == '_' || ch == '-' {
            run.push(ch);
            continue;
        }
        flush_secret_run(&mut out, &mut run);
        out.push(ch);
    }
    flush_secret_run(&mut out, &mut run);
    out
}

fn flush_secret_run(out: &mut String, run: &mut String) {
    if run.len() >= SECRET_RUN {
        out.push('…');
    } else {
        out.push_str(run);
    }
    run.clear();
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn names() -> RefusalNames {
        RefusalNames {
            display: "Z.ai GLM".into(),
            slash: "/huddle:zai:".into(),
            billing_url: "https://z.ai/manage-apikey/billing".into(),
        }
    }

    fn message(answer: &Replacement) -> String {
        let value: serde_json::Value = serde_json::from_slice(&answer.body).expect("json");
        value["error"]["message"]
            .as_str()
            .expect("message")
            .to_string()
    }

    #[test]
    fn no_key_is_a_400_that_is_not_retried_and_names_setup() {
        let answer = no_key_answer(&names());
        assert_eq!(answer.status, 400);
        assert_eq!(answer.headers, REFUSAL_HEADERS.to_vec());
        assert_eq!(
            message(&answer),
            "Z.ai GLM key refused or missing: run /huddle:zai:setup (no key is set). Claude models are not affected."
        );
        let value: serde_json::Value = serde_json::from_slice(&answer.body).expect("json");
        assert_eq!(value["type"], "error");
        assert_eq!(value["error"]["type"], "invalid_request_error");
    }

    #[test]
    fn a_refused_key_says_what_the_provider_answered() {
        let answer = refusal_answer(&names(), Refusal::Key, "Z.ai GLM answered HTTP 401");
        assert_eq!(
            message(&answer),
            "Z.ai GLM key refused or missing: run /huddle:zai:setup (Z.ai GLM answered HTTP 401). Claude models are not affected."
        );
    }

    #[test]
    fn an_empty_balance_points_at_billing() {
        let answer = refusal_answer(&names(), Refusal::Balance, "Z.ai GLM answered HTTP 429");
        assert_eq!(
            message(&answer),
            "Z.ai GLM says this key has no balance or quota left (Z.ai GLM answered HTTP 429): top up at https://z.ai/manage-apikey/billing, then retry. Claude models are not affected."
        );
    }

    #[test]
    fn a_403_is_a_non_retried_400_that_never_claims_the_key_is_bad() {
        assert_eq!(refusal_of(403, b"{}"), Some(Refusal::Forbidden));
        let answer = refusal_answer(&names(), Refusal::Forbidden, "Z.ai GLM answered HTTP 403");
        assert_eq!(answer.status, 400);
        assert_eq!(
            message(&answer),
            "Z.ai GLM refused this request (Z.ai GLM answered HTTP 403). Claude models are not affected."
        );
    }

    #[test]
    fn a_key_file_that_cannot_be_used_says_why() {
        let answer = unusable_key_answer(
            &names(),
            "key file /x/env has mode 0644: run chmod 600 on it",
        );
        assert_eq!(answer.status, 400);
        assert_eq!(
            message(&answer),
            "Z.ai GLM key refused or missing: run /huddle:zai:setup (key file /x/env has mode 0644: run chmod 600 on it). Claude models are not affected."
        );
    }

    #[test]
    fn a_key_lookup_timeout_is_a_503_naming_the_provider() {
        let answer = key_timeout_answer("zai", "Z.ai GLM");
        assert_eq!(answer.status, 503);
        assert_eq!(answer.headers, vec![("content-type", "application/json")]);
        assert_eq!(
            message(&answer),
            "zai router: reading the Z.ai GLM key timed out"
        );
        let value: serde_json::Value = serde_json::from_slice(&answer.body).expect("json");
        assert_eq!(value["error"]["type"], "api_error");
    }

    #[test]
    fn refusal_of_judges_status_and_body() {
        assert_eq!(refusal_of(200, b"{}"), None);
        assert_eq!(refusal_of(401, b"{}"), Some(Refusal::Key));
        assert_eq!(refusal_of(402, b"{}"), Some(Refusal::Balance));
        assert_eq!(refusal_of(500, b"{}"), None);
        assert_eq!(
            refusal_of(429, br#"{"error":{"code":"1113"}}"#),
            Some(Refusal::Balance)
        );
        assert_eq!(
            refusal_of(401, br#"{"error":{"type":"exceeded_current_quota_error"}}"#),
            Some(Refusal::Balance),
            "balance wins over key"
        );
    }

    #[test]
    fn out_of_balance_reads_every_shape_providers_send() {
        assert!(out_of_balance(402, None));
        for body in [
            json!({"error": {"code": "1113"}}),
            json!({"error": {"type": "exceeded_current_quota_error"}}),
            json!({"code": "Arrearage"}),
            json!({"base_resp": {"status_code": 1008, "status_msg": "x"}}),
            json!({"code": 1316}),
            json!({"error": {"message": "Your Insufficient Balance is low"}}),
            json!({"message": "account is in good standing"}),
            json!({"message": "余额不足"}),
            json!({"message": "欠费"}),
        ] {
            assert!(out_of_balance(429, Some(&body)), "{body}");
        }
        assert!(!out_of_balance(
            429,
            Some(&json!({"error": {"code": "9999", "message": "other"}}))
        ));
        assert!(!out_of_balance(429, Some(&json!([1, 2]))));
        assert!(!out_of_balance(500, None));
    }

    #[test]
    fn may_refuse_every_error_status() {
        assert!(may_refuse(400));
        assert!(may_refuse(500));
        assert!(!may_refuse(200));
    }

    #[test]
    fn the_threads_beta_is_found_case_insensitively_across_lines() {
        let tokens = threads_beta_tokens(
            ["a, message-threads-2026-08-12", "b,Message-Threads-X"].into_iter(),
        );
        assert_eq!(
            tokens,
            vec!["message-threads-2026-08-12", "Message-Threads-X"]
        );
        assert!(threads_beta_tokens(["a,b"].into_iter()).is_empty());
    }

    #[test]
    fn the_threads_beta_answer_names_every_token() {
        let answer = threads_beta_answer("zai", &["message-threads-2026-08-12".to_string()]);
        assert_eq!(answer.status, 400);
        assert_eq!(
            message(&answer),
            "Unexpected value(s) `message-threads-2026-08-12` for the `anthropic-beta` header: zai keeps no server-side message thread, so Claude Code must resend the whole conversation."
        );
    }

    #[test]
    fn an_error_that_quotes_the_key_is_redacted() {
        assert_eq!(
            redact_key(b"bad key K123", "K123"),
            Some(b"bad key [key redacted]".to_vec())
        );
        assert_eq!(redact_key(b"fine", "K123"), None);
        assert_eq!(redact_key(b"fine", ""), None);
    }

    #[test]
    fn error_reason_reads_the_shapes_providers_send() {
        let cases = [
            (
                r#"{"error":{"type":"overloaded_error","message":"Overloaded"}}"#,
                "overloaded_error: Overloaded",
            ),
            (
                r#"{"error":{"code":"1210","message":"the balance is out"}}"#,
                "1210: the balance is out",
            ),
            (
                r#"{"code":"1210","msg":"the balance is out"}"#,
                "1210: the balance is out",
            ),
            (r#"{"error":{"code":"1210"}}"#, "1210"),
            (r#"{"error":{"message":"nope"}}"#, "nope"),
            (r#"{"message":"nope"}"#, "nope"),
            ("  Gateway  timeout  ", "Gateway timeout"),
            ("[1,2]", "[1,2]"),
        ];
        for (body, want) in cases {
            assert_eq!(
                error_reason(body.as_bytes(), "").as_deref(),
                Some(want),
                "{body}"
            );
        }
        assert_eq!(error_reason(b"   ", ""), None);
    }

    #[test]
    fn error_reason_redacts_the_key_and_every_credential_shaped_run() {
        let key = "sk-ant-0123456789abcdef0123456789abcdef";
        assert!(
            !error_reason(format!("bad key {key} for that").as_bytes(), key)
                .expect("reason")
                .contains(key)
        );
        assert!(
            !error_reason(b"token AbCdEf0123456789AbCdEf01 refused", "")
                .expect("reason")
                .contains("AbCdEf")
        );
        assert_eq!(
            error_reason(b"the model glm-5.3 is busy", "").as_deref(),
            Some("the model glm-5.3 is busy")
        );
    }

    #[test]
    fn error_reason_is_cut_to_300_chars() {
        let body = "x ".repeat(1000);
        assert_eq!(
            error_reason(body.as_bytes(), "")
                .expect("reason")
                .chars()
                .count(),
            299
        );
    }

    #[test]
    fn k11_refusals_name_the_huddle_provider_command() {
        let answer = no_key_answer(&names());
        assert!(
            message(&answer).contains("run /huddle:zai:setup "),
            "{}",
            message(&answer)
        );
    }
}
