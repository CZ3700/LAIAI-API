package video

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/ledger"
)

// Two real accepted tasks keep separate encrypted credentials and original
// accounting facts after the active model/channel configuration is replaced.
// The wire is synthetic; real Client.Do, Ark parsing, SQL receipts and ledger
// are exercised. This is not a credential takeover test or an official call.
func TestMySQLRuntimeHeaderIsolationPinnedTasks(t *testing.T) {
	first := runtimeHeaderFixture(t, "synthetic-original-org-one")
	second := runtimeHeaderFixture(t, "synthetic-original-org-two")
	if first.f.key.OrgID == second.f.key.OrgID || first.task.ChannelID == second.task.ChannelID || first.state.ProviderScopeID == second.state.ProviderScopeID {
		t.Fatal("fixture did not establish distinct tenant/channel/scope ownership")
	}
	hosts := []*runtimeHTTPFixture{first, second}
	wires := []*runtimeHeaderWire{runtimeHeaderInstall(t, first, "synthetic-original-org-one"), runtimeHeaderInstall(t, second, "synthetic-original-org-two")}
	for i, h := range hosts {
		wires[i].set(http.MethodPost, 200, fmt.Sprintf(`{"id":%q}`, h.upstreamID))
		h.f.dispatch(t, h.id)
		wires[i].assert(t, 1)
		h.pinned(t)
	}
	for _, h := range hosts {
		runtimeHeaderActiveOverride(t, h)
	}
	for i, h := range hosts {
		other := hosts[1-i]
		otherFunds := other.funds(t)
		if view, err := h.f.s.Get(h.f.key.OrgID, other.id); err == nil || view.ID != "" {
			t.Fatal("another organization's task was exposed by local lookup")
		}
		wires[i].set(http.MethodGet, 200, fmt.Sprintf(`{"id":%q,"status":"succeeded","usage":{"completion_tokens":100}}`, h.upstreamID))
		must(t, h.f.s.pollTask(context.Background(), h.f.key.OrgID, h.id))
		wires[i].assert(t, 2)
		h.settle(t, 100)
		if other.funds(t) != otherFunds {
			t.Fatal("one organization's query changed another organization's money")
		}
		row, state := h.rows(t)
		if state.ResultPayloadRef != "" {
			t.Fatal("query fixture unexpectedly supplied a delivery capsule")
		}
		settled := h.funds(t)
		makeFixtureTaskRateDue(t, h.f, h.id, "query")
		link := "https://cdn.example.invalid/" + h.id + ".mp4"
		wires[i].set(http.MethodGet, 200, fmt.Sprintf(`{"id":%q,"status":"succeeded","usage":{"completion_tokens":999999},"content":{"video_url":%q}}`, h.upstreamID, link))
		view, err := h.f.s.deliveryView(context.Background(), row, state, "header-isolation", true)
		must(t, err)
		wires[i].assert(t, 3)
		if view.ContentURL != link || view.ArtifactStatus != "ready" || h.funds(t) != settled || other.funds(t) != otherFunds {
			t.Fatal("delivery refresh changed fixed money, crossed tenant, or lost official link")
		}
		h.pinned(t)
	}
}

func TestMySQLRuntimeHeaderIsolationRedirectEvidence(t *testing.T) {
	for _, mode := range []string{"ordinary_query", "delivery_query"} {
		t.Run(mode, func(t *testing.T) {
			h := runtimeHeaderFixture(t, "synthetic-original-redirect")
			wire := runtimeHeaderInstall(t, h, "synthetic-original-redirect")
			wire.set(http.MethodPost, 200, fmt.Sprintf(`{"id":%q}`, h.upstreamID))
			h.f.dispatch(t, h.id)
			calls := 1
			if mode == "delivery_query" {
				wire.set(http.MethodGet, 200, fmt.Sprintf(`{"id":%q,"status":"succeeded","usage":{"completion_tokens":100}}`, h.upstreamID))
				must(t, h.f.s.pollTask(context.Background(), h.f.key.OrgID, h.id))
				h.settle(t, 100)
				calls++
				makeFixtureTaskRateDue(t, h.f, h.id, "query")
			}
			runtimeHeaderActiveOverride(t, h)
			before := h.funds(t)
			body := fmt.Sprintf(`{"id":%q,"status":"succeeded","usage":{"completion_tokens":777777},"content":{"video_url":"https://cdn.example.invalid/must-not-deliver.mp4"}}`, h.upstreamID)
			wire.set(http.MethodGet, http.StatusTemporaryRedirect, body)
			if mode == "ordinary_query" {
				must(t, h.f.s.pollTask(context.Background(), h.f.key.OrgID, h.id))
				var receipt QueryReceipt
				must(t, h.f.s.DB.Where("org_id = ? AND task_id = ?", h.f.key.OrgID, h.id).Take(&receipt).Error)
				if receipt.HTTPStatus != 307 || receipt.Outcome != string(ark.OutcomeUnknown) || receipt.ObservationJSON != "" || receipt.AppliedAt == nil || receipt.PayloadRef == "" {
					t.Fatal("redirect did not persist as an unknown receipt without an executable observation")
				}
				h.evidence(t, receipt.PayloadRef, body)
				row, state := h.rows(t)
				if row.Status != "queued" || state.SyncState != "needs_review" || h.f.hold(t, h.id).Status != ledger.HoldActive {
					t.Fatal("unknown redirect invented a task outcome or released its hold")
				}
				must(t, h.f.s.applyQueryReceipt(context.Background(), receipt))
			} else {
				row, state := h.rows(t)
				_, err := h.f.s.deliveryView(context.Background(), row, state, "header-isolation", true)
				if !errors.Is(err, ErrUnavailable) {
					t.Fatal("redirect delivery did not refuse the unconfirmed link")
				}
				var attempt Attempt
				must(t, h.f.s.DB.Where("org_id = ? AND task_id = ? AND phase = ?", h.f.key.OrgID, h.id, "delivery_query_response").Take(&attempt).Error)
				if attempt.HTTPStatus != 307 || attempt.EvidenceRef == "" {
					t.Fatal("delivery redirect lost its persisted response evidence")
				}
				h.evidence(t, attempt.EvidenceRef, body)
			}
			wire.assert(t, calls+1)
			_, state := h.rows(t)
			if h.funds(t) != before || state.ResultPayloadRef != "" {
				t.Fatal("redirect or replay changed money or created a delivery capsule")
			}
			h.pinned(t)
			// A separate later query can confirm the result; the 3xx itself is
			// never retried, interpreted as success, or used to release money.
			if mode == "ordinary_query" {
				makeFixtureTaskQueryDue(t, h.f, h.id)
				wire.set(http.MethodGet, 200, fmt.Sprintf(`{"id":%q,"status":"succeeded","usage":{"completion_tokens":100}}`, h.upstreamID))
				must(t, h.f.s.pollTask(context.Background(), h.f.key.OrgID, h.id))
				wire.assert(t, calls+2)
				h.settle(t, 100)
			}
		})
	}
}
