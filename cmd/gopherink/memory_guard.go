package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/Chocola-X/GopherInk/core/services"
	"github.com/Chocola-X/GopherInk/pkg/memoryguard"
)

const (
	defaultMemoryGuardMinimumMB = 50
	memoryGuardCheckInterval    = 500 * time.Millisecond
	memoryGuardConfigInterval   = 5 * time.Second
)

type memoryGuardConfig struct {
	enabled      bool
	minimumBytes uint64
}

func startMemoryGuard(ctx context.Context, options *services.OptionService, cancel context.CancelCauseFunc) error {
	cfg, err := loadMemoryGuardConfig(ctx, options)
	if err != nil {
		return fmt.Errorf("load memory guard settings: %w", err)
	}
	if cfg.enabled {
		if err := checkAvailableMemory(cfg); err != nil {
			if errors.Is(err, memoryguard.ErrLimitReached) {
				return err
			}
			if errors.Is(err, memoryguard.ErrUnsupported) {
				log.Printf("memory guard disabled: %v", err)
				return nil
			}
			log.Printf("initial memory guard check failed: %v", err)
		}
	}
	go watchAvailableMemory(ctx, options, cfg, cancel)
	return nil
}

func watchAvailableMemory(ctx context.Context, options *services.OptionService, cfg memoryGuardConfig, cancel context.CancelCauseFunc) {
	memoryTicker := time.NewTicker(memoryGuardCheckInterval)
	configTicker := time.NewTicker(memoryGuardConfigInterval)
	defer memoryTicker.Stop()
	defer configTicker.Stop()
	var lastReadErrorLog time.Time

	for {
		select {
		case <-ctx.Done():
			return
		case <-configTicker.C:
			if next, err := loadMemoryGuardConfig(ctx, options); err == nil {
				cfg = next
			} else {
				log.Printf("memory guard settings refresh failed: %v", err)
			}
		case <-memoryTicker.C:
			if !cfg.enabled {
				continue
			}
			if err := checkAvailableMemory(cfg); err != nil {
				if errors.Is(err, memoryguard.ErrLimitReached) {
					cancel(err)
					return
				}
				if time.Since(lastReadErrorLog) >= time.Minute {
					log.Printf("memory guard check failed: %v", err)
					lastReadErrorLog = time.Now()
				}
			}
		}
	}
}

func loadMemoryGuardConfig(ctx context.Context, options *services.OptionService) (memoryGuardConfig, error) {
	values, err := options.All(ctx)
	if err != nil {
		return memoryGuardConfig{}, err
	}
	enabled := strings.TrimSpace(values["memory_guard_enabled"]) != "0"
	minimumMB, err := strconv.Atoi(strings.TrimSpace(values["memory_guard_min_available_mb"]))
	if err != nil || minimumMB < 16 {
		minimumMB = defaultMemoryGuardMinimumMB
	}
	return memoryGuardConfig{enabled: enabled, minimumBytes: uint64(minimumMB) << 20}, nil
}

func checkAvailableMemory(cfg memoryGuardConfig) error {
	available, err := memoryguard.Available()
	if err != nil {
		return err
	}
	if available < cfg.minimumBytes {
		return fmt.Errorf("%w: %d MB available, minimum %d MB", memoryguard.ErrLimitReached, available>>20, cfg.minimumBytes>>20)
	}
	return nil
}
