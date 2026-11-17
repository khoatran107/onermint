// Package realnet runs the blockchain node over GossipSub and gRPC streams.
package realnet

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"blockchain-lab01/src/ledger"
	"blockchain-lab01/src/mempool"
	"blockchain-lab01/src/runtime"
	"blockchain-lab01/src/types"
)

// Size limits shared by gRPC, GossipSub and block construction.
const (
	maxFrameBytes = 4 << 20
	// MaxBlockBytes leaves room for envelope/JSON overhead inside one frame.
	MaxBlockBytes = maxFrameBytes - 16<<10
	maxTxBytes    = 1 << 20
)

// Defaults applied by Config.Defaults and written by Generate.
const (
	DefaultChainID        = "lab01-local"
	DefaultBaseTimeoutMS  = 2000
	DefaultRetryMS        = 400
	DefaultMinBlockTXs    = 3
	DefaultMaxBlockTXs    = 128
	DefaultBlockMaxWaitMS = 2000
	DefaultMempoolBytes   = 8 << 20
	DefaultMaxTxBytes     = 64 << 10
	DefaultSendGapMS      = 1
	DefaultQueueLimit     = 256
	DefaultReceiveLimit   = 1000
	DefaultReceiveWindow  = 1000
	DefaultBlockMS        = 500
	maxDurationMS         = uint64((24 * time.Hour) / time.Millisecond)
)

type Peer struct {
	Address       string
	PublicKey     string
	GossipAddress string
}

// NetworkFile is the shared network.json: membership plus cluster-wide limits.
// Its values override the per-node file.
type NetworkFile struct {
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
}

// applyTo copies the shared settings over a node configuration.
func (n NetworkFile) applyTo(c *Config) {
	c.ChainID, c.F, c.Peers = n.ChainID, n.F, n.Peers
	c.MinBlockTXs, c.MaxBlockTXs, c.MaxBlockBytes = n.MinBlockTXs, n.MaxBlockTXs, n.MaxBlockBytes
	c.BlockMaxWaitMS, c.BaseTimeoutMS, c.MempoolTTLMS = n.BlockMaxWaitMS, n.BaseTimeoutMS, n.MempoolTTLMS
	c.AccountSlots, c.AccountQueue = n.AccountSlots, n.AccountQueue
	c.GlobalSlots, c.GlobalQueue = n.GlobalSlots, n.GlobalQueue
}

// Config is one node's JSON configuration. The embedded limit groups are
// flattened in JSON, so field names match network.json and node-*.json.
type Config struct {
	ChainID                          string
	NodeID, F                        int
	Peers                            []Peer
	PrivateKeyFile, OutputDir        string
	BaseTimeoutMS, RetryMS, RunForMS uint64
	BlockMaxWaitMS, MempoolTTLMS     uint64
	TargetHeights                    uint64
	SendGapMS                        uint64
	QueueLimit, ReceiveLimit         int
	ReceiveWindowMS, BlockMS         uint64
	Faults                           Policy
	NetworkFile                      string           `json:",omitempty"`
	AdminAddress                     string           `json:",omitempty"`
	Behavior                         runtime.Behavior `json:",omitempty"`
	Trace                            bool             `json:",omitempty"`
	ConsoleLog                       bool             `json:",omitempty"`
	Recover                          bool             `json:",omitempty"` // Full sync from the existing ledger.db instead of requiring a fresh run directory.
	mempool.Limits
	runtime.BlockLimits
	StartAtUnixMS int64 // Optional common real-clock start for controlled experiments.
}

func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err = json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	base := filepath.Dir(path)
	if c.NetworkFile != "" {
		raw, err := os.ReadFile(resolve(base, c.NetworkFile))
		if err != nil {
			return c, err
		}
		var shared NetworkFile
		if err = json.Unmarshal(raw, &shared); err != nil {
			return c, err
		}
		shared.applyTo(&c)
	}
	c.Defaults()
	c.PrivateKeyFile = resolve(base, c.PrivateKeyFile)
	c.OutputDir = resolve(base, c.OutputDir)
	return c, c.Validate()
}

func resolve(base, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(base, path)
}

func (c *Config) Defaults() {
	setString := func(v *string, d string) {
		if *v == "" {
			*v = d
		}
	}
	setUint := func(v *uint64, d uint64) {
		if *v == 0 {
			*v = d
		}
	}
	setInt := func(v *int, d int) {
		if *v == 0 {
			*v = d
		}
	}
	setString(&c.AdminAddress, "127.0.0.1:0")
	if c.Behavior == "" {
		c.Behavior = runtime.Honest
	}
	setString(&c.PrivateKeyFile, fmt.Sprintf("key-%02d.hex", c.NodeID))
	setString(&c.OutputDir, fmt.Sprintf("node-%02d", c.NodeID))
	setUint(&c.BaseTimeoutMS, DefaultBaseTimeoutMS)
	setUint(&c.RetryMS, DefaultRetryMS)
	setUint(&c.TargetHeights, ^uint64(0))
	setUint(&c.SendGapMS, DefaultSendGapMS)
	setInt(&c.QueueLimit, DefaultQueueLimit)
	setInt(&c.ReceiveLimit, DefaultReceiveLimit)
	setUint(&c.ReceiveWindowMS, DefaultReceiveWindow)
	setUint(&c.BlockMS, DefaultBlockMS)
	setInt(&c.MempoolBytes, DefaultMempoolBytes)
	setInt(&c.MaxTxBytes, DefaultMaxTxBytes)
	setInt(&c.MaxBlockTXs, DefaultMaxBlockTXs)
	setInt(&c.MinBlockTXs, DefaultMinBlockTXs)
	setUint(&c.BlockMaxWaitMS, DefaultBlockMaxWaitMS)
	setInt(&c.MaxBlockBytes, MaxBlockBytes)
}

func (c Config) Validate() error {
	if !c.Behavior.Valid() {
		return fmt.Errorf("unknown node behavior %q", c.Behavior)
	}
	if c.StartAtUnixMS < 0 || c.StartAtUnixMS > time.Now().Add(24*time.Hour).UnixMilli() {
		return fmt.Errorf("invalid experiment start time")
	}
	if len(c.Peers) < 8 || types.CheckValidatorCount(len(c.Peers), c.F) != nil || c.NodeID < 0 || c.NodeID >= len(c.Peers) {
		return fmt.Errorf("require n>=8, n=3f+1 and valid node ID")
	}
	if c.ChainID == "" || c.PrivateKeyFile == "" || c.OutputDir == "" || c.TargetHeights == 0 || c.BaseTimeoutMS == 0 || c.RetryMS == 0 || c.MempoolTTLMS == 0 || c.SendGapMS == 0 || c.QueueLimit < 1 || c.ReceiveLimit < 1 || c.ReceiveWindowMS == 0 || c.BlockMS == 0 {
		return fmt.Errorf("missing node settings")
	}
	if c.MaxTxBytes < 1 || c.MaxTxBytes > maxTxBytes || c.MempoolBytes < c.MaxTxBytes || c.Limits.Validate() != nil || c.MinBlockTXs < 1 || c.MinBlockTXs > c.MaxBlockTXs || c.MaxBlockTXs < 1 || c.MaxBlockBytes < c.MaxTxBytes || c.MaxBlockBytes > MaxBlockBytes {
		return fmt.Errorf("invalid mempool/block capacity")
	}
	for _, ms := range []uint64{c.BaseTimeoutMS, c.RetryMS, c.RunForMS, c.BlockMaxWaitMS, c.MempoolTTLMS, c.SendGapMS, c.ReceiveWindowMS, c.BlockMS} {
		if ms > maxDurationMS {
			return fmt.Errorf("duration exceeds one-day lab limit")
		}
	}
	seenAddr := map[string]bool{}
	seenKey := map[string]bool{}
	for _, p := range c.Peers {
		for _, address := range []string{p.Address, p.GossipAddress} {
			host, port, err := net.SplitHostPort(address)
			if err != nil || port == "" || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || seenAddr[address] {
				return fmt.Errorf("require distinct loopback gRPC and GossipAddress endpoints; regenerate old configurations")
			}
			seenAddr[address] = true
		}
		key, err := hex.DecodeString(p.PublicKey)
		if err != nil || len(key) != 32 || seenKey[string(key)] {
			return fmt.Errorf("invalid/duplicate peer identity or address")
		}
		seenKey[string(key)] = true
	}
	host, port, err := net.SplitHostPort(c.AdminAddress)
	adminIP := net.ParseIP(host)
	if err != nil || port == "" || adminIP == nil || !(adminIP.IsLoopback() || adminIP.IsUnspecified() || adminIP.IsPrivate()) || (port != "0" && seenAddr[c.AdminAddress]) {
		return fmt.Errorf("admin address must be a distinct loopback, private LAN, or unspecified endpoint")
	}
	return c.Faults.Validate()
}

// runtimeConfig maps the node configuration onto the transport-free runtime.
func (c Config) runtimeConfig(ids []types.ValidatorID, key ed25519.PrivateKey, store *ledger.Store) runtime.Config {
	return runtime.Config{ID: c.NodeID, ChainID: c.ChainID, F: c.F, Validators: ids, PrivateKey: key, BaseTimeout: c.BaseTimeoutMS, RetryInterval: c.RetryMS, BlockMaxWait: c.BlockMaxWaitMS, MempoolTTL: c.MempoolTTLMS, TargetHeights: c.TargetHeights, Store: store, Behavior: c.Behavior, Mempool: c.Limits, Blocks: c.BlockLimits, Recover: c.Recover}
}

func (c Config) keys() ([]types.ValidatorID, ed25519.PrivateKey, error) {
	var ids []types.ValidatorID
	for _, p := range c.Peers {
		b, err := hex.DecodeString(p.PublicKey)
		if err != nil || len(b) != 32 {
			return nil, nil, fmt.Errorf("invalid public key")
		}
		ids = append(ids, types.ValidatorID(b))
	}
	b, err := os.ReadFile(c.PrivateKeyFile)
	if err != nil {
		return nil, nil, err
	}
	raw, err := hex.DecodeString(string(b))
	if err != nil || len(raw) != 64 {
		return nil, nil, fmt.Errorf("invalid private key file")
	}
	return ids, ed25519.PrivateKey(raw), nil
}
