package realnet

import (
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"testing"
)

func TestGeneratedPortRangesDoNotDependOnNodeCount(t *testing.T) {
	for _, faultTolerance := range []int{3, 4} {
		count := 3*faultTolerance + 1
		t.Run(fmt.Sprintf("f=%d_n=%d", faultTolerance, count), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "config")
			if err := Generate(dir, "../../local-runs/test", faultTolerance, 29000, 0, Policy{}); err != nil {
				t.Fatal(err)
			}
			seen := make(map[int]bool)
			for i := 0; i < count; i++ {
				cfg, err := LoadConfig(filepath.Join(dir, fmt.Sprintf("node-%02d.json", i)))
				if err != nil {
					t.Fatal(err)
				}
				for address, want := range map[string]int{
					cfg.Peers[i].Address:       29000 + i,
					cfg.Peers[i].GossipAddress: 30000 + i,
					cfg.AdminAddress:           31000 + i,
				} {
					_, rawPort, err := net.SplitHostPort(address)
					port, parseErr := strconv.Atoi(rawPort)
					if err != nil || parseErr != nil || port != want || seen[port] {
						t.Fatalf("address %q: port=%d want=%d duplicate=%v", address, port, want, seen[port])
					}
					seen[port] = true
				}
			}
		})
	}
}

func TestGenerateRejectsClusterLargerThanPortRange(t *testing.T) {
	if err := Generate(filepath.Join(t.TempDir(), "config"), "output", 334, 29000, 0, Policy{}); err == nil {
		t.Fatal("accepted validator count larger than reserved port range")
	}
}
