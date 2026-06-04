package cast

import (
	"testing"
)

func TestTinyIntToBool(t *testing.T) {
	tests := []struct {
		input interface{}
		want  interface{}
	}{
		{int64(0), "f"},
		{int64(1), "t"},
		{int64(42), "t"},
		{nil, nil},
		{"0", "f"},
		{"1", "t"},
		{[]byte("0"), "f"},
	}
	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			got, err := TinyIntToBool(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("TinyIntToBool(%v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestZeroDatesToNull(t *testing.T) {
	tests := []struct {
		input interface{}
		want  interface{}
	}{
		{"0000-00-00", nil},
		{"0000-00-00 00:00:00", nil},
		{"2024-01-15", "2024-01-15"},
		{"2024-01-15 10:30:00", "2024-01-15 10:30:00"},
		{nil, nil},
		{[]byte("0000-00-00"), nil},
		{[]byte("2024-01-15"), []byte("2024-01-15")},
	}
	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			got, err := ZeroDatesToNull(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !equalValues(got, tt.want) {
				t.Errorf("ZeroDatesToNull(%v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// equalValues compares two values, handling []byte comparison.
func equalValues(a, b interface{}) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	ba, oka := a.([]byte)
	bb, okb := b.([]byte)
	if oka && okb {
		if len(ba) != len(bb) {
			return false
		}
		for i := range ba {
			if ba[i] != bb[i] {
				return false
			}
		}
		return true
	}
	return a == b
}

func TestRemoveNullChars(t *testing.T) {
	tests := []struct {
		input interface{}
		want  interface{}
	}{
		{"hello\x00world", "helloworld"},
		{"normal", "normal"},
		{nil, nil},
		{[]byte("a\x00b"), []byte("ab")},
		{[]byte("clean"), []byte("clean")},
	}
	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			got, err := RemoveNullChars(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			// Compare []byte content
			switch v := got.(type) {
			case []byte:
				w, ok := tt.want.([]byte)
				if !ok || string(v) != string(w) {
					t.Errorf("RemoveNullChars(%v) = %v, want %v", tt.input, got, tt.want)
				}
			default:
				if got != tt.want {
					t.Errorf("RemoveNullChars(%v) = %v, want %v", tt.input, got, tt.want)
				}
			}
		})
	}
}

func TestMoneyToNumeric(t *testing.T) {
	tests := []struct {
		input interface{}
		want  interface{}
	}{
		{"$1,234.56", "1234.56"},
		{"$1,234,567.89", "1234567.89"},
		{"EUR 1.234,56", "1234.56"},
		{"1.234,56 €", "1234.56"},
		{"($1,234.56)", "-1234.56"},
		{"$0.00", "0.00"},
		{"$1.99", "1.99"},
		{"$1", "1"},
		{nil, nil},
		{"", ""},
		{[]byte("$500"), "500"},
	}
	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			got, err := MoneyToNumeric(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("MoneyToNumeric(%v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
