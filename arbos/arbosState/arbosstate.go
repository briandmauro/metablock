//
// Copyright 2021, Offchain Labs, Inc. All rights reserved.
//

package arbosState

import (
	"math/big"
	"sync"

	"github.com/offchainlabs/arbstate/arbos/addressSet"

	"github.com/offchainlabs/arbstate/arbos/addressTable"
	"github.com/offchainlabs/arbstate/arbos/l1pricing"
	"github.com/offchainlabs/arbstate/arbos/merkleAccumulator"
	"github.com/offchainlabs/arbstate/arbos/retryables"
	"github.com/offchainlabs/arbstate/arbos/storage"
	"github.com/offchainlabs/arbstate/arbos/util"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
)

// ArbosState contains ArbOS-related state. It is backed by ArbOS's storage in the persistent stateDB.
// An ArbosState object is a cache of data that is stored permanently in a StateDB. We ensure that there is
// at most one ArbosState per StateDB so that caches don't become inconsistent.
//
// Portions of the ArbosState are lazily initialized, so that accesses that only touch some parts are
// efficient.
//
// Modifications to the ArbosState are written through to the underlying StateDB so that the StateDB always
// has the definitive state, stored persistently. (Note that some tests use memory-backed StateDB's that aren't
// persisted beyond the end of the test.)

type ArbosState struct {
	formatVersion  uint64
	gasPool        *storage.StorageBackedInt64
	smallGasPool   *storage.StorageBackedInt64
	gasPriceWei    *storage.StorageBackedBigInt
	maxGasPriceWei *storage.StorageBackedBigInt
	l1PricingState *l1pricing.L1PricingState
	retryableState *retryables.RetryableState //TODO: make this cache-free
	addressTable   *addressTable.AddressTable
	chainOwners    *addressSet.AddressSet
	sendMerkle     *merkleAccumulator.MerkleAccumulator
	timestamp      *storage.StorageBackedUint64
	backingStorage *storage.Storage
}

var openArbosStatesMutex sync.Mutex
var openArbosStates map[vm.StateDB]*ArbosState

func OpenArbosState(stateDB vm.StateDB) *ArbosState {
	openArbosStatesMutex.Lock()
	defer openArbosStatesMutex.Unlock()

	if openArbosStates == nil {
		openArbosStates = make(map[vm.StateDB]*ArbosState)
	}
	if state, exists := openArbosStates[stateDB]; exists {
		return state
	}

	backingStorage := storage.NewGeth(stateDB)

	for tryStorageUpgrade(backingStorage) {
	}

	ret := &ArbosState{
		backingStorage.GetByUint64(uint64(versionKey)).Big().Uint64(),
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		backingStorage.OpenStorageBackedUint64(util.UintToHash(uint64(timestampKey))),
		backingStorage,
	}
	openArbosStates[stateDB] = ret
	return ret
}

// See if we should upgrade the storage format. The format version is stored at location zero of our backing storage.
//
// If we know how to upgrade from the observed storage version, we do so. We return true iff we upgraded.
// It might be that yet another upgrade is possible, so if this returns true, the caller should call this again.
//
// If the latest version is N, then there must always be code to upgrade from every version less than N. Each
// such upgrade must increase the version number by at least 1, so that a sequence of upgrades will converge on
// the latest version.
//
// Because uninitialized storage space always returns 0 for all reads, we define format version 0 to mean that the
// storage space is uninitialized. This uninitialized version 0 will cause an upgrade to version 1, which will
// initialize the storage.
//
// During early development we sometimes change the definition of version 1, for convenience. But as soon as we
// start running long-lived chains, every change to the format will require defining a new version and providing
// upgrade code.
func tryStorageUpgrade(backingStorage *storage.Storage) bool {
	formatVersion := backingStorage.GetByUint64(uint64(versionKey)).Big().Uint64()
	switch formatVersion {
	case 0:
		upgrade_0_to_1(backingStorage)
		return true
	default:
		return false
	}
}

// Don't change the positions of items in the following const block, because they are part of the storage format
//     definition that ArbOS uses, so changes would break format compatibility.
type ArbosStateOffset int64

const (
	versionKey ArbosStateOffset = iota
	gasPoolKey
	smallGasPoolKey
	gasPriceKey
	maxPriceKey
	timestampKey
)

type ArbosStateSubspaceID []byte

var (
	l1PricingSubspace    ArbosStateSubspaceID = []byte{0}
	retryablesSubspace   ArbosStateSubspaceID = []byte{1}
	addressTableSubspace ArbosStateSubspaceID = []byte{2}
	chainOwnerSubspace   ArbosStateSubspaceID = []byte{3}
	sendMerkleSubspace   ArbosStateSubspaceID = []byte{4}
)

func upgrade_0_to_1(backingStorage *storage.Storage) {
	backingStorage.SetByUint64(uint64(versionKey), util.UintToHash(1))
	backingStorage.SetByUint64(uint64(gasPoolKey), util.IntToHash(GasPoolMax))
	backingStorage.SetByUint64(uint64(smallGasPoolKey), util.IntToHash(SmallGasPoolMax))
	backingStorage.SetByUint64(uint64(gasPriceKey), util.UintToHash(InitialGasPriceWei))
	backingStorage.SetByUint64(uint64(maxPriceKey), util.UintToHash(2*InitialGasPriceWei))
	backingStorage.SetByUint64(uint64(timestampKey), util.UintToHash(0))
	l1pricing.InitializeL1PricingState(backingStorage.OpenSubStorage(l1PricingSubspace))
	retryables.InitializeRetryableState(backingStorage.OpenSubStorage(retryablesSubspace))
	addressTable.Initialize(backingStorage.OpenSubStorage(addressTableSubspace))
	merkleAccumulator.InitializeMerkleAccumulator(backingStorage.OpenSubStorage(sendMerkleSubspace))

	// the zero address is the initial chain owner
	ZeroAddressL2 := util.RemapL1Address(common.Address{})
	ownersStorage := backingStorage.OpenSubStorage(chainOwnerSubspace)
	addressSet.Initialize(ownersStorage)
	addressSet.OpenAddressSet(ownersStorage).Add(ZeroAddressL2)

	backingStorage.SetByUint64(uint64(versionKey), util.UintToHash(1))
}

func (state *ArbosState) BackingStorage() *storage.Storage {
	return state.backingStorage
}

func (state *ArbosState) FormatVersion() uint64 {
	return state.formatVersion
}

func (state *ArbosState) SetFormatVersion(val uint64) {
	state.formatVersion = val
	state.backingStorage.SetByUint64(uint64(versionKey), util.UintToHash(val))
}

func (state *ArbosState) GasPool() int64 {
	if state.gasPool == nil {
		state.gasPool = state.backingStorage.OpenStorageBackedInt64(util.UintToHash(uint64(gasPoolKey)))
	}
	return state.gasPool.Get()
}

func (state *ArbosState) SetGasPool(val int64) {
	if state.gasPool == nil {
		state.gasPool = state.backingStorage.OpenStorageBackedInt64(util.UintToHash(uint64(gasPoolKey)))
	}
	state.gasPool.Set(val)
}

func (state *ArbosState) SmallGasPool() int64 {
	if state.smallGasPool == nil {
		state.smallGasPool = state.backingStorage.OpenStorageBackedInt64(util.UintToHash(uint64(smallGasPoolKey)))
	}
	return state.smallGasPool.Get()
}

func (state *ArbosState) SetSmallGasPool(val int64) {
	if state.smallGasPool == nil {
		state.smallGasPool = state.backingStorage.OpenStorageBackedInt64(util.UintToHash(uint64(smallGasPoolKey)))
	}
	state.smallGasPool.Set(val)
}

func (state *ArbosState) GasPriceWei() *storage.StorageBackedBigInt {
	return state.backingStorage.OpenStorageBackedBigInt(util.UintToHash(uint64(gasPriceKey)))
}

func (state *ArbosState) MaxGasPriceWei() *storage.StorageBackedBigInt { // the max gas price ArbOS can set without breaking geth
	return state.backingStorage.OpenStorageBackedBigInt(util.UintToHash(uint64(maxPriceKey)))
}

func (state *ArbosState) SetMaxGasPriceWei(val *big.Int) {
	state.backingStorage.SetByUint64(uint64(maxPriceKey), common.BigToHash(val))
}

func (state *ArbosState) RetryableState() *retryables.RetryableState {
	if state.retryableState == nil {
		state.retryableState = retryables.OpenRetryableState(state.backingStorage.OpenSubStorage(retryablesSubspace))
	}
	return state.retryableState
}

func (state *ArbosState) L1PricingState() *l1pricing.L1PricingState {
	if state.l1PricingState == nil {
		state.l1PricingState = l1pricing.OpenL1PricingState(state.backingStorage.OpenSubStorage(l1PricingSubspace))
	}
	return state.l1PricingState
}

func (state *ArbosState) AddressTable() *addressTable.AddressTable {
	if state.addressTable == nil {
		state.addressTable = addressTable.Open(state.backingStorage.OpenSubStorage(addressTableSubspace))
	}
	return state.addressTable
}

func (state *ArbosState) ChainOwners() *addressSet.AddressSet {
	if state.chainOwners == nil {
		state.chainOwners = addressSet.OpenAddressSet(state.backingStorage.OpenSubStorage(chainOwnerSubspace))
	}
	return state.chainOwners
}

func (state *ArbosState) SendMerkleAccumulator() *merkleAccumulator.MerkleAccumulator {
	if state.sendMerkle == nil {
		state.sendMerkle = merkleAccumulator.OpenMerkleAccumulator(state.backingStorage.OpenSubStorage(sendMerkleSubspace))
	}
	return state.sendMerkle
}

func (state *ArbosState) LastTimestampSeen() uint64 {
	return state.timestamp.Get()
}

func (state *ArbosState) SetLastTimestampSeen(val uint64) {
	ts := state.timestamp.Get()
	if val < ts {
		panic("timestamp decreased")
	}
	if val > ts {
		delta := val - ts
		state.timestamp.Set(val)
		state.NotifyGasPricerThatTimeElapsed(delta)
	}
}
