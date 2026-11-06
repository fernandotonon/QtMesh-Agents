package orchestrator

import "testing"

func TestLooksLikeQuotaExhausted(t *testing.T) {
	cases := []struct {
		out  string
		want bool
	}{
		{"Credit balance is too low", true},
		{"You've hit your session limit · resets 3pm", true},
		{"API Error: rate_limit_error", true},
		{"insufficient_quota", true},
		{"You exceeded your current quota, please check your plan", true},
		{"Implemented the feature successfully", false},
		{"Waiting for approval to continue", false},
		// Source-echo of our own needle table must not trip.
		{"\t{\"credit balance is too low\", \"claude credit/token balance exhausted\"},\n", false},
	}
	for _, tc := range cases {
		ok, msg := looksLikeQuotaExhausted(tc.out)
		if ok != tc.want {
			t.Fatalf("out=%q want=%v got=%v msg=%q", tc.out, tc.want, ok, msg)
		}
		if ok && msg == "" {
			t.Fatalf("expected reason for %q", tc.out)
		}
	}
}

func TestIsQuotaRetryable(t *testing.T) {
	if !isQuotaRetryable(errString("quota exhausted (claude): credit balance")) {
		t.Fatal("expected retryable")
	}
	if isQuotaRetryable(errString("permission denied")) {
		t.Fatal("permission should not be quota-retryable")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
