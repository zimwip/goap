//! cel-go's `ext.Sets()`: `sets.contains`, `sets.equivalent`, `sets.intersects` over lists.
use cel::{
    DeclarationError, Env, ExecutionError,
    common::{
        types::{CelBool, LIST_TYPE},
        value::{CowVal, Val},
    },
};

pub fn extension(env: &mut Env) -> Result<(), DeclarationError> {
    env.add_overload(
        "sets.contains",
        "sets.contains(list,list)",
        vec![LIST_TYPE, LIST_TYPE],
        contains,
    )?;
    env.add_overload(
        "sets.equivalent",
        "sets.equivalent(list,list)",
        vec![LIST_TYPE, LIST_TYPE],
        equivalent,
    )?;
    env.add_overload(
        "sets.intersects",
        "sets.intersects(list,list)",
        vec![LIST_TYPE, LIST_TYPE],
        intersects,
    )?;
    Ok(())
}

fn two<'b, 'v>(
    name: &str,
    args: Vec<CowVal<'b, 'v>>,
) -> Result<[CowVal<'b, 'v>; 2], ExecutionError> {
    let n = args.len();
    args.try_into()
        .map_err(|_| ExecutionError::function_error(name, format!("expected 2 arguments, got {n}")))
}

/// Whether every element of `sub` is in `of`.
fn all_in(sub: &dyn Val, of: &dyn Val, name: &str) -> Result<bool, ExecutionError> {
    let (Some(a), Some(b)) = (sub.as_iterable(), of.as_iterable()) else {
        return Err(ExecutionError::function_error(name, "expected lists"));
    };
    let mut it = a.iter();
    while let Some(x) = it.next() {
        let mut found = false;
        let mut jt = b.iter();
        while let Some(y) = jt.next() {
            if x.equals(y) {
                found = true;
                break;
            }
        }
        if !found {
            return Ok(false);
        }
    }
    Ok(true)
}

fn boolean<'b, 'v>(b: bool) -> CowVal<'b, 'v> {
    CowVal::owned(CelBool::from(b))
}

fn contains<'b, 'v>(args: Vec<CowVal<'b, 'v>>) -> Result<CowVal<'b, 'v>, ExecutionError> {
    let [a, b] = two("sets.contains", args)?;
    Ok(boolean(all_in(b.as_ref(), a.as_ref(), "sets.contains")?))
}

fn equivalent<'b, 'v>(args: Vec<CowVal<'b, 'v>>) -> Result<CowVal<'b, 'v>, ExecutionError> {
    let [a, b] = two("sets.equivalent", args)?;
    Ok(boolean(
        all_in(a.as_ref(), b.as_ref(), "sets.equivalent")?
            && all_in(b.as_ref(), a.as_ref(), "sets.equivalent")?,
    ))
}

fn intersects<'b, 'v>(args: Vec<CowVal<'b, 'v>>) -> Result<CowVal<'b, 'v>, ExecutionError> {
    let [a, b] = two("sets.intersects", args)?;
    let (Some(x), Some(_)) = (a.as_ref().as_iterable(), b.as_ref().as_iterable()) else {
        return Err(ExecutionError::function_error(
            "sets.intersects",
            "expected lists",
        ));
    };
    let mut it = x.iter();
    while let Some(e) = it.next() {
        let one = cel::common::types::CelList::from(vec![e.clone_as_boxed()]);
        if all_in(&one, b.as_ref(), "sets.intersects")? {
            return Ok(boolean(true));
        }
    }
    Ok(boolean(false))
}
