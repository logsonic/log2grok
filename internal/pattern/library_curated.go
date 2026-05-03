package pattern

// KnownPatternsLibrary is the single, ordered source-of-truth for built-in
// pattern entries. It contains the previously-separated golden,
// curated, and catchall tiers — distinguished now only by their
// Priority/Specificity values. Order in the slice still matters: the
// first entry whose normalized pattern matches a later entry wins
// during dedup in composeKnownPatterns.
//
// Populated at init time from internal/pattern/embedded/patterns.json
// (or from disk when LoadConfig is called).
var KnownPatternsLibrary []KnownPattern
