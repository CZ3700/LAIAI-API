package relay

// GCP service account → OAuth2 token 单测。RSA 私钥**测试内现生成**（rsa.GenerateKey），
// 非真实凭证——合规（凭证只走环境变量，绝不入库/fixture）。token_uri 指向本地 httptest
// mock，验签用现生成公钥，全程不触真网络。

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testSAJSON 用现生成的 RSA key 造一份假 SA JSON，token_uri 指向 mock。
func testSAJSON(t *testing.T, tokenURI string) (string, *rsa.PublicKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen rsa: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	sa := map[string]any{
		"type":         "service_account",
		"project_id":   "my-proj",
		"client_email": "svc@my-proj.iam.gserviceaccount.com",
		"private_key":  string(pemBytes),
		"token_uri":    tokenURI,
	}
	b, _ := json.Marshal(sa)
	return string(b), &key.PublicKey
}

func TestParseGCPServiceAccount(t *testing.T) {
	saJSON, _ := testSAJSON(t, "")
	sa, err := parseGCPServiceAccount(saJSON)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if sa.ProjectID != "my-proj" || sa.ClientEmail == "" || sa.PrivateKey == "" {
		t.Fatalf("fields = %+v", sa)
	}
	if sa.TokenURI != gcpDefaultTokenURI { // 空 token_uri 补默认
		t.Fatalf("token_uri default = %q", sa.TokenURI)
	}
	if _, err := parseGCPServiceAccount(`{"project_id":"p"}`); err == nil {
		t.Fatal("missing client_email/private_key should error")
	}
	if _, err := parseGCPServiceAccount(`not json`); err == nil {
		t.Fatal("bad json should error")
	}
}

func TestSignGCPJWT(t *testing.T) {
	saJSON, pub := testSAJSON(t, "https://oauth2.example/token")
	sa, err := parseGCPServiceAccount(saJSON)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	jwt, err := signGCPJWT(sa, now)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt segments = %d", len(parts))
	}
	// claims 可解且字段正确
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	if json.Unmarshal(cb, &claims) != nil {
		t.Fatalf("claims not json: %s", cb)
	}
	if claims["iss"] != sa.ClientEmail || claims["aud"] != sa.TokenURI || claims["scope"] != gcpScope {
		t.Fatalf("claims = %v", claims)
	}
	if claims["iat"].(float64) != float64(now.Unix()) || claims["exp"].(float64) != float64(now.Add(time.Hour).Unix()) {
		t.Fatalf("iat/exp = %v/%v", claims["iat"], claims["exp"])
	}
	// 签名用现生成公钥可验（RS256）
	signingInput := parts[0] + "." + parts[1]
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	h := sha256.Sum256([]byte(signingInput))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, h[:], sig); err != nil {
		t.Fatalf("signature verify failed: %v", err)
	}
}

func TestGCPAccessTokenExchangeAndCache(t *testing.T) {
	gcpTokenCache = map[string]gcpCachedToken{} // 隔离缓存
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		if r.FormValue("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
			t.Errorf("grant_type = %q", r.FormValue("grant_type"))
		}
		if r.FormValue("assertion") == "" {
			t.Error("missing assertion")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"ya29.mock","expires_in":3600,"token_type":"Bearer"}`))
	}))
	defer srv.Close()

	saJSON, _ := testSAJSON(t, srv.URL)
	sa, _ := parseGCPServiceAccount(saJSON)
	now := time.Unix(1_700_000_000, 0)

	tok, err := gcpAccessToken(context.Background(), sa, now)
	if err != nil || tok != "ya29.mock" {
		t.Fatalf("token = %q err=%v", tok, err)
	}
	// 缓存命中：第二次不再打 mock
	tok2, err := gcpAccessToken(context.Background(), sa, now.Add(time.Minute))
	if err != nil || tok2 != "ya29.mock" {
		t.Fatalf("cached token = %q err=%v", tok2, err)
	}
	if h := hits.Load(); h != 1 {
		t.Fatalf("token endpoint hit %d times, want 1 (缓存未生效)", h)
	}
	// 过期后重新换取（提前 60s 过期：3600-60=3540s 后失效）
	_, err = gcpAccessToken(context.Background(), sa, now.Add(3541*time.Second))
	if err != nil {
		t.Fatalf("re-exchange: %v", err)
	}
	if h := hits.Load(); h != 2 {
		t.Fatalf("after expiry hits = %d, want 2", h)
	}
}

func TestGCPAccessTokenServerError(t *testing.T) {
	gcpTokenCache = map[string]gcpCachedToken{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer srv.Close()
	saJSON, _ := testSAJSON(t, srv.URL)
	sa, _ := parseGCPServiceAccount(saJSON)
	_, err := gcpAccessToken(context.Background(), sa, time.Unix(1_700_000_000, 0))
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 error, got %v", err)
	}
}
