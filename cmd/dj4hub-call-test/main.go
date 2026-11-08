package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/WongLoki/DJ4Hub/internal/sip"
)

func main() {
	inspect := flag.Bool("inspect-audio", false, "read module audio formats, mute and volume without opening streams or dialing")
	local := flag.Bool("check-local-audio", false, "check Mac microphone/speaker callbacks without dialing or saving audio")
	prepare := flag.Bool("prepare-module", false, "download verified runtime and prepare the module voice route without dialing")
	number := flag.String("number", "", "explicit test phone number; omitted runs a no-call USB audio check")
	duration := flag.Duration("duration", 15*time.Second, "test tone duration after answering (1s to 60s)")
	flag.Parse()
	modes := 0
	for _, enabled := range []bool{*inspect, *local, *prepare} {
		if enabled {
			modes++
		}
	}
	if modes > 0 {
		if *number != "" || modes > 1 {
			fmt.Fprintln(os.Stderr, "inspection/preparation modes cannot be combined with each other or -number")
			os.Exit(1)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		var err error
		switch {
		case *inspect:
			err = sip.InspectUSBAudio(os.Stdout)
		case *local:
			err = sip.CheckLocalAudio(ctx, os.Stdout)
		case *prepare:
			var dir string
			dir, err = os.UserConfigDir()
			if err == nil {
				dir = filepath.Join(dir, "DJ 4G Hub", "module-voice")
				err = sip.InstallModuleVoice(ctx, dir)
				if err == nil {
					err = sip.PrepareModuleVoice(ctx, dir, true)
				}
				if err == nil {
					fmt.Fprintln(os.Stdout, "Module voice route verified; no call placed.")
				}
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := run(ctx, *number, *duration); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, number string, duration time.Duration) error {
	if number == "" {
		return sip.CheckUSBAudio(ctx, os.Stdout)
	}
	if duration < time.Second || duration > time.Minute {
		return fmt.Errorf("duration must be 1s to 60s")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	dir = filepath.Join(dir, "DJ 4G Hub")
	raw, err := os.ReadFile(filepath.Join(dir, "sip-config.json"))
	if err != nil {
		return err
	}
	cfg := sip.DefaultConfig()
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return err
	}
	password, err := os.ReadFile(filepath.Join(dir, "sip-password"))
	if err != nil {
		return err
	}
	cfg.Password = strings.TrimSpace(string(password))
	deadline, stop := context.WithTimeout(ctx, 90*time.Second)
	defer stop()
	return sip.RunCallDiagnostic(deadline, cfg, number, duration, os.Stdout)
}
