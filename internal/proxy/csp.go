package proxy

import (
	"net/http"
	"strings"
)

// cspHeader is the enforcing Content-Security-Policy header. The
// report-only twin (Content-Security-Policy-Report-Only) never blocks a
// resource, so it is deliberately left untouched.
const cspHeader = "Content-Security-Policy"

// relaxCSPForBar rewrites every enforcing Content-Security-Policy header on
// a response marquee is splicing the bar into, so the bar's own same-origin
// resources are permitted and nothing else changes: 'self' is ensured in the
// directive that governs <script> elements (so /__marquee/bar.js can load)
// and in connect-src (so the bar's fetch of /__marquee/status is allowed).
// It touches only these two directives, only ever adds 'self', and never
// broadens any other resource type. Report-only CSP headers are left alone.
//
// Fail-open is law: a panic cannot escape (the recover), and any header that
// parses to nothing usable is returned verbatim, so a surprising CSP is
// preserved rather than replaced by a malformed one.
func relaxCSPForBar(h http.Header) {
	defer func() { _ = recover() }()

	values := h.Values(cspHeader)
	if len(values) == 0 {
		return
	}
	rewritten := make([]string, len(values))
	for i, value := range values {
		rewritten[i] = relaxCSPValue(value)
	}
	h.Del(cspHeader)
	for _, value := range rewritten {
		h.Add(cspHeader, value)
	}
}

// cspDirective is one CSP directive: a name (original casing preserved for
// output; matched case-insensitively) and its space-separated source tokens.
type cspDirective struct {
	name   string
	tokens []string
}

// relaxCSPValue rewrites a single CSP header value. An unparseable or empty
// value is returned unchanged; a parseable one is reserialized deterministically.
func relaxCSPValue(value string) string {
	directives, ok := parseCSP(value)
	if !ok {
		return value
	}
	directives = ensureScriptSelf(directives)
	directives = ensureConnectSelf(directives)
	return serializeCSP(directives)
}

// parseCSP splits a CSP value into directives. It reports ok=false when the
// value carries no directive at all (empty or only separators), so the caller
// leaves such a header verbatim rather than emitting an empty policy.
func parseCSP(value string) ([]cspDirective, bool) {
	var directives []cspDirective
	for _, segment := range strings.Split(value, ";") {
		fields := strings.Fields(segment)
		if len(fields) == 0 {
			continue
		}
		directives = append(directives, cspDirective{name: fields[0], tokens: fields[1:]})
	}
	if len(directives) == 0 {
		return nil, false
	}
	return directives, true
}

func serializeCSP(directives []cspDirective) string {
	parts := make([]string, len(directives))
	for i, directive := range directives {
		if len(directive.tokens) == 0 {
			parts[i] = directive.name
			continue
		}
		parts[i] = directive.name + " " + strings.Join(directive.tokens, " ")
	}
	return strings.Join(parts, "; ")
}

// ensureScriptSelf guarantees 'self' for the directive that governs <script>
// elements. script-src-elem, when present, governs script elements and
// overrides script-src for them, so it wins; otherwise script-src; otherwise
// a new explicit script-src is derived from default-src (leaving default-src
// untouched so no other resource type is broadened). With no script-governing
// directive at all, there is no restriction to relax.
func ensureScriptSelf(directives []cspDirective) []cspDirective {
	if i := findDirective(directives, "script-src-elem"); i >= 0 {
		directives[i].tokens = ensureSelf(directives[i].tokens)
		return directives
	}
	if i := findDirective(directives, "script-src"); i >= 0 {
		directives[i].tokens = ensureSelf(directives[i].tokens)
		return directives
	}
	if i := findDirective(directives, "default-src"); i >= 0 {
		tokens := ensureSelf(copyTokens(directives[i].tokens))
		return append(directives, cspDirective{name: "script-src", tokens: tokens})
	}
	return directives
}

// ensureConnectSelf guarantees 'self' for connect-src (the directive that
// governs fetch), mirroring ensureScriptSelf's default-src fallback.
func ensureConnectSelf(directives []cspDirective) []cspDirective {
	if i := findDirective(directives, "connect-src"); i >= 0 {
		directives[i].tokens = ensureSelf(directives[i].tokens)
		return directives
	}
	if i := findDirective(directives, "default-src"); i >= 0 {
		tokens := ensureSelf(copyTokens(directives[i].tokens))
		return append(directives, cspDirective{name: "connect-src", tokens: tokens})
	}
	return directives
}

// ensureSelf returns tokens guaranteed to permit 'self'. It is idempotent
// when 'self' is already present. The exact token list 'none' is a total
// block that cannot legally be mixed with sources, so the minimal way to
// permit our resource is to replace 'none' with 'self'. No existing source is
// otherwise removed.
func ensureSelf(tokens []string) []string {
	if len(tokens) == 1 && strings.EqualFold(tokens[0], "'none'") {
		return []string{"'self'"}
	}
	for _, token := range tokens {
		if strings.EqualFold(token, "'self'") {
			return tokens
		}
	}
	return append(tokens, "'self'")
}

func findDirective(directives []cspDirective, name string) int {
	for i := range directives {
		if strings.EqualFold(directives[i].name, name) {
			return i
		}
	}
	return -1
}

func copyTokens(tokens []string) []string {
	out := make([]string, len(tokens))
	copy(out, tokens)
	return out
}
