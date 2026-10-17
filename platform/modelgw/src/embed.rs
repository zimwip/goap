//! Embeddings (ADR 0026): the providers that can embed texts, and the router's embedding call.

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_llm as llm;
use serde::Deserialize;
use serde_json::json;

use crate::protocols::{GEMINI_BASE, Gemini, http_json, trim_base};
use crate::providers::{Fake, OpenAICompatible};
use crate::router::{EmbedCallFn, Router};

/// Embedder is implemented by the providers that can embed texts (ADR 0026). The anthropic protocol has none.
#[async_trait]
pub trait Embedder: Send + Sync {
    /// The vectors of the texts, in order, and the tokens the provider counted.
    async fn embed(
        &self,
        ctx: &Ctx,
        model: &str,
        texts: &[String],
    ) -> Result<(Vec<Vec<f32>>, i64), llm::Error>;
}

impl Router {
    /// Embed implements the embedding call of a provider on the model that alias or "provider/model" designates.
    pub async fn embed(
        &self,
        ctx: &Ctx,
        mut req: llm::EmbedRequest,
    ) -> Result<llm::EmbedResponse, llm::Error> {
        if req.model.is_empty() {
            req.model = llm::EMBED_ALIAS.to_string();
        }
        let (t, p) = self.resolve(&req.model)?;
        if p.embedder().is_none() {
            return Err(llm::Error::Message(format!(
                "{}/{}: the provider cannot embed",
                t.provider, t.model
            )));
        }
        let target = t.clone();
        let n = req.texts.len();
        let call: EmbedCallFn = Box::new(move |ctx: Ctx| {
            Box::pin(async move {
                let e = p.embedder().expect("checked above");
                let (vecs, tokens) = e.embed(&ctx, &target.model, &req.texts).await?;
                if vecs.len() != req.texts.len() {
                    return Err(llm::Error::Message(format!(
                        "{} vectors for {} texts",
                        vecs.len(),
                        req.texts.len()
                    )));
                }
                Ok(llm::EmbedResponse {
                    vectors: vecs,
                    provider: target.provider.clone(),
                    model: target.model.clone(),
                    tokens,
                })
            })
        });
        let res = match &self.instrument_embed {
            Some(i) => i(ctx.clone(), t.provider.clone(), t.model.clone(), n, call).await,
            None => call(ctx.clone()).await,
        };
        res.map_err(|e| e.wrap(format!("{}/{}", t.provider, t.model)))
    }
}

#[async_trait]
impl llm::Embedder for Router {
    async fn embed(
        &self,
        ctx: &Ctx,
        req: llm::EmbedRequest,
    ) -> Result<llm::EmbedResponse, llm::Error> {
        Router::embed(self, ctx, req).await
    }
}

/// Embed implements Embedder on an OpenAI-compatible /embeddings endpoint.
#[async_trait]
impl Embedder for OpenAICompatible {
    async fn embed(
        &self,
        ctx: &Ctx,
        model: &str,
        texts: &[String],
    ) -> Result<(Vec<Vec<f32>>, i64), llm::Error> {
        #[derive(Deserialize, Default)]
        #[serde(default)]
        struct Data {
            index: i64,
            embedding: Vec<f32>,
        }
        #[derive(Deserialize, Default)]
        #[serde(default)]
        struct Usage {
            total_tokens: i64,
        }
        #[derive(Deserialize, Default)]
        #[serde(default)]
        struct Out {
            data: Vec<Data>,
            usage: Usage,
        }
        let mut headers = Vec::new();
        if !self.api_key.is_empty() {
            headers.push(("Authorization", format!("Bearer {}", self.api_key)));
        }
        let u = format!("{}/embeddings", self.base_url.trim_end_matches('/'));
        let out: Out = http_json(
            ctx,
            self.http.as_ref(),
            reqwest::Method::POST,
            &u,
            &headers,
            Some(json!({"model": model, "input": texts})),
        )
        .await
        .map_err(|e| e.wrap("embeddings"))?;
        let mut vecs = vec![Vec::new(); texts.len()];
        for d in out.data {
            if d.index < 0 || d.index as usize >= vecs.len() {
                return Err(llm::Error::Message(format!(
                    "embeddings: index {} out of range",
                    d.index
                )));
            }
            vecs[d.index as usize] = d.embedding;
        }
        Ok((vecs, out.usage.total_tokens))
    }
}

/// Embed implements Embedder with the Gemini batchEmbedContents method.
#[async_trait]
impl Embedder for Gemini {
    async fn embed(
        &self,
        ctx: &Ctx,
        model: &str,
        texts: &[String],
    ) -> Result<(Vec<Vec<f32>>, i64), llm::Error> {
        #[derive(Deserialize, Default)]
        #[serde(default)]
        struct Embedding {
            values: Vec<f32>,
        }
        #[derive(Deserialize, Default)]
        #[serde(default)]
        struct Out {
            embeddings: Vec<Embedding>,
        }
        let reqs: Vec<serde_json::Value> = texts
            .iter()
            .map(|t| json!({"model": format!("models/{model}"), "content": {"parts": [{"text": t}]}}))
            .collect();
        let u = format!(
            "{}/models/{}:batchEmbedContents",
            trim_base(&self.base_url, GEMINI_BASE),
            path_escape(model)
        );
        let out: Out = http_json(
            ctx,
            self.http.as_ref(),
            reqwest::Method::POST,
            &u,
            &[("x-goog-api-key", self.api_key.clone())],
            Some(json!({"requests": reqs})),
        )
        .await
        .map_err(|e| e.wrap("batchEmbedContents"))?;
        Ok((out.embeddings.into_iter().map(|e| e.values).collect(), 0))
    }
}

/// Go's `url.PathEscape`.
pub(crate) fn path_escape(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for b in s.bytes() {
        match b {
            b'A'..=b'Z'
            | b'a'..=b'z'
            | b'0'..=b'9'
            | b'-'
            | b'_'
            | b'.'
            | b'~'
            | b'$'
            | b'&'
            | b'+'
            | b','
            | b':'
            | b'='
            | b'@' => out.push(b as char),
            _ => out.push_str(&format!("%{b:02X}")),
        }
    }
    out
}

/// FakeEmbedDim is the dimension of the fake embeddings.
pub const FAKE_EMBED_DIM: usize = 64;

/// FNV-1a, 32 bits.
fn fnv32a(b: &[u8]) -> u32 {
    let mut h: u32 = 0x811c9dc5;
    for c in b {
        h ^= u32::from(*c);
        h = h.wrapping_mul(0x01000193);
    }
    h
}

/// Embed implements Embedder without a model: a bag of hashed words, so that texts sharing words are near
/// (dev without API keys and tests).
#[async_trait]
impl Embedder for Fake {
    async fn embed(
        &self,
        _ctx: &Ctx,
        _model: &str,
        texts: &[String],
    ) -> Result<(Vec<Vec<f32>>, i64), llm::Error> {
        let mut vecs = Vec::with_capacity(texts.len());
        let mut tokens = 0;
        for t in texts {
            let mut v = vec![0f32; FAKE_EMBED_DIM];
            let lower = t.to_lowercase();
            for w in lower
                .split(|r: char| {
                    !(r.is_ascii_lowercase() || r.is_ascii_digit() || (r as u32) > 127)
                })
                .filter(|w| !w.is_empty())
            {
                v[fnv32a(w.as_bytes()) as usize % FAKE_EMBED_DIM] += 1.0;
                tokens += 1;
            }
            vecs.push(v);
        }
        Ok((vecs, tokens))
    }
}
