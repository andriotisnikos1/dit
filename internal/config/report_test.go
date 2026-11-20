package config

import (
	"strings"
	"testing"
	"time"
)

func TestReportScheduleDefaultsToDisabled(t *testing.T) {
	cfg := DefaultServer()
	if cfg.ReportWeeklyDay != "mon" {
		t.Errorf("ReportWeeklyDay = %q, want mon", cfg.ReportWeeklyDay)
	}
	if cfg.ReportTimezone != "UTC" {
		t.Errorf("ReportTimezone = %q, want UTC", cfg.ReportTimezone)
	}
	schedule, err := cfg.ReportSchedule()
	if err != nil {
		t.Fatalf("ReportSchedule: %v", err)
	}
	if schedule.Daily != nil || schedule.Weekly != nil {
		t.Errorf("default schedule enables a report: daily=%v weekly=%v", schedule.Daily, schedule.Weekly)
	}
	if schedule.Loc().String() != "UTC" {
		t.Errorf("location = %q, want UTC", schedule.Loc())
	}
}

func TestReportScheduleParsesConfiguredValues(t *testing.T) {
	cfg := DefaultServer()
	cfg.ReportDailyAt = " 08:30 "
	cfg.ReportWeeklyAt = "18:00"
	cfg.ReportWeeklyDay = "Friday"
	cfg.ReportTimezone = "Europe/Athens"
	cfg.normalize()

	schedule, err := cfg.ReportSchedule()
	if err != nil {
		t.Fatalf("ReportSchedule: %v", err)
	}
	if schedule.Daily == nil || schedule.Daily.Hour != 8 || schedule.Daily.Minute != 30 {
		t.Errorf("daily = %+v, want 08:30", schedule.Daily)
	}
	if schedule.Weekly == nil || schedule.Weekly.Hour != 18 || schedule.Weekly.Minute != 0 {
		t.Errorf("weekly = %+v, want 18:00", schedule.Weekly)
	}
	if schedule.WeeklyDay != time.Friday {
		t.Errorf("weekly day = %v, want Friday", schedule.WeeklyDay)
	}
	if schedule.Loc().String() != "Europe/Athens" {
		t.Errorf("location = %q, want Europe/Athens", schedule.Loc())
	}
	if got := schedule.Describe("daily"); got != "daily at 08:30 (Europe/Athens)" {
		t.Errorf("describe daily = %q", got)
	}
	if got := schedule.Describe("weekly"); got != "Fridays at 18:00 (Europe/Athens)" {
		t.Errorf("describe weekly = %q", got)
	}
}

func TestReportScheduleRejectsInvalidValues(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Server)
		want   string
	}{
		{"bad daily time", func(c *Server) { c.ReportDailyAt = "25:00" }, EnvReportDailyAt},
		{"bad weekly time", func(c *Server) { c.ReportWeeklyAt = "noon" }, EnvReportWeeklyAt},
		{"bad weekday", func(c *Server) {
			c.ReportWeeklyAt = "08:00"
			c.ReportWeeklyDay = "funday"
		}, EnvReportWeekday},
		{"bad timezone", func(c *Server) { c.ReportTimezone = "Mars/Olympus" }, EnvReportTimezone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultServer()
			cfg.APIToken = "token"
			tc.mutate(cfg)
			cfg.normalize()
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate accepted %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to name %s", err, tc.want)
			}
		})
	}
}

func TestReportEnvOverrides(t *testing.T) {
	t.Setenv(EnvAPIToken, "token")
	t.Setenv(EnvReportDailyAt, "06:15")
	t.Setenv(EnvReportWeeklyAt, "20:45")
	t.Setenv(EnvReportWeekday, "sun")
	t.Setenv(EnvReportTimezone, "Europe/Athens")

	cfg, err := LoadServer("")
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	schedule, err := cfg.ReportSchedule()
	if err != nil {
		t.Fatalf("ReportSchedule: %v", err)
	}
	if schedule.Daily == nil || schedule.Daily.Hour != 6 || schedule.Daily.Minute != 15 {
		t.Errorf("daily = %+v, want 06:15", schedule.Daily)
	}
	if schedule.Weekly == nil || schedule.Weekly.Hour != 20 || schedule.Weekly.Minute != 45 {
		t.Errorf("weekly = %+v, want 20:45", schedule.Weekly)
	}
	if schedule.WeeklyDay != time.Sunday {
		t.Errorf("weekly day = %v, want Sunday", schedule.WeeklyDay)
	}
	if schedule.Loc().String() != "Europe/Athens" {
		t.Errorf("location = %q", schedule.Loc())
	}
}
