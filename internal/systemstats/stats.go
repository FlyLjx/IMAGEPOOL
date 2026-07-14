package systemstats

import (
	"math"
	"strings"
	"sync"
	"time"
)

type Snapshot struct {
	SampledAt time.Time    `json:"sampled_at"`
	CPU       CPUStats     `json:"cpu"`
	Memory    MemoryStats  `json:"memory"`
	Disk      DiskStats    `json:"disk"`
	Network   NetworkStats `json:"network"`
}

type CPUStats struct {
	UsagePercent float64 `json:"usage_percent"`
	Cores        int     `json:"cores"`
	Load1        float64 `json:"load_1"`
	Load5        float64 `json:"load_5"`
	Load15       float64 `json:"load_15"`
}

type MemoryStats struct {
	TotalBytes     uint64  `json:"total_bytes"`
	UsedBytes      uint64  `json:"used_bytes"`
	AvailableBytes uint64  `json:"available_bytes"`
	UsagePercent   float64 `json:"usage_percent"`
}

type DiskStats struct {
	Path           string  `json:"path"`
	TotalBytes     uint64  `json:"total_bytes"`
	UsedBytes      uint64  `json:"used_bytes"`
	AvailableBytes uint64  `json:"available_bytes"`
	UsagePercent   float64 `json:"usage_percent"`
}

type NetworkStats struct {
	ReceivedBytes         uint64  `json:"received_bytes"`
	SentBytes             uint64  `json:"sent_bytes"`
	ReceiveBytesPerSecond float64 `json:"receive_bytes_per_second"`
	SendBytesPerSecond    float64 `json:"send_bytes_per_second"`
}

type cpuCounters struct {
	total uint64
	idle  uint64
	valid bool
}

type networkCounters struct {
	received uint64
	sent     uint64
	valid    bool
}

type rawSnapshot struct {
	cpu     cpuCounters
	cores   int
	load1   float64
	load5   float64
	load15  float64
	memory  MemoryStats
	disk    DiskStats
	network networkCounters
}

type rawReader interface {
	read(diskPath string) rawSnapshot
}

type Sampler struct {
	mu       sync.Mutex
	reader   rawReader
	diskPath string
	now      func() time.Time

	previousCPU       cpuCounters
	previousNetwork   networkCounters
	previousNetworkAt time.Time
}

func New(diskPath string) *Sampler {
	diskPath = strings.TrimSpace(diskPath)
	if diskPath == "" {
		diskPath = "/"
	}
	sampler := &Sampler{reader: newPlatformReader(), diskPath: diskPath, now: time.Now}
	sampler.captureBaseline()
	return sampler
}

func (s *Sampler) captureBaseline() {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw := s.reader.read(s.diskPath)
	at := s.now()
	if raw.cpu.valid {
		s.previousCPU = raw.cpu
	}
	if raw.network.valid {
		s.previousNetwork = raw.network
		s.previousNetworkAt = at
	}
}

func (s *Sampler) Sample() Snapshot {
	if s == nil {
		return Snapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	raw := s.reader.read(s.diskPath)
	at := s.now()
	result := Snapshot{
		SampledAt: at,
		CPU: CPUStats{
			Cores:  raw.cores,
			Load1:  raw.load1,
			Load5:  raw.load5,
			Load15: raw.load15,
		},
		Memory: raw.memory,
		Disk:   raw.disk,
		Network: NetworkStats{
			ReceivedBytes: raw.network.received,
			SentBytes:     raw.network.sent,
		},
	}
	result.Memory.UsagePercent = usagePercent(result.Memory.UsedBytes, result.Memory.TotalBytes)
	result.Disk.UsagePercent = usagePercent(result.Disk.UsedBytes, result.Disk.TotalBytes)

	if raw.cpu.valid {
		if s.previousCPU.valid && raw.cpu.total >= s.previousCPU.total && raw.cpu.idle >= s.previousCPU.idle {
			totalDelta := raw.cpu.total - s.previousCPU.total
			idleDelta := raw.cpu.idle - s.previousCPU.idle
			if totalDelta > 0 && idleDelta <= totalDelta {
				result.CPU.UsagePercent = round2(float64(totalDelta-idleDelta) * 100 / float64(totalDelta))
			}
		}
		s.previousCPU = raw.cpu
	}

	if raw.network.valid {
		elapsed := at.Sub(s.previousNetworkAt).Seconds()
		if s.previousNetwork.valid && elapsed > 0 {
			if raw.network.received >= s.previousNetwork.received {
				result.Network.ReceiveBytesPerSecond = round2(float64(raw.network.received-s.previousNetwork.received) / elapsed)
			}
			if raw.network.sent >= s.previousNetwork.sent {
				result.Network.SendBytesPerSecond = round2(float64(raw.network.sent-s.previousNetwork.sent) / elapsed)
			}
		}
		s.previousNetwork = raw.network
		s.previousNetworkAt = at
	}
	return result
}

func usagePercent(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	if used > total {
		used = total
	}
	return round2(float64(used) * 100 / float64(total))
}

func round2(value float64) float64 {
	return math.Round(value*100) / 100
}
