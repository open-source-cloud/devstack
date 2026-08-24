package selfupdate

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func resp(status int, hdr map[string]string) *http.Response {
	h := http.Header{}
	for k, v := range hdr {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: status, Status: strconv.Itoa(status) + " " + http.StatusText(status), Header: h}
}

// TestRateLimitedForbiddenIsNotReportedAsPermissions is the regression this file
// exists for. A user on a PUBLIC repo exhausted the anonymous 60/hour quota and
// got "set GITHUB_TOKEN if the repo is private", which describes a problem that
// did not exist and hid the one that did.
func TestRateLimitedForbiddenIsNotReportedAsPermissions(t *testing.T) {
	// Exactly the headers GitHub returned in that session.
	r := resp(http.StatusForbidden, map[string]string{
		"X-RateLimit-Limit":     "60",
		"X-RateLimit-Remaining": "0",
		"X-RateLimit-Reset":     strconv.FormatInt(time.Now().Add(10*time.Minute).Unix(), 10),
	})
	err := apiError("https://api.github.com/repos/o/r/releases", r)
	msg := err.Error()

	if !strings.Contains(msg, "rate limit exceeded") {
		t.Errorf("the message must name the real cause, got: %s", msg)
	}
	if strings.Contains(msg, "if the repository is private") || strings.Contains(msg, "if the repo is private") {
		t.Errorf("a rate-limited response must NOT be reported as a permissions problem, got: %s", msg)
	}
	if !strings.Contains(msg, "60 requests/hour") {
		t.Errorf("the message should quote the limit that was hit, got: %s", msg)
	}
	if !strings.Contains(msg, "GITHUB_TOKEN") {
		t.Errorf("the message should still offer the fix, got: %s", msg)
	}
	if !strings.Contains(msg, "resets in") {
		t.Errorf("the message should say when the window rolls over, got: %s", msg)
	}
}

func TestSecondaryRateLimitViaRetryAfter(t *testing.T) {
	r := resp(http.StatusTooManyRequests, map[string]string{"Retry-After": "45"})
	msg := apiError("https://api.github.com/x", r).Error()
	if !strings.Contains(msg, "rate limit exceeded") {
		t.Errorf("429 with Retry-After should read as rate limiting, got: %s", msg)
	}
	if !strings.Contains(msg, "retry in 45s") {
		t.Errorf("should surface Retry-After, got: %s", msg)
	}
}

// TestForbiddenWithQuotaLeftIsNotRateLimit: a 403 that still has quota is a real
// permissions failure and must keep the private-repo hint.
func TestForbiddenWithQuotaLeftIsNotRateLimit(t *testing.T) {
	r := resp(http.StatusForbidden, map[string]string{
		"X-RateLimit-Limit":     "5000",
		"X-RateLimit-Remaining": "4999",
	})
	msg := apiError("https://api.github.com/x", r).Error()
	if strings.Contains(msg, "rate limit exceeded") {
		t.Errorf("403 with quota remaining is not rate limiting, got: %s", msg)
	}
}

func TestNotFoundKeepsThePrivateRepoHint(t *testing.T) {
	msg := apiError("https://api.github.com/x", resp(http.StatusNotFound, nil)).Error()
	if !strings.Contains(msg, "private") {
		t.Errorf("404 is where the private-repo hint belongs, got: %s", msg)
	}
}

func TestUnauthorizedBlamesTheToken(t *testing.T) {
	msg := apiError("https://api.github.com/x", resp(http.StatusUnauthorized, nil)).Error()
	if !strings.Contains(msg, "rejected") {
		t.Errorf("401 means the token is bad, not that the repo is private, got: %s", msg)
	}
}

// TestResetHintNeverShowsAStaleOrNegativeDuration: a reset stamp in the past
// would otherwise render as "resets in -3m0s".
func TestResetHintNeverShowsAStaleOrNegativeDuration(t *testing.T) {
	r := resp(http.StatusForbidden, map[string]string{
		"X-RateLimit-Remaining": "0",
		"X-RateLimit-Reset":     strconv.FormatInt(time.Now().Add(-3*time.Minute).Unix(), 10),
	})
	msg := apiError("https://api.github.com/x", r).Error()
	if strings.Contains(msg, "resets in -") || strings.Contains(msg, "resets in 0s") {
		t.Errorf("a past reset stamp must be omitted, got: %s", msg)
	}
	if !strings.Contains(msg, "rate limit exceeded") {
		t.Errorf("still a rate limit, got: %s", msg)
	}
}

func TestMalformedResetHeaderIsIgnored(t *testing.T) {
	r := resp(http.StatusForbidden, map[string]string{
		"X-RateLimit-Remaining": "0",
		"X-RateLimit-Reset":     "not-a-number",
	})
	msg := apiError("https://api.github.com/x", r).Error()
	if strings.Contains(msg, "resets in") {
		t.Errorf("an unparseable reset header must be dropped, got: %s", msg)
	}
}
