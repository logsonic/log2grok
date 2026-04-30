package pattern

// GrokPrimitives is the merged effective table used by CompileGrok.
// The literal map below is the local override layer; init() in bundle.go
// merges in primitives loaded from BuiltinPatternPacks. The literal entries
// always win on key collision.
var GrokPrimitives = map[string]string{
	// Generic text
	"WORD":         `\w+`,
	"NOTSPACE":     `\S+`,
	"SPACE":        `\s*`,
	"DATA":         `.*?`,
	"GREEDYDATA":   `.*`,
	"QUOTEDSTRING": `"(?:\\.|[^"\\])*"`,
	"QS":           `%{QUOTEDSTRING}`,

	// Numeric
	"INT":       `[+-]?\d+`,
	"NONNEGINT": `\d+`,
	"POSINT":    `[1-9]\d*`,
	"NUMBER":    `-?\d+(?:\.\d+)?`,
	"BASE10NUM": `[+-]?(?:\d+(?:\.\d+)?|\.\d+)`,
	"BASE16NUM": `(?:0[xX])?[0-9A-Fa-f]+`,
	"FLOAT":     `[+-]?(?:\d+(?:\.\d+)?|\.\d+)(?:[eE][+-]?\d+)?`,
	"BOOLEAN":   `(?i:true|false|yes|no|on|off)`,
	"POSREAL":   `(?:[0-9]*\.[0-9]+|[0-9]+)`,

	// Identifiers
	"NOTEMPTY":       `.+`,
	"UUIDURN":        `urn:uuid:%{UUID}`,
	"COLONURI":       `(?:[A-Za-z][A-Za-z0-9-]{0,31}):[^\s]+`,
	"USER":           `[a-zA-Z0-9._-]+`,
	"USERNAME":       `[a-zA-Z0-9._-]+`,
	"PROG":           `[A-Za-z0-9._/%-]+`,
	"PROGNAME":       `[A-Za-z0-9._-]+`,
	"PROCID":         `[A-Za-z0-9._-]+`,
	"THREAD":         `[A-Za-z0-9._#-]+`,
	"LOGGER":         `[A-Za-z0-9_.$-]+`,
	"EMAILADDRESS":   `[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`,
	"EMAILLOCALPART": `[A-Za-z0-9._%+-]+`,
	"UUID":           `[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`,
	"TRACEID":        `[0-9a-fA-F]{16,32}`,
	"SPANID":         `[0-9a-fA-F]{16}`,

	// Network
	"IPV4":     `(?:(?:25[0-5]|2[0-4]\d|[01]?\d?\d)\.){3}(?:25[0-5]|2[0-4]\d|[01]?\d?\d)`,
	"IPV6":     `(?:[0-9A-Fa-f]{1,4}:){7}[0-9A-Fa-f]{1,4}|(?:[0-9A-Fa-f]{1,4}:){1,7}:|(?:[0-9A-Fa-f]{1,4}:){1,6}:[0-9A-Fa-f]{1,4}|(?:[0-9A-Fa-f]{1,4}:){1,5}(?::[0-9A-Fa-f]{1,4}){1,2}|(?:[0-9A-Fa-f]{1,4}:){1,4}(?::[0-9A-Fa-f]{1,4}){1,3}|(?:[0-9A-Fa-f]{1,4}:){1,3}(?::[0-9A-Fa-f]{1,4}){1,4}|(?:[0-9A-Fa-f]{1,4}:){1,2}(?::[0-9A-Fa-f]{1,4}){1,5}|[0-9A-Fa-f]{1,4}:(?:(?::[0-9A-Fa-f]{1,4}){1,6})|:(?:(?::[0-9A-Fa-f]{1,4}){1,7}|:)`,
	"IP":       `(?:%{IPV6}|%{IPV4})`,
	"HOSTNAME": `[A-Za-z0-9_](?:[A-Za-z0-9_-]{0,61}[A-Za-z0-9_])?(?:\.[A-Za-z0-9_](?:[A-Za-z0-9_-]{0,61}[A-Za-z0-9_])?)*`,
	"IPORHOST": `(?:%{IP}|%{HOSTNAME})`,
	"HOSTPORT": `%{IPORHOST}:%{POSINT}`,
	"IPV4PORT": `%{IPV4}:%{POSINT}`,
	"MAC":      `(?:[0-9A-Fa-f]{2}[:-]){5}[0-9A-Fa-f]{2}`,

	// URI
	"URIPATHSEGMENT": `[^\/?#\s]+`,
	"URIQUERY":       `[^#\s]*`,
	"URI_FRAGMENT":   `[^\s]*`,
	"URIPATH":        `/[^\s?#]*`,
	"URIPARAM":       `\?[^\s#]*`,
	"URIPATHPARAM":   `%{URIPATH}(?:%{URIPARAM})?`,
	"URIPROTO":       `[A-Za-z][A-Za-z0-9+\-.]*`,
	"URIHOST":        `%{IPORHOST}(?::%{POSINT})?`,
	"URI":            `%{URIPROTO}://%{URIHOST}%{URIPATHPARAM}?`,
	"URL":            `%{URI}`,
	"HTTPVERSION":    `(?:0\.9|1\.0|1\.1|2(?:\.0)?|3(?:\.0)?)`,
	"HTTPVERB":       `(?i:GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS|TRACE|CONNECT)`,
	"REQUEST":        `%{HTTPVERB}\s+%{NOTSPACE}(?:\s+HTTP/%{HTTPVERSION})?`,
	"WINPATH":        `(?:[A-Za-z]:|\\\\)[^\s]*`,
	"UNIXPATH":       `/(?:[\w.\-]+/)*[\w.\-]*`,
	"PATH":           `(?:%{WINPATH}|%{UNIXPATH})`,

	// Timestamps
	"YEAR":               `\d{4}`,
	"YEAR2":              `\d{2}`,
	"MONTHNUM":           `(?:0?[1-9]|1[0-2])`,
	"MONTHNUM2":          `(?:0[1-9]|1[0-2])`,
	"MONTHDAY":           `(?:0?[1-9]|[12]\d|3[01])`,
	"MONTHDAY2":          `(?:0[1-9]|[12]\d|3[01])`,
	"MONTH":              `(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)(?:[a-z]{2,6})?`,
	"DAY":                `(?:Mon|Tue|Wed|Thu|Fri|Sat|Sun)(?:[a-z]{2,6})?`,
	"HOUR":               `(?:2[0-3]|[01]?\d)`,
	"MINUTE":             `[0-5]?\d`,
	"SECOND":             `(?:[0-5]?\d|60)(?:[:.,]\d+)?`,
	"TZ":                 `[A-Z]{2,5}|[+-]\d{2}:?\d{2}|Z`,
	"ISO8601_TIMEZONE":   `(?:Z|[+-]\d{2}:?\d{2})`,
	"ISO8601_SECOND":     `(?:[0-5]?\d|60)(?:[:.,]\d+)?`,
	"TIMESTAMP_ISO8601":  `\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:[.,]\d+)?(?: ?(?:Z|[+-]\d{2}:?\d{2}))?`,
	"HTTPDATE":           `\d{2}/[A-Za-z]{3}/\d{4}:\d{2}:\d{2}:\d{2} [+-]\d{4}`,
	"HTTPDATE_CONDENSED": `\d{2}/[A-Za-z]{3}/\d{4} \d{2}:\d{2}:\d{2}`,
	"DATESTAMP_RFC822":   `%{DAY} %{MONTH} %{MONTHDAY} %{YEAR} %{TIME} %{TZ}`,
	"DATESTAMP_OTHER":    `%{DAY} %{MONTH} %{MONTHDAY} %{TIME} %{TZ} %{YEAR}`,
	"SYSLOGTIMESTAMP":    `(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec)\s+\d{1,2} \d{2}:\d{2}:\d{2}`,
	"TIME":               `(?:2[0-3]|[01]?\d):[0-5]\d(?::[0-5]\d(?:\.\d+)?)?`,
	"DATE":               `\d{4}-\d{2}-\d{2}`,
	"DATESTAMP":          `\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}`,
	"UNIX":               `\d{10}`,
	"UNIXMS":             `\d{13}`,
	"DURATION":           `\d+(?:\.\d+)?(?:ns|us|µs|ms|s|m|h)`,
	"SYSLOG5424SD":       `\[(?:[A-Za-z0-9@._-]+)(?: [A-Za-z0-9@._-]+=(?:"(?:\\.|[^"\\])*"))*\](?:\[(?:[A-Za-z0-9@._-]+)(?: [A-Za-z0-9@._-]+=(?:"(?:\\.|[^"\\])*"))*\])*`,
	"SYSLOGFACILITY":     `<%{NONNEGINT:facility}\.%{NONNEGINT:priority}>`,
	"SYSLOGPROG":         `%{PROG}(?:\[%{POSINT:pid}\])?`,
	"SYSLOGBASE":         `%{SYSLOGTIMESTAMP:timestamp} %{SYSLOGHOST:logsource} %{SYSLOGPROG}:`,
	"SYSLOGHOST":         `%{IPORHOST}`,

	// Log levels
	"LOGLEVEL": `(?i:trace|debug|info|notice|warn(?:ing)?|err(?:or)?|crit(?:ical)?|fatal|panic|alert|emerg(?:ency)?|verbose|log|statement|audit|severe|inf|wrn|dbg|crt|ftl|trc)`,

	// Database/JVM/Redis-specific helpers used by curated entries.
	"JAVACLASS":  `(?:[A-Za-z0-9_$]+\.)*[A-Za-z0-9_$]+`,
	"REDISLEVEL": `[.\-*#]`,
}

// GrokPrimitivesBundled is populated in init() by bundle.go from
// BuiltinPatternPacks. Keys missing from GrokPrimitives are merged in.
var GrokPrimitivesBundled = map[string]string{}

// GrokPrimitivesOverrides is the union of literal GrokPrimitives entries
// and any override files loaded from internal/pattern/overrides/primitives.
// We keep it accessible for diagnostics; it is not separately consulted at
// CompileGrok time because GrokPrimitives is the merged effective map.
var GrokPrimitivesOverrides = GrokPrimitives
