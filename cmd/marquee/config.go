package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// configFile is the conventional flag file, next to the conventional hook. Its
// contents are the flags you would otherwise type: one per line, `#` starts a
// comment, blank lines are ignored.
//
// The file holds flags rather than a schema of its own on purpose. A second schema
// would need its own keys, its own validation and its own documentation, all of
// which can drift from the flag set; flags cannot drift from themselves. It also
// settles precedence without inventing rules: the file's words are parsed as if
// they came first on the command line, and Go's flag package already lets a later
// occurrence of a scalar flag win.
const configFile = "config"

func configPath(launchDir string) string {
	return filepath.Join(launchDir, conventionDir, configFile)
}

// configName is how the file is named in messages, before any launch directory is
// known.
var configName = filepath.Join(conventionDir, configFile)

// configFlags is what .marquee/config contributes to a run: the flag words the
// file holds, still grouped by the line each was written on, plus the path they
// came from. Keeping the grouping is what lets a flag error name its source rather
// than blame a command line that never mentioned the flag.
type configFlags struct {
	path  string
	lines []configFlagLine
}

type configFlagLine struct {
	number int
	words  []string
}

func (c configFlags) empty() bool { return len(c.lines) == 0 }

// args flattens the file's words back into one argv, in the order written.
func (c configFlags) args() []string {
	var args []string
	for _, line := range c.lines {
		args = append(args, line.words...)
	}
	return args
}

// loadConfigFlags reads .marquee/config in launchDir into flag words. No file means
// no words, which is the zero-config case. A file that exists but cannot be read
// or cannot be parsed is an error: it was written to change how marquee runs, and
// applying none of it while starting anyway is the one outcome nobody wants.
func loadConfigFlags(launchDir string) (configFlags, error) {
	path := configPath(launchDir)
	cfg := configFlags{path: path}
	data, err := os.ReadFile(path) // #nosec G304 -- a fixed name under the launch directory, never request input.
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("could not read %s: %w", path, err)
	}
	for i, line := range strings.Split(string(data), "\n") {
		words, err := splitConfigLine(line)
		if err != nil {
			return configFlags{}, fmt.Errorf("%s:%d: %w", path, i+1, err)
		}
		if len(words) == 0 {
			continue
		}
		if err := checkConfigLine(words); err != nil {
			return configFlags{}, fmt.Errorf("%s:%d: %w", path, i+1, err)
		}
		cfg.lines = append(cfg.lines, configFlagLine{number: i + 1, words: words})
	}
	return cfg, nil
}

// checkConfigLine holds the file to one flag per line. The shape is worth
// enforcing for its own sake (a line is readable as the thing you would have
// typed), but the reason it is checked here is that the file must not be able to
// choose the process marquee spawns: a word that is not a flag would end flag
// parsing and become the command, so a line that does not start with a flag, or
// carries a bare "--", is refused before anything is parsed.
func checkConfigLine(words []string) error {
	for _, word := range words {
		if word == "--" {
			return fmt.Errorf(`"--" is not allowed here: the command marquee runs comes from the command line only`)
		}
	}
	if !strings.HasPrefix(words[0], "-") {
		return fmt.Errorf("%q is not a flag: %s holds flags only, and the command marquee runs comes from the command line only", words[0], configName)
	}
	if len(words) > 2 {
		return fmt.Errorf("%d words on one line: write one flag per line, with at most its value after it", len(words))
	}
	return nil
}

// splitConfigLine splits one line into flag words the way a shell would split the
// same text: whitespace separates, `'` and `"` quote (so a value with a space or a
// glob survives), `\` escapes the next character outside single quotes, and an
// unquoted `#` starts a comment. Quoting is what the file needs to hold a value
// like *.example.test without the reader having to know when to escape.
func splitConfigLine(line string) ([]string, error) {
	var (
		words   []string
		word    strings.Builder
		started bool
	)
	// A quoted empty word is meaningful: --switch-hook '' is how the conventional
	// hook is turned off, so an empty word is kept once quoting started it.
	flush := func() {
		if started {
			words = append(words, word.String())
			word.Reset()
			started = false
		}
	}
	for i := 0; i < len(line); i++ {
		switch c := line[i]; c {
		case ' ', '\t', '\r':
			flush()
		case '#':
			flush()
			return words, nil
		case '\'':
			started = true
			end := strings.IndexByte(line[i+1:], '\'')
			if end < 0 {
				return nil, errors.New("unterminated ' quote")
			}
			word.WriteString(line[i+1 : i+1+end])
			i += end + 1
		case '"':
			started = true
			closed := false
			for i++; i < len(line); i++ {
				if line[i] == '\\' && i+1 < len(line) && (line[i+1] == '"' || line[i+1] == '\\') {
					i++
					word.WriteByte(line[i])
					continue
				}
				if line[i] == '"' {
					closed = true
					break
				}
				word.WriteByte(line[i])
			}
			if !closed {
				return nil, errors.New(`unterminated " quote`)
			}
		case '\\':
			if i+1 >= len(line) {
				return nil, errors.New(`line ends in a lone \`)
			}
			i++
			started = true
			word.WriteByte(line[i])
		default:
			started = true
			word.WriteByte(c)
		}
	}
	flush()
	return words, nil
}

// checkConfigFlags gives the file's own words a parse of their own, line by line,
// so a flag the set does not have or a value it cannot parse is reported against
// the line it was written on. Once this passes, any complaint left for the real
// parse belongs to the command line and keeps the wording it has always had.
//
// Line by line rather than all at once because checkConfigLine already holds the
// file to one flag per line, so a failing Parse call identifies the line without
// any inspection of how far the flag package got through an argv. The set is
// discarded afterwards, so setting values on it here cannot reach the real parse.
func checkConfigFlags(name string, cfg configFlags, out io.Writer) error {
	// Named set rather than fs, which in this file is the io/fs package.
	set, _ := newRunFlagSet(name, io.Discard)
	for _, line := range cfg.lines {
		err := set.Parse(line.words)
		if err == nil {
			continue
		}
		// -h in the file is not an error to attribute; the real parse turns it into
		// the usual help output and exit 0.
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		// The complaint is the flag package's own, so the file's flags are held to
		// exactly the flags that exist; all this adds is where the word came from.
		_, _ = fmt.Fprintf(out, "marquee: %s:%d: %v\n", cfg.path, line.number, err)
		set.SetOutput(out)
		set.Usage()
		return errUsage
	}
	return nil
}

// parseArgsWithConfig parses the config file's flags and the real command line as
// one argv, the file first. That ordering is the whole precedence rule: flag's
// last-occurrence-wins makes the command line override a scalar flag set in the
// file, while a repeatable flag (--allow-host, --worktree-glob) collects both, so
// a command-line host adds to the file's allowlist instead of replacing it.
func parseArgsWithConfig(name string, cfg configFlags, args []string, out io.Writer) (*options, error) {
	if err := checkConfigFlags(name, cfg, out); err != nil {
		return nil, err
	}
	configArgs := cfg.args()
	combined := make([]string, 0, len(configArgs)+len(args))
	combined = append(combined, configArgs...)
	combined = append(combined, args...)
	opts, err := parseArgs(name, combined, out)
	if err != nil {
		return nil, err
	}
	// What the file can never do is choose the process marquee spawns. checkConfigLine
	// rejects the shapes that would try, and this is the invariant behind it, checked
	// rather than reasoned about: everything flag parsing left over has to be a tail
	// of the real command line, which it cannot be if a word from the file ended
	// parsing early.
	if !isSuffix(args, opts.command) {
		_, _ = fmt.Fprintf(out, "marquee: %s cannot set the command marquee runs; it holds flags only, and everything after -- comes from the command line\n", configName)
		return nil, errUsage
	}
	return opts, nil
}

func isSuffix(all, tail []string) bool {
	if len(tail) > len(all) {
		return false
	}
	offset := len(all) - len(tail)
	for i, word := range tail {
		if all[offset+i] != word {
			return false
		}
	}
	return true
}
