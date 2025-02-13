package chain_state

import (
	"encoding/binary"
	"math/big"

	"github.com/patrickmn/go-cache"

	"github.com/vitelabs/go-vite/v2/common/db/xleveldb/errors"
	"github.com/vitelabs/go-vite/v2/common/types"
	"github.com/vitelabs/go-vite/v2/interfaces"
	ledger "github.com/vitelabs/go-vite/v2/interfaces/core"
	"github.com/vitelabs/go-vite/v2/ledger/chain/utils"
)

func (sDB *StateDB) Write(block *interfaces.VmAccountBlock) error {
	batch := sDB.store.NewBatch()

	vmDb := block.VmDb
	if !vmDb.CanWrite() {
		return errors.New("vmDb.CanWrite() is false")
	}

	accountBlock := block.AccountBlock

	var redoLog LogItem

	// write unsaved storage
	unsavedStorage := vmDb.GetUnsavedStorage()

	for _, kv := range unsavedStorage {
		// set latest kv
		if len(kv[1]) <= 0 {
			batch.Delete(chain_utils.CreateStorageValueKey(&accountBlock.AccountAddress, kv[0]).Bytes())
		} else {
			batch.Put(chain_utils.CreateStorageValueKey(&accountBlock.AccountAddress, kv[0]).Bytes(), kv[1])
		}
	}

	redoLog.Storage = unsavedStorage

	// write unsaved balance
	unsavedBalanceMap := vmDb.GetUnsavedBalanceMap()
	redoLog.BalanceMap = make(map[types.TokenTypeId]*big.Int, len(unsavedBalanceMap))

	for tokenTypeId, balance := range unsavedBalanceMap {
		// set latest balance
		sDB.writeBalance(batch, chain_utils.CreateBalanceKey(accountBlock.AccountAddress, tokenTypeId).Bytes(), balance.Bytes())
		redoLog.BalanceMap[tokenTypeId] = balance
	}

	// write unsaved code
	unsavedCode := vmDb.GetUnsavedContractCode()
	if unsavedCode != nil {
		codeKey := chain_utils.CreateCodeKey(accountBlock.AccountAddress)

		batch.Put(codeKey.Bytes(), unsavedCode)

		redoLog.Code = unsavedCode
	}

	// write unsaved contract meta
	unsavedContractMeta := vmDb.GetUnsavedContractMeta()
	if len(unsavedContractMeta) > 0 {
		redoLog.ContractMeta = make(map[types.Address][]byte, len(unsavedContractMeta))
		for addr, meta := range unsavedContractMeta {
			// set create block hash
			meta.CreateBlockHash = accountBlock.Hash

			// set meta
			contractKey := chain_utils.CreateContractMetaKey(addr)
			gidContractKey := chain_utils.CreateGidContractKey(meta.Gid, &addr)

			metaBytes, _ := meta.Serialize()

			// set meta
			sDB.writeContractMeta(batch, contractKey.Bytes(), metaBytes)
			batch.Put(gidContractKey.Bytes(), nil)

			redoLog.ContractMeta[addr] = metaBytes
		}

	}

	// write vm log
	if accountBlock.LogHash != nil && sDB.canWriteVmLog(accountBlock.AccountAddress) {
		vmLogListKey := chain_utils.CreateVmLogListKey(accountBlock.LogHash)

		bytes, err := vmDb.GetLogList().Serialize()
		if err != nil {
			return err
		}
		batch.Put(vmLogListKey.Bytes(), bytes)
		redoLog.VmLogList = map[types.Hash][]byte{*accountBlock.LogHash: bytes}
	}

	// write call depth
	if accountBlock.IsReceiveBlock() && len(accountBlock.SendBlockList) > 0 {
		callDepth, err := vmDb.GetCallDepth(&accountBlock.FromBlockHash)
		if err != nil {
			return err
		}

		callDepth += 1
		callDepthBytes := make([]byte, 2)
		binary.BigEndian.PutUint16(callDepthBytes, callDepth)
		redoLog.CallDepth = make(map[types.Hash]uint16, len(accountBlock.SendBlockList))

		for _, sendBlock := range accountBlock.SendBlockList {
			redoLog.CallDepth[sendBlock.Hash] = callDepth
			batch.Put(chain_utils.CreateCallDepthKey(sendBlock.Hash).Bytes(), callDepthBytes)
		}

	}

	// add storage redo log
	redoLog.Height = accountBlock.Height

	sDB.redo.AddLog(accountBlock.AccountAddress, redoLog)

	// write batch
	sDB.store.WriteAccountBlock(batch, block.AccountBlock)

	return nil
}

func (sDB *StateDB) WriteByRedo(blockHash types.Hash, addr types.Address, redoLog LogItem) {
	batch := sDB.store.NewBatch()

	// write unsaved storage
	for _, kv := range redoLog.Storage {
		// set latest kv
		if len(kv[1]) <= 0 {
			batch.Delete(chain_utils.CreateStorageValueKey(&addr, kv[0]).Bytes())
		} else {
			batch.Put(chain_utils.CreateStorageValueKey(&addr, kv[0]).Bytes(), kv[1])
		}

	}

	// write unsaved balance
	for tokenTypeId, balance := range redoLog.BalanceMap {
		// set latest balance
		sDB.writeBalance(batch, chain_utils.CreateBalanceKey(addr, tokenTypeId).Bytes(), balance.Bytes())
	}

	// write unsaved code
	unsavedCode := redoLog.Code
	if len(unsavedCode) > 0 {
		codeKey := chain_utils.CreateCodeKey(addr)

		batch.Put(codeKey.Bytes(), unsavedCode)
	}

	// write unsaved contract meta
	unsavedContractMeta := redoLog.ContractMeta

	for addr, metaBytes := range unsavedContractMeta {
		contractKey := chain_utils.CreateContractMetaKey(addr)

		gidContractKey := make([]byte, 0, 1+types.AddressSize+types.GidSize)
		gidContractKey = append(gidContractKey, chain_utils.GidContractKeyPrefix)
		gidContractKey = append(gidContractKey, addr.Bytes()...)
		gidContractKey = append(gidContractKey, metaBytes[:types.GidSize]...)

		sDB.writeContractMeta(batch, contractKey.Bytes(), metaBytes)

		batch.Put(gidContractKey, nil)
		// set
	}

	// write vm log

	for logHash, vmLogListBytes := range redoLog.VmLogList {
		batch.Put(chain_utils.CreateVmLogListKey(&logHash).Bytes(), vmLogListBytes)
	}

	// write call depth
	callDepthBytes := make([]byte, 2)
	for sendHash, callDepth := range redoLog.CallDepth {

		binary.BigEndian.PutUint16(callDepthBytes, callDepth)
		batch.Put(chain_utils.CreateCallDepthKey(sendHash).Bytes(), callDepthBytes)

	}
	sDB.store.WriteAccountBlockByHash(batch, blockHash)
}

func (sDB *StateDB) InsertSnapshotBlock(snapshotBlock *ledger.SnapshotBlock, confirmedBlocks []*ledger.AccountBlock) error {
	height := snapshotBlock.Height

	// next snapshot
	sDB.redo.InsertSnapshotBlock(snapshotBlock, confirmedBlocks)

	// write history
	snapshotRedoLog, _, err := sDB.redo.QueryLog(height)
	if err != nil {
		return err
	}

	batch := sDB.store.NewBatch()

	if len(snapshotRedoLog) > 0 {

		redoKvMap, redoBalanceMap, err := parseRedoLog(snapshotRedoLog)
		if err != nil {
			return err
		}

		// put history storage kv
		putKeyTemplate := chain_utils.CreateHistoryStorageValueKey(&types.Address{}, []byte{}, height)

		for addr, kvMap := range redoKvMap {

			//copy(putKeyTemplate[1:1+types.AddressSize], addr.Bytes())
			putKeyTemplate.AddressRefill(addr)

			for keyStr, value := range kvMap {
				// record rollback key
				//key := []byte(keyStr)
				//copy(putKeyTemplate[1+types.AddressSize:], common.RightPadBytes(key, 32))
				//putKeyTemplate[len(putKeyTemplate)-9] = byte(len(key))
				putKeyTemplate.KeyRefill(chain_utils.StorageRealKey{}.Construct([]byte(keyStr)))

				sDB.writeHistoryKey(batch, putKeyTemplate.Bytes(), value)
			}

		}

		putBalanceTemplate := chain_utils.CreateHistoryBalanceKey(types.Address{}, types.TokenTypeId{}, height)

		for addr, balanceMap := range redoBalanceMap {
			//copy(putBalanceTemplate[1:1+types.AddressSize], addr.Bytes())
			putBalanceTemplate.AddressRefill(addr)

			for tokenTypeId, balance := range balanceMap {
				//copy(putBalanceTemplate[1+types.AddressSize:], tokenTypeId.Bytes())
				putBalanceTemplate.TokenIdRefill(tokenTypeId)

				batch.Put(putBalanceTemplate.Bytes(), balance.Bytes())
			}
		}
	}

	// write snapshot
	sDB.store.WriteSnapshot(batch, confirmedBlocks)

	// set round cache
	sDB.roundCache.InsertSnapshotBlock(snapshotBlock, snapshotRedoLog)

	return nil

}

func (sDB *StateDB) writeContractMeta(batch interfaces.Batch, key, value []byte) {
	batch.Put(key, value)

	sDB.cache.Set(contractAddrPrefix+string(key), sDB.copyValue(value), cache.NoExpiration)
}

func (sDB *StateDB) writeBalance(batch interfaces.Batch, key, value []byte) {
	batch.Put(key, value)

	sDB.cache.Set(balancePrefix+string(key), sDB.copyValue(value), cache.NoExpiration)
}

func (sDB *StateDB) writeHistoryKey(batch interfaces.Batch, key, value []byte) {
	// batch put
	batch.Put(key, value)

	// cache
	addrBytes := key[1 : 1+types.AddressSize]
	addr, err := types.BytesToAddress(addrBytes)
	if err != nil {
		panic(err)
	}
	if sDB.shouldCacheContractData(addr) {
		sDB.cache.Set(snapshotValuePrefix+string(addrBytes)+string(sDB.parseStorageKey(key)), sDB.copyValue(value), cache.NoExpiration)
	}
}

func (sDB *StateDB) canWriteVmLog(addr types.Address) bool {
	// save all vm log when sDB.vmLogAll is true
	if sDB.vmLogAll {
		return true
	}

	_, ok := sDB.vmLogWhiteListSet[addr]
	return ok
}
