package pattern

// GrokPrimitives is the effective primitive table used by CompileGrok.
// It is populated at init time from internal/pattern/embedded/primitives.json
// (or from disk when LoadConfig is called).
var GrokPrimitives = map[string]string{}

// GrokPrimitivesOverrides is an alias of GrokPrimitives kept for diagnostics.
// It is not separately consulted at CompileGrok time because GrokPrimitives
// is the merged effective map.
var GrokPrimitivesOverrides = GrokPrimitives
