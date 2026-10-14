// Package money implements an overflow-checked integer amount in minor units.
//
// Amounts are never represented as floating point. On the wire they are
// encoded as JSON strings of base-10 digits so that clients in languages with
// float-only numbers (JavaScript) cannot lose precision.
package money

import (
	"errors"
	"fmt"
	"math"
	"strconv"
)

// Amount is a quantity of a currency in its minor unit (e.g. cents).
type Amount int64

// ErrOverflow is returned when an arithmetic operation would overflow int64.
var ErrOverflow = errors.New("money: amount overflow")

// Add returns a+b or ErrOverflow.
func Add(a, b Amount) (Amount, error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, ErrOverflow
	}
	return a + b, nil
}

// Sub returns a-b or ErrOverflow.
func Sub(a, b Amount) (Amount, error) {
	if (b < 0 && a > math.MaxInt64+b) || (b > 0 && a < math.MinInt64+b) {
		return 0, ErrOverflow
	}
	return a - b, nil
}

// Parse parses a base-10 integer string into an Amount.
func Parse(s string) (Amount, error) {
	if s == "" {
		return 0, errors.New("money: empty amount")
	}
	for i, c := range s {
		if (c < '0' || c > '9') && !(i == 0 && c == '-') {
			return 0, fmt.Errorf("money: invalid amount %q: must be an integer in minor units", s)
		}
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("money: invalid amount %q: %w", s, ErrOverflow)
	}
	return Amount(v), nil
}

// String formats the amount in minor units.
func (a Amount) String() string { return strconv.FormatInt(int64(a), 10) }

// MarshalJSON encodes the amount as a JSON string.
func (a Amount) MarshalJSON() ([]byte, error) {
	return []byte(`"` + a.String() + `"`), nil
}

// UnmarshalJSON accepts only a JSON string of digits.
func (a *Amount) UnmarshalJSON(b []byte) error {
	if len(b) < 2 || b[0] != '"' || b[len(b)-1] != '"' {
		return fmt.Errorf("money: amount must be a JSON string of minor units, got %s", b)
	}
	v, err := Parse(string(b[1 : len(b)-1]))
	if err != nil {
		return err
	}
	*a = v
	return nil
}

// Format renders an amount in major units using the currency exponent,
// e.g. Format(1050, 2) == "10.50". Intended for display only.
func Format(a Amount, exponent int) string {
	neg := a < 0
	u := uint64(a)
	if neg {
		u = uint64(-(a + 1)) + 1 // safe for MinInt64
	}
	s := strconv.FormatUint(u, 10)
	if exponent > 0 {
		for len(s) <= exponent {
			s = "0" + s
		}
		s = s[:len(s)-exponent] + "." + s[len(s)-exponent:]
	}
	if neg {
		s = "-" + s
	}
	return s
}
