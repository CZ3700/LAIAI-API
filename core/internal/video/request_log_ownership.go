package video

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

type requestLogActorKey struct{}
type requestLogActor struct {
	OrgID, UserID uint64
	Internal      bool
}

// WithRequestLogActor is installed only after live session/membership checks.
// It scopes private asset history without changing existing task billing views.
func WithRequestLogActor(ctx context.Context, org, user uint64, internal bool) context.Context {
	return context.WithValue(ctx, requestLogActorKey{}, requestLogActor{org, user, internal})
}

const privateAssetLogSQL = `(r.route LIKE '%/video/asset-%' OR r.route LIKE '%/video/assets%' OR r.route LIKE '%/video/portrait-%' OR r.route LIKE '%/video/media-%' OR r.resource_type IN ('asset','asset_group','portrait','portrait_authorization') OR (r.resource_type='operation' AND EXISTS (SELECT 1 FROM video_asset_operations vao WHERE vao.org_id=r.org_id AND vao.operation_id=r.resource_id)))`

func privateAssetLogOwner(db *gorm.DB, org uint64) *gorm.DB {
	a, present := db.Statement.Context.Value(requestLogActorKey{}).(requestLogActor)
	if !present {
		return db
	} // Trusted internal callers retain their explicit org scope.
	if a.OrgID != org || a.UserID == 0 {
		return db.Where("1=0")
	}
	if a.Internal {
		return db
	}
	// No historical user means there is no proof of a named creator. A current
	// user or current Key lookup must not retroactively claim that history.
	return db.Where("(NOT "+privateAssetLogSQL+" OR r.user_id = ?)", a.UserID)
}

func requestLogCursorScope(ctx context.Context, scope string) string {
	if a, ok := ctx.Value(requestLogActorKey{}).(requestLogActor); ok {
		return fmt.Sprintf("%s/actor/%d/%d/%t", scope, a.OrgID, a.UserID, a.Internal)
	}
	return scope
}
