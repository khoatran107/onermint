package realnet

import (
	"encoding/hex"
	"net/http/httptest"
	"os/exec"
	"testing"

	cons "blockchain-lab01/src/consensus"
	crypt "blockchain-lab01/src/crypto"
	"blockchain-lab01/src/types"
)

func TestLedgerRange(t *testing.T) {
	blocks := []cons.Finalized{
		{Block: types.Block{Header: types.BlockHeader{Height: 1}}},
		{Block: types.Block{Header: types.BlockHeader{Height: 2}}},
		{Block: types.Block{Header: types.BlockHeader{Height: 3}}},
	}
	request := httptest.NewRequest("GET", "/ledger?from=2&limit=1", nil)
	from, limit, err := ledgerRange(request)
	if err != nil || from != 2 || limit != 1 {
		t.Fatalf("range parse: %d %d %v", from, limit, err)
	}
	selected := selectLedgerRange(blocks, from, limit)
	if len(selected) != 1 || selected[0].Block.Header.Height != 2 {
		t.Fatal("wrong ledger range")
	}
	if empty := selectLedgerRange(nil, 1, 0); empty == nil || len(empty) != 0 {
		t.Fatal("empty ledger must encode as an empty JSON array")
	}
	for _, target := range []string{"/ledger?from=0", "/ledger?limit=0", "/ledger?from=x"} {
		if _, _, err := ledgerRange(httptest.NewRequest("GET", target, nil)); err == nil {
			t.Fatalf("accepted invalid range %s", target)
		}
	}
}

func TestBrowserCanonicalTransactionEncodingMatchesGo(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node.js is not installed")
	}
	var sender types.PublicKey
	for i := range sender {
		sender[i] = byte(i)
	}
	tx := types.Transaction{ChainID: "encoding-test", Sender: sender, TargetKey: hex.EncodeToString(sender[:]) + "/message", Value: []byte("hello"), Nonce: 7}
	want, err := crypt.CanonicalTxBytes(tx)
	if err != nil {
		t.Fatal(err)
	}
	script := `const a=require('./ui/app.js'); const sender=Array.from({length:32},(_,i)=>i); const tx={ChainID:'encoding-test',Sender:sender,TargetKey:Buffer.from(sender).toString('hex')+'/message',Value:new TextEncoder().encode('hello'),Nonce:7}; process.stdout.write(a.bytesToHex(a.canonicalTxBytes(tx)));`
	output, err := exec.Command("node", "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("browser encoder: %v: %s", err, output)
	}
	if string(output) != hex.EncodeToString(want) {
		t.Fatalf("browser canonical bytes differ from Go\n got %s\nwant %x", output, want)
	}
}
