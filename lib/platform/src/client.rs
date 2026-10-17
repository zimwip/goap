use std::time::Duration;

/// An HTTP client speaking HTTP/2 cleartext (prior knowledge), for service-to-service Connect calls inside the
/// cluster (`H2CClient`).
pub fn h2c_client() -> reqwest::Client {
    reqwest::Client::builder()
        .http2_prior_knowledge()
        .timeout(Duration::from_secs(5 * 60))
        .build()
        .unwrap_or_default()
}
