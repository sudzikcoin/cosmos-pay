package x402cosmos

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"cosmossdk.io/x/tx/signing"
	bip39 "github.com/cosmos/go-bip39"
	gogoproto "github.com/cosmos/gogoproto/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	cometrpc "github.com/cometbft/cometbft/rpc/client/http"

	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/cosmos/cosmos-sdk/codec/address"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/std"
	sdkclient "github.com/cosmos/cosmos-sdk/client"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/bech32"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	authtx "github.com/cosmos/cosmos-sdk/x/auth/tx"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/cosmos/cosmos-sdk/x/authz"
	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	signingtypes "github.com/cosmos/cosmos-sdk/types/tx/signing"
)

// NobleClient implements ChainClient for Noble (Cosmos SDK chain with
// native USDC where gas is paid in uusdc).
type NobleClient struct {
	chainID  string
	prefix   string
	denom    string
	gasPrice float64 // uusdc per gas

	conn      *grpc.ClientConn
	rpcClient *cometrpc.HTTP

	signer  cryptotypes.PrivKey
	address string

	cdc      codec.Codec
	txConfig sdkclient.TxConfig

	authzQC authz.QueryClient
	bankQC  banktypes.QueryClient
	authQC  authtypes.QueryClient
}

// Compile-time interface satisfaction.
var _ ChainClient = (*NobleClient)(nil)

// nobleConfig holds the per-chain knobs for the Noble client.
type nobleConfig struct {
	GRPCEndpoint string
	GRPCUseTLS   bool
	RPCEndpoint  string
	ChainID      string
	Prefix       string
	Denom        string
	GasPrice     float64
	Mnemonic     string
}

// NewNobleClient constructs a NobleClient from environment variables.
// Required: X402_FACILITATOR_MNEMONIC.
// Optional: X402_NOBLE_GRPC, X402_NOBLE_GRPC_TLS, X402_NOBLE_RPC,
// X402_NOBLE_CHAIN_ID, X402_NOBLE_PREFIX, X402_NOBLE_DENOM,
// X402_NOBLE_GAS_PRICE.
func NewNobleClient(ctx context.Context) (*NobleClient, error) {
	cfg := nobleConfig{
		GRPCEndpoint: envOr("X402_NOBLE_GRPC", "noble-testnet-grpc.polkachu.com:21590"),
		GRPCUseTLS:   os.Getenv("X402_NOBLE_GRPC_TLS") == "true",
		RPCEndpoint:  envOr("X402_NOBLE_RPC", "https://noble-testnet-rpc.polkachu.com"),
		ChainID:      envOr("X402_NOBLE_CHAIN_ID", "grand-1"),
		Prefix:       envOr("X402_NOBLE_PREFIX", "noble"),
		Denom:        envOr("X402_NOBLE_DENOM", "uusdc"),
		GasPrice:     0.1,
		Mnemonic:     os.Getenv("X402_FACILITATOR_MNEMONIC"),
	}
	if cfg.Mnemonic == "" {
		return nil, errors.New("X402_FACILITATOR_MNEMONIC is required")
	}

	// Configure the SDK's global bech32 prefix so AccAddressFromBech32
	// and AccAddress.String() agree with our chain. This is a
	// process-wide setting; if you ever wire multiple chains with
	// different prefixes into one process, this needs a redesign.
	sdkCfg := sdk.GetConfig()
	sdkCfg.SetBech32PrefixForAccount(cfg.Prefix, cfg.Prefix+"pub")
	sdkCfg.SetBech32PrefixForValidator(cfg.Prefix+"valoper", cfg.Prefix+"valoperpub")
	sdkCfg.SetBech32PrefixForConsensusNode(cfg.Prefix+"valcons", cfg.Prefix+"valconspub")

	priv, addr, err := deriveCosmosKey(cfg.Mnemonic, cfg.Prefix)
	if err != nil {
		return nil, fmt.Errorf("derive facilitator key: %w", err)
	}

	conn, err := dialGRPC(cfg.GRPCEndpoint, cfg.GRPCUseTLS)
	if err != nil {
		return nil, fmt.Errorf("dial grpc %s: %w", cfg.GRPCEndpoint, err)
	}

	rpcCli, err := cometrpc.New(cfg.RPCEndpoint, "/websocket")
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("dial rpc %s: %w", cfg.RPCEndpoint, err)
	}

	cdc, txCfg := makeCodec(cfg.Prefix)

	c := &NobleClient{
		chainID:   cfg.ChainID,
		prefix:    cfg.Prefix,
		denom:     cfg.Denom,
		gasPrice:  cfg.GasPrice,
		conn:      conn,
		rpcClient: rpcCli,
		signer:    priv,
		address:   addr,
		cdc:       cdc,
		txConfig:  txCfg,
		authzQC:   authz.NewQueryClient(conn),
		bankQC:    banktypes.NewQueryClient(conn),
		authQC:    authtypes.NewQueryClient(conn),
	}
	return c, nil
}

// Address returns the facilitator's bech32 address (grantee in
// SendAuthorization grants).
func (c *NobleClient) Address() string { return c.address }

// Close releases the gRPC connection. RPC HTTP client has no Close in
// cometbft v0.38.
func (c *NobleClient) Close() error { return c.conn.Close() }

func (c *NobleClient) ChainID() string       { return c.chainID }
func (c *NobleClient) AccountPrefix() string { return c.prefix }

// QuerySendAuthorization scans grants from granter→grantee for the first
// SendAuthorization whose SpendLimit includes `denom`. Returns the limit
// amount as a decimal string.
func (c *NobleClient) QuerySendAuthorization(ctx context.Context, granter, grantee, denom string) (string, time.Time, error) {
	resp, err := c.authzQC.Grants(ctx, &authz.QueryGrantsRequest{
		Granter:    granter,
		Grantee:    grantee,
		MsgTypeUrl: sdk.MsgTypeURL(&banktypes.MsgSend{}),
	})
	if err != nil {
		// The authz module returns a NotFound-style error when no grant
		// exists for the (granter, grantee, msg_type) triple. Map it to
		// our sentinel so the verifier can produce the right error code.
		if strings.Contains(err.Error(), "authorization not found") || strings.Contains(err.Error(), "NotFound") {
			return "", time.Time{}, ErrNoGrant
		}
		return "", time.Time{}, fmt.Errorf("%w: %v", ErrChainRPC, err)
	}

	for _, g := range resp.Grants {
		var auth authz.Authorization
		if err := c.cdc.UnpackAny(g.Authorization, &auth); err != nil {
			continue
		}
		sendAuth, ok := auth.(*banktypes.SendAuthorization)
		if !ok {
			continue
		}
		for _, coin := range sendAuth.SpendLimit {
			if coin.Denom != denom {
				continue
			}
			var exp time.Time
			if g.Expiration != nil {
				exp = *g.Expiration
			}
			return coin.Amount.String(), exp, nil
		}
	}
	return "", time.Time{}, ErrNoGrant
}

// QueryBalance reads the payer's spendable balance for `denom`.
func (c *NobleClient) QueryBalance(ctx context.Context, addr, denom string) (string, error) {
	resp, err := c.bankQC.Balance(ctx, &banktypes.QueryBalanceRequest{
		Address: addr,
		Denom:   denom,
	})
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrChainRPC, err)
	}
	if resp.Balance == nil {
		return "0", nil
	}
	return resp.Balance.Amount.String(), nil
}

// BroadcastAuthzSend builds and broadcasts MsgExec{MsgSend{from→to: amount denom}}
// signed by the facilitator. Returns the tx hash on inclusion, polling up
// to 30 seconds.
func (c *NobleClient) BroadcastAuthzSend(ctx context.Context, from, to, denom, amount string) (string, error) {
	coin, err := sdk.ParseCoinNormalized(amount + denom)
	if err != nil {
		return "", fmt.Errorf("parse coin: %w", err)
	}

	inner := &banktypes.MsgSend{
		FromAddress: from,
		ToAddress:   to,
		Amount:      sdk.NewCoins(coin),
	}
	facilitatorAcc, err := sdk.AccAddressFromBech32(c.address)
	if err != nil {
		return "", fmt.Errorf("parse facilitator addr: %w", err)
	}
	exec := authz.NewMsgExec(facilitatorAcc, []sdk.Msg{inner})

	// Query account number + sequence for the signer.
	accNum, accSeq, err := c.queryAccountNumSeq(ctx, c.address)
	if err != nil {
		return "", fmt.Errorf("query account: %w", err)
	}

	// Build the tx.
	const gasLimit = uint64(250_000)
	feeAmt := uint64(float64(gasLimit) * c.gasPrice) // 25_000 uusdc at 0.1 → ~$0.025
	txBuilder := c.txConfig.NewTxBuilder()
	if err := txBuilder.SetMsgs(&exec); err != nil {
		return "", fmt.Errorf("set msgs: %w", err)
	}
	txBuilder.SetGasLimit(gasLimit)
	txBuilder.SetFeeAmount(sdk.NewCoins(sdk.NewInt64Coin(c.denom, int64(feeAmt))))
	txBuilder.SetMemo("x402-cosmos")

	// First pass: set empty signature so signer info is populated.
	sigMode := signingtypes.SignMode_SIGN_MODE_DIRECT
	emptySig := signingtypes.SignatureV2{
		PubKey: c.signer.PubKey(),
		Data: &signingtypes.SingleSignatureData{
			SignMode:  sigMode,
			Signature: nil,
		},
		Sequence: accSeq,
	}
	if err := txBuilder.SetSignatures(emptySig); err != nil {
		return "", fmt.Errorf("set empty sig: %w", err)
	}

	// Second pass: real signature.
	signerData := authsigning.SignerData{
		ChainID:       c.chainID,
		AccountNumber: accNum,
		Sequence:      accSeq,
		PubKey:        c.signer.PubKey(),
		Address:       c.address,
	}
	signBytes, err := authsigning.GetSignBytesAdapter(
		ctx, c.txConfig.SignModeHandler(), sigMode, signerData, txBuilder.GetTx(),
	)
	if err != nil {
		return "", fmt.Errorf("get sign bytes: %w", err)
	}
	sig, err := c.signer.Sign(signBytes)
	if err != nil {
		return "", fmt.Errorf("sign: %w", err)
	}
	signedSig := signingtypes.SignatureV2{
		PubKey: c.signer.PubKey(),
		Data: &signingtypes.SingleSignatureData{
			SignMode:  sigMode,
			Signature: sig,
		},
		Sequence: accSeq,
	}
	if err := txBuilder.SetSignatures(signedSig); err != nil {
		return "", fmt.Errorf("set real sig: %w", err)
	}

	// Encode tx.
	txBytes, err := c.txConfig.TxEncoder()(txBuilder.GetTx())
	if err != nil {
		return "", fmt.Errorf("encode tx: %w", err)
	}

	// Broadcast (sync mode — returns after CheckTx).
	res, err := c.rpcClient.BroadcastTxSync(ctx, txBytes)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrChainRPC, err)
	}
	if res.Code != 0 {
		return "", fmt.Errorf("broadcast rejected: code=%d log=%q", res.Code, res.Log)
	}
	txHash := strings.ToUpper(hex.EncodeToString(res.Hash))

	// Poll for inclusion.
	if err := c.waitForTx(ctx, res.Hash, 30*time.Second); err != nil {
		return txHash, fmt.Errorf("wait for inclusion: %w", err)
	}
	return txHash, nil
}

// queryAccountNumSeq returns the (account_number, sequence) for an
// address. Unpacks the Any to BaseAccount.
func (c *NobleClient) queryAccountNumSeq(ctx context.Context, addr string) (uint64, uint64, error) {
	resp, err := c.authQC.Account(ctx, &authtypes.QueryAccountRequest{Address: addr})
	if err != nil {
		return 0, 0, err
	}
	var acc sdk.AccountI
	if err := c.cdc.UnpackAny(resp.Account, &acc); err != nil {
		return 0, 0, fmt.Errorf("unpack account: %w", err)
	}
	return acc.GetAccountNumber(), acc.GetSequence(), nil
}

// waitForTx polls Tx(hash) until the transaction is included or timeout.
func (c *NobleClient) waitForTx(ctx context.Context, hash []byte, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		// Best-effort: cometbft will return not-found until the tx lands.
		res, err := c.rpcClient.Tx(ctx, hash, false)
		if err == nil && res != nil && res.Height > 0 {
			if res.TxResult.Code != 0 {
				return fmt.Errorf("tx failed on chain: code=%d log=%q",
					res.TxResult.Code, res.TxResult.Log)
			}
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout waiting for tx %X", hash)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// ------------------------------------------------------------------
// helpers
// ------------------------------------------------------------------

// deriveCosmosKey derives a secp256k1 private key from a BIP-39 mnemonic
// using the standard Cosmos HD path (m/44'/118'/0'/0/0) and bech32-encodes
// the resulting address with `prefix`.
func deriveCosmosKey(mnemonic, prefix string) (cryptotypes.PrivKey, string, error) {
	if !bip39.IsMnemonicValid(mnemonic) {
		return nil, "", errors.New("invalid mnemonic")
	}
	seed := bip39.NewSeed(mnemonic, "")
	masterKey, chainCode := hd.ComputeMastersFromSeed(seed)
	keyBz, err := hd.DerivePrivateKeyForPath(masterKey, chainCode, "m/44'/118'/0'/0/0")
	if err != nil {
		return nil, "", fmt.Errorf("derive: %w", err)
	}
	priv := &secp256k1.PrivKey{Key: keyBz}
	addr, err := bech32.ConvertAndEncode(prefix, priv.PubKey().Address().Bytes())
	if err != nil {
		return nil, "", fmt.Errorf("bech32 encode: %w", err)
	}
	return priv, addr, nil
}

// makeCodec builds an InterfaceRegistry + ProtoCodec + TxConfig wired with
// the modules this client uses (auth, bank, authz, crypto, std).
// The signing-options' AddressCodec is bound to `prefix` so that signers
// derived from the tx will bech32-encode correctly.
func makeCodec(prefix string) (codec.Codec, sdkclient.TxConfig) {
	ir, err := codectypes.NewInterfaceRegistryWithOptions(codectypes.InterfaceRegistryOptions{
		ProtoFiles: gogoproto.HybridResolver,
		SigningOptions: signing.Options{
			AddressCodec:          address.NewBech32Codec(prefix),
			ValidatorAddressCodec: address.NewBech32Codec(prefix + "valoper"),
		},
	})
	if err != nil {
		panic(fmt.Errorf("interface registry: %w", err))
	}
	std.RegisterInterfaces(ir)
	cryptocodec.RegisterInterfaces(ir)
	authtypes.RegisterInterfaces(ir)
	banktypes.RegisterInterfaces(ir)
	authz.RegisterInterfaces(ir)

	cdc := codec.NewProtoCodec(ir)
	txCfg := authtx.NewTxConfig(cdc, authtx.DefaultSignModes)
	return cdc, txCfg
}

// dialGRPC opens a gRPC connection; uses TLS if useTLS=true, else plaintext.
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
