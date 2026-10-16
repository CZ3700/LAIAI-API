package videobilling

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// BeginImportTx creates a resumable import. The caller has authenticated the
// source, encrypted the raw bill, and verified its SHA-256 before this call.
func BeginImportTx(tx *gorm.DB, in BeginImportInput) (BeginImportResult, error) {
	if err := requireTx(tx); err != nil {
		return BeginImportResult{}, err
	}
	if !validID(in.ImportKey) || in.ProviderScopeID == 0 || !validID(in.Source) || !validID(in.SourceVersion) || !isHash(in.SourceHash) || in.PayloadRef == "" || len(in.PayloadRef) > 128 || in.ExpectedRows < 0 || in.Now.IsZero() {
		return BeginImportResult{}, ErrInvalid
	}
	var old BillImport
	err := locked(tx).Where("provider_scope_id = ? AND import_key = ?", in.ProviderScopeID, in.ImportKey).Take(&old).Error
	if err == nil {
		if old.Source != in.Source || old.SourceVersion != in.SourceVersion || old.SourceHash != in.SourceHash || old.ExpectedRows != in.ExpectedRows {
			return BeginImportResult{}, ErrConflict
		}
		return BeginImportResult{Import: old, Replay: true}, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return BeginImportResult{}, err
	}
	i := BillImport{ImportKey: in.ImportKey, ProviderScopeID: in.ProviderScopeID, Source: in.Source, SourceVersion: in.SourceVersion, SourceHash: in.SourceHash, PayloadRef: in.PayloadRef, ExpectedRows: in.ExpectedRows, State: "open", CreatedAt: in.Now.UTC()}
	if err := tx.Create(&i).Error; err != nil {
		return BeginImportResult{}, err
	}
	return BeginImportResult{Import: i}, nil
}

func canonicalLine(in BillLine) (BillLine, error) {
	if in.ProviderScopeID == 0 || !validID(in.Source) || in.SourceRecordID == "" || len(in.SourceRecordID) > 128 || !validID(in.SourceVersion) || in.SourceRevision <= 0 || !isHash(in.EvidenceHash) || in.PayloadRef == "" || len(in.PayloadRef) > 128 || !validID(in.Model) || in.PeriodStart.IsZero() || !in.PeriodEnd.After(in.PeriodStart) || in.ObservedAt.IsZero() || !validMoney(in.AmountCNY.Abs()) || !in.AmountCNY.Equal(in.AmountCNY.Round(8)) || (in.CompletionTokens != nil && *in.CompletionTokens < 0) {
		return in, ErrInvalid
	}
	if in.Granularity != BillTaskTotal && in.Granularity != BillAggregate {
		return in, ErrInvalid
	}
	if in.Granularity == BillTaskTotal && in.AmountCNY.IsNegative() {
		return in, ErrInvalid
	}
	if (in.Granularity == BillTaskTotal && (in.UpstreamTaskID == "" || len(in.UpstreamTaskID) > 128)) || (in.Granularity == BillAggregate && in.UpstreamTaskID != "") {
		return in, ErrInvalid
	}
	in.ID = 0
	in.ContentHash = ""
	in.PeriodStart = in.PeriodStart.UTC()
	in.PeriodEnd = in.PeriodEnd.UTC()
	in.ObservedAt = in.ObservedAt.UTC()
	// Retrieval time and ciphertext rotation do not change source identity.
	digest, _ := json.Marshal([]any{"video-bill-line-v1", in.ProviderScopeID, in.Source, in.SourceRecordID, in.SourceVersion, in.SourceRevision, in.EvidenceHash, in.Granularity, in.UpstreamTaskID, in.Model, in.PeriodStart, in.PeriodEnd, in.CompletionTokens, in.AmountCNY.String()})
	h := sha256.Sum256(digest)
	in.ContentHash = hex.EncodeToString(h[:])
	return in, nil
}

// ImportLineTx persists one immutable evidence line and an import-row receipt.
// Aggregate/unresolved lines stay quarantined. This function never settles,
// releases, debits, refunds or treats allocated aggregate costs as exact usage.
func ImportLineTx(tx *gorm.DB, in ImportLineInput) (ImportLineResult, error) {
	if err := requireTx(tx); err != nil {
		return ImportLineResult{}, err
	}
	if in.ImportID == 0 || in.RowNumber <= 0 || in.Now.IsZero() {
		return ImportLineResult{}, ErrInvalid
	}
	l, err := canonicalLine(in.Line)
	if err != nil {
		return ImportLineResult{}, err
	}
	var imp BillImport
	if err := locked(tx).Where("id = ?", in.ImportID).Take(&imp).Error; err != nil {
		return ImportLineResult{}, err
	}
	if l.ProviderScopeID != imp.ProviderScopeID || l.Source != imp.Source || in.RowNumber > imp.ExpectedRows {
		return ImportLineResult{}, ErrInvalid
	}
	var row BillImportRow
	err = tx.Where("import_id = ? AND source_ordinal = ?", imp.ID, in.RowNumber).Take(&row).Error
	replay := err == nil
	if replay && row.ContentHash != l.ContentHash {
		return ImportLineResult{}, ErrConflict
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return ImportLineResult{}, err
	}
	if !replay && imp.State != "open" {
		return ImportLineResult{}, ErrConflict
	}
	h, err := lockBillHead(tx, l)
	if err != nil {
		return ImportLineResult{}, err
	}
	if h.LineID != 0 {
		var current BillLine
		if err := tx.Where("id = ?", h.LineID).Take(&current).Error; err != nil {
			return ImportLineResult{}, err
		}
		if current.Granularity != l.Granularity || current.UpstreamTaskID != l.UpstreamTaskID || current.Model != l.Model {
			return ImportLineResult{}, ErrConflict
		}
	}
	var old BillLine
	err = tx.Where("provider_scope_id = ? AND source = ? AND source_record_id = ? AND source_version = ?", l.ProviderScopeID, l.Source, l.SourceRecordID, l.SourceVersion).Take(&old).Error
	if err == nil {
		if old.ContentHash != l.ContentHash {
			return ImportLineResult{}, ErrConflict
		}
		l = old
	} else {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return ImportLineResult{}, err
		}
		// Same revision with another version is also ambiguous, not a correction.
		var n int64
		if err := tx.Model(&BillLine{}).Where("provider_scope_id = ? AND source = ? AND source_record_id = ? AND source_revision = ?", l.ProviderScopeID, l.Source, l.SourceRecordID, l.SourceRevision).Count(&n).Error; err != nil {
			return ImportLineResult{}, err
		}
		if n != 0 {
			return ImportLineResult{}, ErrConflict
		}
		if err := tx.Create(&l).Error; err != nil {
			return ImportLineResult{}, err
		}
	}
	if l.SourceRevision > h.SourceRevision {
		if err := tx.Model(&h).Updates(map[string]any{"source_revision": l.SourceRevision, "line_id": l.ID}).Error; err != nil {
			return ImportLineResult{}, err
		}
	}
	if !replay {
		row = BillImportRow{ImportID: imp.ID, RowNumber: in.RowNumber, LineID: l.ID, ContentHash: l.ContentHash}
		if err := tx.Create(&row).Error; err != nil {
			return ImportLineResult{}, err
		}
	}
	var item ReconciliationItem
	if in.Binding != nil {
		item, err = bindBillLine(tx, l, *in.Binding, in.Now)
	} else {
		var binding BillBinding
		err = tx.Where("line_id = ?", l.ID).Take(&binding).Error
		if err == nil {
			item, err = reconcileLine(tx, l, &binding, in.Now)
		} else if errors.Is(err, gorm.ErrRecordNotFound) {
			item, err = reconcileLine(tx, l, nil, in.Now)
		}
	}
	if err != nil {
		return ImportLineResult{}, err
	}
	return ImportLineResult{Line: l, Item: item, Replay: replay}, nil
}

func lockBillHead(tx *gorm.DB, l BillLine) (BillHead, error) {
	h := BillHead{ProviderScopeID: l.ProviderScopeID, Source: l.Source, SourceRecordID: l.SourceRecordID}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&h).Error; err != nil {
		return h, err
	}
	err := locked(tx).Where("provider_scope_id = ? AND source = ? AND source_record_id = ?", l.ProviderScopeID, l.Source, l.SourceRecordID).Take(&h).Error
	return h, err
}

// CompleteImportTx requires all declared 1-based rows. An interrupted importer
// resumes by replaying its source rows; it never skips based on a partial count.
func CompleteImportTx(tx *gorm.DB, importID uint64, now time.Time) (BillImport, error) {
	if err := requireTx(tx); err != nil {
		return BillImport{}, err
	}
	if importID == 0 || now.IsZero() {
		return BillImport{}, ErrInvalid
	}
	var i BillImport
	if err := locked(tx).Where("id = ?", importID).Take(&i).Error; err != nil {
		return i, err
	}
	if i.State == "complete" {
		return i, nil
	}
	var n int64
	if err := tx.Model(&BillImportRow{}).Where("import_id = ?", i.ID).Count(&n).Error; err != nil {
		return i, err
	}
	if n != i.ExpectedRows {
		return i, ErrConflict
	}
	t := now.UTC()
	if err := tx.Model(&i).Updates(map[string]any{"state": "complete", "completed_at": t}).Error; err != nil {
		return i, err
	}
	i.State = "complete"
	i.CompletedAt = &t
	return i, nil
}

// BindBillLineTx recovers a quarantined task-total line. No historical row is
// rewritten; a unique verified binding and another reconciliation item append.
func BindBillLineTx(tx *gorm.DB, lineID uint64, binding VerifiedBinding, now time.Time) (ReconciliationItem, error) {
	if err := requireTx(tx); err != nil {
		return ReconciliationItem{}, err
	}
	var l BillLine
	if err := tx.Where("id = ?", lineID).Take(&l).Error; err != nil {
		return ReconciliationItem{}, err
	}
	if _, err := lockBillHead(tx, l); err != nil {
		return ReconciliationItem{}, err
	}
	return bindBillLine(tx, l, binding, now)
}

func bindBillLine(tx *gorm.DB, l BillLine, b VerifiedBinding, now time.Time) (ReconciliationItem, error) {
	if now.IsZero() || b.OrgID == 0 || !validID(b.TaskID) || !validID(b.BindingVersion) || b.ProviderScopeID != l.ProviderScopeID || b.UpstreamTaskID != l.UpstreamTaskID || l.Granularity != BillTaskTotal {
		return ReconciliationItem{}, ErrUnmapped
	}
	var s Snapshot
	err := tx.Where("org_id = ? AND task_id = ? AND provider_scope_id = ? AND model = ?", b.OrgID, b.TaskID, b.ProviderScopeID, l.Model).Take(&s).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ReconciliationItem{}, ErrUnmapped
	}
	if err != nil {
		return ReconciliationItem{}, err
	}
	var conflicts int64
	if err := tx.Table("video_provider_bill_bindings AS b").Joins("JOIN video_provider_bill_lines l ON l.id = b.line_id").Where("l.provider_scope_id = ? AND l.source = ? AND l.source_record_id = ? AND (b.org_id <> ? OR b.task_id <> ?)", l.ProviderScopeID, l.Source, l.SourceRecordID, b.OrgID, b.TaskID).Count(&conflicts).Error; err != nil {
		return ReconciliationItem{}, err
	}
	if conflicts != 0 {
		return ReconciliationItem{}, ErrConflict
	}
	var existing BillBinding
	err = tx.Where("line_id = ?", l.ID).Take(&existing).Error
	if err == nil {
		if existing.OrgID != b.OrgID || existing.TaskID != b.TaskID || existing.BindingVersion != b.BindingVersion {
			return ReconciliationItem{}, ErrConflict
		}
	} else {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return ReconciliationItem{}, err
		}
		existing = BillBinding{LineID: l.ID, OrgID: b.OrgID, TaskID: b.TaskID, BindingVersion: b.BindingVersion, CreatedAt: now.UTC()}
		if err := tx.Create(&existing).Error; err != nil {
			return ReconciliationItem{}, err
		}
	}
	return reconcileLine(tx, l, &existing, now)
}

func reconcileLine(tx *gorm.DB, l BillLine, b *BillBinding, now time.Time) (ReconciliationItem, error) {
	version := ""
	if b != nil {
		version = b.BindingVersion
	}
	var item ReconciliationItem
	err := tx.Where("line_id = ? AND binding_version = ?", l.ID, version).Take(&item).Error
	if err == nil {
		return item, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return item, err
	}
	item = ReconciliationItem{LineID: l.ID, BindingVersion: version, State: ReconcileQuarantined, Reason: "mapping_unresolved", ObservedTokens: l.CompletionTokens, ObservedCNY: l.AmountCNY, CreatedAt: now.UTC()}
	if l.Granularity == BillAggregate {
		item.Reason = "aggregate_not_task_usage"
	}
	if b != nil {
		item.OrgID = b.OrgID
		item.TaskID = b.TaskID
		e, err := initialEntry(tx, b.OrgID, b.TaskID)
		if err != nil {
			return item, err
		}
		if e == nil {
			item.State = ReconcilePending
			item.Reason = "initial_settlement_missing"
		} else {
			item.InitialEntryID = &e.ID
			item.ExpectedTokens = e.CompletionTokens
			item.ExpectedCNY = e.SupplierCNY
			item.DifferenceCNY = l.AmountCNY.Sub(e.SupplierCNY)
			item.State = ReconcileMatched
			item.Reason = "evidence_matches_initial"
			if !equalTokens(l.CompletionTokens, e.CompletionTokens) || !item.DifferenceCNY.IsZero() {
				item.State = ReconcileDifference
				item.Reason = "evidence_differs_initial"
			}
			if l.CompletionTokens == nil {
				item.State = ReconcilePending
				item.Reason = "bill_usage_missing"
			}
		}
		var head BillHead
		if err := tx.Where("provider_scope_id = ? AND source = ? AND source_record_id = ?", l.ProviderScopeID, l.Source, l.SourceRecordID).Take(&head).Error; err != nil {
			return item, err
		}
		if head.LineID != l.ID {
			item.State = ReconcileStale
			item.Reason = "older_source_revision"
		}
	}
	if err := tx.Create(&item).Error; err != nil {
		return item, err
	}
	return item, nil
}
