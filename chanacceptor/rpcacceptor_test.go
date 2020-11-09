package chanacceptor

import (
	"errors"
	"strings"
	"testing"

	"github.com/decred/dcrd/chaincfg/v3"
	"github.com/decred/dcrd/dcrutil/v4"
	"github.com/decred/dcrlnd/input"
	"github.com/decred/dcrlnd/lnrpc"
	"github.com/decred/dcrlnd/lnwallet/chancloser"
	"github.com/decred/dcrlnd/lnwire"
	"github.com/stretchr/testify/require"
)

// TestValidateAcceptorResponse test validation of acceptor responses.
func TestValidateAcceptorResponse(t *testing.T) {
	var (
		customError = errors.New("custom error")
		validAddr   = "TsZwLVNTYxwcT5tgcqMzGUujjPydy5nGMk2"
		addr, _     = chancloser.ParseUpfrontShutdownAddress(
			validAddr, chaincfg.TestNet3Params(),
		)
	)

	tests := []struct {
		name        string
		dustLimit   dcrutil.Amount
		response    lnrpc.ChannelAcceptResponse
		accept      bool
		acceptorErr error
		error       error
		shutdown    lnwire.DeliveryAddress
	}{
		{
			name: "accepted with error",
			response: lnrpc.ChannelAcceptResponse{
				Accept: true,
				Error:  customError.Error(),
			},
			accept:      false,
			acceptorErr: errChannelRejected,
			error:       errAcceptWithError,
		},
		{
			name: "custom error too long",
			response: lnrpc.ChannelAcceptResponse{
				Accept: false,
				Error:  strings.Repeat(" ", maxErrorLength+1),
			},
			accept:      false,
			acceptorErr: errChannelRejected,
			error:       errCustomLength,
		},
		{
			name: "accepted",
			response: lnrpc.ChannelAcceptResponse{
				Accept:          true,
				UpfrontShutdown: validAddr,
			},
			accept:      true,
			acceptorErr: nil,
			error:       nil,
			shutdown:    addr,
		},
		{
			name: "rejected with error",
			response: lnrpc.ChannelAcceptResponse{
				Accept: false,
				Error:  customError.Error(),
			},
			accept:      false,
			acceptorErr: customError,
			error:       nil,
		},
		{
			name: "rejected with no error",
			response: lnrpc.ChannelAcceptResponse{
				Accept: false,
			},
			accept:      false,
			acceptorErr: errChannelRejected,
			error:       nil,
		},
		{
			name: "invalid upfront shutdown",
			response: lnrpc.ChannelAcceptResponse{
				Accept:          true,
				UpfrontShutdown: "invalid addr",
			},
			accept:      false,
			acceptorErr: errChannelRejected,
			error:       errInvalidUpfrontShutdown,
		},
		{
			name:      "reserve too low",
			dustLimit: 100,
			response: lnrpc.ChannelAcceptResponse{
				Accept:       true,
				ReserveAtoms: 10,
			},
			accept:      false,
			acceptorErr: errChannelRejected,
			error:       errInsufficientReserve,
		},
		{
			name:      "max htlcs too high",
			dustLimit: 100,
			response: lnrpc.ChannelAcceptResponse{
				Accept:       true,
				MaxHtlcCount: 1 + input.MaxHTLCNumber/2,
			},
			accept:      false,
			acceptorErr: errChannelRejected,
			error:       errMaxHtlcTooHigh,
		},
	}

	for _, test := range tests {
		test := test

		t.Run(test.name, func(t *testing.T) {
			// Create an acceptor, everything can be nil because
			// we just need the params.
			acceptor := NewRPCAcceptor(
				nil, nil, 0, chaincfg.TestNet3Params(), nil,
			)

			accept, acceptErr, shutdown, err := acceptor.validateAcceptorResponse(
				test.dustLimit, test.response,
			)
			require.Equal(t, test.accept, accept)
			require.Equal(t, test.acceptorErr, acceptErr)
			require.Equal(t, test.error, err)
			require.Equal(t, test.shutdown, shutdown)
		})
	}
}
