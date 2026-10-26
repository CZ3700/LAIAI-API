package video

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/routinggroup"
	"gorm.io/gorm"
)

func actionSubjectDB(t *testing.T) (*Service, *identity.APIKey) {
	t.Helper()
	db := catalogLocalDB(t)
	for _, ddl := range []string{
		`CREATE TABLE organizations(id INTEGER PRIMARY KEY,status INTEGER)`,
		`CREATE TABLE users(id INTEGER PRIMARY KEY,status INTEGER)`,
		`CREATE TABLE org_members(org_id INTEGER,user_id INTEGER)`,
		`CREATE TABLE api_keys(id INTEGER PRIMARY KEY,org_id INTEGER,user_id INTEGER,status INTEGER,expires_at DATETIME,model_access_mode TEXT,routing_group_id INTEGER,policy_version INTEGER,model_limits TEXT,scopes TEXT)`,
		`CREATE TABLE org_entitlements(org_id INTEGER PRIMARY KEY,feature_images BOOLEAN,feature_video BOOLEAN)`,
		`INSERT INTO organizations VALUES(1,1)`,
		`INSERT INTO api_keys VALUES(2,1,NULL,1,NULL,'legacy',NULL,1,'','video:*')`,
	} {
		must(t, db.Exec(ddl).Error)
	}
	return &Service{DB: db, Now: time.Now}, &identity.APIKey{ID: 2, OrgID: 1, Scopes: "video:*", ModelAccessMode: "legacy", PolicyVersion: 1}
}

func actionSubject(t *testing.T, s *Service, key *identity.APIKey) officialActionSubject {
	t.Helper()
	var subject officialActionSubject
	must(t, s.DB.Transaction(func(tx *gorm.DB) error {
		var err error
		subject, err = s.readOfficialActionSubjectTx(context.Background(), tx, CatalogSubjectRef{OrgID: key.OrgID, KeyID: key.ID})
		return err
	}))
	return subject
}

func TestOfficialActionSubjectUsesCurrentDatabaseFacts(t *testing.T) {
	for _, fault := range []string{"disabled_key", "expired_key", "disabled_org", "model_removed", "scope_removed", "tab_scope", "nbsp_scope", "feature_disabled"} {
		t.Run(fault, func(t *testing.T) {
			s, key := actionSubjectDB(t)
			before := actionSubject(t, s, key)
			if err := requireOfficialActionSubject(before, ark.Model20, officialCreate); err != nil {
				t.Fatal("valid legacy subject rejected")
			}
			switch fault {
			case "disabled_key":
				must(t, s.DB.Exec("UPDATE api_keys SET status=2 WHERE id=2").Error)
			case "expired_key":
				must(t, s.DB.Exec("UPDATE api_keys SET expires_at=? WHERE id=2", time.Now().Add(-time.Hour)).Error)
			case "disabled_org":
				must(t, s.DB.Exec("UPDATE organizations SET status=2 WHERE id=1").Error)
			case "model_removed":
				must(t, s.DB.Exec("UPDATE api_keys SET model_limits='other-model' WHERE id=2").Error)
			case "scope_removed":
				must(t, s.DB.Exec("UPDATE api_keys SET scopes='video:read' WHERE id=2").Error)
			case "tab_scope":
				must(t, s.DB.Exec("UPDATE api_keys SET scopes=? WHERE id=2", "video:read\tvideo:write").Error)
			case "nbsp_scope":
				must(t, s.DB.Exec("UPDATE api_keys SET scopes=? WHERE id=2", "video:read\u00a0video:write").Error)
			case "feature_disabled":
				must(t, s.DB.Exec("INSERT INTO org_entitlements VALUES(1,1,0)").Error)
			}
			// Deliberately retain the authenticated key copy. The public service
			// must query the caller transaction before attempting publication.
			_, err := s.EstimateForKey(context.Background(), key, ark.CreateRequest{Model: ark.Model20})
			want := ErrForbidden
			if fault == "feature_disabled" {
				// No configured model here: passing subject checks reaches plan.
				want = ErrCapability
			}
			if !errors.Is(err, want) {
				t.Fatal("estimate did not use the database subject boundary")
			}
		})
	}
}

func TestOfficialActionMembershipSharesCallerTransactionAndThreeReads(t *testing.T) {
	s, key := actionSubjectDB(t)
	tx := s.DB.Begin()
	defer tx.Rollback()
	must(t, tx.Exec("UPDATE api_keys SET scopes='video:read',model_limits=? WHERE id=2", ark.Model20).Error)
	reads := 0
	must(t, s.DB.Callback().Query().Before("gorm:query").Register("action:caller", func(q *gorm.DB) {
		reads++
		if q.Statement.ConnPool != tx.Statement.ConnPool {
			t.Error("membership escaped caller transaction")
		}
		if _, locked := q.Statement.Clauses["FOR"]; locked {
			t.Error("membership introduced current-read locking")
		}
	}))
	defer s.DB.Callback().Query().Remove("action:caller")
	subject, err := s.readOfficialActionSubjectTx(context.Background(), tx.Where("id=999").Select("private_value").Limit(0), CatalogSubjectRef{OrgID: 1, KeyID: 2})
	must(t, err)
	for _, model := range []string{ark.Model20, ark.Model20Mini, " unknown cfg "} {
		_ = officialActionDecision(subject, model, 3, officialModelsRead, officialActionFacts{published: true})
	}
	if reads != 3 || !subject.allowsModel(ark.Model20) || subject.allowsModel(ark.Model20Mini) || !errors.Is(requireOfficialActionSubject(subject, ark.Model20, officialEstimate), ErrForbidden) {
		t.Fatal("caller state, exact membership or bounded reads changed")
	}
	must(t, tx.Rollback().Error)
	must(t, s.DB.Callback().Query().Remove("action:caller"))
	if err := requireOfficialActionSubject(actionSubject(t, s, key), ark.Model20, officialCreate); err != nil {
		t.Fatal("reader committed caller changes")
	}
}

func TestOfficialActionCompositionAndLegacyDiagnosticCompatibility(t *testing.T) {
	s, key := actionSubjectDB(t)
	subject := actionSubject(t, s, key)
	if !(CatalogObservation{subject: subject}).AllowsModel(" malformed cfg \t") {
		t.Fatal("legacy unrestricted diagnostic names were narrowed")
	}
	facts := officialActionFacts{published: true}
	if !officialActionDecision(subject, ark.Model20, 3, officialEstimate, facts).Eligible || officialActionDecision(subject, ark.Model20, 3, officialCreate, facts).Eligible {
		t.Fatal("estimate acquired execution-only prerequisites")
	}
	for _, a := range []officialAction{officialModelsRead, officialEstimate, officialCreate, "unknown"} {
		if officialActionDecision(officialActionSubject{}, ark.Model20, 3, a, facts).Eligible {
			t.Fatal("zero or invalid observation admitted")
		}
	}
	for _, a := range []officialAction{officialModelsRead, officialEstimate, officialCreate} {
		if officialActionDecision(subject, ark.Model20, 0, a, facts).Eligible || officialActionDecision(subject, ark.Model20, 3, a, officialActionFacts{}).Eligible {
			t.Fatal("unpublished or missing fixed binding admitted")
		}
	}
}

func TestOfficialActionSubjectErrorsRemainSafe(t *testing.T) {
	for _, fault := range []string{"sql", "cancel"} {
		t.Run(fault, func(t *testing.T) {
			s, key := actionSubjectDB(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if fault == "sql" {
				must(t, s.DB.Exec("DROP TABLE api_keys").Error)
			} else {
				must(t, s.DB.Callback().Query().After("gorm:query").Register("action:cancel", func(q *gorm.DB) {
					if q.Statement.Table == "api_keys" {
						cancel()
					}
				}))
				defer s.DB.Callback().Query().Remove("action:cancel")
			}
			_, err := s.EstimateForKey(ctx, key, ark.CreateRequest{Model: ark.Model20})
			want := ErrCatalogUnavailable
			if fault == "cancel" {
				want = context.Canceled
			}
			if !errors.Is(err, want) || strings.Contains(err.Error(), "api_keys") || strings.Contains(err.Error(), "SELECT") {
				t.Fatal("subject failure was erased or exposed storage detail")
			}
		})
	}
}

func TestOfficialActionGroupedCompositionRequiresExactFixedChannel(t *testing.T) {
	s, key := actionSubjectDB(t)
	for _, ddl := range []string{
		`CREATE TABLE routing_groups(id INTEGER PRIMARY KEY,owner_org_id INTEGER,status TEXT,policy_version INTEGER)`,
		`CREATE TABLE org_group_grants(id INTEGER PRIMARY KEY,org_id INTEGER,group_id INTEGER,status TEXT,grant_version INTEGER)`,
		`CREATE TABLE channel_group_bindings(group_id INTEGER,channel_id INTEGER)`,
		`CREATE TABLE channels(id INTEGER PRIMARY KEY,owner_org_id INTEGER,status INTEGER,models TEXT)`,
		`INSERT INTO routing_groups VALUES(4,0,'enabled',1)`,
		`INSERT INTO org_group_grants VALUES(5,1,4,'enabled',1)`,
		`INSERT INTO channel_group_bindings VALUES(4,7)`,
		`UPDATE api_keys SET model_access_mode='inherit_group',routing_group_id=4 WHERE id=2`,
	} {
		must(t, s.DB.Exec(ddl).Error)
	}
	must(t, s.DB.Exec("INSERT INTO channels VALUES(7,0,1,?)", ark.Model20).Error)
	resolver := routinggroup.CandidateResolver{Subjects: officialCandidateSubjectBridge{}, Channels: officialPublishedChannelBridge{}}
	must(t, s.DB.Transaction(func(tx *gorm.DB) error {
		membership, err := resolver.ReadMembershipTx(context.Background(), tx, routinggroup.CandidateSubjectRef{OrgID: 1, KeyID: 2}, s.Now())
		if err != nil {
			return err
		}
		facts, err := membership.ContextFacts()
		if err != nil {
			return err
		}
		subject := officialActionSubject{membership: membership, facts: facts}
		published := officialActionFacts{published: true, credential: true, policyExists: true, policy: Policy{AsyncExecutionAllowed: true, RetentionDays: 30}}
		if !officialActionDecision(subject, ark.Model20, 7, officialCreate, published).Eligible || officialActionDecision(subject, ark.Model20, 8, officialCreate, published).Eligible {
			t.Fatal("group member on another same-model channel substituted for the fixed channel")
		}
		_, err = s.readOfficialActionSubjectTx(context.Background(), tx, CatalogSubjectRef{OrgID: 1, KeyID: 2})
		if !errors.Is(err, ErrCatalogDenied) {
			t.Fatal("internal composition opened grouped production admission")
		}
		return nil
	}))
	if _, err := s.EstimateForKey(context.Background(), key, ark.CreateRequest{Model: ark.Model20}); !errors.Is(err, ErrForbidden) {
		t.Fatal("stale legacy key bypassed grouped mode closure")
	}
}
