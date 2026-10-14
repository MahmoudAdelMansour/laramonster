package php

import "testing"

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{"php@8.4": "8.4", "8.4": "8.4", "84": "8.4", "8.4.12": "8.4", "PHP8.3": "8.3"} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCompareOrdersNumerically(t *testing.T) {
	if compare("8.10", "8.9") <= 0 || compare("8.4", "8.4") != 0 || compare("7.4", "8.0") >= 0 {
		t.Fatal("compare is not numeric")
	}
}
