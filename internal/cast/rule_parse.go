package cast

import (
	"fmt"
	"strings"
)

// ParseCastRules parses CAST rule strings (from a .load config file) into CastRule structs.
// Each string is one rule like "type integer to bigint drop typemod".
// Returns an empty slice (not nil) when rules is empty or nil.
func ParseCastRules(rules []string) ([]CastRule, error) {
	if len(rules) == 0 {
		return nil, nil
	}
	result := make([]CastRule, 0, len(rules))
	for i, r := range rules {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		rule, err := parseCastRule(r)
		if err != nil {
			return nil, fmt.Errorf("cast rule %d: %w", i+1, err)
		}
		result = append(result, rule)
	}
	return result, nil
}

// parseCastRule parses a single CAST rule string into a CastRule.
//
// Grammar:
//
//	rule      = "type" source-type [modifier...] "to" target-type [option...] ["using" transform]
//	modifier  = "auto_increment" | "unsigned"
//	option    = "drop" ("typemod"|"default"|"not" "null")
//	          | "keep" ("typemod"|"default"|"not" "null")
func parseCastRule(s string) (CastRule, error) {
	tokens := strings.Fields(s)
	if len(tokens) == 0 {
		return CastRule{}, fmt.Errorf("empty rule")
	}

	pos := 0

	// Optional "type" keyword
	if strings.EqualFold(tokens[pos], "type") {
		pos++
	}
	if pos >= len(tokens) {
		return CastRule{}, fmt.Errorf("expected source type: %q", s)
	}

	// Source type with optional attached typemod: "tinyint" or "tinyint(1)"
	sourceType, typeMod := splitTypeMod(tokens[pos])
	pos++

	// Handle detached typemod: "bit (1)" — next token is "(1)"
	if pos < len(tokens) && isParenToken(tokens[pos]) {
		if typeMod == "" {
			typeMod = tokens[pos]
		}
		pos++
	}

	rule := CastRule{
		Match: MatchRule{
			SourceType:    sourceType,
			TypeMod:       typeMod,
			Unsigned:      Any,
			AutoIncrement: Any,
		},
	}

	// Read modifiers until "to"
	for pos < len(tokens) && !strings.EqualFold(tokens[pos], "to") {
		switch strings.ToLower(tokens[pos]) {
		case "auto_increment":
			rule.Match.AutoIncrement = Yes
		case "unsigned":
			rule.Match.Unsigned = Yes
		}
		pos++
	}

	// Expect "to"
	if pos >= len(tokens) || !strings.EqualFold(tokens[pos], "to") {
		return CastRule{}, fmt.Errorf("expected 'to' in cast rule: %q", s)
	}
	pos++

	if pos >= len(tokens) {
		return CastRule{}, fmt.Errorf("expected target type after 'to': %q", s)
	}

	// Collect target type (may be multi-word: "double precision", "bit varying($mod)")
	var targetTokens []string
	for pos < len(tokens) && !isCastSpecial(tokens[pos]) {
		targetTokens = append(targetTokens, tokens[pos])
		pos++
	}
	if len(targetTokens) == 0 {
		return CastRule{}, fmt.Errorf("expected target type: %q", s)
	}
	rule.TargetType = strings.Join(targetTokens, " ")

	// Parse remaining options: drop/keep, using <transform>
	for pos < len(tokens) {
		switch strings.ToLower(tokens[pos]) {
		case "drop":
			pos++
			if pos < len(tokens) {
				switch strings.ToLower(tokens[pos]) {
				case "typemod":
					rule.DropTypemod = true
				case "default":
					// drop default — noted but not stored
				case "not":
					pos++ // skip "not null" — drop not null
				}
			}
		case "keep":
			pos++ // keep typemod/default/not null — default behavior
		case "using":
			pos++
			if pos < len(tokens) {
				rule.Transform = tokens[pos]
			}
		}
		pos++
	}

	return rule, nil
}

// isCastSpecial returns true if the token is a keyword that terminates
// the target-type part of a CAST rule.
func isCastSpecial(tok string) bool {
	switch strings.ToLower(tok) {
	case "drop", "keep", "using":
		return true
	}
	return false
}

// isParenToken checks if a token is a parenthesized expression like "(1)" or "($mod)".
func isParenToken(tok string) bool {
	return len(tok) >= 2 && tok[0] == '(' && tok[len(tok)-1] == ')'
}

// splitTypeMod splits "tinyint(1)" into ("tinyint", "(1)") and "numeric" into ("numeric", "").
func splitTypeMod(s string) (string, string) {
	parenIdx := strings.IndexByte(s, '(')
	if parenIdx < 0 {
		return s, ""
	}
	closeParen := strings.LastIndexByte(s, ')')
	if closeParen < 0 {
		return s, ""
	}
	return s[:parenIdx], s[parenIdx : closeParen+1]
}
