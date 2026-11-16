package realnet

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	crypt "blockchain-lab01/src/crypto"
)

const (
	portRangeSize   = 1000
	gossipPortShift = portRangeSize
	adminPortShift  = 2 * portRangeSize
)

type MempoolSettings struct {
	TTLMS        uint64
	AccountSlots int
	AccountQueue int
	GlobalSlots  int
	GlobalQueue  int
}

// Generate creates a fresh local development cluster. Key randomness is always
// crypto/rand through GenerateIdentity, independently of Faults.Seed.
func Generate(dir, outputRoot string, faultTolerance, basePort int, runMS uint64, faults Policy, mempool MempoolSettings) error {
	count := 3*faultTolerance + 1
	if faultTolerance < 3 || count > portRangeSize || basePort < 1024 || basePort+adminPortShift+count-1 > 65535 {
		return fmt.Errorf("invalid cluster size or ports")
	}
	if outputRoot == "" {
		return fmt.Errorf("output root is required")
	}
	if err := faults.Validate(); err != nil {
		return err
	}
	if mempool.TTLMS == 0 || mempool.AccountSlots < 1 || mempool.AccountQueue < 1 || mempool.GlobalSlots < mempool.AccountSlots || mempool.GlobalQueue < mempool.AccountQueue {
		return fmt.Errorf("invalid mempool settings")
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	} // Refuse to overwrite an existing cluster.
	var peers []Peer
	for i := 0; i < count; i++ {
		pub, key, err := crypt.GenerateIdentity()
		if err != nil {
			return err
		}
		name := fmt.Sprintf("key-%02d.hex", i)
		if err = os.WriteFile(filepath.Join(dir, name), []byte(hex.EncodeToString(key)), 0600); err != nil {
			return err
		}
		peers = append(peers, Peer{Address: fmt.Sprintf("127.0.0.1:%d", basePort+i), PublicKey: hex.EncodeToString(pub[:]), GossipAddress: fmt.Sprintf("127.0.0.1:%d", basePort+gossipPortShift+i)})
	}
	shared := struct {
		ChainID        string
		F              int
		Peers          []Peer
		MinBlockTXs    int
		BlockMaxWaitMS uint64
		BaseTimeoutMS  uint64
		MaxBlockTXs    int
		MaxBlockBytes  int
		MempoolTTLMS   uint64
		AccountSlots   int
		AccountQueue   int
		GlobalSlots    int
		GlobalQueue    int
	}{"lab01-local", faultTolerance, peers, 3, 2000, 2000, 128, (4 << 20) - (16 << 10), mempool.TTLMS, mempool.AccountSlots, mempool.AccountQueue, mempool.GlobalSlots, mempool.GlobalQueue}
	b, err := json.MarshalIndent(shared, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "network.json"), b, 0600); err != nil {
		return err
	}
	for i := 0; i < count; i++ {
		policy := faults
		policy.Seed += int64(i)
		c := struct {
			NodeID       int
			NetworkFile  string
			OutputDir    string
			Behavior     string
			AdminAddress string
			SendGapMS    uint64
			RunForMS     uint64  `json:",omitempty"`
			Faults       *Policy `json:",omitempty"`
		}{NodeID: i, NetworkFile: "network.json", OutputDir: filepath.Join(outputRoot, fmt.Sprintf("node-%02d", i)), Behavior: "honest", AdminAddress: fmt.Sprintf("127.0.0.1:%d", basePort+adminPortShift+i), SendGapMS: 1, RunForMS: runMS}
		if faults != (Policy{}) && faults != (Policy{Seed: 1}) {
			c.Faults = &policy
		}
		b, err := json.MarshalIndent(c, "", "  ")
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(dir, fmt.Sprintf("node-%02d.json", i)), b, 0600); err != nil {
			return err
		}
	}
	return nil
}
