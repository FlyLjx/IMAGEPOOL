package systemstats

import (
	"sync"
	"testing"
	"time"
)

func TestProcParsers(t *testing.T) {
	cpu := parseProcStat([]byte("cpu  100 10 20 200 30 5 15 20 7 3\ncpu0 1 2 3 4\n"))
	if !cpu.valid || cpu.total != 400 || cpu.idle != 230 {
		t.Fatalf("cpu=%+v", cpu)
	}
	load1, load5, load15 := parseLoadAvg([]byte("1.25 2.50 3.75 2/100 123\n"))
	if load1 != 1.25 || load5 != 2.5 || load15 != 3.75 {
		t.Fatalf("load=%v,%v,%v", load1, load5, load15)
	}
	memory := parseMemInfo([]byte("MemTotal: 1000 kB\nMemAvailable: 400 kB\nMemFree: 100 kB\n"))
	if memory.TotalBytes != 1000*1024 || memory.AvailableBytes != 400*1024 || memory.UsedBytes != 600*1024 {
		t.Fatalf("memory=%+v", memory)
	}
	network := parseNetDev([]byte("Inter-| Receive | Transmit\n lo: 50 0 0 0 0 0 0 0 60 0 0 0 0 0 0 0\n eth0: 100 0 0 0 0 0 0 0 200 0 0 0 0 0 0 0\n eth1: 300 0 0 0 0 0 0 0 400 0 0 0 0 0 0 0\n"))
	if !network.valid || network.received != 400 || network.sent != 600 {
		t.Fatalf("network=%+v", network)
	}
}

type sequenceReader struct {
	items []rawSnapshot
	index int
}

func (r *sequenceReader) read(string) rawSnapshot {
	index := r.index
	if index >= len(r.items) {
		index = len(r.items) - 1
	}
	r.index++
	return r.items[index]
}

func TestSamplerComputesCPUAndNetworkDeltas(t *testing.T) {
	reader := &sequenceReader{items: []rawSnapshot{
		{cpu: cpuCounters{total: 1000, idle: 400, valid: true}, network: networkCounters{received: 1000, sent: 2000, valid: true}},
		{
			cpu:     cpuCounters{total: 1200, idle: 450, valid: true},
			cores:   8,
			load1:   1.2,
			load5:   1.1,
			load15:  1,
			memory:  MemoryStats{TotalBytes: 1000, UsedBytes: 250, AvailableBytes: 750},
			disk:    DiskStats{Path: "/data", TotalBytes: 2000, UsedBytes: 1000, AvailableBytes: 900},
			network: networkCounters{received: 1500, sent: 3000, valid: true},
		},
	}}
	times := []time.Time{time.Unix(100, 0), time.Unix(102, 0)}
	timeIndex := 0
	sampler := &Sampler{reader: reader, diskPath: "/data", now: func() time.Time {
		at := times[timeIndex]
		timeIndex++
		return at
	}}
	sampler.captureBaseline()
	snapshot := sampler.Sample()
	if snapshot.CPU.UsagePercent != 75 || snapshot.CPU.Cores != 8 {
		t.Fatalf("cpu=%+v", snapshot.CPU)
	}
	if snapshot.Memory.UsagePercent != 25 || snapshot.Disk.UsagePercent != 50 {
		t.Fatalf("memory=%+v disk=%+v", snapshot.Memory, snapshot.Disk)
	}
	if snapshot.Network.ReceiveBytesPerSecond != 250 || snapshot.Network.SendBytesPerSecond != 500 {
		t.Fatalf("network=%+v", snapshot.Network)
	}
}

type countingReader struct {
	value uint64
}

func (r *countingReader) read(string) rawSnapshot {
	r.value++
	return rawSnapshot{
		cpu:     cpuCounters{total: r.value * 100, idle: r.value * 25, valid: true},
		network: networkCounters{received: r.value * 100, sent: r.value * 200, valid: true},
	}
}

func TestSamplerSerializesConcurrentReads(t *testing.T) {
	reader := &countingReader{}
	sampler := &Sampler{reader: reader, diskPath: "/", now: time.Now}
	sampler.captureBaseline()
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			sampler.Sample()
		}()
	}
	wait.Wait()
	if reader.value != 33 {
		t.Fatalf("reads=%d", reader.value)
	}
}
