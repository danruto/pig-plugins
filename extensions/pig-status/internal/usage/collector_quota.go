package usage

import (
	"math"
	"regexp"
	"time"
)

var monthWindow = regexp.MustCompile(`^(672|696|720|744)h$`)

func reportWindows(report Report, now time.Time) []Window {
	windows := []Window{}
	for _, bucket := range report.Buckets {
		used, ok := bucketPercent(bucket)
		if !ok {
			continue
		}
		window := Window{Label: bucket.Label, UsedPercent: max(0, min(100, used))}
		if monthWindow.MatchString(window.Label) {
			window.Label = "Month"
		}
		if bucket.ResetsAt != nil && finiteQuota(*bucket.ResetsAt) {
			seconds := *bucket.ResetsAt
			if seconds <= float64(now.UnixNano())/1e9 {
				continue
			}
			if seconds < float64(math.MaxInt64)/1e9 {
				window.ResetsAt = time.Unix(0, int64(seconds*1e9))
				window.Countdown = window.ResetsAt.Sub(now)
			}
		}
		windows = append(windows, window)
	}
	return windows
}

func bucketPercent(bucket Bucket) (float64, bool) {
	denominator := 100.0
	if bucket.Unit != "percent" {
		if bucket.Limit == nil || !finiteQuota(*bucket.Limit) || *bucket.Limit <= 0 {
			return 0, false
		}
		denominator = *bucket.Limit
	}
	if bucket.Used != nil && finiteQuota(*bucket.Used) {
		value := *bucket.Used / denominator * 100
		return value, finiteQuota(value)
	}
	if bucket.Remaining != nil && finiteQuota(*bucket.Remaining) {
		value := (1 - *bucket.Remaining/denominator) * 100
		return value, finiteQuota(value)
	}
	return 0, false
}

func finiteQuota(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
