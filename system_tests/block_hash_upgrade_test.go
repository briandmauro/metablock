// Copyright 2021-2022, Offchain Labs, Inc.
// For license information, see https://github.com/nitro/blob/master/LICENSE

package arbtest

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/offchainlabs/nitro/arbnode"
	"github.com/offchainlabs/nitro/arbutil"
	"github.com/offchainlabs/nitro/solgen/go/mocksgen"
	"github.com/offchainlabs/nitro/solgen/go/precompilesgen"
)

func TestUpgradeBlockHash(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	chainConfig := *params.ArbitrumTestnetChainConfig()
	ownerKey, err := crypto.GenerateKey()
	Require(t, err)
	auth, err := bind.NewKeyedTransactorWithChainID(ownerKey, chainConfig.ChainID)
	Require(t, err)
	chainConfig.ArbitrumChainParams.InitialChainOwner = auth.From

	l2info, _, l2client, _, _, _, stack := CreateTestNodeOnL1WithConfig(t, ctx, true, arbnode.ConfigDefaultL1Test(), &chainConfig)
	defer stack.Close()

	l2info.SetFullAccountInfo("RealOwner", &AccountInfo{
		Address:    auth.From,
		PrivateKey: ownerKey,
		Nonce:      0,
	})
	TransferBalance(t, "Faucet", "RealOwner", big.NewInt(5*params.Ether), l2info, l2client, ctx)

	_, _, simple, err := mocksgen.DeploySimple(auth, l2client)
	Require(t, err)

	_, err = simple.CheckBlockHashes(&bind.CallOpts{Context: ctx})
	if err == nil {
		Fail(t, "CheckBlockHashes succeeded pre-upgrade")
	}

	arbOwner, err := precompilesgen.NewArbOwner(common.HexToAddress("0x70"), l2client)
	Require(t, err)

	tx, err := arbOwner.ScheduleArbOSUpgrade(auth, 2, 0)
	Require(t, err)
	_, err = arbutil.WaitForTx(ctx, l2client, tx.Hash(), time.Second*5)
	Require(t, err)

	TransferBalance(t, "Faucet", "Faucet", common.Big0, l2info, l2client, ctx)

	_, err = simple.CheckBlockHashes(&bind.CallOpts{Context: ctx})
	Require(t, err)
}
