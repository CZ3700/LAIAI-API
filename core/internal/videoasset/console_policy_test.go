package videoasset

import (
	"context"
	"errors"
	"testing"
)

func TestConsolePolicyBlockKeepsHistoricalReadsAndLocalCleanup(t *testing.T) {
	for _, missing := range []bool{true, false} {
		name := "disabled"
		if missing {
			name = "missing"
		}
		t.Run(name, func(t *testing.T) {
			s, _, _, _ := setup(t)
			group := rejectedGroup(t, s)
			resolve := s.options.ResolveScope
			s.options.ResolveScope = func(ctx context.Context, org uint64, scope Scope) (RuntimeScope, error) {
				if missing {
					return RuntimeScope{}, ErrExecutionPolicy
				}
				r, err := resolve(ctx, org, scope)
				r.ExecutionRetentionConsent = false
				return r, err
			}
			ctx := context.Background()
			caps := s.ConsoleCapabilities(ctx, 1)
			if caps.Ready || caps.Reason != "execution_policy_required" || caps.Virtual || caps.Sync || caps.Update || caps.Delete || caps.DeleteEmptyGroup || caps.Upload || caps.Portrait || len(caps.MediaTypes) != 0 {
				t.Fatalf("consent missing but execution advertised: %+v", caps)
			}
			if missing {
				if _, err := s.resolve(ctx, 1, Scope{}); !errors.Is(err, ErrUnsupported) {
					t.Fatal("operation resolution error contract changed", err)
				}
			}
			page, err := s.ConsoleGroups(ctx, 1, ListFilter{Kind: KindVirtual, Limit: 20})
			if err != nil || len(page.Data) != 1 || page.Data[0].ID != group.ID || page.Data[0].DeletionMode != "local_failed" {
				t.Fatal("policy diagnostic blocked existing historical read", page, err)
			}
			if _, _, err = s.CreateGroup(ctx, createContext("blocked"), GroupCreate{Kind: KindVirtual, Name: "must not execute"}); err == nil {
				t.Fatal("missing execution consent admitted new work")
			}
			if _, done, err := s.DeleteGroup(ctx, createContext("cleanup"), group.ID, false); err != nil || !done {
				t.Fatal("local failed cleanup incorrectly requires execution consent", done, err)
			}
		})
	}
}
