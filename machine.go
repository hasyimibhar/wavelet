package wavelet

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"github.com/heptio/workgroup"
	"github.com/perlin-network/noise/identity"
	"github.com/perlin-network/noise/protocol"
	"github.com/perlin-network/noise/signature/eddsa"
	"github.com/perlin-network/wavelet/avl"
	"github.com/perlin-network/wavelet/common"
	"github.com/perlin-network/wavelet/log"
	"github.com/perlin-network/wavelet/store"
	"github.com/perlin-network/wavelet/sys"
	"github.com/phf/go-queue/queue"
	"github.com/pkg/errors"
	"golang.org/x/crypto/blake2b"
	"runtime"
	"sort"
	"sync"
	"time"
)

const (
	SyncChunkSize = 1048576
)

type EventBroadcast struct {
	Tag       byte
	Payload   []byte
	Creator   common.AccountID
	Signature common.Signature

	Result chan Transaction
	Error  chan error
}

type EventIncomingGossip struct {
	TX Transaction

	Vote chan error
}

type VoteGossip struct {
	Voter common.AccountID
	Ok    bool
}

type EventGossip struct {
	TX Transaction

	Result chan []VoteGossip
	Error  chan error
}

type EventIncomingQuery struct {
	TX Transaction

	Response chan *Transaction
	Error    chan error
}

type VoteQuery struct {
	Voter     common.AccountID
	Preferred Transaction
}

type EventQuery struct {
	TX Transaction

	Result chan []VoteQuery
	Error  chan error
}

type VoteOutOfSync struct {
	Voter common.AccountID
	Root  Transaction
}

type EventOutOfSyncCheck struct {
	Root Transaction

	Result chan []VoteOutOfSync
	Error  chan error
}

type EventIncomingOutOfSyncCheck struct {
	Root Transaction

	Response chan *Transaction
}

type SyncInitMetadata struct {
	User        protocol.ID
	ViewID      uint64
	ChunkHashes [][blake2b.Size256]byte
}

type EventSyncInit struct {
	ViewID uint64

	Result chan []SyncInitMetadata
	Error  chan error
}

type EventIncomingSyncInit struct {
	ViewID uint64

	Response chan SyncInitMetadata
}

type ChunkSource struct {
	Hash  [blake2b.Size256]byte
	Peers []protocol.ID
}

type EventSyncDiff struct {
	Sources []ChunkSource

	Result chan [][]byte
	Error  chan error
}

type EventIncomingSyncDiff struct {
	ChunkHash [blake2b.Size256]byte

	Response chan []byte
}

type Ledger struct {
	keys identity.Keypair
	kv   store.KV

	v *graph
	a *accounts

	cr *Snowball
	sr *Snowball

	processors map[byte]TransactionProcessor

	BroadcastQueue chan<- EventBroadcast
	broadcastQueue <-chan EventBroadcast

	GossipIn chan<- EventIncomingGossip
	gossipIn <-chan EventIncomingGossip

	GossipOut <-chan EventGossip
	gossipOut chan<- EventGossip

	QueryIn chan<- EventIncomingQuery
	queryIn <-chan EventIncomingQuery

	QueryOut <-chan EventQuery
	queryOut chan<- EventQuery

	OutOfSyncIn chan<- EventIncomingOutOfSyncCheck
	outOfSyncIn <-chan EventIncomingOutOfSyncCheck

	OutOfSyncOut <-chan EventOutOfSyncCheck
	outOfSyncOut chan<- EventOutOfSyncCheck

	SyncInitIn chan<- EventIncomingSyncInit
	syncInitIn <-chan EventIncomingSyncInit

	SyncInitOut <-chan EventSyncInit
	syncInitOut chan<- EventSyncInit

	SyncDiffIn chan<- EventIncomingSyncDiff
	syncDiffIn <-chan EventIncomingSyncDiff

	SyncDiffOut <-chan EventSyncDiff
	syncDiffOut chan<- EventSyncDiff

	cacheChunk *lru

	kill chan struct{}
}

func NewLedger(keys identity.Keypair, kv store.KV) *Ledger {
	broadcastQueue := make(chan EventBroadcast, 1024)

	gossipIn := make(chan EventIncomingGossip, 128)
	gossipOut := make(chan EventGossip, 128)

	queryIn := make(chan EventIncomingQuery, 128)
	queryOut := make(chan EventQuery, 128)

	outOfSyncIn := make(chan EventIncomingOutOfSyncCheck, 16)
	outOfSyncOut := make(chan EventOutOfSyncCheck, 16)

	syncInitIn := make(chan EventIncomingSyncInit, 16)
	syncInitOut := make(chan EventSyncInit, 16)

	syncDiffIn := make(chan EventIncomingSyncDiff, 128)
	syncDiffOut := make(chan EventSyncDiff, 128)

	accounts := newAccounts(kv)

	genesis, err := performInception(accounts.tree, nil)
	if err != nil {
		panic(err)
	}

	err = accounts.commit(nil)
	if err != nil {
		panic(err)
	}

	view := newGraph(kv, genesis)

	return &Ledger{
		keys: keys,
		kv:   kv,

		v: view,
		a: accounts,

		cr: NewSnowball().WithK(sys.SnowballQueryK).WithAlpha(sys.SnowballQueryAlpha).WithBeta(sys.SnowballQueryBeta),
		sr: NewSnowball().WithK(sys.SnowballSyncK).WithAlpha(sys.SnowballSyncAlpha).WithBeta(sys.SnowballSyncBeta),

		processors: map[byte]TransactionProcessor{
			sys.TagNop:      ProcessNopTransaction,
			sys.TagTransfer: ProcessTransferTransaction,
			sys.TagContract: ProcessContractTransaction,
			sys.TagStake:    ProcessStakeTransaction,
		},

		BroadcastQueue: broadcastQueue,
		broadcastQueue: broadcastQueue,

		GossipIn: gossipIn,
		gossipIn: gossipIn,

		GossipOut: gossipOut,
		gossipOut: gossipOut,

		QueryIn: queryIn,
		queryIn: queryIn,

		QueryOut: queryOut,
		queryOut: queryOut,

		OutOfSyncIn: outOfSyncIn,
		outOfSyncIn: outOfSyncIn,

		OutOfSyncOut: outOfSyncOut,
		outOfSyncOut: outOfSyncOut,

		SyncInitIn: syncInitIn,
		syncInitIn: syncInitIn,

		SyncInitOut: syncInitOut,
		syncInitOut: syncInitOut,

		SyncDiffIn: syncDiffIn,
		syncDiffIn: syncDiffIn,

		SyncDiffOut: syncDiffOut,
		syncDiffOut: syncDiffOut,

		cacheChunk: newLRU(1024), // 1024 * 4MB

		kill: make(chan struct{}),
	}
}

/** BEGIN EXPORTED METHODS **/

func NewTransaction(sender identity.Keypair, tag byte, payload []byte) (Transaction, error) {
	tx := Transaction{Tag: tag, Payload: payload}

	signature, err := eddsa.Sign(sender.PrivateKey(), append([]byte{tx.Tag}, tx.Payload...))
	if err != nil {
		return tx, err
	}

	copy(tx.Creator[:], sender.PublicKey())
	copy(tx.CreatorSignature[:], signature)

	return tx, nil
}

func (l *Ledger) ViewID() uint64 {
	return l.v.loadViewID()
}

func (l *Ledger) Difficulty() uint64 {
	return l.v.loadDifficulty()
}

func (l *Ledger) Root() *Transaction {
	return l.v.loadRoot()
}

func (l *Ledger) Height() uint64 {
	return l.v.height.Load()
}

func (l *Ledger) Snapshot() *avl.Tree {
	return l.a.snapshot()
}

func (l *Ledger) FindTransaction(id common.TransactionID) (*Transaction, bool) {
	return l.v.lookupTransaction(id)
}

func (l *Ledger) ListTransactions(offset, limit uint64, sender, creator common.AccountID) (transactions []*Transaction) {
	l.v.Lock()

	for _, tx := range l.v.transactions {
		if (sender == common.ZeroAccountID && creator == common.ZeroAccountID) || (sender != common.ZeroAccountID && tx.Sender == sender) || (creator != common.ZeroAccountID && tx.Creator == creator) {
			transactions = append(transactions, tx)
		}
	}

	l.v.Unlock()

	sort.Slice(transactions, func(i, j int) bool {
		return transactions[i].depth < transactions[j].depth
	})

	if offset != 0 || limit != 0 {
		if offset >= limit || offset >= uint64(len(transactions)) {
			return nil
		}

		if offset+limit > uint64(len(transactions)) {
			limit = uint64(len(transactions)) - offset
		}

		transactions = transactions[offset : offset+limit]
	}

	return
}

/** END EXPORTED METHODS **/

func (l *Ledger) attachSenderToTransaction(tx Transaction) (Transaction, error) {
	copy(tx.Sender[:], l.keys.PublicKey())

	if tx.ParentIDs = l.v.findEligibleParents(); len(tx.ParentIDs) == 0 {
		return tx, errors.New("no eligible parents available, please try again")
	}

	sort.Slice(tx.ParentIDs, func(i, j int) bool {
		return bytes.Compare(tx.ParentIDs[i][:], tx.ParentIDs[j][:]) < 0
	})

	tx.Timestamp = uint64(time.Duration(time.Now().UnixNano()) / time.Millisecond)

	for _, parentID := range tx.ParentIDs {
		if parent, exists := l.v.lookupTransaction(parentID); exists && tx.Timestamp <= parent.Timestamp {
			tx.Timestamp = parent.Timestamp + 1
		}
	}

	tx.ViewID = l.v.loadViewID()

	if tx.IsCritical(l.v.loadDifficulty()) {
		snapshot, err := l.collapseTransactions(tx, false)
		if err != nil {
			return tx, errors.Wrap(err, "unable to collapse ancestry to create critical transaction")
		}

		tx.AccountsMerkleRoot = snapshot.Checksum()

		root := l.v.loadRoot()

		tx.DifficultyTimestamps = append(root.DifficultyTimestamps, root.Timestamp)

		if size := computeCriticalTimestampWindowSize(tx.ViewID); len(tx.DifficultyTimestamps) > size {
			tx.DifficultyTimestamps = tx.DifficultyTimestamps[len(tx.DifficultyTimestamps)-size:]
		}
	}

	senderSignature, err := eddsa.Sign(l.keys.PrivateKey(), tx.Write())
	if err != nil {
		return tx, errors.Wrap(err, "failed to make sender signature")
	}

	copy(tx.SenderSignature[:], senderSignature)

	tx.rehash()

	return tx, nil
}

func (l *Ledger) addTransaction(tx Transaction) error {
	if err := AssertInView(l.v, tx); err != nil {
		return err
	}

	if err := AssertValidTransaction(tx); err != nil {
		return err
	}

	if err := AssertValidAncestry(l.v, tx); err != nil {
		return err
	}

	critical := tx.IsCritical(l.v.loadDifficulty())

	if critical {
		if err := l.assertCollapsible(tx); err != nil {
			return err
		}
	}

	if err := l.v.addTransaction(&tx); err != nil && errors.Cause(err) != ErrTxAlreadyExists {
		return errors.Wrap(err, "got an error adding queried transaction to view-graph")
	}

	if critical && l.cr.Preferred() == nil && tx.ID != l.v.loadRoot().ID {
		l.cr.Prefer(tx)
	}

	return nil
}

// collapseTransactions takes all transactions recorded in the graph view so far, and
// applies all valid ones to a snapshot of all accounts stored in the ledger.
//
// It returns an updated accounts snapshot after applying all finalized transactions.
func (l *Ledger) collapseTransactions(tx Transaction, logging bool) (*avl.Tree, error) {
	root := l.v.loadRoot()

	ss := l.a.snapshot()
	ss.SetViewID(root.ViewID + 1)

	visited := make(map[common.TransactionID]struct{})
	visited[root.ID] = struct{}{}

	q := queue.New()

	for _, parentID := range tx.ParentIDs {
		if parent, exists := l.v.lookupTransaction(parentID); exists {
			q.PushBack(parent)
		} else {
			return ss, errors.New("not all ancestry of tx provided are available")
		}

		visited[parentID] = struct{}{}
	}

	applyQueue := queue.New()

	for q.Len() > 0 {
		popped := q.PopFront().(*Transaction)

		for _, parentID := range popped.ParentIDs {
			if _, seen := visited[parentID]; !seen {
				if parent, exists := l.v.lookupTransaction(parentID); exists {
					q.PushBack(parent)
				} else {
					return ss, errors.New("not all ancestry of tx provided are available")
				}

				visited[parentID] = struct{}{}
			}
		}

		applyQueue.PushBack(popped)
	}

	// Apply transactions in reverse order from the root of the view-graph all
	// the way up to the newly created critical transaction.
	for applyQueue.Len() > 0 {
		popped := applyQueue.PopBack().(*Transaction)

		// If any errors occur while applying our transaction to our accounts
		// snapshot, silently log it and continue applying other transactions.
		if err := l.applyTransactionToSnapshot(ss, popped); err != nil {
			if logging {
				logger := log.TX(popped.ID, popped.Sender, popped.Creator, popped.ParentIDs, popped.Tag, popped.Payload, "failed")
				logger.Log().Err(err).Msg("Failed to apply transaction to the ledger.")
			}
		} else {
			if logging {
				logger := log.TX(popped.ID, popped.Sender, popped.Creator, popped.ParentIDs, popped.Tag, popped.Payload, "applied")
				logger.Log().Msg("Successfully applied transaction to the ledger.")
			}
		}

		if err := l.rewardValidators(ss, popped); err != nil {
			if logging {
				logger := log.Node()
				logger.Warn().Err(err).Msg("Failed to reward a validator while collapsing down transactions.")
			}
		}
	}

	return ss, nil
}

func (l *Ledger) applyTransactionToSnapshot(ss *avl.Tree, tx *Transaction) error {
	ctx := newTransactionContext(ss, tx)

	err := ctx.apply(l.processors)
	if err != nil {
		return errors.Wrap(err, "wavelet: could not apply transaction to snapshot")
	}

	return nil
}

func (l *Ledger) rewardValidators(ss *avl.Tree, tx *Transaction) error {
	var candidates []*Transaction
	var stakes []uint64
	var totalStake uint64

	visited := make(map[common.AccountID]struct{})
	q := queue.New()

	for _, parentID := range tx.ParentIDs {
		if parent, exists := l.v.lookupTransaction(parentID); exists {
			q.PushBack(parent)
		}

		visited[parentID] = struct{}{}
	}

	// Ignore error; should be impossible as not using HMAC mode.
	hasher, _ := blake2b.New256(nil)

	for q.Len() > 0 {
		popped := q.PopFront().(*Transaction)

		// If we exceed the max eligible depth we search for candidate
		// validators to reward from, stop traversing.
		if popped.depth+sys.MaxEligibleParentsDepthDiff < tx.depth {
			continue
		}

		// Filter for all ancestral transactions not from the same sender,
		// and within the desired graph depth.
		if popped.Sender != tx.Sender {
			stake, _ := ReadAccountStake(ss, popped.Sender)

			if stake > sys.MinimumStake {
				candidates = append(candidates, popped)
				stakes = append(stakes, stake)

				totalStake += stake

				// Record entropy source.
				_, err := hasher.Write(popped.ID[:])
				if err != nil {
					return errors.Wrap(err, "stake: failed to hash transaction ID for entropy src")
				}
			}
		}

		for _, parentID := range popped.ParentIDs {
			if _, seen := visited[parentID]; !seen {
				if parent, exists := l.v.lookupTransaction(parentID); exists {
					q.PushBack(parent)
				}

				visited[parentID] = struct{}{}
			}
		}
	}

	// If there are no eligible rewardee candidates, do not reward anyone.
	if len(candidates) == 0 || len(candidates) != len(stakes) || totalStake == 0 {
		return nil
	}

	entropy := hasher.Sum(nil)
	acc, threshold := float64(0), float64(binary.LittleEndian.Uint64(entropy)%uint64(0xffff))/float64(0xffff)

	var rewardee *Transaction

	// Model a weighted uniform distribution by a random variable X, and select
	// whichever validator has a weight X ≥ X' as a reward recipient.
	for i, tx := range candidates {
		acc += float64(stakes[i]) / float64(totalStake)

		if acc >= threshold {
			rewardee = tx
			break
		}
	}

	// If there is no selected transaction that deserves a reward, give the
	// reward to the last reward candidate.
	if rewardee == nil {
		rewardee = candidates[len(candidates)-1]
	}

	senderBalance, _ := ReadAccountBalance(ss, tx.Sender)
	recipientBalance, _ := ReadAccountBalance(ss, rewardee.Sender)

	deducted := sys.TransactionFeeAmount

	if senderBalance < deducted {
		return errors.Errorf("stake: sender %x does not have enough PERLs to pay transaction fees (requested %d PERLs) to %x", tx.Sender, deducted, rewardee.Sender)
	}

	WriteAccountBalance(ss, tx.Sender, senderBalance-deducted)
	WriteAccountBalance(ss, rewardee.Sender, recipientBalance+deducted)

	logger := log.Stake("reward_validator")
	logger.Log().
		Hex("sender", tx.Sender[:]).
		Hex("recipient", rewardee.Sender[:]).
		Hex("sender_tx_id", tx.ID[:]).
		Hex("rewardee_tx_id", rewardee.ID[:]).
		Hex("entropy", entropy).
		Float64("acc", acc).
		Float64("threshold", threshold).Msg("Rewarded validator.")

	return nil
}

func (l *Ledger) assertCollapsible(tx Transaction) error {
	snapshot, err := l.collapseTransactions(tx, false)
	if err != nil {
		return err
	}

	if snapshot.Checksum() != tx.AccountsMerkleRoot {
		return errors.Errorf("collapsing down tranasction %x's ancestry gives an accounts checksum of %x, but the transaction has %x recorded as an accounts checksum instead", tx.ID, snapshot.Checksum(), tx.AccountsMerkleRoot)
	}

	return nil
}

func Run(l *Ledger) {
	initial := gossiping(l)

	for state := initial; state != nil; {
		state = state(l)
	}
}

type transition func(*Ledger) transition

func gossiping(l *Ledger) transition {
	fmt.Println("NOW GOSSIPING")
	var g workgroup.Group

	for i := 0; i < runtime.NumCPU(); i++ {
		g.Add(continuously(gossip(l)))
		g.Add(continuously(listenForGossip(l)))
	}

	g.Add(continuously(checkIfOutOfSync(l)))
	g.Add(continuously(listenForOutOfSyncChecks(l)))
	g.Add(continuously(listenForSyncInits(l)))
	g.Add(continuously(listenForSyncDiffChunks(l)))

	if err := g.Run(); err != nil {
		switch errors.Cause(err) {
		case ErrPreferredSelected:
			return querying
		case ErrOutOfSync:
			return syncing
		default:
			fmt.Println(err)
		}
	}

	return nil
}

type stateQuerying struct {
	resetOnce sync.Once
}

func querying(l *Ledger) transition {
	fmt.Println("NOW QUERYING")

	state := new(stateQuerying)

	var g workgroup.Group

	for i := 0; i < 1; i++ {
		g.Add(continuously(query(l, state)))
		g.Add(continuously(listenForQueries(l)))
	}

	g.Add(continuously(checkIfOutOfSync(l)))
	g.Add(continuously(listenForOutOfSyncChecks(l)))
	g.Add(continuously(listenForSyncInits(l)))
	g.Add(continuously(listenForSyncDiffChunks(l)))

	defer func() {
		num := len(l.QueryOut)

		for i := 0; i < num; i++ {
			<-l.QueryOut
		}
	}()

	if err := g.Run(); err != nil {
		switch errors.Cause(err) {
		case ErrConsensusRoundFinished:
			return gossiping
		case ErrOutOfSync:
			return syncing
		default:
			fmt.Println(err)
		}
	}

	return nil
}

func syncing(l *Ledger) transition {
	fmt.Println("NOW SYNCING")
	var g workgroup.Group

	root := l.sr.Preferred()
	l.sr.Reset()

	g.Add(syncUp(l, *root))

	if err := g.Run(); err != nil {
		switch errors.Cause(err) {
		case ErrSyncFailed:
			fmt.Println("failed to sync:", err)
		default:
			fmt.Println(err)
		}
	}

	return gossiping
}

func continuously(fn func(stop <-chan struct{}) error) func(stop <-chan struct{}) error {
	return func(stop <-chan struct{}) error {
		for {
			select {
			case <-stop:
				return nil
			default:
			}

			if err := fn(stop); err != nil {
				switch errors.Cause(err) {
				case ErrTimeout:
				case ErrStopped:
					return nil
				default:
					return err
				}
			}
		}
	}
}

var (
	ErrStopped = errors.New("worker stopped")
	ErrTimeout = errors.New("worker timed out")

	ErrPreferredSelected      = errors.New("attempting to finalize consensus round")
	ErrConsensusRoundFinished = errors.New("consensus round finalized")

	ErrOutOfSync  = errors.New("need to sync up with peers")
	ErrSyncFailed = errors.New("sync failed")
)

func gossip(l *Ledger) func(stop <-chan struct{}) error {
	var broadcastNops bool

	return func(stop <-chan struct{}) error {
		snapshot := l.a.snapshot()

		var tx Transaction
		var err error

		var Result chan<- Transaction
		var Error chan<- error

		select {
		case <-l.kill:
			return ErrStopped
		case <-stop:
			return ErrStopped
		case item := <-l.broadcastQueue:
			tx = Transaction{
				Tag:              item.Tag,
				Payload:          item.Payload,
				Creator:          item.Creator,
				CreatorSignature: item.Signature,
			}

			Result = item.Result
			Error = item.Error

			defer close(Result)
			defer close(Error)
		default:
			if !broadcastNops {
				time.Sleep(100 * time.Millisecond)
				return nil
			}

			// Check if we have enough money available to create and broadcast a nop transaction.
			var self common.AccountID
			copy(self[:], l.keys.PublicKey())

			if balance, _ := ReadAccountBalance(snapshot, self); balance < sys.TransactionFeeAmount {
				time.Sleep(100 * time.Millisecond)
				return nil
			}

			// Create a nop transaction.
			tx, err = NewTransaction(l.keys, sys.TagNop, nil)

			if err != nil {
				return err
			}
		}

		tx, err = l.attachSenderToTransaction(tx)

		if err != nil {
			if Error != nil {
				Error <- errors.Wrap(err, "failed to sign off transaction")
			}
			return nil
		}

		evt := EventGossip{
			TX:     tx,
			Result: make(chan []VoteGossip, 1),
			Error:  make(chan error, 1),
		}

		select {
		case <-l.kill:
			if Error != nil {
				Error <- ErrStopped
			}

			return ErrStopped
		case <-stop:
			if Error != nil {
				Error <- ErrStopped
			}

			return ErrStopped
		case <-time.After(3 * time.Second):
			if Error != nil {
				Error <- errors.Wrap(ErrTimeout, "gossip queue is full")
			}

			return nil
		case l.gossipOut <- evt:
		}

		select {
		case <-l.kill:
			if Error != nil {
				Error <- ErrStopped
			}

			return ErrStopped
		case <-stop:
			if Error != nil {
				Error <- ErrStopped
			}

			return ErrStopped
		case err := <-evt.Error:
			if err != nil {
				if Error != nil {
					Error <- errors.Wrap(err, "got an error gossiping transaction out")
				}
				return nil
			}
		case votes := <-evt.Result:
			if len(votes) == 0 {
				return nil
			}

			voters := make([]common.AccountID, len(votes))

			for i, vote := range votes {
				voters[i] = vote.Voter
			}

			weights := computeStakeDistribution(snapshot, voters, sys.SnowballQueryK)

			positives := 0.0

			for _, vote := range votes {
				if vote.Ok {
					positives += weights[vote.Voter]
				}
			}

			if positives < sys.SnowballQueryAlpha {
				if Error != nil {
					Error <- errors.Errorf("only %.2f%% of queried peers find transaction %x valid", positives, evt.TX.ID)
				}

				return nil
			}

			// Double-check that after gossiping, we have not progressed a single view ID and
			// that the transaction is still valid for us to add to our view-graph.

			if err := l.addTransaction(tx); err != nil {
				if Error != nil {
					Error <- err
				}

				return nil
			}

			/** At this point, the transaction was successfully added to our view-graph. **/

			// If we have nothing else to broadcast and we are not broadcasting out
			// nop transactions, then start broadcasting out nop transactions.
			if len(l.broadcastQueue) == 0 && broadcastNops == false {
				broadcastNops = true
			}

			if Result != nil {
				Result <- tx
			}
		case <-time.After(3 * time.Second):
			if Error != nil {
				Error <- errors.Wrap(ErrTimeout, "did not get back a gossip response")
			}
		}

		if l.cr.Preferred() != nil {
			return ErrPreferredSelected
		}

		return nil
	}
}

func listenForGossip(l *Ledger) func(stop <-chan struct{}) error {
	return func(stop <-chan struct{}) error {
		select {
		case <-l.kill:
			return ErrStopped
		case <-stop:
			return ErrStopped
		case evt := <-l.queryIn:
			defer close(evt.Response)
			defer close(evt.Error)

			// If we got a query while we were gossiping, check if the queried transaction
			// is valid. If it is, then we prefer it and move on to querying.

			// Respond to the query with either:
			//
			// a) the queriers transaction indicating that we will now query with their transaction.
			// b) should they be in a prior view ID, the prior consensus rounds root.
			// c) no response indicating that we do not prefer any transaction.

			if root := l.v.loadRoot(); root.ViewID != 0 && evt.TX.ViewID == root.ViewID {
				evt.Response <- root
				return nil
			}

			critical := evt.TX.IsCritical(l.v.loadDifficulty())

			if !critical {
				evt.Error <- errors.New("transaction which was queried is not critical")
				return nil
			}

			if err := l.addTransaction(evt.TX); err != nil {
				evt.Error <- err
				return nil
			}

			// If the transaction we were queried with is critical, then prefer the incoming
			// queried transaction and move on to querying.

			evt.Response <- l.cr.Preferred()
		case evt := <-l.gossipIn:
			defer close(evt.Vote)

			// Handle some incoming gossip.

			// Sending `nil` to `evt.Vote` is equivalent to voting that the
			// incoming gossiped transaction has been accepted by our node.

			// If we already have the transaction in our view-graph, we tell the gossiper
			// that the transaction has already been well-received by us.
			if _, exists := l.v.lookupTransaction(evt.TX.ID); exists {
				evt.Vote <- nil
				return nil
			}

			if err := l.addTransaction(evt.TX); err != nil {
				evt.Vote <- err
				return nil
			}

			evt.Vote <- nil
		}

		if l.cr.Preferred() != nil {
			return ErrPreferredSelected
		}

		return nil
	}
}

func query(l *Ledger, state *stateQuerying) func(stop <-chan struct{}) error {
	return func(stop <-chan struct{}) error {
		snapshot := l.a.snapshot()
		preferred := l.cr.Preferred()

		if preferred == nil {
			return ErrConsensusRoundFinished
		}

		evt := EventQuery{
			TX:     *preferred,
			Result: make(chan []VoteQuery, 1),
			Error:  make(chan error, 1),
		}

		select {
		case <-l.kill:
			return ErrStopped
		case <-stop:
			return ErrStopped
		case <-time.After(3 * time.Second):
			return errors.Wrap(ErrTimeout, "query queue is full")
		case l.queryOut <- evt:
		}

		select {
		case <-l.kill:
			return ErrStopped
		case <-stop:
			return ErrStopped
		case err := <-evt.Error:
			return errors.Wrap(err, "error while querying")
		case votes := <-evt.Result:
			if len(votes) == 0 {
				return nil
			}

			ourViewID := l.v.loadViewID()

			voters := make([]common.AccountID, len(votes))
			counts := make(map[common.TransactionID]float64)
			transactions := make(map[common.TransactionID]Transaction)

			for i, vote := range votes {
				if vote.Preferred.ViewID == ourViewID && vote.Preferred.ID != common.ZeroTransactionID {
					transactions[vote.Preferred.ID] = vote.Preferred
					voters[i] = vote.Voter
				}
			}

			weights := computeStakeDistribution(snapshot, voters, sys.SnowballQueryK)

			for _, vote := range votes {
				if vote.Preferred.ViewID == ourViewID && vote.Preferred.ID != common.ZeroTransactionID {
					counts[vote.Preferred.ID] += weights[vote.Voter]
				}
			}

			l.cr.Tick(counts, transactions)

			// Once Snowball has finalized, collapse down our transactions, reset everything, and
			// commit the newly officiated ledger state to our database.

			if l.cr.Decided() {
				var exception error

				state.resetOnce.Do(func() {
					newRoot := l.cr.Preferred()
					oldRoot := l.v.loadRoot()

					state, err := l.collapseTransactions(*newRoot, true)
					if err != nil {
						exception = errors.Wrap(err, "decided a new root, but got an error collapsing down its ancestry")
						return
					}

					if err = l.a.commit(state); err != nil {
						exception = errors.Wrap(err, "failed to commit collapsed state to our database")
						return
					}

					l.cr.Reset()
					l.v.reset(newRoot)

					logger := log.Consensus("round_end")
					logger.Info().
						Uint64("old_view_id", oldRoot.ViewID+1).
						Uint64("new_view_id", newRoot.ViewID+1).
						Hex("new_root", newRoot.ID[:]).
						Hex("old_root", oldRoot.ID[:]).
						Hex("new_accounts_checksum", newRoot.AccountsMerkleRoot[:]).
						Hex("old_accounts_checksum", oldRoot.AccountsMerkleRoot[:]).
						Msg("Finalized consensus round, and incremented view ID.")
				})

				if exception != nil {
					return exception
				}

				return ErrConsensusRoundFinished
			}
		case <-time.After(3 * time.Second):
			return errors.Wrap(ErrTimeout, "did not get back a query response")
		}

		return nil
	}
}

func listenForQueries(l *Ledger) func(stop <-chan struct{}) error {
	return func(stop <-chan struct{}) error {
		select {
		case <-l.kill:
			return ErrStopped
		case <-stop:
			return ErrStopped
		case evt := <-l.queryIn:
			defer close(evt.Response)
			defer close(evt.Error)

			// Respond to the query with either:
			//
			// a) our own preferred transaction.
			// b) should they be in a prior view ID, the prior consensus rounds root.

			if root := l.v.loadRoot(); root.ViewID != 0 && evt.TX.ViewID == root.ViewID {
				evt.Response <- root
			} else if preferred := l.cr.Preferred(); preferred != nil {
				evt.Response <- preferred
			} else {
				evt.Response <- nil
			}
		}

		if l.cr.Preferred() == nil {
			return ErrConsensusRoundFinished
		}

		return nil
	}
}

func checkIfOutOfSync(l *Ledger) func(stop <-chan struct{}) error {
	return func(stop <-chan struct{}) error {
		time.Sleep(10 * time.Millisecond)

		snapshot := l.a.snapshot()

		evt := EventOutOfSyncCheck{
			Root:   *l.v.loadRoot(),
			Result: make(chan []VoteOutOfSync, 1),
			Error:  make(chan error, 1),
		}

		select {
		case <-l.kill:
			return ErrStopped
		case <-stop:
			return ErrStopped
		case l.outOfSyncOut <- evt:
		}

		select {
		case <-l.kill:
			return ErrStopped
		case <-stop:
			return ErrStopped
		case err, ok := <-evt.Error:
			if err != nil || ok {
				fmt.Println("got error while checking if out of sync:", err)
			}
			return nil
		case votes := <-evt.Result:
			if len(votes) == 0 {
				return nil
			}

			voters := make([]common.AccountID, len(votes))
			counts := make(map[common.TransactionID]float64)
			transactions := make(map[common.TransactionID]Transaction)

			for i, vote := range votes {
				if vote.Root.ID != common.ZeroTransactionID {
					transactions[vote.Root.ID] = vote.Root
					voters[i] = vote.Voter
				}
			}

			weights := computeStakeDistribution(snapshot, voters, sys.SnowballSyncK)

			for _, vote := range votes {
				if vote.Root.ID != common.ZeroTransactionID {
					counts[vote.Root.ID] += weights[vote.Voter]
				}
			}

			l.sr.Tick(counts, transactions)

			if l.sr.Decided() {
				root := l.sr.Preferred()

				// The view ID we came to consensus to being the latest within the network
				// is less than or equal to ours. Go back to square one.
				if l.v.loadRoot().ID == root.ID || l.v.loadViewID() >= root.ViewID+1 {
					time.Sleep(1 * time.Second)

					l.sr.Reset()
					return nil
				}

				return ErrOutOfSync
			}
		}

		return nil
	}
}

func listenForOutOfSyncChecks(l *Ledger) func(stop <-chan struct{}) error {
	return func(stop <-chan struct{}) error {
		select {
		case <-l.kill:
			return ErrStopped
		case <-stop:
			return ErrStopped
		case evt := <-l.outOfSyncIn:
			evt.Response <- l.v.loadRoot()
			close(evt.Response)
		}

		return nil
	}
}

func listenForSyncInits(l *Ledger) func(stop <-chan struct{}) error {
	return func(stop <-chan struct{}) error {
		select {
		case <-l.kill:
			return ErrStopped
		case <-stop:
			return ErrStopped
		case evt := <-l.syncInitIn:
			data := SyncInitMetadata{
				ViewID: l.v.loadViewID(),
			}

			diff := l.a.snapshot().DumpDiff(evt.ViewID)

			for i := 0; i < len(diff); i += SyncChunkSize {
				end := i + SyncChunkSize

				if end > len(diff) {
					end = len(diff)
				}

				hash := blake2b.Sum256(diff[i:end])

				l.cacheChunk.put(hash, diff[i:end])
				data.ChunkHashes = append(data.ChunkHashes, hash)
			}

			evt.Response <- data
			close(evt.Response)
		}

		return nil
	}
}

func listenForSyncDiffChunks(l *Ledger) func(stop <-chan struct{}) error {
	return func(stop <-chan struct{}) error {
		select {
		case <-l.kill:
			return ErrStopped
		case <-stop:
			return ErrStopped
		case evt := <-l.syncDiffIn:
			if chunk, found := l.cacheChunk.load(evt.ChunkHash); found {
				chunk := chunk.([]byte)

				providedHash := blake2b.Sum256(chunk)

				logger := log.Sync("provide-chunk")
				logger.Info().
					Hex("requested_hash", evt.ChunkHash[:]).
					Hex("provided_hash", providedHash[:]).
					Msg("Responded to sync chunk request.")

				evt.Response <- chunk
			} else {
				evt.Response <- nil
			}

			close(evt.Response)
		}

		return nil
	}
}

func syncUp(l *Ledger, root Transaction) func(stop <-chan struct{}) error {
	return func(stop <-chan struct{}) error {
		evt := EventSyncInit{
			ViewID: l.v.loadViewID(),
			Result: make(chan []SyncInitMetadata, 1),
			Error:  make(chan error, 1),
		}

		select {
		case <-l.kill:
			return ErrStopped
		case <-stop:
			return ErrStopped
		case l.syncInitOut <- evt:
		}

		var votes []SyncInitMetadata

		select {
		case <-l.kill:
			return ErrStopped
		case <-stop:
			return ErrStopped
		case err := <-evt.Error:
			return errors.Wrap(ErrSyncFailed, err.Error())
		case v := <-evt.Result:
			votes = v
		}

		votesByViewID := make(map[uint64][]SyncInitMetadata)

		for _, vote := range votes {
			votesByViewID[vote.ViewID] = append(votesByViewID[vote.ViewID], vote)
		}

		var selected []SyncInitMetadata

		for _, v := range votesByViewID {
			if len(v) >= len(votes)*2/3 {
				selected = votes
				break
			}
		}

		// There is no consensus on view IDs to sync towards; cancel the sync.
		if selected == nil {
			return errors.Wrap(ErrSyncFailed, "no consensus on which view ID to sync towards")
		}

		var sources []ChunkSource

		for i := 0; ; i++ {
			hashCount := make(map[[blake2b.Size256]byte][]protocol.ID)
			hashInRange := false

			for _, vote := range selected {
				if i >= len(vote.ChunkHashes) {
					continue
				}

				hashCount[vote.ChunkHashes[i]] = append(hashCount[vote.ChunkHashes[i]], vote.User)
				hashInRange = true
			}

			if !hashInRange {
				break
			}

			consistent := false

			for hash, peers := range hashCount {
				if len(peers) >= len(selected)*2/3 && len(peers) > 0 {
					sources = append(sources, ChunkSource{Hash: hash, Peers: peers})

					consistent = true
					break
				}
			}

			if !consistent {
				return errors.Wrap(ErrSyncFailed, "chunk IDs are not consistent")
			}
		}

		evtc := EventSyncDiff{
			Sources: sources,
			Result:  make(chan [][]byte, 1),
			Error:   make(chan error, 1),
		}

		select {
		case <-l.kill:
			return ErrStopped
		case <-stop:
			return ErrStopped
		case <-time.After(3 * time.Second):
			return errors.Wrap(ErrSyncFailed, "timed out while waiting for sync chunk queue to empty up")
		case l.syncDiffOut <- evtc:
		}

		var chunks [][]byte

		select {
		case <-l.kill:
			return ErrStopped
		case <-stop:
			return ErrStopped
		case err := <-evtc.Error:
			fmt.Println("got an error while getting sync diffs:", err)
			return errors.Wrap(ErrSyncFailed, err.Error())
		case c := <-evtc.Result:
			chunks = c
		}

		var diff []byte

		for _, chunk := range chunks {
			diff = append(diff, chunk...)
		}

		// Attempt to apply the diff to a snapshot of our ledger state.
		snapshot := l.a.snapshot()

		if err := snapshot.ApplyDiff(diff); err != nil {
			return errors.Wrapf(ErrSyncFailed, "failed to apply diff to state - got error: %+v", err.Error())
		}

		// The diff did not get us the intended merkle root we wanted. Stop syncing.
		if snapshot.Checksum() != root.AccountsMerkleRoot {
			return errors.Wrap(ErrSyncFailed, "applying the diff yielded an unexpected merkle root representative our expected state")
		}

		// Apply the diff to our official ledger state.
		if err := l.a.commit(snapshot); err != nil {
			return errors.Wrapf(ErrSyncFailed, "failed to commit collapsed state to our database - got error %+v", err.Error())
		}

		l.cr.Reset()
		l.v.reset(&root)

		// Sync successful.
		logger := log.Sync("apply")
		logger.Info().
			Int("num_chunks", len(chunks)).
			Msg("Successfully built a new state tree out of chunk(s) we have received from peers.")

		return nil
	}
}