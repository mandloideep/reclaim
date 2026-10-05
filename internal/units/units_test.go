package units

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseSize(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{in: "0", want: 0},
		{in: "1000", want: 1000},
		{in: "50MB", want: 50_000_000},
		{in: "50mb", want: 50_000_000},
		{in: "1.5 GB", want: 1_500_000_000},
		{in: "512KiB", want: 512 * 1024},
		{in: "2GiB", want: 2 << 30},
		{in: "1T", want: 1_000_000_000_000},
		{in: "10k", want: 10_000},
		{in: "", wantErr: true},
		{in: "MB", wantErr: true},
		{in: "5 parsecs", wantErr: true},
		{in: "-5MB", wantErr: true},
		{in: "1.2.3MB", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseSize(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestFormatSize(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{999, "999 B"},
		{1000, "1.0 kB"},
		{1234567, "1.2 MB"},
		{999_960_000, "1.0 GB"},
		{57_000_000_000, "57.0 GB"},
		{1_500_000_000_000, "1.5 TB"},
		{-2000, "-2.0 kB"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, FormatSize(tt.in), "FormatSize(%d)", tt.in)
	}
}

func TestParseAge(t *testing.T) {
	day := 24 * time.Hour
	tests := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{in: "90d", want: 90 * day},
		{in: "2w", want: 14 * day},
		{in: "1y", want: 365 * day},
		{in: "36h", want: 36 * time.Hour},
		{in: "1.5d", want: 36 * time.Hour},
		{in: "", wantErr: true},
		{in: "xd", wantErr: true},
		{in: "soon", wantErr: true},
		{in: "-3h", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseAge(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestFormatAge(t *testing.T) {
	day := 24 * time.Hour
	require.Equal(t, "now", FormatAge(time.Minute))
	require.Equal(t, "5h", FormatAge(5*time.Hour))
	require.Equal(t, "3d", FormatAge(3*day))
	require.Equal(t, "4w", FormatAge(30*day))
	require.Equal(t, "2.0y", FormatAge(730*day))
}
