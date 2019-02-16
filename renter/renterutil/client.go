package renterutil

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
	"time"

	"lukechampine.com/us/hostdb"

	"gitlab.com/NebulousLabs/Sia/crypto"
	"gitlab.com/NebulousLabs/Sia/encoding"
	"gitlab.com/NebulousLabs/Sia/modules"
	"gitlab.com/NebulousLabs/Sia/node/api/client"
	"gitlab.com/NebulousLabs/Sia/types"
)

var errNoHostAnnouncement = errors.New("host announcement not found")

// SiadClient wraps the siad API client. It satisfies the proto.Wallet,
// proto.TransactionPool, and renter.HostKeyResolver interfaces. The
// proto.Wallet methods require that the wallet is unlocked.
type SiadClient struct {
	siad *client.Client
}

// ChainHeight returns the current block height.
func (c *SiadClient) ChainHeight() (types.BlockHeight, error) {
	cg, err := c.siad.ConsensusGet()
	return cg.Height, err
}

// Synced returns whether the siad node believes it is fully synchronized with
// the rest of the network.
func (c *SiadClient) Synced() (bool, error) {
	cg, err := c.siad.ConsensusGet()
	return cg.Synced, err
}

// AcceptTransactionSet submits a transaction set to the transaction pool,
// where it will be broadcast to other peers.
func (c *SiadClient) AcceptTransactionSet(txnSet []types.Transaction) error {
	if len(txnSet) == 0 {
		return errors.New("empty transaction set")
	}
	txn, parents := txnSet[len(txnSet)-1], txnSet[:len(txnSet)-1]
	return c.siad.TransactionPoolRawPost(txn, parents)
}

// FeeEstimate returns the current estimate for transaction fees, in Hastings
// per byte.
func (c *SiadClient) FeeEstimate() (minFee, maxFee types.Currency, err error) {
	tfg, err := c.siad.TransactionPoolFeeGet()
	return tfg.Minimum, tfg.Maximum, err
}

// NewWalletAddress returns a new address generated by the wallet.
func (c *SiadClient) NewWalletAddress() (types.UnlockHash, error) {
	wag, err := c.siad.WalletAddressGet()
	return wag.Address, err
}

// SignTransaction adds the specified signatures to the transaction using
// private keys known to the wallet.
func (c *SiadClient) SignTransaction(txn *types.Transaction, toSign []crypto.Hash) error {
	wspr, err := c.siad.WalletSignPost(*txn, toSign)
	if err == nil {
		*txn = wspr.Transaction
	}
	return err
}

// UnspentOutputs returns the set of outputs tracked by the wallet that are
// spendable.
func (c *SiadClient) UnspentOutputs() ([]modules.UnspentOutput, error) {
	wug, err := c.siad.WalletUnspentGet()
	return wug.Outputs, err
}

// UnlockConditions returns the UnlockConditions that correspond to the
// specified address.
func (c *SiadClient) UnlockConditions(addr types.UnlockHash) (types.UnlockConditions, error) {
	wucg, err := c.siad.WalletUnlockConditionsGet(addr)
	return wucg.UnlockConditions, err
}

// HostDB

// Hosts returns the public keys of every host that has announced on the blockchain.
func (c *SiadClient) Hosts() ([]hostdb.HostPublicKey, error) {
	hdag, err := c.siad.HostDbAllGet()
	hosts := make([]hostdb.HostPublicKey, len(hdag.Hosts))
	for i, h := range hdag.Hosts {
		hosts[i] = hostdb.HostPublicKey(h.PublicKeyString)
	}
	return hosts, err
}

// ResolveHostKey resolves a host public key to that host's most recently
// announced network address.
func (c *SiadClient) ResolveHostKey(pubkey hostdb.HostPublicKey) (modules.NetAddress, error) {
	hhg, err := c.siad.HostDbHostsGet(pubkey.SiaPublicKey())
	if err != nil && strings.Contains(err.Error(), "requested host does not exist") {
		return "", errNoHostAnnouncement
	}
	return hhg.Entry.NetAddress, err
}

// Scan scans the specified host.
func (c *SiadClient) Scan(pubkey hostdb.HostPublicKey) (hostdb.ScannedHost, error) {
	hhg, err := c.siad.HostDbHostsGet(pubkey.SiaPublicKey())
	if err != nil {
		return hostdb.ScannedHost{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return hostdb.Scan(ctx, hhg.Entry.NetAddress, pubkey)
}

// NewSiadClient returns a SiadClient that communicates with the siad API
// server at the specified address.
func NewSiadClient(addr, password string) *SiadClient {
	c := client.New(addr)
	c.Password = password
	return &SiadClient{siad: c}
}

// A SHARDClient communicates with a SHARD server. It satisfies the
// renter.HostKeyResolver interface.
type SHARDClient struct {
	addr string
}

func (c *SHARDClient) req(route string, fn func(*http.Response) error) error {
	resp, err := http.Get(fmt.Sprintf("http://%v%v", c.addr, route))
	if err != nil {
		return err
	}
	defer io.Copy(ioutil.Discard, resp.Body)
	defer resp.Body.Close()

	if !(200 <= resp.StatusCode && resp.StatusCode <= 299) {
		errString, _ := ioutil.ReadAll(resp.Body)
		return errors.New(string(errString))
	}
	err = fn(resp)
	return err

}

// ChainHeight returns the current block height.
func (c *SHARDClient) ChainHeight() (types.BlockHeight, error) {
	var height types.BlockHeight
	err := c.req("/height", func(resp *http.Response) error {
		return json.NewDecoder(resp.Body).Decode(&height)
	})
	return height, err
}

// Synced returns whether the blockchain is synced.
func (c *SHARDClient) Synced() (bool, error) {
	var synced bool
	err := c.req("/synced", func(resp *http.Response) error {
		data, err := ioutil.ReadAll(io.LimitReader(resp.Body, 8))
		if err != nil {
			return err
		}
		synced, err = strconv.ParseBool(string(data))
		return err
	})
	return synced, err
}

// ResolveHostKey resolves a host public key to that host's most recently
// announced network address.
func (c *SHARDClient) ResolveHostKey(pubkey hostdb.HostPublicKey) (modules.NetAddress, error) {
	var ha modules.HostAnnouncement
	var sig crypto.Signature
	err := c.req("/host/"+string(pubkey), func(resp *http.Response) error {
		if resp.ContentLength == 0 {
			return errNoHostAnnouncement
		}
		return encoding.NewDecoder(resp.Body, encoding.DefaultAllocLimit).DecodeAll(&ha, &sig)
	})
	if err != nil {
		return "", err
	}

	// verify signature
	if crypto.VerifyHash(crypto.HashObject(ha), pubkey.Ed25519(), sig) != nil {
		return "", errors.New("invalid signature")
	}

	return ha.NetAddress, err
}

// NewSHARDClient returns a SHARDClient that communicates with the SHARD
// server at the specified address.
func NewSHARDClient(addr string) *SHARDClient {
	return &SHARDClient{addr: addr}
}
