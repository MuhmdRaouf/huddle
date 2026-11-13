//! huddle-router: quota. What is left of each set-up provider's plan or balance, asked while a
//! dashboard is open and kept with its time. One provider is asked at most every five minutes,
//! failures included. The key is read as a lane reads it, sent only to that provider's own
//! quota URL and never kept in a view or a log line; an answer that fails or changes shape reads
//! "quota unknown".

use std::collections::{BTreeMap, HashMap};
use std::sync::{Arc, Mutex, MutexGuard, PoisonError};
use std::time::Duration;

use http_body_util::BodyExt;
use huddle_catalog::providers::{Provider, Quota, provider_by_id, providers};
use huddle_config::env::Env;
use hyper::header::{HeaderMap, HeaderValue};
use hyper::{Method, StatusCode, Uri};
use serde_json::Value;
use tokio::task::JoinSet;

use crate::keycache::{KEY_BUDGET, KeyAnswer, KeyCache, KeySources, STORE_SHARE};
use crate::upstream::{RequestBody, UpstreamClient, UpstreamRequest, upstream_url};

/// The fewest milliseconds between two asks of one provider.
const MIN_GAP_MS: u64 = 300_000;
/// One ask's ceiling, connect to last byte.
const ASK_TIMEOUT: Duration = Duration::from_secs(10);
/// The most answer bytes read.
const BODY_CAP: usize = 64 * 1024;
/// What the view says when an ask fails or its answer does not parse.
const UNKNOWN: &str = "quota unknown";
const NOT_REPORTED: &str = "not reported";

#[derive(Debug, Clone, Copy, PartialEq, Eq, serde::Serialize)]
#[serde(rename_all = "lowercase")]
pub enum QuotaKind {
    Plan,
    Balance,
    None,
}

/// One usage window: the share used and when it resets, in epoch milliseconds.
#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize)]
pub struct Window {
    pub name: String,
    pub used_pct: Option<u64>,
    pub resets_ms: Option<u64>,
    /// Set when the provider did not report the window.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub note: Option<String>,
}

#[derive(Debug, Clone, PartialEq, serde::Serialize)]
pub struct Balance {
    pub amount: f64,
    pub currency: String,
}

/// A provider's quota as the snapshot shows it.
#[derive(Debug, Clone, PartialEq, serde::Serialize)]
pub struct QuotaView {
    pub kind: QuotaKind,
    pub windows: Vec<Window>,
    pub balance: Option<Balance>,
    pub fetched_ms: Option<u64>,
    pub error: Option<String>,
    /// True until the answer's shape has been confirmed against a live key.
    pub unverified: bool,
}

impl QuotaView {
    fn empty(kind: QuotaKind) -> QuotaView {
        QuotaView {
            kind,
            windows: Vec::new(),
            balance: None,
            fetched_ms: None,
            error: None,
            unverified: false,
        }
    }

    fn unknown(kind: QuotaKind, now_ms: u64, unverified: bool) -> QuotaView {
        QuotaView {
            fetched_ms: Some(now_ms),
            error: Some(UNKNOWN.to_owned()),
            unverified,
            ..QuotaView::empty(kind)
        }
    }

    /// Anthropic's plan windows from the latest answer headers; a window the headers did not
    /// carry reads "not reported".
    pub fn anthropic(
        five_hour: (Option<u64>, Option<u64>),
        week: (Option<u64>, Option<u64>),
    ) -> QuotaView {
        let window = |name: &str, (pct, reset): (Option<u64>, Option<u64>)| Window {
            name: name.to_owned(),
            used_pct: pct,
            resets_ms: reset,
            note: (pct.is_none() && reset.is_none()).then(|| NOT_REPORTED.to_owned()),
        };
        QuotaView {
            windows: vec![window("5 hours", five_hour), window("7 days", week)],
            ..QuotaView::empty(QuotaKind::Plan)
        }
    }
}

/// The URL to ask for a provider, or `None` when it has no quota API.
pub type UrlFor = Arc<dyn Fn(&'static Provider) -> Option<String> + Send + Sync>;
/// Where a provider's key comes from.
pub type KeysFor = Arc<dyn Fn(&'static Provider) -> KeySources + Send + Sync>;

/// The catalog's quota URL for each provider. A non-empty `<P>_BASE_URL` in `env`, the knob the
/// lane honours, moves the ask to that URL's scheme and host; the catalog's path stays.
pub fn catalog_urls(env: &Env) -> UrlFor {
    let moved: HashMap<&'static str, String> = providers()
        .iter()
        .filter_map(|provider| {
            let name = format!("{}_BASE_URL", provider.env_prefix);
            let base = env.get(&name).map(str::trim).filter(|v| !v.is_empty())?;
            Some((provider.id, base.to_owned()))
        })
        .collect();
    Arc::new(move |provider| {
        let catalog = provider.quota.url()?;
        let Some(base) = moved.get(provider.id) else {
            return Some(catalog.to_owned());
        };
        // A base that is not a URL cannot take the ask: the provider reads "quota unknown".
        let origin = Uri::try_from(base.as_str()).ok()?;
        let path = Uri::try_from(catalog).ok()?;
        Some(format!(
            "{}://{}{}",
            origin.scheme_str()?,
            origin.authority()?,
            path.path_and_query()?
        ))
    })
}

#[derive(Default)]
struct Book {
    views: BTreeMap<String, QuotaView>,
    /// When each provider was last asked, in the caller's clock.
    asked_ms: HashMap<String, u64>,
}

pub struct QuotaFetcher {
    client: Arc<UpstreamClient>,
    keys_for: KeysFor,
    url_for: UrlFor,
    keys: Mutex<HashMap<String, Arc<KeyCache>>>,
    book: Mutex<Book>,
}

fn lock<T>(mutex: &Mutex<T>) -> MutexGuard<'_, T> {
    mutex.lock().unwrap_or_else(PoisonError::into_inner)
}

impl QuotaFetcher {
    pub fn new(
        client: Arc<UpstreamClient>,
        keys_for: KeysFor,
        url_for: UrlFor,
    ) -> Arc<QuotaFetcher> {
        Arc::new(QuotaFetcher {
            client,
            keys_for,
            url_for,
            keys: Mutex::new(HashMap::new()),
            book: Mutex::new(Book::default()),
        })
    }

    /// The last answer for each provider asked so far; Qwen's row says it has none.
    pub fn view(&self) -> BTreeMap<String, QuotaView> {
        lock(&self.book).views.clone()
    }

    /// Asks every set-up provider whose last ask is at least five minutes old, all at once, and
    /// stores the answers. Nothing is asked while `viewers` is zero.
    pub async fn refresh(&self, now_ms: u64, viewers: usize, enabled: &[String]) {
        if viewers == 0 {
            return;
        }
        let due = self.claim(now_ms, enabled);
        let mut asks = JoinSet::new();
        for provider in due {
            let source = provider.quota;
            let url = (self.url_for)(provider);
            let key = self.key_cache(provider);
            let client = Arc::clone(&self.client);
            asks.spawn(async move {
                let view = ask(&client, source, url, &key, now_ms).await;
                (provider.id, view)
            });
        }
        while let Some(done) = asks.join_next().await {
            if let Ok((id, view)) = done {
                lock(&self.book).views.insert(id.to_owned(), view);
            }
        }
    }

    /// The providers to ask now, marked as asked so a second caller skips them. A provider with
    /// no quota API gets its row at once.
    fn claim(&self, now_ms: u64, enabled: &[String]) -> Vec<&'static Provider> {
        let mut book = lock(&self.book);
        let mut due = Vec::new();
        for id in enabled {
            let Some(provider) = provider_by_id(id) else {
                continue;
            };
            if provider.quota == Quota::None {
                book.views
                    .entry(id.clone())
                    .or_insert_with(|| QuotaView::empty(QuotaKind::None));
                continue;
            }
            let fresh = book
                .asked_ms
                .get(id)
                .is_some_and(|at| now_ms.saturating_sub(*at) < MIN_GAP_MS);
            if !fresh {
                book.asked_ms.insert(id.clone(), now_ms);
                due.push(provider);
            }
        }
        due
    }

    fn key_cache(&self, provider: &'static Provider) -> Arc<KeyCache> {
        let mut keys = lock(&self.keys);
        let cache = keys
            .entry(provider.id.to_owned())
            .or_insert_with(|| Arc::new(KeyCache::new((self.keys_for)(provider), STORE_SHARE)));
        Arc::clone(cache)
    }
}

/// One ask. Every failure is a view that reads "quota unknown".
async fn ask(
    client: &UpstreamClient,
    source: Quota,
    url: Option<String>,
    keys: &KeyCache,
    now_ms: u64,
) -> QuotaView {
    let kind = kind_of(source);
    let unverified = matches!(source, Quota::MiniMax(_));
    let unknown = || QuotaView::unknown(kind, now_ms, unverified);
    let Some(url) = url else {
        return unknown();
    };
    let KeyAnswer::Found(key) = keys.get(KEY_BUDGET).await else {
        return unknown();
    };
    let Some(request) = build_request(&url, source, key.expose()) else {
        return unknown();
    };
    let Ok(Ok(answer)) = tokio::time::timeout(ASK_TIMEOUT, client.send(request)).await else {
        return unknown();
    };
    let status = answer.status;
    let mut body = answer.body;
    let conn = answer.conn;
    let read = tokio::time::timeout(ASK_TIMEOUT, read_capped(&mut body)).await;
    conn.finish();
    if matches!(status, StatusCode::UNAUTHORIZED | StatusCode::FORBIDDEN) {
        // The key is refused: the next ask reads the stores again.
        keys.clear();
    }
    let Ok(Some(bytes)) = read else {
        return unknown();
    };
    if !status.is_success() {
        return unknown();
    }
    match parse(source, &bytes, now_ms) {
        Some(parsed) => QuotaView {
            kind,
            windows: parsed.windows,
            balance: parsed.balance,
            fetched_ms: Some(now_ms),
            error: None,
            unverified,
        },
        None => unknown(),
    }
}

fn kind_of(source: Quota) -> QuotaKind {
    match source {
        Quota::None => QuotaKind::None,
        Quota::Zai(_) | Quota::MiniMax(_) => QuotaKind::Plan,
        Quota::Balance(_) => QuotaKind::Balance,
    }
}

/// The body up to the cap; `None` when it fails or runs past the cap.
async fn read_capped(body: &mut hyper::body::Incoming) -> Option<Vec<u8>> {
    let mut bytes = Vec::new();
    while let Some(frame) = body.frame().await {
        let frame = frame.ok()?;
        if let Some(data) = frame.data_ref() {
            if bytes.len() + data.len() > BODY_CAP {
                return None;
            }
            bytes.extend_from_slice(data);
        }
    }
    Some(bytes)
}

/// A GET with `host` naming the upstream and the key in `authorization`: raw for Z.ai, which
/// takes no `Bearer`, and `Bearer` for the rest.
fn build_request(url: &str, source: Quota, key: &str) -> Option<UpstreamRequest> {
    let target = Uri::try_from(url).ok()?;
    let host = target.authority()?.as_str().to_owned();
    let path = target
        .path_and_query()
        .map_or("/", |p| p.as_str())
        .to_owned();
    let authorization = match source {
        Quota::Zai(_) => key.to_owned(),
        _ => format!("Bearer {key}"),
    };
    let mut headers = HeaderMap::new();
    headers.insert("host", HeaderValue::from_str(&host).ok()?);
    let mut value = HeaderValue::from_str(&authorization).ok()?;
    value.set_sensitive(true);
    headers.insert("authorization", value);
    headers.insert("accept", HeaderValue::from_static("application/json"));
    let origin = Uri::try_from(format!(
        "{}://{}",
        target.scheme_str()?,
        target.authority()?.as_str()
    ))
    .ok()?;
    Some(UpstreamRequest {
        url: upstream_url(&origin, &path).ok()?,
        method: Method::GET,
        headers,
        body: RequestBody::Whole(hyper::body::Bytes::new()),
    })
}

struct Parsed {
    windows: Vec<Window>,
    balance: Option<Balance>,
}

/// The windows or balance in an answer body, or `None` when it is not the expected shape.
fn parse(source: Quota, body: &[u8], now_ms: u64) -> Option<Parsed> {
    let json: Value = serde_json::from_slice(body).ok()?;
    match source {
        Quota::None => None,
        Quota::Zai(_) => parse_zai(&json),
        Quota::MiniMax(_) => parse_minimax(&json, now_ms),
        Quota::Balance(url) if url.contains("deepseek") => parse_deepseek(&json),
        Quota::Balance(_) => parse_kimi(&json),
    }
}

fn window(name: &str, used_pct: u64, resets_ms: Option<u64>) -> Window {
    Window {
        name: name.to_owned(),
        used_pct: Some(used_pct),
        resets_ms,
        note: None,
    }
}

fn percent(value: &Value) -> Option<u64> {
    let number = value.as_f64().filter(|n| n.is_finite() && *n >= 0.0)?;
    Some(number.round().min(100.0) as u64)
}

/// `data.limits[]`: the first TOKENS_LIMIT is the 5 hour window, the second the week;
/// TIME_LIMIT is the month. A reset of null or 0 is unknown.
fn parse_zai(json: &Value) -> Option<Parsed> {
    let limits = json.get("data")?.get("limits")?.as_array()?;
    let mut tokens = 0;
    let mut windows = Vec::new();
    for limit in limits {
        let name = match limit.get("type")?.as_str()? {
            "TOKENS_LIMIT" => {
                tokens += 1;
                match tokens {
                    1 => "5 hours",
                    2 => "7 days",
                    _ => continue,
                }
            }
            "TIME_LIMIT" => "Month",
            _ => continue,
        };
        let used = percent(limit.get("percentage")?)?;
        let reset = limit
            .get("nextResetTime")
            .and_then(Value::as_u64)
            .filter(|ms| *ms > 0);
        windows.push(window(name, used, reset));
    }
    (!windows.is_empty()).then_some(Parsed {
        windows,
        balance: None,
    })
}

/// `balance_infos[0]`: a currency and a decimal string.
fn parse_deepseek(json: &Value) -> Option<Parsed> {
    let info = json.get("balance_infos")?.as_array()?.first()?;
    let amount = info
        .get("total_balance")?
        .as_str()?
        .trim()
        .parse::<f64>()
        .ok()?;
    let currency = info.get("currency")?.as_str()?.to_owned();
    amount.is_finite().then_some(Parsed {
        windows: Vec::new(),
        balance: Some(Balance { amount, currency }),
    })
}

/// `data.available_balance`; the `.ai` host bills in US dollars.
fn parse_kimi(json: &Value) -> Option<Parsed> {
    let amount = json.get("data")?.get("available_balance")?.as_f64()?;
    amount.is_finite().then_some(Parsed {
        windows: Vec::new(),
        balance: Some(Balance {
            amount,
            currency: "USD".to_owned(),
        }),
    })
}

/// `model_remains[]` with a total count, a used count and the milliseconds to the reset. The
/// field names come from secondary sources and are unverified until a live check.
fn parse_minimax(json: &Value, now_ms: u64) -> Option<Parsed> {
    let remains = json.get("model_remains")?.as_array()?;
    let mut windows = Vec::new();
    for item in remains {
        let total = item.get("current_interval_total_count")?.as_f64()?;
        let used = item.get("current_interval_usage_count")?.as_f64()?;
        if total <= 0.0 || used < 0.0 {
            return None;
        }
        let reset = item
            .get("remains_time")
            .and_then(Value::as_u64)
            .map(|ms| now_ms.saturating_add(ms));
        windows.push(window(
            "Plan",
            percent(&Value::from(used / total * 100.0))?,
            reset,
        ));
    }
    (!windows.is_empty()).then_some(Parsed {
        windows,
        balance: None,
    })
}
