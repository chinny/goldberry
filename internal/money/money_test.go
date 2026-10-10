package money

import (
	"errors"
	"testing"
)

func TestLookup(t *testing.T) {
	for _, tc := range []struct {
		code string
		exp  int
	}{{"USD", 2}, {"usd", 2}, {"JPY", 0}, {"EUR", 2}, {"KWD", 3}} {
		c, err := Lookup(tc.code)
		if err != nil {
			t.Fatalf("%s: %v", tc.code, err)
		}
		if c.Exponent != tc.exp {
			t.Errorf("%s exponent = %d, want %d", tc.code, c.Exponent, tc.exp)
		}
	}
	if _, err := Lookup("XYZ1"); err == nil {
		t.Error("want error for bogus code")
	}
}

func TestParse(t *testing.T) {
	usd, jpy := MustLookup("USD"), MustLookup("JPY")
	for _, tc := range []struct {
		c    Currency
		in   string
		want int64
		err  error
	}{
		{usd, "5", 500, nil},
		{usd, "5.5", 550, nil},
		{usd, "5.05", 505, nil},
		{usd, " $1,234.50 ", 123450, nil},
		{usd, ".75", 75, nil},
		{usd, "0", 0, nil},
		{usd, "", 0, ErrEmpty},
		{usd, "$", 0, ErrEmpty},
		{usd, ".", 0, ErrInvalid},
		{usd, "5.001", 0, ErrPrecision},
		{usd, "-5", 0, ErrInvalid},
		{usd, "1e3", 0, ErrInvalid},
		{usd, "5.0.0", 0, ErrInvalid},
		{usd, "99999999999999", 0, ErrTooLarge},
		{jpy, "500", 500, nil},
		{jpy, "500.0", 0, ErrPrecision},
	} {
		got, err := tc.c.Parse(tc.in)
		if !errors.Is(err, tc.err) || got != tc.want {
			t.Errorf("%s Parse(%q) = %d, %v; want %d, %v", tc.c.Code, tc.in, got, err, tc.want, tc.err)
		}
	}
}

func TestFormat(t *testing.T) {
	usd, jpy, kwd := MustLookup("USD"), MustLookup("JPY"), MustLookup("KWD")
	for _, tc := range []struct {
		got, want string
	}{
		{usd.Format(0), "$0.00"},
		{usd.Format(5), "$0.05"},
		{usd.Format(123456789), "$1,234,567.89"},
		{usd.Format(-250), "−$2.50"},
		{usd.Signed(500), "+$5.00"},
		{usd.Signed(-250), "−$2.50"},
		{usd.Plain(500), "5.00"},
		{usd.Plain(-7), "-0.07"},
		{jpy.Format(1500), "¥1,500"},
		{jpy.Plain(1500), "1500"},
		{kwd.Format(1234), "KWD 1.234"},
		{usd.Short(2000), "$20"},
		{usd.Short(250), "$2.50"},
		{usd.Short(-100000), "−$1,000"},
		{usd.Short(0), "$0"},
		{jpy.Short(1500), "¥1,500"},
	} {
		if tc.got != tc.want {
			t.Errorf("got %q, want %q", tc.got, tc.want)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	usd := MustLookup("USD")
	for _, v := range []int64{0, 1, 99, 100, 12345, 100000000} {
		got, err := usd.Parse(usd.Format(v))
		if err != nil || got != v {
			t.Errorf("round trip %d: got %d, %v", v, got, err)
		}
	}
}

func TestSplit(t *testing.T) {
	for _, tc := range []struct {
		amount int64
		bps    []int
		want   []int64
	}{
		{1000, []int{7000, 2000, 1000}, []int64{700, 200, 100}},
		{1001, []int{7000, 2000, 1000}, []int64{701, 200, 100}},
		{1, []int{3334, 3333, 3333}, []int64{1, 0, 0}},
		{100, []int{3334, 3333, 3333}, []int64{34, 33, 33}},
		{999, []int{10000}, []int64{999}},
		{-1001, []int{7000, 2000, 1000}, []int64{-701, -200, -100}},
	} {
		got, err := Split(tc.amount, tc.bps)
		if err != nil {
			t.Fatal(err)
		}
		var sum int64
		for i := range got {
			sum += got[i]
			if got[i] != tc.want[i] {
				t.Errorf("Split(%d, %v) = %v, want %v", tc.amount, tc.bps, got, tc.want)
				break
			}
		}
		if sum != tc.amount {
			t.Errorf("Split(%d) parts sum to %d", tc.amount, sum)
		}
	}
	if _, err := Split(100, []int{5000, 4000}); err == nil {
		t.Error("want error when weights don't sum to 10000")
	}
}
