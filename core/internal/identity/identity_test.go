package identity

import (
	"errors"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// 镜像 migrations/0001_identity.sql（SQLite 方言）
const testSchema = `
CREATE TABLE organizations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    slug TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    kind INTEGER NOT NULL DEFAULT 0,
    white_label INTEGER NOT NULL DEFAULT 0,
    group_name TEXT NOT NULL DEFAULT 'default', aff_code TEXT, inviter_org_id INTEGER NOT NULL DEFAULT 0,
    status INTEGER NOT NULL DEFAULT 1,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    email TEXT NOT NULL,
    email_canonical TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '',
    totp_secret_enc TEXT NOT NULL DEFAULT '', totp_enabled INTEGER NOT NULL DEFAULT 0,
    status INTEGER NOT NULL DEFAULT 1,
    role TEXT NOT NULL DEFAULT 'user', admin_remark TEXT NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE org_members (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id INTEGER NOT NULL,
    user_id INTEGER NOT NULL,
    role TEXT NOT NULL DEFAULT 'member',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (org_id, user_id)
);
CREATE TABLE api_keys (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    org_id INTEGER NOT NULL,
    user_id INTEGER,
    name TEXT NOT NULL DEFAULT '',
    prefix TEXT NOT NULL,
    key_hash TEXT NOT NULL UNIQUE,
    scopes TEXT NOT NULL DEFAULT '',
    status INTEGER NOT NULL DEFAULT 1,
    expires_at DATETIME,
    quota_limit NUMERIC NOT NULL DEFAULT 0, used_amount NUMERIC NOT NULL DEFAULT 0,
    model_limits TEXT NOT NULL DEFAULT '', allow_ips TEXT NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE org_entitlements (
    org_id INTEGER PRIMARY KEY,
    feature_images INTEGER NOT NULL DEFAULT 1, feature_video INTEGER NOT NULL DEFAULT 1,
    max_members INTEGER NOT NULL DEFAULT 0, max_api_keys INTEGER NOT NULL DEFAULT 0,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE twofa_backup_codes (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL, code_hash TEXT NOT NULL,
    used_at INTEGER, created_at INTEGER NOT NULL,
    UNIQUE (user_id, code_hash)
);
`

func setupDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.Exec(testSchema).Error; err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func TestCanonicalizeEmail(t *testing.T) {
	cases := []struct {
		in, want string
		wantErr  bool
	}{
		// 薅羊毛事故的真实攻击形态（旧平台 2026-05 事故）
		{"Alice@Gmail.com", "alice@gmail.com", false},
		{"a.l.i.c.e@gmail.com", "alice@gmail.com", false},
		{"alice+farm1@gmail.com", "alice@gmail.com", false},
		{"A.lice+x.y@GoogleMail.com", "alice@gmail.com", false},
		// 非 gmail：点号保留，+alias 剥离
		{"bob.smith@outlook.com", "bob.smith@outlook.com", false},
		{"bob+spam@outlook.com", "bob@outlook.com", false},
		{"  carol@Example.ORG  ", "carol@example.org", false},
		// 非法输入
		{"nodomain", "", true},
		{"@example.com", "", true},
		{"x@", "", true},
		{"x@nodot", "", true},
		{"+only@gmail.com", "", true}, // 剥 alias 后 local 为空
	}
	for _, c := range cases {
		got, err := CanonicalizeEmail(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("CanonicalizeEmail(%q) = %q, want error", c.in, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("CanonicalizeEmail(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
}

func TestPasswordHashAndVerify(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPassword("correct horse battery", h); err != nil {
		t.Fatalf("verify correct: %v", err)
	}
	if err := VerifyPassword("wrong password!", h); !errors.Is(err, ErrPasswordMismatch) {
		t.Fatalf("verify wrong: want ErrPasswordMismatch, got %v", err)
	}
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("short password accepted")
	}
	// 同一密码两次 hash 必须不同（盐随机）
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Fatal("two hashes identical — salt not random")
	}
}

func TestRegisterCreatesUserOrgMember(t *testing.T) {
	db := setupDB(t)
	var u *User
	var org *Organization
	err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		u, org, err = Register(tx, RegisterInput{Email: "Dave@Gmail.com", Password: "longenough1"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if u.EmailCanonical != "dave@gmail.com" || u.Email != "Dave@Gmail.com" {
		t.Fatalf("email = %q canonical = %q", u.Email, u.EmailCanonical)
	}
	if org.Kind != OrgKindPersonal {
		t.Fatalf("org kind = %d", org.Kind)
	}
	m := &OrgMember{}
	if err := db.Where("org_id = ? AND user_id = ?", org.ID, u.ID).First(m).Error; err != nil {
		t.Fatalf("membership missing: %v", err)
	}
	if m.Role != RoleOwner {
		t.Fatalf("role = %q", m.Role)
	}
}

func TestRegisterFarmingVariantsRejected(t *testing.T) {
	db := setupDB(t)
	_ = db.Transaction(func(tx *gorm.DB) error {
		_, _, err := Register(tx, RegisterInput{Email: "eve@gmail.com", Password: "longenough1"})
		if err != nil {
			t.Fatal(err)
		}
		return nil
	})
	// 同一 canonical 邮箱的全部变体必须被 UNIQUE 拒绝
	for _, variant := range []string{"e.ve@gmail.com", "eve+1@gmail.com", "EVE@gmail.com", "e.v.e+bonus@googlemail.com"} {
		err := db.Transaction(func(tx *gorm.DB) error {
			_, _, err := Register(tx, RegisterInput{Email: variant, Password: "longenough1"})
			return err
		})
		if !errors.Is(err, ErrEmailTaken) {
			t.Errorf("variant %q: want ErrEmailTaken, got %v", variant, err)
		}
	}
	var n int64
	db.Model(&User{}).Count(&n)
	if n != 1 {
		t.Fatalf("users = %d, want 1 (farming!)", n)
	}
}

func TestFindUserByEmailFoldsAlias(t *testing.T) {
	db := setupDB(t)
	_ = db.Transaction(func(tx *gorm.DB) error {
		_, _, err := Register(tx, RegisterInput{Email: "frank@gmail.com", Password: "longenough1"})
		return err
	})
	u, err := FindUserByEmail(db, "F.rank+login@GMAIL.com")
	if err != nil {
		t.Fatalf("alias lookup failed: %v", err)
	}
	if u.EmailCanonical != "frank@gmail.com" {
		t.Fatalf("found wrong user: %q", u.EmailCanonical)
	}
}

func TestAPIKeyLifecycle(t *testing.T) {
	db := setupDB(t)
	now := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)
	// org-level key 需其 org active（P0-1：VerifyKey 校验 org 仍有效）
	db.Exec(`INSERT INTO organizations (id, slug, name, status) VALUES (1, 'o1', 'O1', 1)`)

	var plaintext string
	var key *APIKey
	err := db.Transaction(func(tx *gorm.DB) error {
		var err error
		plaintext, key, err = IssueKey(tx, IssueKeyInput{OrgID: 1, Name: "test key"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plaintext) != len("sk-laiai-")+43 {
		t.Fatalf("plaintext length = %d", len(plaintext))
	}
	// 明文绝不落库（红线 8）
	var raw string
	db.Raw("SELECT key_hash FROM api_keys WHERE id = ?", key.ID).Scan(&raw)
	if raw == plaintext || raw == "" {
		t.Fatal("key stored as plaintext or missing")
	}

	got, err := VerifyKey(db, plaintext, now)
	if err != nil || got.ID != key.ID {
		t.Fatalf("verify: %v", err)
	}
	if _, err := VerifyKey(db, "sk-laiai-forgedforgedforgedforgedforgedforg", now); !errors.Is(err, ErrKeyInvalid) {
		t.Fatalf("forged key: want ErrKeyInvalid, got %v", err)
	}

	// 过期
	exp := now.Add(-time.Hour)
	var expiredPlain string
	_ = db.Transaction(func(tx *gorm.DB) error {
		expiredPlain, _, err = IssueKey(tx, IssueKeyInput{OrgID: 1, Name: "expired", ExpiresAt: &exp})
		return err
	})
	if _, err := VerifyKey(db, expiredPlain, now); !errors.Is(err, ErrKeyExpired) {
		t.Fatalf("expired key: want ErrKeyExpired, got %v", err)
	}

	// 吊销（幂等）
	for i := 0; i < 2; i++ {
		if err := db.Transaction(func(tx *gorm.DB) error { return RevokeKey(tx, key.ID) }); err != nil {
			t.Fatalf("revoke round %d: %v", i, err)
		}
	}
	if _, err := VerifyKey(db, plaintext, now); !errors.Is(err, ErrKeyRevoked) {
		t.Fatalf("revoked key: want ErrKeyRevoked, got %v", err)
	}
}
