package server

import (
	"context"
	"errors"

	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/routinggroup"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Host adapters map narrow domain facts; they neither query private tables nor
// open another transaction. All reads use the exact transaction supplied by the
// routing domain, preserving its current-read lock and snapshot boundaries.
type routingAdminOrgBridge struct{}
type routingAdminChannelBridge struct{}

func routingAdminBridgeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return routinggroup.ErrUnavailable
}

func (routingAdminOrgBridge) LockOrganizations(ctx context.Context, tx *gorm.DB, ids []uint64) ([]routinggroup.Organization, error) {
	rows, err := identity.LockRoutingOrganizations(ctx, tx, ids)
	if err != nil {
		return nil, routingAdminBridgeError(err)
	}
	out := make([]routinggroup.Organization, 0, len(rows))
	for _, row := range rows {
		out = append(out, routinggroup.Organization{ID: row.ID, Active: row.Active})
	}
	return out, nil
}

func (routingAdminOrgBridge) ReadOrganizations(ctx context.Context, tx *gorm.DB, ids []uint64) ([]routinggroup.Organization, error) {
	rows, err := identity.ReadRoutingOrganizations(ctx, tx, ids)
	if err != nil {
		return nil, routingAdminBridgeError(err)
	}
	out := make([]routinggroup.Organization, 0, len(rows))
	for _, row := range rows {
		out = append(out, routinggroup.Organization{ID: row.ID, Active: row.Active})
	}
	return out, nil
}

func (routingAdminChannelBridge) LockChannels(ctx context.Context, tx *gorm.DB, ids []uint64) ([]routinggroup.Channel, error) {
	rows, err := relay.LockRoutingChannels(ctx, tx, ids)
	if err != nil {
		return nil, routingAdminBridgeError(err)
	}
	out := make([]routinggroup.Channel, 0, len(rows))
	for _, row := range rows {
		out = append(out, routinggroup.Channel{ID: row.ID, OwnerOrgID: row.OwnerOrgID, Status: row.Status})
	}
	return out, nil
}

func (routingAdminChannelBridge) ReadChannels(ctx context.Context, tx *gorm.DB, ids []uint64) ([]routinggroup.AdminChannel, error) {
	rows, err := relay.ReadRoutingChannelSummaries(ctx, tx, ids)
	if err != nil {
		return nil, routingAdminBridgeError(err)
	}
	out := make([]routinggroup.AdminChannel, 0, len(rows))
	for _, row := range rows {
		out = append(out, routinggroup.AdminChannel{ID: row.ID, OwnerOrgID: row.OwnerOrgID, Name: row.Name, Provider: row.Provider, Status: row.Status})
	}
	return out, nil
}

func (s *Server) routingAdminReader() *routinggroup.AdminReadService {
	return &routinggroup.AdminReadService{DB: s.db, Organizations: routingAdminOrgBridge{}, Channels: routingAdminChannelBridge{}}
}

func (s *Server) routingAdminWriter() *routinggroup.Service {
	var db *gorm.DB
	if s.db != nil {
		db = s.db.Session(&gorm.Session{Logger: logger.Discard})
	}
	return &routinggroup.Service{DB: db, Organizations: routingAdminOrgBridge{}, Channels: routingAdminChannelBridge{}}
}
