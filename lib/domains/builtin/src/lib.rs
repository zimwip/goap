//! The domains shipped with the platform, frozen (ADR 0012 §4): the meta-domain `methodology` and the
//! `organisation` and `platform` domains. The YAML files of this directory are the source; `goap-def` parses them.

/// The meta-domain `methodology`.
pub const METHODOLOGY: &str = include_str!("../methodology.yaml");
/// The domain `organisation`.
pub const ORGANISATION: &str = include_str!("../organisation.yaml");
/// The domain `platform`.
pub const PLATFORM: &str = include_str!("../platform.yaml");

/// The YAML definitions of the built-in domains, by file name, in name order (Go's `FS`, `*.yaml`).
pub const FILES: &[(&str, &str)] = &[
    ("methodology.yaml", METHODOLOGY),
    ("organisation.yaml", ORGANISATION),
    ("platform.yaml", PLATFORM),
];

/// The content of a file (`organisation.yaml`).
pub fn file(name: &str) -> Option<&'static str> {
    FILES.iter().find(|(n, _)| *n == name).map(|(_, c)| *c)
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Every YAML file of the directory is embedded, and only those.
    #[test]
    fn every_yaml_is_embedded() {
        let mut on_disk: Vec<String> = std::fs::read_dir(env!("CARGO_MANIFEST_DIR"))
            .unwrap()
            .filter_map(|e| e.ok()?.file_name().into_string().ok())
            .filter(|n| n.ends_with(".yaml"))
            .collect();
        on_disk.sort();
        let embedded: Vec<&str> = FILES.iter().map(|(n, _)| *n).collect();
        assert_eq!(on_disk, embedded);
        for (name, content) in FILES {
            assert!(!content.is_empty(), "{name}");
            assert_eq!(
                std::fs::read_to_string(format!("{}/{name}", env!("CARGO_MANIFEST_DIR"))).unwrap(),
                *content
            );
        }
        assert!(file("organisation.yaml").unwrap().contains("OrgUnit"));
        assert!(file("nope.yaml").is_none());
    }
}
