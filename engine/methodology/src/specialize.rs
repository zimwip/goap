//! The generic Activity-specialization mechanism (`specialize.go`).

/// RankByPriority is the generic Activity-specialization mechanism (ADR 0009 §5, ADR 0035 §1): among candidates
/// each specializing the same, more generic Activity, the ones whose own condition holds, highest priority first
/// (ties keep declaration order: a stable sort).
pub fn rank_by_priority<T>(
    items: impl IntoIterator<Item = T>,
    applicable: impl Fn(&T) -> bool,
    priority: impl Fn(&T) -> i64,
) -> Vec<T> {
    let mut out: Vec<T> = items.into_iter().filter(|it| applicable(it)).collect();
    out.sort_by_key(|it| std::cmp::Reverse(priority(it)));
    out
}

#[cfg(test)]
mod tests {
    use super::rank_by_priority;

    #[test]
    fn test_rank_by_priority() {
        let items = [
            ("low", true, 1),
            ("excluded", false, 100),
            ("high", true, 10),
            ("tie-a", true, 5),
            ("tie-b", true, 5),
        ];
        let got = rank_by_priority(items, |i| i.1, |i| i.2);
        let names: Vec<&str> = got.iter().map(|i| i.0).collect();
        assert_eq!(names, ["high", "tie-a", "tie-b", "low"]);
    }

    #[test]
    fn test_rank_by_priority_empty() {
        let got = rank_by_priority([1i64, 2, 3], |_| false, |i| *i);
        assert!(got.is_empty());
    }
}
