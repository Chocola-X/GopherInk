//go:build linux

package memoryguard

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func Available() (uint64, error) {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer file.Close()

	var available, free, buffers, cached uint64
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		name, valueText, ok := strings.Cut(scanner.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(valueText)
		if len(fields) == 0 {
			continue
		}
		value, parseErr := strconv.ParseUint(fields[0], 10, 64)
		if parseErr != nil {
			continue
		}
		value *= 1024
		switch name {
		case "MemAvailable":
			available = value
		case "MemFree":
			free = value
		case "Buffers":
			buffers = value
		case "Cached":
			cached = value
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	if available == 0 {
		available = free + buffers + cached
	}
	if available == 0 {
		return 0, fmt.Errorf("MemAvailable is missing from /proc/meminfo")
	}
	if cgroupAvailable, ok := availableCgroupMemory(); ok && cgroupAvailable < available {
		available = cgroupAvailable
	}
	return available, nil
}

func availableCgroupMemory() (uint64, bool) {
	body, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(body), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		if parts[0] == "0" && parts[1] == "" {
			base := filepath.Join("/sys/fs/cgroup", strings.TrimPrefix(parts[2], "/"))
			return cgroupAvailableBytes(filepath.Join(base, "memory.max"), filepath.Join(base, "memory.current"))
		}
		for _, controller := range strings.Split(parts[1], ",") {
			if controller == "memory" {
				base := filepath.Join("/sys/fs/cgroup/memory", strings.TrimPrefix(parts[2], "/"))
				return cgroupAvailableBytes(filepath.Join(base, "memory.limit_in_bytes"), filepath.Join(base, "memory.usage_in_bytes"))
			}
		}
	}
	return 0, false
}

func cgroupAvailableBytes(limitPath, currentPath string) (uint64, bool) {
	limitText, err := os.ReadFile(limitPath)
	if err != nil || strings.TrimSpace(string(limitText)) == "max" {
		return 0, false
	}
	currentText, err := os.ReadFile(currentPath)
	if err != nil {
		return 0, false
	}
	limit, err := strconv.ParseUint(strings.TrimSpace(string(limitText)), 10, 64)
	if err != nil || limit == 0 {
		return 0, false
	}
	current, err := strconv.ParseUint(strings.TrimSpace(string(currentText)), 10, 64)
	if err != nil {
		return 0, false
	}
	if current >= limit {
		return 0, true
	}
	return limit - current, true
}
