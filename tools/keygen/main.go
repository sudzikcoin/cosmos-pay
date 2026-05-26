// Command keygen generates fresh BIP-39 mnemonics for use as PAYER and
// FACILITATOR in x402-cosmos end-to-end tests. Prints mnemonic + the
// derived noble bech32 address. TESTNET USE ONLY — never reuse on
// mainnet.
package main

import (
	"fmt"

	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	"github.com/cosmos/cosmos-sdk/types/bech32"
	bip39 "github.com/cosmos/go-bip39"
)

func main() {
	for _, label := range []string{"FACILITATOR", "PAYER"} {
		mnemonic, addr := newAccount("noble")
		fmt.Printf("=== %s ===\n", label)
		fmt.Printf("mnemonic: %s\n", mnemonic)
		fmt.Printf("address:  %s\n\n", addr)
	}
}

func newAccount(prefix string) (string, string) {
	entropy, err := bip39.NewEntropy(256) // 256 bits = 24 words
	if err != nil {
		panic(err)
	}
	mnemonic, err := bip39.NewMnemonic(entropy)
	if err != nil {
		panic(err)
	}
	seed := bip39.NewSeed(mnemonic, "")
	master, ch := hd.ComputeMastersFromSeed(seed)
	keyBz, err := hd.DerivePrivateKeyForPath(master, ch, "m/44'/118'/0'/0/0")
	if err != nil {
		panic(err)
	}
	priv := &secp256k1.PrivKey{Key: keyBz}
	addr, err := bech32.ConvertAndEncode(prefix, priv.PubKey().Address().Bytes())
	if err != nil {
		panic(err)
	}
	return mnemonic, addr
}
