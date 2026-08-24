package selfupdate

import (
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// nowFn is swappable so the reset-countdown wording is testable.
var nowFn = time.Now

// apiError turns a non-200 GitHub response into an error that names the ACTUAL
// cause.
//
// GitHub reports an exhausted rate limit as a plain 403, which is
// indistinguishable from a permissions failure unless you read the headers. The
// previous message assumed the permissions case for every 403 and told the user
// to "set GITHUB_TOKEN if the repo is private" — so a user hitting the
// unauthenticated 60-requests/hour cap on a PUBLIC repo went looking for a
// permissions problem that did not exist.
//
// The distinguishing signal is X-RateLimit-Remaining: 0. Setting a token is
// still the right advice when rate-limited (it raises the cap to 5000/hour), but
// the reason and the wait time matter more than the guess about visibility.
func apiError(url string, resp *http.Response) error {
	if isRateLimited(resp) {
		limit := resp.Header.Get("X-RateLimit-Limit")
		if limit == "" {
			limit = "the anonymous"
		} else {
			limit += " requests/hour"
		}
		return fmt.Errorf(
			"GitHub API rate limit exceeded (%s)%s.\n"+
				"This is not a permissions problem — the repository is reachable, you have simply "+
				"used up the anonymous quota for your IP.\n"+
				"Set GITHUB_TOKEN (or GH_TOKEN) to raise the limit to 5000 requests/hour: %s",
			limit, resetHint(resp), url)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("GitHub API %s returned %s — GITHUB_TOKEN is set but was rejected; "+
			"check that it is valid and not expired", url, resp.Status)
	case http.StatusNotFound:
		return fmt.Errorf("GitHub API %s returned %s (set GITHUB_TOKEN if the repository is private)",
			url, resp.Status)
	}
	return fmt.Errorf("GitHub API %s returned %s", url, resp.Status)
}

// isRateLimited reports whether a response is a rate-limit rejection. GitHub uses
// 403 for the primary limit and 429 for secondary limits; both carry a zeroed
// X-RateLimit-Remaining, and a secondary limit may carry only Retry-After.
func isRateLimited(resp *http.Response) bool {
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		return false
	}
	if resp.Header.Get("X-RateLimit-Remaining") == "0" {
		return true
	}
	return resp.Header.Get("Retry-After") != ""
}

// resetHint renders ", resets in 9m30s" when the response says when the window
// rolls over, and "" when it does not — never a bare or negative duration.
func resetHint(resp *http.Response) string {
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, err := strconv.Atoi(ra); err == nil && secs > 0 {
			return fmt.Sprintf(", retry in %s", (time.Duration(secs) * time.Second).String())
		}
	}
	reset := resp.Header.Get("X-RateLimit-Reset")
	if reset == "" {
		return ""
	}
	epoch, err := strconv.ParseInt(reset, 10, 64)
	if err != nil {
		return ""
	}
	d := time.Until(time.Unix(epoch, 0)).Round(time.Second)
	if nowFn != nil {
		d = time.Unix(epoch, 0).Sub(nowFn()).Round(time.Second)
	}
	if d <= 0 {
		return ""
	}
	return fmt.Sprintf(", resets in %s", d.String())
}
