// A refused key or an empty balance on a provider route reaches Claude Code as a non-retryable
// 400 that says what to run or where to top up, never as the 401 it would read as its own login
// failing or the 429 it would retry for minutes. Anthropic's and a peer's answers are never
// touched. The refusals' cases, inputs and expected outputs come from refusal.go and
// router.go.
package router

import "testing"

func TestRefusalOfBalanceAnswers(t *testing.T) {
	t.Parallel()
	// Each provider's documented out-of-balance answer.
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"Z.ai 1113", 429, `{"error":{"code":"1113","message":"Insufficient balance or no resource package. Please recharge."}}`},
		{"Z.ai 1113 as a number", 429, `{"error":{"code":1113,"message":"x"}}`},
		{"Z.ai 1316", 429, `{"error":{"code":"1316","message":"Usage limit reached for the past 5 hours."}}`},
		{"Z.ai 1317", 429, `{"error":{"code":"1317","message":"Usage limit reached for the past 7 days."}}`},
		{"Moonshot exceeded_current_quota_error", 429, `{"error":{"type":"exceeded_current_quota_error","message":"Your account is suspended, please check billing"}}`},
		{"DeepSeek 402", 402, `{"error":{"message":"Insufficient Balance","type":"unknown_error"}}`},
		{"MiniMax 1008", 500, `{"base_resp":{"status_code":1008,"status_msg":"insufficient balance"}}`},
		{"DashScope Arrearage", 400, `{"error":{"code":"Arrearage","message":"Access denied, please make sure your account is in good standing."}}`},
		{"DashScope isv.OUT_OF_SERVICE", 400, `{"code":"isv.OUT_OF_SERVICE","message":"service paused"}`},
		{"the wording alone", 403, `{"error":{"message":"insufficient balance"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name+" is out of balance", func(t *testing.T) {
			refusal, ok := RefusalOf(tc.status, []byte(tc.body))
			if !ok || refusal != RefusalBalance {
				t.Errorf("RefusalOf(%d, %s) = %v, %v", tc.status, tc.body, refusal, ok)
			}
		})
	}
}

func TestRefusalOfKeys(t *testing.T) {
	t.Parallel()
	t.Run("reads a 401 or 403 as a refused key, and every other answer as not a refusal", func(t *testing.T) {
		if refusal, ok := RefusalOf(401, []byte(`{"error":{"code":"1000","message":"Authentication Failed"}}`)); !ok || refusal != RefusalKey {
			t.Errorf("401 = %v, %v", refusal, ok)
		}
		if refusal, ok := RefusalOf(403, []byte("forbidden")); !ok || refusal != RefusalKey {
			t.Errorf("403 = %v, %v", refusal, ok)
		}
		if refusal, ok := RefusalOf(402, []byte("not json")); !ok || refusal != RefusalBalance {
			t.Errorf("402 = %v, %v", refusal, ok)
		}
		// A plain rate limit (Z.ai 1302, Moonshot rate_limit_reached_error) stays a retryable 429.
		if _, ok := RefusalOf(429, []byte(`{"error":{"code":"1302","message":"rate limit"}}`)); ok {
			t.Error("Z.ai 1302 must not be a refusal")
		}
		if _, ok := RefusalOf(429, []byte(`{"error":{"type":"rate_limit_reached_error","message":"slow down"}}`)); ok {
			t.Error("rate_limit_reached_error must not be a refusal")
		}
		if _, ok := RefusalOf(400, []byte(`{"error":{"type":"invalid_request_error","message":"bad"}}`)); ok {
			t.Error("an invalid request must not be a refusal")
		}
		if _, ok := RefusalOf(500, []byte("<html>")); ok {
			t.Error("a 500 must not be a refusal")
		}
		if _, ok := RefusalOf(500, []byte("[1113]")); ok {
			t.Error("a bare array must not be a refusal")
		}
		if _, ok := RefusalOf(200, []byte(`{"error":{"code":"1113"}}`)); ok {
			t.Error("a success must not be a refusal")
		}
	})
}

// answeringState is one fake upstream's mutable answer, swapped between requests.
