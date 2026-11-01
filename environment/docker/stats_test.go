package docker

import (
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/require"
)

func TestCalculateDockerAbsoluteCpu(t *testing.T) {
	base := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		stats    container.StatsResponse
		expected float64
	}{
		{
			name: "uses elapsed wall time",
			stats: func() container.StatsResponse {
				stats := cpuStatsResponse(base, 2*time.Second, 5_000_000_000, 6_000_000_000)
				stats.PreCPUStats.SystemUsage = 10_000_000_000
				stats.CPUStats.SystemUsage = 74_000_000_000
				stats.CPUStats.OnlineCPUs = 64
				return stats
			}(),
			expected: 50,
		},
		{
			name:     "multiple cores",
			stats:    cpuStatsResponse(base, time.Second, 5_000_000_000, 7_500_000_000),
			expected: 250,
		},
		{
			name:     "rounds to three decimal places",
			stats:    cpuStatsResponse(base, 3*time.Second, 5_000_000_000, 6_000_000_000),
			expected: 33.333,
		},
		{
			name:     "no CPU usage",
			stats:    cpuStatsResponse(base, time.Second, 5_000_000_000, 5_000_000_000),
			expected: 0,
		},
		{
			name:     "CPU counter reset",
			stats:    cpuStatsResponse(base, time.Second, 5_000_000_000, 1_000_000_000),
			expected: 0,
		},
		{
			name:     "missing previous timestamp",
			stats:    cpuStatsResponse(time.Time{}, time.Second, 5_000_000_000, 6_000_000_000),
			expected: 0,
		},
		{
			name:     "non-increasing timestamp",
			stats:    cpuStatsResponse(base, 0, 5_000_000_000, 6_000_000_000),
			expected: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, calculateDockerAbsoluteCpu(tt.stats))
		})
	}
}

func cpuStatsResponse(preRead time.Time, elapsed time.Duration, previous, current uint64) container.StatsResponse {
	return container.StatsResponse{
		Read:    preRead.Add(elapsed),
		PreRead: preRead,
		CPUStats: container.CPUStats{
			CPUUsage: container.CPUUsage{TotalUsage: current},
		},
		PreCPUStats: container.CPUStats{
			CPUUsage: container.CPUUsage{TotalUsage: previous},
		},
	}
}

func TestWindowsStats(t *testing.T) {
	base := time.Date(2026, time.November, 1, 12, 0, 0, 0, time.UTC)
	// Windows counts CPU time in units of 100ns, 10_000_000 of them are one second.
	stats := cpuStatsResponse(base, time.Second, 10_000_000, 15_000_000)
	stats.MemoryStats.PrivateWorkingSet = 293_564_416
	stats.StorageStats.ReadSizeBytes = 8_407_040
	stats.StorageStats.WriteSizeBytes = 7_515_648

	got := windowsStats(stats)

	require.Equal(t, uint64(293_564_416), calculateDockerMemory(got.MemoryStats))
	// Half a second of CPU time in one second of wall time.
	require.Equal(t, 50.0, calculateDockerAbsoluteCpu(got))
	require.Len(t, got.BlkioStats.IoServiceBytesRecursive, 2)
	require.Equal(t, uint64(8_407_040), got.BlkioStats.IoServiceBytesRecursive[0].Value)
	require.Equal(t, uint64(7_515_648), got.BlkioStats.IoServiceBytesRecursive[1].Value)
}
