// (c) 2020-2021, Ava Labs, Inc.
// See the file LICENSE for licensing terms.

package evm

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/log"

	"github.com/ava-labs/avalanchego/codec"
	"github.com/ava-labs/avalanchego/database"
	"github.com/ava-labs/avalanchego/database/prefixdb"
	"github.com/ava-labs/avalanchego/database/versiondb"
	"github.com/ava-labs/avalanchego/ids"
	"github.com/ava-labs/avalanchego/utils/units"
	"github.com/ava-labs/avalanchego/utils/wrappers"
)

const (
	commitSizeCap = 10 * units.MiB
)

var (
	atomicTxIDDBPrefix     = []byte("atomicTxDB")
	atomicHeightTxDBPrefix = []byte("atomicHeightTxDB")
	maxIndexedHeightKey    = []byte("maxIndexedAtomicTxHeight")
)

// AtomicTxRepository defines an entity that manages storage and indexing of
// atomic transactions
type AtomicTxRepository interface {
	// GetIndexHeight() (uint64, bool, error)
	GetByTxID(txID ids.ID) (*Tx, uint64, error)
	GetByHeight(height uint64) ([]*Tx, error)
	Write(height uint64, txs []*Tx) error
	IterateByTxID() database.Iterator
	IterateByHeight([]byte) database.Iterator
}

// atomicTxRepository is a prefixdb implementation of the AtomicTxRepository interface
type atomicTxRepository struct {
	// [acceptedAtomicTxDB] maintains an index of [txID] => [height]+[atomic tx] for all accepted atomic txs.
	acceptedAtomicTxDB database.Database

	// [acceptedAtomicTxByHeightDB] maintains an index of [height] => [atomic txs] for all accepted block heights.
	acceptedAtomicTxByHeightDB database.Database

	// This db is used to store [maxIndexedHeightKey] to avoid interfering with the iterators over the atomic transaction DBs.
	db *versiondb.Database

	// Use this codec for serializing
	codec codec.Manager
}

func NewAtomicTxRepository(db *versiondb.Database, codec codec.Manager, lastAcceptedHeight uint64) (AtomicTxRepository, error) {
	acceptedAtomicTxDB := prefixdb.New(atomicTxIDDBPrefix, db)
	acceptedAtomicTxByHeightDB := prefixdb.New(atomicHeightTxDBPrefix, db)

	repo := &atomicTxRepository{
		acceptedAtomicTxDB:         acceptedAtomicTxDB,
		acceptedAtomicTxByHeightDB: acceptedAtomicTxByHeightDB,
		codec:                      codec,
		db:                         db,
	}
	return repo, repo.initialize(lastAcceptedHeight)
}

// initialize initializes the atomic repository and takes care of any required migration from the previous database
// format which did not have a height -> txs index.
func (a *atomicTxRepository) initialize(lastAcceptedHeight uint64) error {
	startTime := time.Now()
	lastLogTime := startTime

	// [lastTxID] will be initialized to the last transaction that we indexed
	// if we are part way through a migration.
	var lastTxID []byte
	indexHeightBytes, err := a.db.Get(maxIndexedHeightKey)
	switch {
	case err != nil && err != database.ErrNotFound: // unexpected error
		return err
	case err == database.ErrNotFound: // initializing from scratch
		break
	case len(indexHeightBytes) == wrappers.LongLen: // already initialized
		return nil
	case len(indexHeightBytes) == common.HashLength: // partially initialized
		lastTxID = indexHeightBytes
	default: // unexpected value in the database
		return fmt.Errorf("found invalid value at max indexed height: %v", indexHeightBytes)
	}

	// Iterate from [lastTxID] to complete the re-index -> generating an index
	// from height to a slice of transactions accepted at that height
	iter := a.acceptedAtomicTxDB.NewIteratorWithStart(lastTxID)
	defer iter.Release()

	if len(lastTxID) == 0 {
		log.Info("Initializing atomic transaction repository from scratch")
	} else {
		log.Info("Initializing atomic transaction repository from txID: %v", lastTxID)
	}

	indexedTxs := 0

	// Keep track of the size of the currently pending writes
	pendingBytesApproximation := 0
	for iter.Next() {
		if err := iter.Error(); err != nil {
			return fmt.Errorf("atomic tx DB iterator errored while initializing atomic trie: %w", err)
		}

		// iter.Value() consists of [height packed as uint64] + [tx serialized as packed []byte]
		iterValue := iter.Value()
		heightBytes := iterValue[:wrappers.LongLen]

		// Get the tx iter is pointing to, len(txs) == 1 is expected here.
		txBytes := iterValue[wrappers.LongLen+wrappers.IntLen:]
		tx, err := ExtractAtomicTx(txBytes, a.codec)
		if err != nil {
			return err
		}

		// Check if there are already transactions at [height], to ensure that we
		// add [txs] to the already indexed transactions at [height] instead of
		// overwriting them.
		if err := a.addTxToHeightIndex(heightBytes, tx); err != nil {
			return err
		}
		txID := tx.ID()
		lastTxID = txID[:]
		pendingBytesApproximation += len(txBytes)

		// call commitFn to write to underlying DB if we have reached
		// [commitSizeCap]
		if pendingBytesApproximation > commitSizeCap {
			if err := a.db.Put(maxIndexedHeightKey, lastTxID); err != nil {
				return err
			}
			if err := a.db.Commit(); err != nil {
				return err
			}
			log.Info("Committing work initializing the atomic repository", "lastTxID", lastTxID)
			pendingBytesApproximation = 0
		}
		indexedTxs++
		// Periodically log progress
		if time.Since(lastLogTime) > 15*time.Second {
			lastLogTime = time.Now()
			log.Info("Atomic repository initialization", "indexedTxs", indexedTxs)
		}
	}

	// Updated the value stored [maxIndexedHeightKey] to be the lastAcceptedHeight
	indexedHeight := make([]byte, wrappers.LongLen)
	binary.BigEndian.PutUint64(indexedHeight, lastAcceptedHeight)
	if err := a.db.Put(maxIndexedHeightKey, indexedHeight); err != nil {
		return err
	}

	log.Info("Completed atomic transaction repository migration", "lastAcceptedHeight", lastAcceptedHeight, "duration", time.Since(startTime))
	return a.db.Commit()
}

// GetIndexHeight returns the last height that was indexed by the atomic repository
func (a *atomicTxRepository) GetIndexHeight() (uint64, error) {
	indexHeightBytes, err := a.db.Get(maxIndexedHeightKey)
	if err != nil {
		return 0, err
	}

	if len(indexHeightBytes) != wrappers.LongLen {
		return 0, fmt.Errorf("unexpected length for indexHeightBytes %d", len(indexHeightBytes))
	}
	indexHeight := binary.BigEndian.Uint64(indexHeightBytes)
	return indexHeight, nil
}

// GetByTxID queries [acceptedAtomicTxDB] for the [txID], parses a [*Tx] object
// if an entry is found, and returns it with the block height the atomic tx it
// represents was accepted on, along with an optional error.
func (a *atomicTxRepository) GetByTxID(txID ids.ID) (*Tx, uint64, error) {
	indexedTxBytes, err := a.acceptedAtomicTxDB.Get(txID[:])
	if err != nil {
		return nil, 0, err
	}

	if len(indexedTxBytes) < wrappers.LongLen {
		return nil, 0, fmt.Errorf("acceptedAtomicTxDB entry too short: %d", len(indexedTxBytes))
	}

	// value is stored as [height]+[tx bytes], decompose with a packer.
	packer := wrappers.Packer{Bytes: indexedTxBytes}
	height := packer.UnpackLong()
	txBytes := packer.UnpackBytes()
	tx, err := ExtractAtomicTx(txBytes, a.codec)
	if err != nil {
		return nil, 0, err
	}

	return tx, height, nil
}

// GetByHeight returns all atomic txs processed on block at [height].
func (a *atomicTxRepository) GetByHeight(height uint64) ([]*Tx, error) {
	heightBytes := make([]byte, wrappers.LongLen)
	binary.BigEndian.PutUint64(heightBytes, height)

	return a.getByHeightBytes(heightBytes)
}

func (a *atomicTxRepository) getByHeightBytes(heightBytes []byte) ([]*Tx, error) {
	txsBytes, err := a.acceptedAtomicTxByHeightDB.Get(heightBytes)
	if err != nil {
		return nil, err
	}
	return ExtractAtomicTxsBatch(txsBytes, a.codec)
}

// Write updates indexes maintained on atomic txs, so they can be queried
// by txID or height. This method must be called only once per height,
// and [txs] must include all atomic txs for the block accepted at the
// corresponding height.
func (a *atomicTxRepository) Write(height uint64, txs []*Tx) error {
	heightBytes := make([]byte, wrappers.LongLen)
	binary.BigEndian.PutUint64(heightBytes, height)

	for _, tx := range txs {
		if err := a.indexTxByID(heightBytes, tx); err != nil {
			return err
		}
	}
	if err := a.indexTxsAtHeight(heightBytes, txs); err != nil {
		return err
	}

	return a.db.Put(maxIndexedHeightKey, heightBytes)
}

func (a *atomicTxRepository) indexTxByID(heightBytes []byte, tx *Tx) error {
	txBytes, err := a.codec.Marshal(codecVersion, tx)
	if err != nil {
		return err
	}

	// map txID => [height]+[tx bytes]
	heightTxPacker := wrappers.Packer{Bytes: make([]byte, wrappers.LongLen+wrappers.IntLen+len(txBytes))}
	heightTxPacker.PackFixedBytes(heightBytes)
	heightTxPacker.PackBytes(txBytes)
	txID := tx.ID()

	if err := a.acceptedAtomicTxDB.Put(txID[:], heightTxPacker.Bytes); err != nil {
		return err
	}

	return nil
}

func (a *atomicTxRepository) indexTxsAtHeight(heightBytes []byte, txs []*Tx) error {
	txsBytes, err := a.codec.Marshal(codecVersion, txs)
	if err != nil {
		return err
	}
	if err := a.acceptedAtomicTxByHeightDB.Put(heightBytes, txsBytes); err != nil {
		return err
	}
	return nil
}

func (a *atomicTxRepository) addTxToHeightIndex(heightBytes []byte, tx *Tx) error {
	txs, err := a.getByHeightBytes(heightBytes)
	if err != nil && err != database.ErrNotFound {
		return err
	}

	// Iterate over the existing transactions to ensure we do not add a
	// duplicate to the index
	for _, existingTx := range txs {
		if existingTx.ID() == tx.ID() {
			// return nil
		}
	}

	txs = append(txs, tx)
	return a.indexTxsAtHeight(heightBytes, txs)
}

func (a *atomicTxRepository) IterateByTxID() database.Iterator {
	return a.acceptedAtomicTxDB.NewIterator()
}

func (a *atomicTxRepository) IterateByHeight(heightBytes []byte) database.Iterator {
	return a.acceptedAtomicTxByHeightDB.NewIteratorWithStart(heightBytes)
}
