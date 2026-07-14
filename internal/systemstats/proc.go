package systemstats

import (
	"bufio"
	"strconv"
	"strings"
)

func parseProcStat(data []byte) cpuCounters {
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "cpu" {
			continue
		}
		limit := len(fields)
		if limit > 9 {
			limit = 9 // Guest times are already included in user and nice.
		}
		values := make([]uint64, 0, limit-1)
		for _, field := range fields[1:limit] {
			value, err := strconv.ParseUint(field, 10, 64)
			if err != nil {
				return cpuCounters{}
			}
			values = append(values, value)
		}
		var total uint64
		for _, value := range values {
			total += value
		}
		idle := values[3]
		if len(values) > 4 {
			idle += values[4]
		}
		return cpuCounters{total: total, idle: idle, valid: total > 0}
	}
	return cpuCounters{}
}

func parseLoadAvg(data []byte) (float64, float64, float64) {
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return 0, 0, 0
	}
	load1, _ := strconv.ParseFloat(fields[0], 64)
	load5, _ := strconv.ParseFloat(fields[1], 64)
	load15, _ := strconv.ParseFloat(fields[2], 64)
	return load1, load5, load15
}

func parseMemInfo(data []byte) MemoryStats {
	values := map[string]uint64{}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		switch key {
		case "MemTotal", "MemAvailable", "MemFree", "Buffers", "Cached", "SReclaimable", "Shmem":
			value, err := strconv.ParseUint(fields[1], 10, 64)
			if err == nil {
				values[key] = value * 1024
			}
		}
	}
	total := values["MemTotal"]
	available, hasAvailable := values["MemAvailable"]
	if !hasAvailable {
		available = values["MemFree"] + values["Buffers"] + values["Cached"] + values["SReclaimable"]
		if shmem := values["Shmem"]; shmem < available {
			available -= shmem
		}
	}
	if available > total {
		available = total
	}
	return MemoryStats{TotalBytes: total, UsedBytes: total - available, AvailableBytes: available}
}

func parseNetDev(data []byte) networkCounters {
	var result networkCounters
	for _, line := range strings.Split(string(data), "\n") {
		name, counters, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) == "lo" {
			continue
		}
		fields := strings.Fields(counters)
		if len(fields) < 16 {
			continue
		}
		received, receiveErr := strconv.ParseUint(fields[0], 10, 64)
		sent, sendErr := strconv.ParseUint(fields[8], 10, 64)
		if receiveErr != nil || sendErr != nil {
			continue
		}
		result.received += received
		result.sent += sent
		result.valid = true
	}
	return result
}
