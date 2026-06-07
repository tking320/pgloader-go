package cast

import (
	"fmt"
	"strings"
)

// TransformFunc is a function that transforms a value during data loading.
type TransformFunc func(val interface{}) (interface{}, error)

// GetTransform returns the named transform function, or nil if not found.
func GetTransform(name string) TransformFunc {
	switch name {
	case "tinyint-to-bool":
		return TinyIntToBool
	case "zero-dates-to-null":
		return ZeroDatesToNull
	case "remove-null-chars":
		return RemoveNullChars
	case "int-to-id":
		return IntToID
	case "bit-to-bool":
		return BitToBool
	case "wkt-to-geometry":
		return WKTToGeometry
	case "bit-to-binstr":
		return BitToBinStr
	case "money-to-numeric":
		return MoneyToNumeric
	case "sql-server-bit-to-boolean":
		return MssqlBitToBoolean
	case "sql-server-uniqueidentifier-to-uuid":
		return MssqlUniqueIdentifierToUUID
	case "float-to-string":
		return FloatToString
	case "byte-vector-to-bytea":
		return ByteVectorToBytea
	default:
		return nil
	}
}

// ---------------------------------------------------------------------------
// Transform implementations
// ---------------------------------------------------------------------------

// TinyIntToBool converts MySQL TINYINT(1) values to PostgreSQL boolean format.
// 0 → "f", 1 → "t", nil → nil
var TinyIntToBool TransformFunc = func(val interface{}) (interface{}, error) {
	if val == nil {
		return nil, nil
	}
	switch v := val.(type) {
	case int64:
		if v == 0 {
			return "f", nil
		}
		return "t", nil
	case float64:
		if v == 0 {
			return "f", nil
		}
		return "t", nil
	case []byte:
		if len(v) == 1 && v[0] == '0' {
			return "f", nil
		}
		return "t", nil
	case string:
		if v == "0" {
			return "f", nil
		}
		return "t", nil
	default:
		return val, nil
	}
}

// ZeroDatesToNull converts MySQL zero dates to nil.
// "0000-00-00" → nil, "0000-00-00 00:00:00" → nil
var ZeroDatesToNull TransformFunc = func(val interface{}) (interface{}, error) {
	if val == nil {
		return nil, nil
	}
	switch v := val.(type) {
	case string:
		if v == "0000-00-00" || v == "0000-00-00 00:00:00" {
			return nil, nil
		}
	case []byte:
		s := string(v)
		if s == "0000-00-00" || s == "0000-00-00 00:00:00" {
			return nil, nil
		}
	}
	return val, nil
}

// RemoveNullChars strips null bytes from string values.
var RemoveNullChars TransformFunc = func(val interface{}) (interface{}, error) {
	if val == nil {
		return nil, nil
	}
	switch v := val.(type) {
	case string:
		if strings.ContainsRune(v, 0) {
			return strings.ReplaceAll(v, "\x00", ""), nil
		}
	case []byte:
		if containsNullByte(v) {
			cleaned := make([]byte, 0, len(v))
			for _, b := range v {
				if b != 0 {
					cleaned = append(cleaned, b)
				}
			}
			return cleaned, nil
		}
	}
	return val, nil
}

func containsNullByte(b []byte) bool {
	for _, c := range b {
		if c == 0 {
			return true
		}
	}
	return false
}

// IntToID passes integer values through as-is (used for auto_increment columns).
// This is a no-op transform for consistency/identity.
var IntToID TransformFunc = func(val interface{}) (interface{}, error) {
	return val, nil
}

// BitToBool converts MySQL BIT(1) values to PostgreSQL boolean format.
// []byte{0x00} → "f", []byte{0x01} → "t", nil → nil
var BitToBool TransformFunc = func(val interface{}) (interface{}, error) {
	if val == nil {
		return nil, nil
	}
	switch v := val.(type) {
	case []byte:
		if len(v) == 1 && v[0] == 0 {
			return "f", nil
		}
		return "t", nil
	case string:
		if len(v) >= 1 && v[0] == 0 {
			return "f", nil
		}
		return "t", nil
	default:
		return val, nil
	}
}

// WKTToGeometry converts WKT (Well-Known Text) format to PostgreSQL native format.
// "POINT(x y)" → "(x,y)"
// "LINESTRING(x1 y1, x2 y2, ...)" → "((x1,y1),(x2,y2),...)"
var WKTToGeometry TransformFunc = func(val interface{}) (interface{}, error) {
	if val == nil {
		return nil, nil
	}
	s, ok := val.(string)
	if !ok {
		return val, nil
	}

	// Remove leading/trailing whitespace
	s = strings.TrimSpace(s)

	// Handle POINT(x y)
	if strings.HasPrefix(s, "POINT(") && strings.HasSuffix(s, ")") {
		inner := s[6 : len(s)-1] // extract "x y"
		parts := strings.Fields(inner)
		if len(parts) == 2 {
			return "(" + parts[0] + "," + parts[1] + ")", nil
		}
	}

	// Handle LINESTRING(x1 y1, x2 y2, ...)
	if strings.HasPrefix(s, "LINESTRING(") && strings.HasSuffix(s, ")") {
		inner := s[11 : len(s)-1]
		points := strings.Split(inner, ",")
		converted := make([]string, len(points))
		for i, pt := range points {
			pt = strings.TrimSpace(pt)
			coords := strings.Fields(pt)
			if len(coords) == 2 {
				converted[i] = "(" + coords[0] + "," + coords[1] + ")"
			} else {
				converted[i] = pt
			}
		}
		return "(" + strings.Join(converted, ",") + ")", nil
	}

	return val, nil
}

// BitToBinStr converts MySQL BIT(n) binary data to PostgreSQL bit/bool format.
// For BIT(1): []byte{0} → "f", []byte{1} → "t" (boolean)
// For BIT(n>1): []byte{0x00, 0x01} → "0000000000000001" (binary string)
var BitToBinStr TransformFunc = func(val interface{}) (interface{}, error) {
	if val == nil {
		return nil, nil
	}
	switch v := val.(type) {
	case []byte:
		if len(v) == 1 {
			// Single byte: check if it's 0 or 1 for boolean-like behavior
			if v[0] == 0 {
				return "0", nil
			}
			return "1", nil
		}
		// Multi-byte: convert each bit to '0'/'1' characters
		var sb strings.Builder
		for _, b := range v {
			for j := 7; j >= 0; j-- {
				if b&(1<<j) != 0 {
					sb.WriteByte('1')
				} else {
					sb.WriteByte('0')
				}
			}
		}
		return sb.String(), nil
	case string:
		if len(v) == 0 {
			return "0", nil
		}
		// Convert each byte to bit characters, handling null bytes
		var sb strings.Builder
		for _, b := range []byte(v) {
			for j := 7; j >= 0; j-- {
				if b&(1<<j) != 0 {
					sb.WriteByte('1')
				} else {
					sb.WriteByte('0')
				}
			}
		}
		return sb.String(), nil
	default:
		return val, nil
	}
}

// MoneyToNumeric converts PostgreSQL money format to numeric string.
// PostgreSQL outputs money as locale-specific strings like "$1,234.56"
// or "1.234,56 €". This transform strips non-numeric characters
// except for the decimal separator.
var MoneyToNumeric TransformFunc = func(val interface{}) (interface{}, error) {
	if val == nil {
		return nil, nil
	}
	switch v := val.(type) {
	case string:
		return cleanMoneyString(v), nil
	case []byte:
		return cleanMoneyString(string(v)), nil
	default:
		return val, nil
	}
}

// cleanMoneyString strips currency formatting from a money string.
// Handles: "$1,234.56" → "1234.56", "1.234,56 €" → "1234.56",
// "($1,234.56)" → "-1234.56" (parentheses = negative)
func cleanMoneyString(s string) string {
	if len(s) == 0 {
		return s
	}

	// Handle negative amounts in parentheses: ($1,234.56)
	negative := false
	if s[0] == '(' && s[len(s)-1] == ')' {
		negative = true
		s = s[1 : len(s)-1]
	}

	// Detect decimal separator by checking which separators appear
	hasDot := false
	hasComma := false
	for _, ch := range s {
		if ch == '.' {
			hasDot = true
		} else if ch == ',' {
			hasComma = true
		}
	}

	var decSep byte = '.'
	// If both dot and comma appear: the last one is the decimal separator
	// If only comma appears: comma is decimal separator
	if !hasDot && hasComma {
		decSep = ','
	} else if hasDot && hasComma {
		lastDot := strings.LastIndex(s, ".")
		lastComma := strings.LastIndex(s, ",")
		if lastComma > lastDot {
			decSep = ','
		}
	}

	// Build the cleaned string
	var b strings.Builder
	for _, ch := range s {
		if ch >= '0' && ch <= '9' {
			b.WriteByte(byte(ch))
		} else if byte(ch) == decSep {
			b.WriteByte('.')
		}
		// Skip all other characters (currency symbols, thousand separators, etc.)
	}

	result := b.String()
	if result == "" {
		result = "0"
	}
	if negative {
		result = "-" + result
	}
	return result
}

// MssqlBitToBoolean converts MSSQL bit values (int64 0/1) to PostgreSQL boolean format.
// 0 → "f", 1 → "t", nil → nil
var MssqlBitToBoolean TransformFunc = func(val interface{}) (interface{}, error) {
	if val == nil {
		return nil, nil
	}
	switch v := val.(type) {
	case int64:
		if v == 0 {
			return "f", nil
		}
		return "t", nil
	case float64:
		if v == 0 {
			return "f", nil
		}
		return "t", nil
	case bool:
		if v {
			return "t", nil
		}
		return "f", nil
	case []byte:
		if len(v) > 0 && v[0] == 0 {
			return "f", nil
		}
		return "t", nil
	case string:
		if v == "0" || v == "false" {
			return "f", nil
		}
		return "t", nil
	default:
		return val, nil
	}
}

// MssqlUniqueIdentifierToUUID converts MSSQL uniqueidentifier to PostgreSQL UUID format.
// Handles both pre-formatted strings and raw 16-byte binary from go-mssqldb.
var MssqlUniqueIdentifierToUUID TransformFunc = func(val interface{}) (interface{}, error) {
	if val == nil {
		return nil, nil
	}
	switch v := val.(type) {
	case string:
		return cleanUUID(v), nil
	case []byte:
		if len(v) == 16 {
			// Raw 16-byte GUID binary: reformat as canonical UUID hex string
			// MSSQL stores GUID in mixed-endian (Data1/Data2/Data3 LE, Data4 BE)
			return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
				v[3], v[2], v[1], v[0],
				v[5], v[4],
				v[7], v[6],
				v[8], v[9],
				v[10], v[11], v[12], v[13], v[14], v[15]), nil
		}
		return cleanUUID(string(v)), nil
	default:
		return val, nil
	}
}

func cleanUUID(s string) string {
	// Strip braces and brackets
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "{")
	s = strings.TrimSuffix(s, "}")
	s = strings.TrimPrefix(s, "(")
	s = strings.TrimSuffix(s, ")")
	// Lowercase
	s = strings.ToLower(s)
	return s
}

// FloatToString formats float values as strings for COPY protocol compatibility.
// MSSQL float/real/numeric/decimal/money types need explicit string formatting
// to avoid precision issues in the COPY text protocol.
var FloatToString TransformFunc = func(val interface{}) (interface{}, error) {
	if val == nil {
		return nil, nil
	}
	switch v := val.(type) {
	case float64:
		return formatFloat(v), nil
	case float32:
		return formatFloat(float64(v)), nil
	case int64:
		return formatInt64(v), nil
	case string:
		return v, nil
	case []byte:
		return string(v), nil
	default:
		return fmt.Sprintf("%v", v), nil
	}
}

// ByteVectorToBytea converts binary data to PostgreSQL bytea hex format.
// The hex format is: \x followed by hex digits (e.g., \x48656c6c6f).
var ByteVectorToBytea TransformFunc = func(val interface{}) (interface{}, error) {
	if val == nil {
		return nil, nil
	}
	switch v := val.(type) {
	case []byte:
		return formatByteaHex(v), nil
	case string:
		return val, nil // already a string representation
	default:
		return val, nil
	}
}

func formatByteaHex(data []byte) string {
	var b strings.Builder
	b.WriteString("\\x")
	hex := "0123456789abcdef"
	for _, byt := range data {
		b.WriteByte(hex[byt>>4])
		b.WriteByte(hex[byt&0x0f])
	}
	return b.String()
}

// formatFloat formats a float64 as a string, using scientific notation for very
// large/small values and decimal notation otherwise.
func formatFloat(f float64) string {
	// Use 'f' format for normal range, 'e' for extreme values
	if f > 1e15 || f < -1e15 || (f < 1e-10 && f > -1e-10 && f != 0) {
		return fmt.Sprintf("%e", f)
	}
	return fmt.Sprintf("%v", f)
}
