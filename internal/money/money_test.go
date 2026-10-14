package money

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func TestAddSubOverflow(t *testing.T) {
	cases := []struct {
		name    string
		fn      func(a, b Amount) (Amount, error)
		a, b    Amount
		want    Amount
		wantErr bool
	}{
		{"add", Add, 1, 2, 3, false},
		{"add max", Add, math.MaxInt64, 1, 0, true},
		{"add min", Add, math.MinInt64, -1, 0, true},
		{"add mixed", Add, math.MaxInt64, -1, math.MaxInt64 - 1, false},
		{"sub", Sub, 5, 7, -2, false},
		{"sub min", Sub, math.MinInt64, 1, 0, true},
		{"sub max", Sub, math.MaxInt64, -1, 0, true},
		{"sub mixed", Sub, math.MinInt64, -1, math.MinInt64 + 1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.fn(c.a, c.b)
			if c.wantErr {
				if !errors.Is(err, ErrOverflow) {
					t.Fatalf("want overflow, got %v (%v)", got, err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("got %v, %v; want %v", got, err, c.want)
			}
		})
	}
}

func TestJSONRoundTrip(t *testing.T) {
	var v struct{ A Amount }
	if err := json.Unmarshal([]byte(`{"A":"1050"}`), &v); err != nil || v.A != 1050 {
		t.Fatalf("unmarshal: %v %v", v.A, err)
	}
	b, _ := json.Marshal(v)
	if string(b) != `{"A":"1050"}` {
		t.Fatalf("marshal: %s", b)
	}
	for _, bad := range []string{`{"A":1050}`, `{"A":"10.5"}`, `{"A":"1e3"}`, `{"A":""}`, `{"A":"99999999999999999999"}`, `{"A":"+5"}`} {
		if err := json.Unmarshal([]byte(bad), &v); err == nil {
			t.Errorf("expected error for %s", bad)
		}
	}
}

func TestFormat(t *testing.T) {
	cases := []struct {
		a    Amount
		exp  int
		want string
	}{
		{1050, 2, "10.50"},
		{5, 2, "0.05"},
		{-5, 2, "-0.05"},
		{100, 0, "100"},
		{1, 8, "0.00000001"},
		{math.MinInt64, 2, "-92233720368547758.08"},
	}
	for _, c := range cases {
		if got := Format(c.a, c.exp); got != c.want {
			t.Errorf("Format(%d,%d)=%s want %s", c.a, c.exp, got, c.want)
		}
	}
}
