package lab

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNetworkWindowsCountersAndLoopbackICMP(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 35*time.Second)
	defer stop()
	state := readHostSection(ctx, hostCheck{31, "tcp", []string{"workers", "v4", "v6"}, hostTCPState})
	if state.Error != "" || len(state.Rows) != 1 {
		t.Fatalf("host TCP counters: %+v", state)
	}
	if _, err := strconv.Atoi(state.Rows[0][0]); err != nil {
		t.Fatal("invalid worker process count", state.Rows)
	}
	icmp := readHostSection(ctx, hostCheck{31, "icmp", []string{"sample", "utc", "ms", "status"}, strings.ReplaceAll(hostICMP, "PEER", "127.0.0.1")})
	if icmp.Error != "" || len(icmp.Rows) != ERPNetworkSamples {
		t.Fatalf("local ICMP: %+v", icmp)
	}
	for i, r := range icmp.Rows {
		if r[0] != strconv.Itoa(i+1) || r[1] == "" || r[3] == "" {
			t.Fatalf("invalid ICMP row %d: %v", i, r)
		}
	}
}
