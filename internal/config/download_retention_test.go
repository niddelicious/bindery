package config

import "testing"

// TestLoad_DownloadRetentionDays: 90 by default, 0 is a real value meaning
// "keep every row", and a negative or unparseable value falls back to the
// default rather than silently turning retention off.
func TestLoad_DownloadRetentionDays(t *testing.T) {
	cases := []struct {
		env  string
		want int
	}{
		{"", 90},
		{"0", 0},
		{"30", 30},
		{" 365 ", 365},
		{"-5", 90},
		{"abc", 90},
	}
	for _, tc := range cases {
		t.Run("env="+tc.env, func(t *testing.T) {
			t.Setenv("BINDERY_DOWNLOAD_RETENTION_DAYS", tc.env)
			if got := Load().DownloadRetentionDays; got != tc.want {
				t.Errorf("BINDERY_DOWNLOAD_RETENTION_DAYS=%q gave %d, want %d", tc.env, got, tc.want)
			}
		})
	}
}
