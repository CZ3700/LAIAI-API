package videoasset

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestPublicResourceVersionsSupportOptimisticRename(t *testing.T) {
	s, _, now, _ := setup(t)
	ctx := context.Background()
	c := CreateContext{CallerKind: "api_key", OrgID: 1, KeyID: 11, RequestID: "public-version-create", IdempotencyKey: "public-version-create"}
	group, _, err := s.CreateGroup(ctx, c, GroupCreate{Kind: KindVirtual, Name: "original group"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ProcessOnce(ctx); err != nil {
		t.Fatal(err)
	}
	asset := readyAsset(t, s, now, group.ID, "public-version-asset")
	for _, tc := range []struct {
		name, id string
		group    bool
	}{
		{name: "group", id: group.ID, group: true},
		{name: "asset", id: asset.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := func() (uint64, string) {
				t.Helper()
				var dto any
				var readErr error
				if tc.group {
					dto, readErr = s.GetGroup(ctx, 1, tc.id)
				} else {
					dto, readErr = s.GetAsset(ctx, 1, tc.id)
				}
				if readErr != nil {
					t.Fatal(readErr)
				}
				raw, marshalErr := json.Marshal(dto)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				var wire struct {
					Version uint64 `json:"version"`
					Name    string `json:"name"`
				}
				if json.Unmarshal(raw, &wire) != nil || wire.Version == 0 {
					t.Fatal("public JSON lacks a usable optimistic version")
				}
				return wire.Version, wire.Name
			}
			version, _ := read()
			caller := c
			caller.RequestID, caller.IdempotencyKey = "rename-"+tc.name, "rename-"+tc.name
			op, renameErr := s.RenameResource(ctx, caller, tc.id, tc.group, Rename{Name: "updated " + tc.name, ExpectedVersion: version})
			if renameErr != nil {
				t.Fatal(renameErr)
			}
			if err := s.ProcessOnce(ctx); err != nil {
				t.Fatal(err)
			}
			result, opErr := s.GetOperation(ctx, 1, op.ID)
			if opErr != nil || result.Status != "confirmed" {
				t.Fatal(result, opErr)
			}
			current, name := read()
			if current <= version || name != "updated "+tc.name {
				t.Fatal("rename did not expose the updated name and version")
			}
			caller.RequestID, caller.IdempotencyKey = "stale-"+tc.name, "stale-"+tc.name
			if _, err := s.RenameResource(ctx, caller, tc.id, tc.group, Rename{Name: "stale update", ExpectedVersion: version}); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale public version was not rejected: %v", err)
			}
			if after, afterName := read(); after != current || afterName != name {
				t.Fatal("rejected stale update changed resource")
			}
		})
	}
}
