package video

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"
)

// Frozen pre-S2 QueryReceipt shape, independent of the production projection.
// The original model SHA was 36e106dd6c283ea2029c4d9039dd23c4bf43e025bd69277e51deebbff321f216.
// GORM tags do not affect encoding/json. Future ORM fields are deliberately absent.
type queryReceiptBeforeS2 struct {
	AttemptID                string
	OrgID                    uint64
	TaskID                   string
	Sequence                 uint64
	TaskRowVersion           uint64
	RuntimeVersion           string
	AdapterVersion           string
	ProtocolContractVersion  string
	ObservationSchemaVersion uint32
	HTTPStatus               int
	Outcome                  string
	ProviderRequestID        string
	ObservationJSON          string
	HasVideo                 bool
	HasLastFrame             bool
	PayloadRef               string
	ReceivedAt               time.Time
	AppliedAt                *time.Time
	ApplyErrorCode           string
	RecoveryDueAt            *time.Time
	RecoveryVersion          uint64
}

func queryReceiptOldCanonical(t *testing.T, current QueryReceipt) ([]byte, error) {
	t.Helper()
	var row queryReceiptBeforeS2
	src, dst := reflect.ValueOf(current), reflect.ValueOf(&row).Elem()
	for i := 0; i < dst.NumField(); i++ {
		field := src.FieldByName(dst.Type().Field(i).Name)
		if !field.IsValid() || field.Type() != dst.Field(i).Type() {
			t.Fatal("old persisted field missing or changed type")
		}
		dst.Field(i).Set(field)
	}
	// Exact operations from queryEvidenceDigest before the extraction.
	row.AppliedAt, row.RecoveryDueAt = nil, nil
	row.ApplyErrorCode, row.RecoveryVersion = "", 0
	row.ReceivedAt = row.ReceivedAt.UTC()
	return json.Marshal(row)
}

const queryReceiptLiteral = `{"AttemptID":"query-s2-1","OrgID":9007199254740993,"TaskID":"task-s2-1","Sequence":2,"TaskRowVersion":3,"RuntimeVersion":"ark-official-legacy-v1","AdapterVersion":"ark-v3-v1","ProtocolContractVersion":"video-contract-v1","ObservationSchemaVersion":1,"HTTPStatus":200,"Outcome":"accepted","ProviderRequestID":"request-\u003ctest\u003e\u0026\u2028","ObservationJSON":"{\"task_id\":\"up-s2-1\",\"model\":\"synthetic-upstream\",\"status\":\"succeeded\",\"completion_tokens\":100,\"total_tokens\":100,\"has_error\":false,\"delivery_payload_ref\":\"delivery-s2-1\"}","HasVideo":true,"HasLastFrame":false,"PayloadRef":"query-payload-s2-1","ReceivedAt":"2026-10-27T02:03:04.123Z","AppliedAt":null,"ApplyErrorCode":"","RecoveryDueAt":null,"RecoveryVersion":0}`
const queryReceiptLiteralSHA = "03ecddf38109048a9705fcb82fb614dbe86a43507c23002d3b10b575888dd2f7"

func queryReceiptDigestFixture(t *testing.T) QueryReceipt {
	t.Helper()
	var row QueryReceipt
	if err := json.Unmarshal([]byte(queryReceiptLiteral), &row); err != nil {
		t.Fatal(err)
	}
	// Preserve the independent source's non-UTC representation and mutable progress.
	at, err := time.Parse(time.RFC3339Nano, "2026-10-27T10:03:04.123+08:00")
	if err != nil {
		t.Fatal(err)
	}
	row.ReceivedAt = at
	applied, due := at.Add(time.Minute), at.Add(2*time.Minute)
	row.AppliedAt, row.RecoveryDueAt = &applied, &due
	row.ApplyErrorCode, row.RecoveryVersion = "synthetic-progress", 7
	return row
}

func TestQueryReceiptDigestIndependentLiteral(t *testing.T) {
	row := queryReceiptDigestFixture(t)
	before := row
	raw, err := json.Marshal(queryReceiptDigestProjection(row))
	if err != nil || string(raw) != queryReceiptLiteral || len(raw) != 750 {
		t.Fatal("21-field literal encoding changed")
	}
	old, err := queryReceiptOldCanonical(t, row)
	if err != nil || string(old) != queryReceiptLiteral {
		t.Fatal("literal differs from frozen old encoding")
	}
	sum, err := queryEvidenceDigest(row)
	if err != nil || hex.EncodeToString(sum[:]) != queryReceiptLiteralSHA {
		t.Fatal("independent digest golden changed")
	}
	if !reflect.DeepEqual(before, row) {
		t.Fatal("digest mutated input or progress pointers")
	}
}

func TestQueryReceiptDigestMatchesPreS2Encoding(t *testing.T) {
	base := queryReceiptDigestFixture(t)
	max := base
	max.OrgID, max.Sequence, max.TaskRowVersion, max.RecoveryVersion = math.MaxUint64, math.MaxUint64, math.MaxUint64, math.MaxUint64
	max.ObservationSchemaVersion = math.MaxUint32
	max.HTTPStatus = -1
	nano := base
	nano.ReceivedAt = time.Date(2026, 10, 27, 10, 3, 4, 123456789, time.FixedZone("odd", 5*3600+45*60))
	invalid := base
	invalid.ObservationJSON = "raw-" + string([]byte{0xff, 0xfe})
	invalid.ProviderRequestID = "\x00<>&\u2028\u2029"
	unknown := base
	unknown.RuntimeVersion = "future-contract "
	unknown.ObservationSchemaVersion = 99
	unknown.ObservationJSON = "{malformed"
	unknown.PayloadRef = ""
	unknown.HasVideo, unknown.HasLastFrame = false, true
	cases := []struct {
		name string
		row  QueryReceipt
	}{{"zero", QueryReceipt{}}, {"full", base}, {"unsigned_max", max}, {"nanoseconds_timezone", nano}, {"legacy_invalid_utf8", invalid}, {"unknown_raw_evidence", unknown}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want, oldErr := queryReceiptOldCanonical(t, tc.row)
			got, err := json.Marshal(queryReceiptDigestProjection(tc.row))
			if oldErr != nil || err != nil || string(got) != string(want) {
				t.Fatal("extraction changed old JSON")
			}
			digest, err := queryEvidenceDigest(tc.row)
			if err != nil || digest != sha256.Sum256(want) {
				t.Fatal("extraction changed old digest or added validation")
			}
		})
	}
}

func TestQueryReceiptDigestOriginalFieldsStayBound(t *testing.T) {
	base := queryReceiptDigestFixture(t)
	want, err := queryEvidenceDigest(base)
	if err != nil {
		t.Fatal(err)
	}
	fields := reflect.TypeFor[queryReceiptBeforeS2]()
	for i := 0; i < fields.NumField(); i++ {
		name := fields.Field(i).Name
		t.Run(name, func(t *testing.T) {
			changed := base
			v := reflect.ValueOf(&changed).Elem().FieldByName(name)
			switch v.Kind() {
			case reflect.String:
				v.SetString(v.String() + "-changed")
			case reflect.Uint64, reflect.Uint32:
				v.SetUint(v.Uint() + 1)
			case reflect.Int:
				v.SetInt(v.Int() + 1)
			case reflect.Bool:
				v.SetBool(!v.Bool())
			case reflect.Struct:
				v.Set(reflect.ValueOf(base.ReceivedAt.Add(time.Nanosecond)))
			case reflect.Pointer:
				at := base.ReceivedAt.Add(3 * time.Hour)
				v.Set(reflect.ValueOf(&at))
			default:
				t.Fatal("unhandled old field type")
			}
			got, err := queryEvidenceDigest(changed)
			if err != nil {
				t.Fatal(err)
			}
			progress := name == "AppliedAt" || name == "ApplyErrorCode" || name == "RecoveryDueAt" || name == "RecoveryVersion"
			if (got == want) != progress {
				t.Fatal("original evidence/progress binding changed")
			}
		})
	}
	equivalent := base
	equivalent.ReceivedAt = base.ReceivedAt.UTC()
	got, err := queryEvidenceDigest(equivalent)
	if err != nil || got != want {
		t.Fatal("same instant in UTC changed digest")
	}
}

func TestQueryReceiptDigestPreservesMarshalFailure(t *testing.T) {
	row := queryReceiptDigestFixture(t)
	row.ReceivedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	_, oldErr := queryReceiptOldCanonical(t, row)
	digest, err := queryEvidenceDigest(row)
	var oldMarshal, newMarshal *json.MarshalerError
	if !errors.As(oldErr, &oldMarshal) || !errors.As(err, &newMarshal) || digest != ([32]byte{}) {
		t.Fatal("invalid time did not preserve safe marshal cause and zero result")
	}
	if oldMarshal.Type != newMarshal.Type || oldMarshal.Err.Error() != newMarshal.Err.Error() {
		t.Fatal("marshal failure semantics changed")
	}
}
