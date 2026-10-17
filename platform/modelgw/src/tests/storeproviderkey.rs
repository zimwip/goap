use std::sync::Arc;

use goap_ctx::Ctx;
use parking_lot::Mutex;

use super::helpers::{memory, new_service};

#[tokio::test]
async fn test_store_provider_key_rejects_bad_name() {
    let (svc, _) = new_service(memory());
    let err = svc
        .store_provider_key(&Ctx::background(), "../etc", "k")
        .await
        .unwrap_err();
    assert!(err.is_invalid(), "want invalid, got {err}");
}

#[tokio::test]
async fn test_store_provider_key_rejects_empty_key() {
    let (svc, _) = new_service(memory());
    let err = svc
        .store_provider_key(&Ctx::background(), "acme", "")
        .await
        .unwrap_err();
    assert!(err.is_invalid(), "want invalid, got {err}");
}

#[tokio::test]
async fn test_store_provider_key_no_vault_configured() {
    let (svc, _) = new_service(memory()); // svc.vault is None
    assert!(
        svc.store_provider_key(&Ctx::background(), "acme", "sk-raw")
            .await
            .is_err(),
        "want an error when no vault write support is configured"
    );
}

#[tokio::test]
async fn test_store_provider_key_success() {
    let (mut svc, _) = new_service(memory());
    let got: Arc<Mutex<(String, String, String)>> = Arc::default();
    let g = got.clone();
    svc.vault = Some(Arc::new(move |_, path, field, value| {
        *g.lock() = (path, field, value);
        Box::pin(async { Ok(()) })
    }));
    let r = svc
        .store_provider_key(&Ctx::background(), "acme", "sk-raw")
        .await
        .unwrap();
    assert_eq!(r, "goap/modelgw/acme#api_key");
    let got = got.lock().clone();
    assert_eq!(
        got,
        (
            "goap/modelgw/acme".to_string(),
            "api_key".to_string(),
            "sk-raw".to_string()
        ),
        "vault called with {got:?}"
    );
}
