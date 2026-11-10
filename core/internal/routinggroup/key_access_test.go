package routinggroup

import (
	"errors"
	"testing"
)

func TestDefaultKeyGroupRequiresCurrentEnabledGrant(t *testing.T) {
	s, _ := serviceFixture(t)
	if group, err := DefaultKeyGroupTx(s.DB, 1); err != nil || group != nil {
		t.Fatal("absent default", group, err)
	}
	if err := s.DB.Exec(`INSERT INTO routing_groups(id,slug,name,description,owner_org_id,status,policy_version,created_at,updated_at) VALUES(7,'video','Video','',0,'enabled',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP);
INSERT INTO org_group_grants(id,org_id,group_id,status,grant_version,default_slot,created_at,updated_at) VALUES(9,1,7,'enabled',1,1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP);`).Error; err != nil {
		t.Fatal(err)
	}
	if group, err := DefaultKeyGroupTx(s.DB, 1); err != nil || group == nil || *group != 7 {
		t.Fatal("enabled default", group, err)
	}
	if err := s.DB.Exec("UPDATE routing_groups SET status='disabled',policy_version=2 WHERE id=7").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := DefaultKeyGroupTx(s.DB, 1); !errors.Is(err, ErrInactive) {
		t.Fatal("disabled group accepted", err)
	}
	if err := s.DB.Exec("UPDATE routing_groups SET status='enabled',owner_org_id=2,policy_version=3 WHERE id=7").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := DefaultKeyGroupTx(s.DB, 1); !errors.Is(err, ErrInactive) {
		t.Fatal("foreign group accepted", err)
	}
}
