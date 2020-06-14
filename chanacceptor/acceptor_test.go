package chanacceptor

import (
	"bytes"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lightningnetwork/lnd/lnrpc"

	"github.com/btcsuite/btcd/btcec"
	"github.com/lightningnetwork/lnd/lnwire"
)

func randKey(t *testing.T) *btcec.PublicKey {
	t.Helper()

	priv, err := btcec.NewPrivateKey(btcec.S256())
	if err != nil {
		t.Fatalf("unable to generate new public key")
	}

	return priv.PubKey()
}

// requestInfo encapsulates the information sent from the RPCAcceptor to the
// receiver on the other end of the stream.
type requestInfo struct {
	chanReq      *ChannelAcceptRequest
	responseChan chan lnrpc.ChannelAcceptResponse
}

var defaultAcceptTimeout = 5 * time.Second

func acceptAndIncrementCtr(rpc ChannelAcceptor, req *ChannelAcceptRequest,
	ctr *uint32, success chan struct{}) {

	if err := rpc.Accept(req); err != nil {
		return
	}

	val := atomic.AddUint32(ctr, 1)
	if val == 3 {
		success <- struct{}{}
	}
}

// TestMultipleRPCClients tests that the RPCAcceptor is able to handle multiple
// callers to its Accept method and respond to them correctly.
func TestRPCMultipleAcceptClients(t *testing.T) {

	var (
		node = randKey(t)

		firstOpenReq = &ChannelAcceptRequest{
			Node: node,
			OpenChanMsg: &lnwire.OpenChannel{
				PendingChannelID: [32]byte{0},
			},
		}

		secondOpenReq = &ChannelAcceptRequest{
			Node: node,
			OpenChanMsg: &lnwire.OpenChannel{
				PendingChannelID: [32]byte{1},
			},
		}

		thirdOpenReq = &ChannelAcceptRequest{
			Node: node,
			OpenChanMsg: &lnwire.OpenChannel{
				PendingChannelID: [32]byte{2},
			},
		}

		counter uint32
	)

	quit := make(chan struct{})
	defer close(quit)

	// Create channels to handle requests and successes.
	requests := make(chan *requestInfo)
	successChan := make(chan struct{})
	errChan := make(chan struct{}, 4)

	// demultiplexReq is a closure used to abstract the RPCAcceptor's
	// request and response logic.
	demultiplexReq := func(req *ChannelAcceptRequest) error {
		respChan := make(chan lnrpc.ChannelAcceptResponse, 1)

		newRequest := &requestInfo{
			chanReq:      req,
			responseChan: respChan,
		}

		// Send the newRequest to the requests channel.
		select {
		case requests <- newRequest:
		case <-quit:
			return errors.New("quit")
		}

		// Receive the response and verify that the PendingChanId
		// matches the ID found in the ChannelAcceptRequest. If no
		// response has been received in defaultAcceptTimeout, then
		// return false.
		select {
		case resp := <-respChan:
			pendingID := req.OpenChanMsg.PendingChannelID
			if !bytes.Equal(pendingID[:], resp.PendingChanId) {
				errChan <- struct{}{}
				return errors.New("PendingChanId doesn't " +
					"match the ID in ChannelAcceptRequest")
			}

			if !resp.Accept {
				return errors.New(resp.GetRejectionReason())
			}

			return nil
		case <-time.After(defaultAcceptTimeout):
			errChan <- struct{}{}
			return errors.New("RPCAcceptor timed out")
		case <-quit:
			return errors.New("quit")
		}
	}

	rpcAcceptor := NewRPCAcceptor(demultiplexReq)

	// Now we call the Accept method for each request.
	go func() {
		acceptAndIncrementCtr(rpcAcceptor, firstOpenReq, &counter, successChan)
	}()

	go func() {
		acceptAndIncrementCtr(rpcAcceptor, secondOpenReq, &counter, successChan)
	}()

	go func() {
		acceptAndIncrementCtr(rpcAcceptor, thirdOpenReq, &counter, successChan)
	}()

	for {
		select {
		case newReq := <-requests:
			chanID := newReq.chanReq.OpenChanMsg.PendingChannelID[:]
			newResponse := lnrpc.ChannelAcceptResponse{
				Accept:          true,
				PendingChanId:   chanID,
				RejectionReason: "",
			}

			newReq.responseChan <- newResponse
		case <-errChan:
			t.Fatalf("unable to accept ChannelAcceptRequest")
		case <-successChan:
			return
		case <-quit:
		}
	}
}
