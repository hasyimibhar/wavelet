package server

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"

	"github.com/perlin-network/wavelet"
	"github.com/perlin-network/wavelet/cmd/cli/tui/logger"
	"github.com/perlin-network/wavelet/sys"
	"github.com/pkg/errors"
)

func (s *Server) Status() {
	preferredID := "N/A"

	if preferred := s.Ledger.Finalizer().Preferred(); preferred != nil {
		preferredID = hex.EncodeToString(preferred.ID[:])
	}

	count := s.Ledger.Finalizer().Progress()

	snapshot := s.Ledger.Snapshot()
	publicKey := s.Keys.PublicKey()

	accountsLen := wavelet.ReadAccountsLen(snapshot)

	balance, _ := wavelet.ReadAccountBalance(snapshot, publicKey)
	stake, _ := wavelet.ReadAccountStake(snapshot, publicKey)
	reward, _ := wavelet.ReadAccountReward(snapshot, publicKey)
	nonce, _ := wavelet.ReadAccountNonce(snapshot, publicKey)

	round := s.Ledger.Rounds().Latest()
	rootDepth := s.Ledger.Graph().RootDepth()

	peers := s.Client.ClosestPeerIDs()
	peerIDs := make([]string, 0, len(peers))

	for _, id := range peers {
		peerIDs = append(peerIDs, id.String())
	}

	s.logger.Level(logger.WithInfo("Node status:").
		F("difficulty", "%v", round.ExpectedDifficulty(
			sys.MinDifficulty, sys.DifficultyScaleFactor)).
		F("round", "%d", round.Index).
		F("root_id", "%x", round.End.ID).
		F("height", "%d", s.Ledger.Graph().Height()).
		F("id", "%x", publicKey).
		F("balance", "%d", balance).
		F("stake", "%d", stake).
		F("reward", "%d", reward).
		F("nonce", "%d", nonce).
		F("peers", "%v", peerIDs).
		F("num_tx", "%d", s.Ledger.Graph().DepthLen(&rootDepth, nil)).
		F("num_missing_tx", "%d", s.Ledger.Graph().MissingLen()).
		F("num_tx_in_store", "%d", s.Ledger.Graph().Len()).
		F("num_accounts_in_store", "%d", accountsLen).
		F("preferred_id", preferredID).
		F("preferred_votes", "%d", count))
}

// gasLimit is optional
func (s *Server) Pay(recipient [wavelet.SizeAccountID]byte,
	amount, gasLimit int, additional []byte) {

	// Create a new payload and write the recipient
	payload := bytes.NewBuffer(nil)
	payload.Write(recipient[:])

	// Make an int64 bytes buffer
	var intBuf = make([]byte, 8)

	// Write the amount
	binary.LittleEndian.PutUint64(intBuf, uint64(amount))
	payload.Write(intBuf)

	// Write the gas limit
	binary.LittleEndian.PutUint64(intBuf, uint64(gasLimit))
	payload.Write(intBuf)

	if additional != nil {
		payload.Write(additional)
	}

	tx := s.sendTx(wavelet.NewTransaction(
		s.Keys, sys.TagTransfer, payload.Bytes(),
	))

	s.logger.Level(logger.WithSuccess("Paid").
		F("tx_id", "%x", tx.ID))
}

// TODO(diamond): Port the rest of the CLI calls to this

func (s *Server) sendTx(tx wavelet.Transaction) wavelet.Transaction {
	tx = wavelet.AttachSenderToTransaction(
		s.Keys, tx,
		s.Ledger.Graph().FindEligibleParents()...,
	)

	if err := s.Ledger.AddTransaction(tx); err != nil {
		if errors.Cause(err) != wavelet.ErrMissingParents {
			s.logger.Level(logger.WithError(err).
				Wrap("Failed to create transaction").
				F("tx_id", "%x", tx.ID))
		}
	}

	return tx
}
