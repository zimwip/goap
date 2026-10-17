//! Port of `tag.go`: tags (ADR 0056) name the state a change leaves. A baseline is the state after a change, computed
//! from the history and possibly kept as a snapshot; naming one labels the change that produced it, it never makes a
//! baseline. Tags are not unique: a name may label several changes, a change may carry several names.

use goap_ctx::Ctx;
use goap_domain::{
    Baseline, CHANGE_APPLIED, Change, ChangeID, Tag, TagFilter, TagID, namespace_of,
};

use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

impl Graph {
    /// TagChange names the state change id leaves. The change must have landed (applied): a draft has no state yet.
    /// The same name on the same change is not repeated: the tag already there is returned.
    pub async fn tag_change(&self, ctx: &Ctx, id: &ChangeID, name: &str, by: &str) -> Result<Tag> {
        let mut tx = self.change_tx(ctx, id).await?;
        let c = tx.change(ctx, id).await?;
        let t = self.tag_tx(ctx, &mut tx, &c, name, by).await?;
        tx.commit(ctx).await?;
        Ok(t)
    }

    pub(crate) async fn tag_tx(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
        name: &str,
        by: &str,
    ) -> Result<Tag> {
        if name.is_empty() {
            return Err(Error::wrap(ErrorKind::Invalid, "a tag needs a name"));
        }
        if c.status != CHANGE_APPLIED {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!(
                    "change {} is {}: only the state of an applied change can be named",
                    c.id, c.status
                ),
            ));
        }
        let have = tx
            .tags(
                ctx,
                &TagFilter {
                    change: c.id.clone(),
                    name: name.to_string(),
                    ..Default::default()
                },
            )
            .await?;
        if let Some(t) = have.into_iter().next() {
            return Ok(t);
        }
        let t = Tag {
            id: TagID::new(self.new_id()),
            name: name.to_string(),
            namespace: namespace_of(&c.namespace).to_string(),
            change_id: c.id.clone(),
            baseline_id: c.result_baseline_id.clone(),
            by: by.to_string(),
            created_at: self.now(),
        };
        tx.put_tag(ctx, t.clone()).await?;
        Ok(t)
    }

    /// Tags lists the tags matching f, oldest first.
    pub async fn tags(&self, ctx: &Ctx, f: &TagFilter) -> Result<Vec<Tag>> {
        let mut tx = self.begin(ctx).await?;
        let out = tx.tags(ctx, f).await?;
        tx.commit(ctx).await?;
        Ok(out)
    }

    /// DeleteTag removes a tag; the change and its state are untouched.
    pub async fn delete_tag(&self, ctx: &Ctx, id: &TagID) -> Result<()> {
        let mut tx = self.begin(ctx).await?;
        tx.delete_tag(ctx, id).await?;
        tx.commit(ctx).await
    }

    /// StateAfter returns the state of the namespace after change id landed: the baseline that change produced.
    pub async fn state_after(&self, ctx: &Ctx, id: &ChangeID) -> Result<Baseline> {
        let mut tx = self.begin(ctx).await?;
        let c = tx.change(ctx, id).await?;
        if c.status != CHANGE_APPLIED || c.result_baseline_id.is_empty() {
            return Err(Error::wrap(
                ErrorKind::NotFound,
                format!("change {id} is {}: it has left no state", c.status),
            ));
        }
        let b = tx.baseline(ctx, &c.result_baseline_id).await?;
        tx.commit(ctx).await?;
        Ok(b)
    }
}
