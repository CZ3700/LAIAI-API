package server

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/routinggroup"
)

func TestRoutingCandidatesBridgeMapsSafeDomainErrors(t *testing.T) {
	for _, pair := range []struct{ in, want error }{
		{identity.ErrRoutingCandidateInvalid, routinggroup.ErrCandidatesInvalid}, {relay.ErrRoutingPublishedInvalid, routinggroup.ErrCandidatesInvalid},
		{identity.ErrRoutingCandidateFacts, routinggroup.ErrCandidatesInvalidFacts}, {relay.ErrRoutingPublishedFacts, routinggroup.ErrCandidatesInvalidFacts},
		{identity.ErrRoutingCandidateLimit, routinggroup.ErrCandidatesLimit}, {relay.ErrRoutingPublishedLimit, routinggroup.ErrCandidatesLimit},
		{identity.ErrRoutingCandidateUnavailable, routinggroup.ErrCandidatesUnavailable}, {relay.ErrRoutingPublishedUnavailable, routinggroup.ErrCandidatesUnavailable},
		{context.Canceled, context.Canceled}, {context.DeadlineExceeded, context.DeadlineExceeded},
		{errors.New("private sql parameters"), routinggroup.ErrCandidatesUnavailable},
	} {
		got := routingCandidateBridgeError(fmt.Errorf("private driver details: %w", pair.in))
		if got != pair.want {
			t.Fatal("unsafe or inaccurate mapping", got, pair.want)
		}
	}
	if routingCandidateBridgeError(nil) != nil {
		t.Fatal("nil error changed")
	}
}
func TestRoutingCandidatesBridgesRequireRealTransaction(t *testing.T) {
	if _, err := (routingCandidateSubjectBridge{}).ReadCandidateSubject(context.Background(), nil, routinggroup.CandidateSubjectRef{OrgID: 1, KeyID: 1}, time.Now()); !errors.Is(err, routinggroup.ErrCandidatesInvalid) {
		t.Fatal(err)
	}
	if _, err := (routingPublishedChannelBridge{}).ReadPublishedChannels(context.Background(), nil, nil); !errors.Is(err, routinggroup.ErrCandidatesInvalid) {
		t.Fatal(err)
	}
}
