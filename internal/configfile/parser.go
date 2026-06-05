package configfile

import (
	"fmt"
	"strings"
)

// Parser states for the .load file parser.
type parseState int

const (
	stateInit         parseState = iota // looking for LOAD keyword
	stateLoadType                       // reading source type (DATABASE, CSV)
	stateFrom                           // reading FROM clause
	stateInto                           // reading INTO clause
	stateTargetTable                    // reading TARGET TABLE
	stateTargetSchema                   // reading TARGET SCHEMA
	stateWith                           // reading WITH options
	stateSet                            // reading SET settings
	stateCast                           // reading CAST rules
	stateBeforeLoad                     // reading BEFORE LOAD DO
	stateAfterLoad                      // reading AFTER LOAD DO
	stateIncluding                      // reading INCLUDING ONLY TABLE NAMES
	stateExcluding                      // reading EXCLUDING TABLE NAMES
	stateMaterialize                    // reading MATERIALIZE VIEWS
	stateDone                           // command terminated by ;
)

// ParseFile parses a pgloader .load file and returns the parsed commands.
func ParseFile(input string) (*ConfigFile, error) {
	cleaned := removeComments(input)
	if strings.TrimSpace(cleaned) == "" {
		return nil, fmt.Errorf("empty or comment-only config file")
	}

	commands, err := splitCommands(cleaned)
	if err != nil {
		return nil, fmt.Errorf("split commands: %w", err)
	}

	cf := &ConfigFile{}
	for i, cmd := range commands {
		parsed, err := parseCommand(cmd)
		if err != nil {
			return nil, fmt.Errorf("command %d: %w", i+1, err)
		}
		cf.Commands = append(cf.Commands, parsed)
	}

	if len(cf.Commands) == 0 {
		return nil, fmt.Errorf("no LOAD commands found in config file")
	}

	return cf, nil
}

// removeComments strips -- line comments and /* */ block comments.
func removeComments(s string) string {
	var out strings.Builder
	out.Grow(len(s))

	i := 0
	for i < len(s) {
		if i+1 < len(s) && s[i] == '/' && s[i+1] == '*' {
			i += 2
			for i < len(s) {
				if i+1 < len(s) && s[i] == '*' && s[i+1] == '/' {
					i += 2
					break
				}
				i++
			}
			continue
		}
		if i+1 < len(s) && s[i] == '-' && s[i+1] == '-' {
			i += 2
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

// splitCommands splits the input into separate commands at ; boundaries,
// respecting dollar-quoted ($$...$$) and single-quoted ('...') strings.
func splitCommands(s string) ([]string, error) {
	var commands []string
	var cur strings.Builder

	i := 0
	for i < len(s) {
		switch {
		case strings.HasPrefix(s[i:], "$$"):
			cur.WriteString("$$")
			i += 2
			end := strings.Index(s[i:], "$$")
			if end < 0 {
				return nil, fmt.Errorf("unclosed dollar-quoted string")
			}
			cur.WriteString(s[i : i+end])
			cur.WriteString("$$")
			i += end + 2
		case s[i] == '\'':
			cur.WriteByte('\'')
			i++
			for i < len(s) {
				if s[i] == '\'' {
					if i+1 < len(s) && s[i+1] == '\'' {
						cur.WriteString("''")
						i += 2
						continue
					}
					cur.WriteByte('\'')
					i++
					break
				}
				cur.WriteByte(s[i])
				i++
			}
		case s[i] == ';':
			cmd := strings.TrimSpace(cur.String())
			if cmd != "" {
				commands = append(commands, cmd)
			}
			cur.Reset()
			i++
		default:
			cur.WriteByte(s[i])
			i++
		}
	}

	remaining := strings.TrimSpace(cur.String())
	if remaining != "" {
		return nil, fmt.Errorf("missing semicolon after command: %q...", truncate(remaining, 60))
	}

	return commands, nil
}

// parseCommand parses a single LOAD command (without trailing ;).
func parseCommand(s string) (*LoadCommand, error) {
	s = strings.TrimSpace(s)
	if !hasPrefixFold(s, "LOAD") {
		return nil, fmt.Errorf("command must start with LOAD")
	}

	cmd := &LoadCommand{}
	state := stateInit
	pos := 0
	var err error

	for pos < len(s) {
		for pos < len(s) && (s[pos] == ' ' || s[pos] == '\t' || s[pos] == '\n' || s[pos] == '\r') {
			pos++
		}
		if pos >= len(s) {
			break
		}

		remaining := s[pos:]

		switch state {
		case stateInit:
			if !hasPrefixFold(remaining, "LOAD") {
				return nil, fmt.Errorf("expected LOAD keyword")
			}
			pos += 4
			state = stateLoadType

		case stateLoadType:
			remaining = strings.TrimSpace(s[pos:])
			if hasPrefixFold(remaining, "DATABASE") {
				pos += 8
			} else if hasPrefixFold(remaining, "CSV") {
				cmd.LoadType = SourceCSV
				pos += 3
			} else {
				return nil, fmt.Errorf("unsupported LOAD type near: %q", truncate(remaining, 30))
			}
			state = stateFrom

		case stateFrom:
			pos, err = skipKeyword(s, pos, "FROM")
			if err != nil {
				return nil, err
			}
			pos = skipSpace(s, pos)
			if pos >= len(s) {
				return nil, fmt.Errorf("unexpected end after FROM")
			}

			var uri string
			uri, pos, err = readValue(s, pos)
			if err != nil {
				return nil, fmt.Errorf("read source: %w", err)
			}

			if cmd.LoadType == SourceCSV {
				cmd.FilePath = uri
				pos = skipSpace(s, pos)
				if pos < len(s) && s[pos] == '(' {
					cmd.SourceColumns, pos, err = readParenList(s, pos)
					if err != nil {
						return nil, fmt.Errorf("read source columns: %w", err)
					}
				}
			} else {
				cmd.SourceURI = uri
			}
			state = stateInto

		case stateInto:
			pos, err = skipKeyword(s, pos, "INTO")
			if err != nil {
				return nil, err
			}
			pos = skipSpace(s, pos)
			if pos+11 < len(s) && strings.EqualFold(s[pos:pos+11], "TARGET TABLE") {
				state = stateTargetTable
				continue
			}
			cmd.TargetURI, pos, err = readValue(s, pos)
			if err != nil {
				return nil, fmt.Errorf("read target: %w", err)
			}
			state = advanceState(s, pos)

		case stateTargetTable:
			pos, err = skipKeyword(s, pos, "TARGET TABLE")
			if err != nil {
				return nil, err
			}
			pos = skipSpace(s, pos)
			var table string
			table, pos, err = readValue(s, pos)
			if err != nil {
				return nil, fmt.Errorf("read target table: %w", err)
			}
			if parts := strings.SplitN(table, ".", 2); len(parts) == 2 {
				cmd.TargetSchema = parts[0]
				cmd.TargetTable = parts[1]
			} else {
				cmd.TargetTable = table
				cmd.TargetSchema = "public"
			}
			state = advanceState(s, pos)

		case stateTargetSchema:
			pos, err = skipKeyword(s, pos, "TARGET SCHEMA")
			if err != nil {
				return nil, err
			}
			pos = skipSpace(s, pos)
			cmd.TargetSchema, pos, err = readValue(s, pos)
			if err != nil {
				return nil, fmt.Errorf("read target schema: %w", err)
			}
			state = advanceState(s, pos)
		case stateWith:
			pos, err = skipKeyword(s, pos, "WITH")
			if err != nil {
				return nil, err
			}
			pos = skipSpace(s, pos)
			cmd.WITH, pos, err = readOptionsUntil(s, pos, []string{"SET", "CAST", "BEFORE", "AFTER", "INCLUDING", "EXCLUDING", "MATERIALIZE"})
			if err != nil {
				return nil, fmt.Errorf("read WITH options: %w", err)
			}
			state = advanceState(s, pos)

		case stateSet:
			pos, err = skipKeyword(s, pos, "SET")
			if err != nil {
				return nil, err
			}
			pos = skipSpace(s, pos)
			cmd.SET, pos, err = readOptionsUntil(s, pos, []string{"CAST", "BEFORE", "AFTER"})
			if err != nil {
				return nil, fmt.Errorf("read SET options: %w", err)
			}
			state = advanceState(s, pos)

		case stateCast:
			pos, err = skipKeyword(s, pos, "CAST")
			if err != nil {
				return nil, err
			}
			pos = skipSpace(s, pos)
			cmd.CastRules, pos, err = readOptionsUntil(s, pos, []string{"BEFORE", "AFTER"})
			if err != nil {
				return nil, fmt.Errorf("read CAST rules: %w", err)
			}
			state = advanceState(s, pos)

		case stateBeforeLoad:
			pos, err = skipKeyword(s, pos, "BEFORE LOAD DO")
			if err != nil {
				pos, err = skipKeyword(s, pos, "BEFORE LOAD EXECUTE")
				if err != nil {
					return nil, fmt.Errorf("expected BEFORE LOAD DO/EXECUTE")
				}
			}
			pos = skipSpace(s, pos)
			cmd.BeforeLoad, pos, err = readSQLStatements(s, pos)
			if err != nil {
				return nil, fmt.Errorf("read BEFORE LOAD: %w", err)
			}
			state = advanceState(s, pos)

		case stateAfterLoad:
			pos, err = skipKeyword(s, pos, "AFTER LOAD DO")
			if err != nil {
				pos, err = skipKeyword(s, pos, "AFTER LOAD EXECUTE")
				if err != nil {
					return nil, fmt.Errorf("expected AFTER LOAD DO/EXECUTE")
				}
			}
			pos = skipSpace(s, pos)
			cmd.AfterLoad, pos, err = readSQLStatements(s, pos)
			if err != nil {
				return nil, fmt.Errorf("read AFTER LOAD: %w", err)
			}
			state = advanceState(s, pos)

		case stateIncluding:
			pos, err = skipKeyword(s, pos, "INCLUDING ONLY TABLE NAMES MATCHING")
			if err != nil {
				return nil, fmt.Errorf("expected INCLUDING ONLY TABLE NAMES MATCHING")
			}
			cmd.IncludingOnly, pos, err = readOptionsUntil(s, pos, []string{"WITH", "BEFORE", "AFTER"})
			if err != nil {
				return nil, fmt.Errorf("read INCLUDING patterns: %w", err)
			}
			state = advanceState(s, pos)

		case stateExcluding:
			pos, err = skipKeyword(s, pos, "EXCLUDING TABLE NAMES MATCHING")
			if err != nil {
				return nil, fmt.Errorf("expected EXCLUDING TABLE NAMES MATCHING")
			}
			cmd.Excluding, pos, err = readOptionsUntil(s, pos, []string{"WITH", "BEFORE", "AFTER"})
			if err != nil {
				return nil, fmt.Errorf("read EXCLUDING patterns: %w", err)
			}
			state = advanceState(s, pos)

		case stateMaterialize:
			pos, err = skipKeyword(s, pos, "MATERIALIZE")
			if err != nil {
				return nil, err
			}
			pos = skipSpace(s, pos)
			if hasPrefixFold(s[pos:], "ALL VIEWS") {
				pos += 9
				cmd.MaterializeViews = []string{"*"}
			} else {
				cmd.MaterializeViews, pos, err = readOptionsUntil(s, pos, nil)
				if err != nil {
					return nil, fmt.Errorf("read MATERIALIZE VIEWS: %w", err)
				}
			}
			state = advanceState(s, pos)

		default:
			// Unknown state: skip forward silently.
			// All expected states are handled above; this prevents
			// infinite loops on malformed input.
			pos++
		}
	}

	if cmd.LoadType == SourceCSV {
		return cmd, nil
	}

	if strings.HasPrefix(cmd.SourceURI, "mysql://") {
		cmd.LoadType = SourceMySQL
	} else if strings.HasPrefix(cmd.SourceURI, "postgresql://") || strings.HasPrefix(cmd.SourceURI, "postgres://") || strings.HasPrefix(cmd.SourceURI, "pgsql://") {
		cmd.LoadType = SourcePostgreSQL
	} else if strings.HasPrefix(cmd.SourceURI, "sqlite://") {
		cmd.LoadType = SourceSQLite
	} else {
		return nil, fmt.Errorf("unsupported source URI scheme: %s", cmd.SourceURI)
	}

	return cmd, nil
}

// ---------------------------------------------------------------------------
// Parsing helpers
// ---------------------------------------------------------------------------

// skipKeyword skips a specific keyword at the current position.
func skipKeyword(s string, pos int, kw string) (int, error) {
	pos = skipSpace(s, pos)
	if !hasPrefixFold(s[pos:], kw) {
		return pos, fmt.Errorf("expected %q near: %q", kw, truncate(s[pos:], 40))
	}
	return pos + len(kw), nil
}

// skipSpace advances past whitespace characters.
func skipSpace(s string, pos int) int {
	for pos < len(s) && (s[pos] == ' ' || s[pos] == '\t' || s[pos] == '\n' || s[pos] == '\r') {
		pos++
	}
	return pos
}

// skipComma skips an optional comma and following whitespace.
func skipComma(s string, pos int) int {
	pos = skipSpace(s, pos)
	if pos < len(s) && s[pos] == ',' {
		pos++
		pos = skipSpace(s, pos)
	}
	return pos
}

// readValue reads a value which can be a quoted string or an unquoted identifier.
func readValue(s string, pos int) (string, int, error) {
	pos = skipSpace(s, pos)
	if pos >= len(s) {
		return "", pos, fmt.Errorf("unexpected end while reading value")
	}

	switch s[pos] {
	case '\'':
		pos++
		var val strings.Builder
		for pos < len(s) {
			if s[pos] == '\'' {
				if pos+1 < len(s) && s[pos+1] == '\'' {
					val.WriteByte('\'')
					pos += 2
					continue
				}
				pos++
				break
			}
			if strings.HasPrefix(s[pos:], "$$") {
				end := strings.Index(s[pos+2:], "$$")
				if end < 0 {
					return val.String(), pos, fmt.Errorf("unclosed $$ inside single-quoted string")
				}
				val.WriteString(s[pos : pos+end+4])
				pos += end + 4
				continue
			}
			val.WriteByte(s[pos])
			pos++
		}
		return val.String(), pos, nil

	case '~':
		pos++
		if pos < len(s) && s[pos] == '/' {
			pos++
			var pat strings.Builder
			for pos < len(s) && s[pos] != '/' {
				pat.WriteByte(s[pos])
				pos++
			}
			if pos < len(s) {
				pos++
			}
			return "~/" + pat.String() + "/", pos, nil
		}
		return "~", pos, nil

	default:
		var val strings.Builder
		for pos < len(s) && s[pos] != ' ' && s[pos] != '\t' && s[pos] != '\n' &&
			s[pos] != '\r' && s[pos] != ',' && s[pos] != ';' && s[pos] != ')' {
			val.WriteByte(s[pos])
			pos++
		}
		return val.String(), pos, nil
	}
}

// readParenList reads a parenthesized comma-separated list like (col1, col2, col3).
func readParenList(s string, pos int) ([]string, int, error) {
	pos = skipSpace(s, pos)
	if pos >= len(s) || s[pos] != '(' {
		return nil, pos, fmt.Errorf("expected (")
	}
	pos++

	var items []string
	for pos < len(s) {
		pos = skipSpace(s, pos)
		if pos >= len(s) {
			return nil, pos, fmt.Errorf("unclosed parenthesized list")
		}
		if s[pos] == ')' {
			pos++
			return items, pos, nil
		}
		if s[pos] == ',' {
			pos++
			continue
		}
		val, newPos, err := readValue(s, pos)
		if err != nil {
			return nil, pos, fmt.Errorf("read list item: %w", err)
		}
		items = append(items, val)
		pos = newPos
		pos = skipComma(s, pos)
	}
	return nil, pos, fmt.Errorf("unclosed parenthesized list")
}

// readOptionsUntil reads comma-separated options keeping internal whitespace intact.
func readOptionsUntil(s string, pos int, terminators []string) ([]string, int, error) {
	var options []string
	pos = skipSpace(s, pos)

	for pos < len(s) {
		pos = skipSpace(s, pos)
		if pos >= len(s) {
			break
		}

		if s[pos] == ',' {
			pos++
			continue
		}

		if hasTerminator(s[pos:], terminators) {
			break
		}

		if hasPrefixKeywordFold(s[pos:], "BEFORE") || hasPrefixKeywordFold(s[pos:], "AFTER") ||
			hasPrefixKeywordFold(s[pos:], "INCLUDING") || hasPrefixKeywordFold(s[pos:], "EXCLUDING") ||
			hasPrefixKeywordFold(s[pos:], "MATERIALIZE") || hasPrefixKeywordFold(s[pos:], "TARGET") ||
			hasPrefixKeywordFold(s[pos:], "WITH") {
			break
		}
		if hasPrefixKeywordFold(s[pos:], "SET") {
			break
		}
		if hasPrefixKeywordFold(s[pos:], "CAST") {
			break
		}

		var opt strings.Builder
		for pos < len(s) {
			if pos >= len(s) {
				break
			}

			if s[pos] == ',' {
				pos++
				break
			}
			if s[pos] == ';' {
				break
			}
			// Check for section keywords at word boundaries only.
			// The outer loop handles SET/CATCH transition. Here we only
			// check for other section keywords that could appear mid-option.
			if opt.Len() > 0 && (s[pos-1] == ' ' || s[pos-1] == '\t') {
				if hasPrefixKeywordFold(s[pos:], "BEFORE") ||
					hasPrefixKeywordFold(s[pos:], "AFTER") ||
					hasPrefixKeywordFold(s[pos:], "INCLUDING") ||
					hasPrefixKeywordFold(s[pos:], "EXCLUDING") ||
					hasPrefixKeywordFold(s[pos:], "MATERIALIZE") ||
					hasPrefixKeywordFold(s[pos:], "CAST") ||
					hasPrefixKeywordFold(s[pos:], "WITH") ||
					hasPrefixKeywordFold(s[pos:], "TARGET") {
					break
				}
			}

			if s[pos] == '\n' || s[pos] == '\r' {
				break
			}

			if s[pos] == ' ' || s[pos] == '\t' {
				opt.WriteByte(s[pos])
				pos++
				continue
			}

			if s[pos] == '\'' {
				qs, newPos, err := readValue(s, pos)
				if err != nil {
					return options, pos, err
				}
				opt.WriteString("'")
				opt.WriteString(qs)
				opt.WriteString("'")
				pos = newPos
				continue
			}

			opt.WriteByte(s[pos])
			pos++
		}

		optStr := strings.TrimSpace(opt.String())
		if optStr != "" {
			options = append(options, optStr)
		}

		pos = skipSpace(s, pos)
	}

	return options, pos, nil
}

// readSQLStatements reads one or more dollar-quoted SQL statements separated by commas.
func readSQLStatements(s string, pos int) ([]string, int, error) {
	var statements []string
	pos = skipSpace(s, pos)

	for pos < len(s) {
		if s[pos] == ',' {
			pos++
			pos = skipSpace(s, pos)
			continue
		}

		if !strings.HasPrefix(s[pos:], "$$") {
			break
		}

		pos += 2
		end := strings.Index(s[pos:], "$$")
		if end < 0 {
			return statements, pos, fmt.Errorf("unclosed $$ in SQL statement")
		}
		sql := strings.TrimSpace(s[pos : pos+end])
		statements = append(statements, sql)
		pos += end + 2

		pos = skipSpace(s, pos)
		if pos < len(s) && s[pos] == ',' {
			pos++
			pos = skipSpace(s, pos)
		}
	}

	return statements, pos, nil
}

// advanceState determines the next state based on the next keyword in the input.
func advanceState(s string, pos int) parseState {
	pos = skipSpace(s, pos)
	remaining := s[pos:]

	switch {
	case hasPrefixFold(remaining, "WITH"):
		return stateWith
	case hasPrefixFold(remaining, "SET"):
		return stateSet
	case hasPrefixFold(remaining, "CAST"):
		return stateCast
	case hasPrefixFold(remaining, "BEFORE LOAD"):
		return stateBeforeLoad
	case hasPrefixFold(remaining, "AFTER LOAD"):
		return stateAfterLoad
	case hasPrefixFold(remaining, "INCLUDING"):
		return stateIncluding
	case hasPrefixFold(remaining, "EXCLUDING"):
		return stateExcluding
	case hasPrefixFold(remaining, "MATERIALIZE"):
		return stateMaterialize
	case hasPrefixFold(remaining, "TARGET SCHEMA"):
		return stateTargetSchema
	case hasPrefixFold(remaining, "TARGET TABLE"):
		return stateTargetTable
	case hasPrefixFold(remaining, "TARGET"):
		return stateTargetTable
	default:
		return stateDone
	}
}

// hasTerminator checks if the string starts with one of the terminator keywords
// at a word boundary.
func hasTerminator(s string, terminators []string) bool {
	for _, t := range terminators {
		if hasPrefixKeywordFold(s, t) {
			return true
		}
	}
	return false
}

// hasPrefixFold reports whether s starts with prefix (case-insensitive).
func hasPrefixFold(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	return strings.EqualFold(s[:len(prefix)], prefix)
}

// hasPrefixKeywordFold checks if s starts with keyword (case-insensitive) followed
// by a word boundary (space, newline, comma, semicolon, parenthesis, or end).
func hasPrefixKeywordFold(s, kw string) bool {
	if !hasPrefixFold(s, kw) {
		return false
	}
	n := len(kw)
	if n >= len(s) {
		return true
	}
	ch := s[n]
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' ||
		ch == ',' || ch == ';' || ch == ')' || ch == '('
}

// truncate truncates a string to n characters for error messages.
func truncate(s string, n int) string {
	if len(s) <= n {
		s = strings.ReplaceAll(s, "\n", " ")
		return s
	}
	s = strings.ReplaceAll(s[:n], "\n", " ")
	return s + "..."
}
