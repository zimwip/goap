use std::collections::{BTreeMap, HashMap, HashSet};

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_domain::{NodeID, Version};
use parking_lot::Mutex;

use crate::index::{cosine, kind_of};
use crate::{Doc, Error, Filter, Hit, KIND_NODE, Ref, Store, tokens};

/// Memory is an in-memory Store (tests, and goap-dev without a database). Its full text is a token match ranked by
/// term frequency; vectors are scanned exactly.
#[derive(Default)]
pub struct Memory {
    docs: Mutex<HashMap<Ref, Doc>>,
}

impl Memory {
    /// NewMemory returns an empty store.
    pub fn new() -> Self {
        Self::default()
    }

    fn scan(&self, f: &Filter, score: impl Fn(&Doc) -> Option<f64>, limit: usize) -> Vec<Hit> {
        let docs = self.docs.lock();
        let mut out: Vec<Hit> = Vec::new();
        for d in docs.values() {
            let mut h = d.hit();
            if !f.matches(&h) {
                continue;
            }
            if let Some(s) = score(d) {
                h.score = s;
                out.push(h);
            }
        }
        out.sort_by(|a, b| {
            b.score
                .partial_cmp(&a.score)
                .unwrap_or(std::cmp::Ordering::Equal)
                .then_with(|| a.key.cmp(&b.key))
                .then_with(|| a.version.cmp(&b.version))
        });
        if limit > 0 && out.len() > limit {
            out.truncate(limit);
        }
        out
    }
}

#[async_trait]
impl Store for Memory {
    async fn upsert(&self, _ctx: &Ctx, mut d: Doc) -> Result<(), Error> {
        let mut docs = self.docs.lock();
        if let Some(old) = docs.get(&d.r#ref()) {
            d.main = d.main || old.main;
            if d.embedding.is_none() && old.hash == d.hash {
                d.embedding = old.embedding.clone();
            }
        }
        d.kind = kind_of(&d.kind).to_string();
        docs.insert(d.r#ref(), d);
        Ok(())
    }

    async fn hash(
        &self,
        _ctx: &Ctx,
        kind: &str,
        id: &NodeID,
        v: Version,
    ) -> Result<Option<(String, bool)>, Error> {
        let docs = self.docs.lock();
        let r = Ref {
            kind: kind_of(kind).to_string(),
            id: id.clone(),
            version: v,
        };
        Ok(docs
            .get(&r)
            .map(|d| (d.hash.clone(), d.embedding.is_some())))
    }

    async fn delete(&self, _ctx: &Ctx, kind: &str, id: &NodeID) -> Result<(), Error> {
        let kind = kind_of(kind);
        self.docs
            .lock()
            .retain(|r, _| !(r.kind == kind && r.id == *id));
        Ok(())
    }

    async fn texts(&self, _ctx: &Ctx, refs: &[Ref]) -> Result<HashMap<Ref, String>, Error> {
        let docs = self.docs.lock();
        Ok(refs
            .iter()
            .filter_map(|r| docs.get(r).map(|d| (r.clone(), d.text.clone())))
            .collect())
    }

    async fn vector(
        &self,
        _ctx: &Ctx,
        kind: &str,
        id: &NodeID,
    ) -> Result<Option<(Hit, Option<Vec<f32>>)>, Error> {
        let kind = kind_of(kind);
        let docs = self.docs.lock();
        let mut best: Option<&Doc> = None;
        for (r, d) in docs.iter() {
            if r.kind != kind || r.id != *id {
                continue;
            }
            let better = match best {
                None => true,
                Some(b) => (d.main && !b.main) || (d.main == b.main && d.version > b.version),
            };
            if better {
                best = Some(d);
            }
        }
        Ok(best.map(|d| (d.hit(), d.embedding.clone())))
    }

    async fn set_main(
        &self,
        _ctx: &Ctx,
        set: &BTreeMap<NodeID, Version>,
        removed: &[NodeID],
    ) -> Result<(), Error> {
        let gone: HashSet<&NodeID> = removed.iter().collect();
        for (r, d) in self.docs.lock().iter_mut() {
            if r.kind != KIND_NODE {
                continue;
            }
            if let Some(v) = set.get(&r.id) {
                d.main = *v == r.version;
            } else if gone.contains(&r.id) {
                d.main = false;
            }
        }
        Ok(())
    }

    async fn list(&self, _ctx: &Ctx, f: &Filter, limit: usize) -> Result<Vec<Hit>, Error> {
        Ok(self.scan(f, |_| Some(0.0), limit))
    }

    async fn full_text(
        &self,
        _ctx: &Ctx,
        text: &str,
        f: &Filter,
        limit: usize,
    ) -> Result<Vec<Hit>, Error> {
        let terms = tokens(text);
        Ok(self.scan(
            f,
            |d| {
                let toks = tokens(&format!("{} {}", d.key, d.text));
                let mut score = 0.0;
                for t in &terms {
                    let n = toks.iter().filter(|x| x.starts_with(t.as_str())).count();
                    if n == 0 {
                        return None; // every term must match
                    }
                    score += n as f64;
                }
                (!terms.is_empty()).then_some(score)
            },
            limit,
        ))
    }

    async fn nearest(
        &self,
        _ctx: &Ctx,
        vec: &[f32],
        f: &Filter,
        limit: usize,
    ) -> Result<Vec<Hit>, Error> {
        Ok(self.scan(f, |d| d.embedding.as_ref().map(|e| cosine(vec, e)), limit))
    }

    async fn reset(&self, _ctx: &Ctx) -> Result<(), Error> {
        self.docs.lock().clear();
        Ok(())
    }
}
