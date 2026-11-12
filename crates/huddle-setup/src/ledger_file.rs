//! The ledgers are TOML files holding typed structs. A settings.json value is arbitrary JSON and
//! TOML has no null, so such a field is kept as a JSON string (`before = '{"a":null}'`) through
//! [`json_string`]; the restore stays byte for byte.

use std::path::Path;

use serde::Serialize;
use serde::de::DeserializeOwned;

use crate::settings::SettingsError;

/// The ledger's text.
pub fn render<T: Serialize>(path: &Path, ledger: &T) -> Result<String, SettingsError> {
    toml::to_string(ledger).map_err(|err| SettingsError::Toml {
        path: path.to_path_buf(),
        message: err.to_string(),
    })
}

/// The ledger the file's bytes hold.
pub fn parse<T: DeserializeOwned>(path: &Path, bytes: &[u8]) -> Result<T, SettingsError> {
    toml::from_str(&String::from_utf8_lossy(bytes)).map_err(|err| SettingsError::Toml {
        path: path.to_path_buf(),
        message: err.message().to_string(),
    })
}

/// `#[serde(with = "json_string")]`: the field travels as one JSON string.
pub mod json_string {
    use serde::de::{DeserializeOwned, Error as _};
    use serde::ser::Error as _;
    use serde::{Deserialize, Deserializer, Serialize, Serializer};

    pub fn serialize<T: Serialize, S: Serializer>(
        value: &T,
        serializer: S,
    ) -> Result<S::Ok, S::Error> {
        let text = serde_json::to_string(value).map_err(S::Error::custom)?;
        serializer.serialize_str(&text)
    }

    pub fn deserialize<'de, T: DeserializeOwned, D: Deserializer<'de>>(
        deserializer: D,
    ) -> Result<T, D::Error> {
        let text = String::deserialize(deserializer)?;
        serde_json::from_str(&text).map_err(D::Error::custom)
    }
}

#[cfg(test)]
mod tests {
    use std::collections::BTreeMap;
    use std::path::PathBuf;

    use serde::{Deserialize, Serialize};
    use serde_json::{Value, json};

    use super::*;
    use crate::model_entries::ModelLedger;
    use crate::settings::Ledger;

    fn path() -> PathBuf {
        PathBuf::from("ledger.toml")
    }

    #[derive(Debug, PartialEq, Serialize, Deserialize)]
    struct Holder {
        #[serde(with = "json_string")]
        value: Value,
    }

    #[test]
    fn t2_a_json_value_with_null_nesting_and_quotes_round_trips() {
        let holder = Holder {
            value: json!({"a": null, "n": {"list": [1, "two \"quoted\"", null]}, "q": "it's \"x\""}),
        };
        let text = render(&path(), &holder).expect("render");
        assert!(text.starts_with("value = "), "{text}");
        assert_eq!(
            parse::<Holder>(&path(), text.as_bytes()).expect("parse"),
            holder
        );
    }

    #[test]
    fn t2_the_settings_ledger_round_trips_with_absent_keys() {
        let ledger = Ledger {
            created_file: false,
            created_env: true,
            before: BTreeMap::from([
                (
                    "HTTPS_PROXY".to_string(),
                    Some("http://a \"b\"".to_string()),
                ),
                ("NO_PROXY".to_string(), None),
            ]),
            after_sha256: "ab12".to_string(),
            linked_bin: true,
        };
        let text = render(&path(), &ledger).expect("render");
        assert_eq!(
            parse::<Ledger>(&path(), text.as_bytes()).expect("parse"),
            ledger
        );
    }

    #[test]
    fn t2_the_provider_ledger_round_trips() {
        let ledger = ModelLedger {
            created_file: true,
            options_added: vec!["zai:glm".to_string()],
            available_appended: vec!["zai:glm \"x\"".to_string()],
            efforts_added: BTreeMap::from([("zai:glm".to_string(), "high".to_string())]),
            created_model_picker: true,
            created_options: false,
            created_model_settings: true,
            after_sha256: "cd34".to_string(),
        };
        let text = render(&path(), &ledger).expect("render");
        assert_eq!(
            parse::<ModelLedger>(&path(), text.as_bytes()).expect("parse"),
            ledger
        );
    }

    #[test]
    fn t2_a_broken_ledger_is_a_toml_error_naming_the_file() {
        let err = parse::<Ledger>(&path(), b"createdFile = ").unwrap_err();
        assert!(
            err.to_string().starts_with("ledger.toml: not valid TOML ("),
            "{err}"
        );
    }
}
