package utils

import (
	"context"
	"fmt"
	"github.com/go-co-op/gocron/v2"
	"github.com/robfig/cron/v3"
	"strconv"
	"strings"
	"time"
)

func ScheduleJob(ctx context.Context, interval string, loc *time.Location, jobFunc func()) (gocron.Scheduler, error) {
	if loc == nil {
		loc = time.Local
	}
	s, err := gocron.NewScheduler(gocron.WithLocation(loc))
	if err != nil {
		return s, fmt.Errorf("failed to create scheduler: %w", err)
	}
	jd, err := ConvertToJobDef(interval)
	if err != nil {
		return s, fmt.Errorf("failed to convert interval to job definition: %w", err)
	}
	// Schedule the job
	if _, err = s.NewJob(jd, gocron.NewTask(jobFunc), gocron.WithContext(ctx)); err != nil {
		return s, fmt.Errorf("failed to create job: %w", err)
	}
	return s, nil
}

// ConvertToJobDef converts a string interval to a gocron.JobDefinition.
func ConvertToJobDef(interval string) (gocron.JobDefinition, error) {
	// Parse the interval string
	// Interval could be in the format "1h", "30m", "15s" or "1h30m" or "04:05"
	var jd gocron.JobDefinition

	if t, ok := parseClockTime(interval); ok {
		return gocron.DailyJob(1, gocron.NewAtTimes(
			gocron.NewAtTime(uint(t.Hour()), uint(t.Minute()), uint(t.Second())),
		)), nil
	}

	if _, err := cron.ParseStandard(interval); err == nil {
		return gocron.CronJob(interval, false), nil
	}

	if dur, err := time.ParseDuration(interval); err == nil {
		return gocron.DurationJob(dur), nil
	}

	return jd, fmt.Errorf("invalid interval format: %s", interval)
}

func parseClockTime(s string) (time.Time, bool) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return time.Time{}, false
	}
	h, err := strconv.Atoi(parts[0])
	if err != nil || h < 0 || h > 23 {
		return time.Time{}, false
	}
	m, err := strconv.Atoi(parts[1])
	if err != nil || m < 0 || m > 59 {
		return time.Time{}, false
	}
	now := time.Now()
	// build a time.Time for today at h:m:00 in the local zone
	t := time.Date(
		now.Year(), now.Month(), now.Day(),
		h, m, 0, 0,
		time.Local,
	)
	return t, true
}
