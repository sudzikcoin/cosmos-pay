// Command grant sends a one-time x/authz MsgGrant with a SendAuthorization
// from the payer's account (derived from a BIP-39 mnemonic) to the
// facilitator (grantee). After this grant exists on chain, the facilitator
// can pull funds up to spend_limit on the payer's behalf via x402.
//
// Usage:
//
//	go run ./tools/grant \
//	    --mnemonic "twelve words..." \
//	    --grantee noble1facilitator... \
//	    --spend-limit 1000000uusdc \
//	    --expiration 24h
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"cosmossdk.io/x/tx/signing"
	gogoproto "github.com/cosmos/gogoproto/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	cometrpc "github.com/cometbft/cometbft/rpc/client/http"

	sdkclient "github.com/cosmos/cosmos-sdk/client"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/codec/address"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/std"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/bech32"
	signingtypes "github.com/cosmos/cosmos-sdk/types/tx/signing"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	bip39 "github.com/cosmos/go-bip39"
)

func main() {
	mnemonic := flag.String("mnemonic", os.Getenv("PAYER_MNEMONIC"), "Payer BIP-39 mnemonic (or set PAYER_MNEMONIC)")
	grantee := flag.String("grantee", "", "Grantee bech32 address (facilitator)")
	spendLimit := flag.String("spend-limit", "1000000uusdc", "Spend limit, e.g. 1000000uusdc")
	expiration := flag.Duration("expiration", 24*time.Hour, "Grant expiration window from now")
	chainID := flag.String("chain-id", envOr("X402_NOBLE_CHAIN_ID", "grand-1"), "Cosmos chain-id")
	prefix := flag.String("prefix", envOr("X402_NOBLE_PREFIX", "noble"), "Bech32 prefix")
	grpcEndpoint := flag.String("grpc", envOr("X402_NOBLE_GRPC", "noble-testnet-grpc.polkachu.com:21590"), "gRPC endpoint")
	grpcTLS := flag.Bool("grpc-tls", os.Getenv("X402_NOBLE_GRPC_TLS") == "true", "Use TLS for gRPC")
	rpcEndpoint := flag.String("rpc", envOr("X402_NOBLE_RPC", "https://noble-testnet-rpc.polkachu.com"), "Tendermint RPC endpoint")
	gasDenom := flag.String("gas-denom", "uusdc", "Gas fee denom")
	flag.Parse()

	if *mnemonic == "" {
		log.Fatal("--mnemonic (or PAYER_MNEMONIC) is required")
	}
	if *grantee == "" {
		log.Fatal("--grantee is required")
	}

	if err := run(*mnemonic, *grantee, *spendLimit, *expiration, *chainID, *prefix, *grpcEndpoint, *grpcTLS, *rpcEndpoint, *gasDenom); err != nil {
		log.Fatal(err)
	}
}

func run(mnemonic, grantee, spendLimit string, expiration time.Duration, chainID, prefix, grpcEndpoint string, grpcTLS bool, rpcEndpoint, gasDenom string) error {
	ctx := context.Background()

	// Configure the SDK's global bech32 prefix so AccAddressFromBech32
	// and AccAddress.String() use the right HRP.
	cfg := sdk.GetConfig()
	cfg.SetBech32PrefixForAccount(prefix, prefix+"pub")
	cfg.SetBech32PrefixForValidator(prefix+"valoper", prefix+"valoperpub")
	cfg.SetBech32PrefixForConsensusNode(prefix+"valcons", prefix+"valconspub")

	priv, granter, err := deriveKey(mnemonic, prefix)
	if err != nil {
		return fmt.Errorf("derive payer key: %w", err)
	}
	log.Printf("payer (granter) addr: %s", granter)
	log.Printf("grantee (facilitator) addr: %s", grantee)

	coins, err := sdk.ParseCoinsNormalized(spendLimit)
	if err != nil {
		return fmt.Errorf("parse spend-limit: %w", err)
	}

	expTime := time.Now().Add(expiration)
	auth := banktypes.NewSendAuthorization(coins, nil)

	granterAcc, err := sdk.AccAddressFromBech32(granter)
	if err != nil {
		return fmt.Errorf("parse granter: %w", err)
	}
	granteeAcc, err := sdk.AccAddressFromBech32(grantee)
	if err != nil {
		return fmt.Errorf("parse grantee: %w", err)
	}

	msg, err := authz.NewMsgGrant(granterAcc, granteeAcc, auth, &expTime)
	if err != nil {
		return fmt.Errorf("build MsgGrant: %w", err)
	}

	conn, err := dialGRPC(grpcEndpoint, grpcTLS)
	if err != nil {
		return fmt.Errorf("dial grpc: %w", err)
	}
	defer conn.Close()

	rpcCli, err := cometrpc.New(rpcEndpoint, "/websocket")
	if err != nil {
		return fmt.Errorf("dial rpc: %w", err)
	}

	cdc, txCfg := makeCodec(prefix)
	authQC := authtypes.NewQueryClient(conn)

	accResp, err := authQC.Account(ctx, &authtypes.QueryAccountRequest{Address: granter})
	if err != nil {
		return fmt.Errorf("query account: %w", err)
	}
	var acc sdk.AccountI
	if err := cdc.UnpackAny(accResp.Account, &acc); err != nil {
		return fmt.Errorf("unpack account: %w", err)
	}
	accNum, accSeq := acc.GetAccountNumber(), acc.GetSequence()

	const gasLimit = uint64(200_000)
	feeAmt := int64(float64(gasLimit) * 0.1) // gasDenom @ 0.1 per gas

	txBuilder := txCfg.NewTxBuilder()
	if err := txBuilder.SetMsgs(msg); err != nil {
		return fmt.Errorf("set msg: %w", err)
	}
	txBuilder.SetGasLimit(gasLimit)
	txBuilder.SetFeeAmount(sdk.NewCoins(sdk.NewInt64Coin(gasDenom, feeAmt)))
	txBuilder.SetFeePayer(granterAcc)
	txBuilder.SetMemo("x402-cosmos grant")

	sigMode := signingtypes.SignMode_SIGN_MODE_DIRECT
	emptySig := signingtypes.SignatureV2{
		PubKey:   priv.PubKey(),
		Data:     &signingtypes.SingleSignatureData{SignMode: sigMode, Signature: nil},
		Sequence: accSeq,
	}
	if err := txBuilder.SetSignatures(emptySig); err != nil {
		return fmt.Errorf("set empty sig: %w", err)
	}
	signerData := authsigning.SignerData{
		ChainID:       chainID,
		AccountNumber: accNum,
		Sequence:      accSeq,
		PubKey:        priv.PubKey(),
		Address:       granter,
	}
	signBytes, err := authsigning.GetSignBytesAdapter(
		ctx, txCfg.SignModeHandler(), sigMode, signerData, txBuilder.GetTx(),
	)
	if err != nil {
		return fmt.Errorf("get sign bytes: %w", err)
	}
	sig, err := priv.Sign(signBytes)
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}
	signed := signingtypes.SignatureV2{
		PubKey:   priv.PubKey(),
		Data:     &signingtypes.SingleSignatureData{SignMode: sigMode, Signature: sig},
		Sequence: accSeq,
	}
	if err := txBuilder.SetSignatures(signed); err != nil {
		return fmt.Errorf("set sig: %w", err)
	}

	txBytes, err := txCfg.TxEncoder()(txBuilder.GetTx())
	if err != nil {
		return fmt.Errorf("encode tx: %w", err)
	}

	res, err := rpcCli.BroadcastTxSync(ctx, txBytes)
	if err != nil {
		return fmt.Errorf("broadcast: %w", err)
	}
	if res.Code != 0 {
		return fmt.Errorf("broadcast rejected: code=%d log=%q", res.Code, res.Log)
	}
	txHash := strings.ToUpper(hex.EncodeToString(res.Hash))
	log.Printf("tx submitted: %s", txHash)
	log.Printf("waiting for inclusion (max 30s)...")

	if err := waitForTx(ctx, rpcCli, res.Hash, 30*time.Second); err != nil {
		return fmt.Errorf("inclusion: %w", err)
	}
	log.Printf("grant confirmed on chain")
	log.Printf("explorer: https://www.mintscan.io/noble-testnet/txs/%s", txHash)
	return nil
}

func waitForTx(ctx context.Context, rpc *cometrpc.HTTP, hash []byte, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	tick := time.NewTicker(1 * time.Second)
	defer tick.Stop()
	for {
		res, err := rpc.Tx(ctx, hash, false)
		if err == nil && res != nil && res.Height > 0 {
			if res.TxResult.Code != 0 {
				return fmt.Errorf("tx failed: code=%d log=%q", res.TxResult.Code, res.TxResult.Log)
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for %X", hash)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func deriveKey(mnemonic, prefix string) (cryptotypes.PrivKey, string, error) {
	mnemonic = strings.TrimSpace(mnemonic)
	if !bip39.IsMnemonicValid(mnemonic) {
		return nil, "", fmt.Errorf("invalid mnemonic")
	}
	seed := bip39.NewSeed(mnemonic, "")
	master, ch := hd.ComputeMastersFromSeed(seed)
	keyBz, err := hd.DerivePrivateKeyForPath(master, ch, "m/44'/118'/0'/0/0")
	if err != nil {
		return nil, "", err
	}
	priv := &secp256k1.PrivKey{Key: keyBz}
	addr, err := bech32.ConvertAndEncode(prefix, priv.PubKey().Address().Bytes())
	if err != nil {
		return nil, "", err
	}
	return priv, addr, nil
}

func makeCodec(prefix string) (codec.Codec, sdkclient.TxConfig) {
	ir, err := codectypes.NewInterfaceRegistryWithOptions(codectypes.InterfaceRegistryOptions{
		ProtoFiles: gogoproto.HybridResolver,
		SigningOptions: signing.Options{
			AddressCodec:          address.NewBech32Codec(prefix),
			ValidatorAddressCodec: address.NewBech32Codec(prefix + "valoper"),
		},
	})
	if err != nil {
		panic(err)
	}
	std.RegisterInterfaces(ir)
	cryptocodec.RegisterInterfaces(ir)
	authtypes.RegisterInterfaces(ir)
	banktypes.RegisterInterfaces(ir)
	authz.RegisterInterfaces(ir)
	cdc := codec.NewProtoCodec(ir)
	return cdc, authtx.NewTxConfig(cdc, authtx.DefaultSignModes)
}

func dialGRPC(endpoint string, useTLS bool) (*grpc.ClientConn, error) {
	var creds grpc.DialOption
	if useTLS {
		creds = grpc.WithTransportCredentials(credentials.NewTLS(nil))
	} else {
		creds = grpc.WithTransportCredentials(insecure.NewCredentials())
	}
	return grpc.NewClient(endpoint, creds)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
