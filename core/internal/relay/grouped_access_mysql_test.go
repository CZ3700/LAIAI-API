package relay

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/migrate"
	"github.com/laiai/laiai-next/core/internal/routinggroup"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Real InnoDB transactions and tracked migrations exercise the same grouped
// candidate reader used at each generic relay dispatch. A private schema is
// created only from the dedicated ark_test DSN.
func groupedRelayMySQLDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("ARK_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("ARK_TEST_MYSQL_DSN=ark_test required for real grouped relay dispatch")
	}
	config, err := mysqlDriver.ParseDSN(dsn)
	if err != nil || config.DBName != "ark_test" {
		t.Fatal("grouped relay tests require the isolated ark_test database")
	}
	config.ParseTime = true
	config.Timeout, config.ReadTimeout, config.WriteTimeout = 5*time.Second, 5*time.Second, 5*time.Second
	config.Logger = log.New(io.Discard, "", 0)
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal("cannot allocate private grouped relay schema")
	}
	name := "ark_test_relgroup_" + hex.EncodeToString(nonce[:])
	if !regexp.MustCompile(`^ark_test_relgroup_[0-9a-f]{24}$`).MatchString(name) {
		t.Fatal("invalid private grouped relay schema")
	}
	adminConfig := config.Clone()
	adminConfig.ReadTimeout = 15 * time.Second
	connector, err := mysqlDriver.NewConnector(adminConfig)
	if err != nil {
		t.Fatal("cannot connect to schema administrator")
	}
	admin := sql.OpenDB(connector)
	created := false
	var pool *sql.DB
	t.Cleanup(func() {
		if pool != nil {
			if err := pool.Close(); err != nil {
				t.Error("cannot close grouped relay test pool")
			}
		}
		if created {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := admin.ExecContext(ctx, "DROP DATABASE `"+name+"`"); err != nil {
				t.Error("cannot remove private grouped relay test schema")
			}
		}
		_ = admin.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+name+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"); err != nil {
		t.Fatal("cannot create private grouped relay test schema")
	}
	created = true
	config.DBName = name
	db, err := gorm.Open(mysql.Open(config.FormatDSN()), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal("cannot connect to private grouped relay test schema")
	}
	pool, err = db.DB()
	if err != nil {
		t.Fatal("cannot obtain grouped relay test pool")
	}
	pool.SetMaxOpenConns(12)
	pool.SetMaxIdleConns(12)
	if _, err := migrate.Up(db, os.DirFS("../../../migrations")); err != nil {
		t.Fatal("tracked migrations failed in grouped relay test schema")
	}
	return db
}

func TestMySQLGroupedRelayDispatchStaysWithinCurrentGroup(t *testing.T) {
	db := groupedRelayMySQLDB(t)
	seed := func(query string, args ...any) {
		t.Helper()
		if err := db.Exec(query, args...).Error; err != nil {
			t.Fatal("cannot seed grouped relay fixture")
		}
	}
	seed("INSERT INTO organizations(id,slug,name) VALUES(8801,'relay-group-fixture','Relay group fixture')")
	plain := "sk-laiai-relay-fixture"
	sum := sha256.Sum256([]byte(plain))
	seed("INSERT INTO api_keys(id,org_id,name,prefix,key_hash,scopes,model_limits,model_access_mode,routing_group_id,policy_version) VALUES(8801,8801,'relay-group-key','fixture-prefix',?,'video:read video:write','','inherit_group',8801,1)", hex.EncodeToString(sum[:]))
	seed("INSERT INTO routing_groups(id,slug,name,owner_org_id,status,policy_version,created_at,updated_at) VALUES(8801,'relay-group','Relay group',0,'enabled',1,UTC_TIMESTAMP(3),UTC_TIMESTAMP(3))")
	seed("INSERT INTO org_group_grants(org_id,group_id,status,grant_version,created_at,updated_at) VALUES(8801,8801,'enabled',1,UTC_TIMESTAMP(3),UTC_TIMESTAMP(3))")
	seed("INSERT INTO channels(id,name,provider,base_url,credential_enc,models,priority,status,owner_org_id) VALUES(8801,'bound-a','openai','https://never-send.invalid','synthetic','relay-m',20,1,0),(8802,'bound-b','openai','https://never-send.invalid','synthetic','relay-m',10,1,0),(8803,'unbound','openai','https://never-send.invalid','synthetic','relay-m,outside-m',100,1,0)")
	seed("INSERT INTO model_prices(model,input_usd_per_1m,output_usd_per_1m) VALUES('relay-m',1,1),('outside-m',1,1)")
	seed("INSERT INTO channel_group_bindings(group_id,channel_id,created_at,updated_at) VALUES(8801,8801,UTC_TIMESTAMP(3),UTC_TIMESTAMP(3)),(8801,8802,UTC_TIMESTAMP(3),UTC_TIMESTAMP(3))")
	r := New(db, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	group := uint64(8801)
	key := &identity.APIKey{ID: 8801, OrgID: 8801, ModelAccessMode: identity.ModelAccessInheritGroup, RoutingGroupID: &group, PolicyVersion: 1, Scopes: "video:read video:write"}
	ctx := context.Background()
	if code, message, ok := r.gateRelayKey(ctx, key, "127.0.0.1", "relay-m"); !ok || code != 0 || message != "" {
		t.Fatal("generic relay admission rejected active grouped model", code, message)
	}
	engine := gin.New()
	engine.GET("/v1/models", r.HandleListModels)
	modelsReq := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	modelsReq.Header.Set("Authorization", "Bearer "+plain)
	modelsResponse := httptest.NewRecorder()
	engine.ServeHTTP(modelsResponse, modelsReq)
	var listed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if modelsResponse.Code != http.StatusOK || json.Unmarshal(modelsResponse.Body.Bytes(), &listed) != nil || len(listed.Data) != 1 || listed.Data[0].ID != "relay-m" {
		t.Fatal("grouped model list disclosed unbound model", modelsResponse.Code, modelsResponse.Body.String())
	}
	snapshot, err := r.cachedChannels()
	if err != nil {
		t.Fatal("cannot read relay channel snapshot")
	}
	first, err := r.pickRelayChannel(ctx, key, snapshot, "relay-m", 0, nil, "", nil)
	if err != nil || first.ID != 8801 {
		t.Fatal("initial pick escaped group or priority", first, err)
	}
	second, err := r.pickRelayChannel(ctx, key, snapshot, "relay-m", 1, map[uint64]bool{8801: true}, "", nil)
	if err != nil || second.ID != 8802 {
		t.Fatal("failover escaped group", second, err)
	}
	seed("DELETE FROM channel_group_bindings WHERE group_id=8801 AND channel_id=8802")
	if _, err := r.pickRelayChannel(ctx, key, snapshot, "relay-m", 1, map[uint64]bool{8801: true}, "", nil); !errors.Is(err, ErrNoChannel) {
		t.Fatal("unbound failover selected outsider", err)
	}
	seed("INSERT INTO channel_group_bindings(group_id,channel_id,created_at,updated_at) VALUES(8801,8802,UTC_TIMESTAMP(3),UTC_TIMESTAMP(3))")
	seed("UPDATE channels SET models='' WHERE id=8801")
	if next, err := r.pickRelayChannel(ctx, key, snapshot, "relay-m", 0, nil, "", nil); err != nil || next.ID != 8802 {
		t.Fatal("unpublished model was picked from stale channel cache", next, err)
	}
	seed("UPDATE channels SET status=0 WHERE id=8802")
	if _, err := r.pickRelayChannel(ctx, key, snapshot, "relay-m", 0, nil, "", nil); !errors.Is(err, routinggroup.ErrCandidatesDenied) {
		t.Fatal("disabled last published channel was picked", err)
	}
	seed("UPDATE channels SET models='relay-m' WHERE id=8801")
	seed("UPDATE channels SET status=1 WHERE id=8802")
	seed("UPDATE org_group_grants SET status='revoked',grant_version=2 WHERE org_id=8801 AND group_id=8801")
	if _, _, ok := r.gateRelayKey(ctx, key, "127.0.0.1", "relay-m"); ok {
		t.Fatal("revoked grant passed generic relay admission")
	}
	if _, err := r.pickRelayChannel(ctx, key, snapshot, "relay-m", 0, nil, "", nil); !errors.Is(err, routinggroup.ErrCandidatesDenied) {
		t.Fatal("revoked grant dispatched", err)
	}
	seed("UPDATE org_group_grants SET status='enabled',grant_version=3 WHERE org_id=8801 AND group_id=8801")
	seed("UPDATE routing_groups SET status='disabled',policy_version=2 WHERE id=8801")
	if _, err := r.pickRelayChannel(ctx, key, snapshot, "relay-m", 0, nil, "", nil); !errors.Is(err, routinggroup.ErrCandidatesDenied) {
		t.Fatal("disabled group dispatched", err)
	}
}
